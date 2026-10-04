package sync

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
)

// New wires the store, both clients and the production feeds and defaults.
func TestNew_WiresTheService(t *testing.T) {
	store := &billSyncStore{}
	api := congress.NewClientWithBaseURL(http.DefaultClient, "https://api.congress.test")
	s := New(store, api, http.DefaultClient)

	if s.store != store || s.api != api || s.http != http.DefaultClient || s.logger == nil || s.legislators == nil {
		t.Errorf("New = %+v, want the store, clients, a logger and a legislators client", s)
	}
	if s.feeds != defaultVoteFeeds() {
		t.Errorf("feeds = %+v, want the default feeds", s.feeds)
	}
	for _, u := range []string{s.feeds.houseIndexURL, s.feeds.houseVoteURL} {
		if !strings.HasPrefix(u, "https://clerk.house.gov/") {
			t.Errorf("House feed %q isn't the Clerk's", u)
		}
	}
	for _, u := range []string{s.feeds.senateIndexURL, s.feeds.senateVoteURL, s.feeds.senateMembersURL} {
		if !strings.HasPrefix(u, "https://www.senate.gov/") {
			t.Errorf("Senate feed %q isn't the Senate's", u)
		}
	}
	if s.summaryJob != DefaultSummaryJobConfig() {
		t.Errorf("summaryJob = %+v, want the defaults", s.summaryJob)
	}
	if s.govinfo != nil {
		t.Error("govinfo set before SetGovInfo")
	}

	g := govinfo.NewClientWithBaseURL(http.DefaultClient, "https://api.govinfo.test")
	s.SetGovInfo(g)
	cfg := SummaryJobConfig{Batch: 3, Workers: 1}
	s.SetSummaryJob(cfg)
	if s.govinfo != g || s.summaryJob != cfg {
		t.Errorf("after the setters govinfo = %p, summaryJob = %+v; want %p, %+v", s.govinfo, s.summaryJob, g, cfg)
	}
}

func TestRequestCount_WithoutAPIIsZero(t *testing.T) {
	if n := (&Service{}).requestCount(); n != 0 {
		t.Errorf("requestCount = %d, want 0", n)
	}
}

// A failure before any bill syncs (the retry queue, the listing, the resume lookup) fails the
// step and syncs nothing.
func TestSyncBills_EarlyFailuresFailTheStep(t *testing.T) {
	tests := []struct {
		name    string
		store   *billSyncStore
		api     *billAPI
		resume  bool
		wantErr string
	}{
		{
			name: "due retries", store: &billSyncStore{dueErr: errFakeStore},
			api: &billAPI{n: 1}, wantErr: errFakeStore.Error(),
		},
		{
			name: "the list", store: &billSyncStore{},
			api: &billAPI{n: 1, failPaths: map[string]bool{"/bill/119": true}}, wantErr: "list bills",
		},
		{
			name: "the resume lookup", store: &billSyncStore{sinceErr: errFakeStore},
			api: &billAPI{n: 1}, resume: true, wantErr: "list bills synced since",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newBillSyncService(t, tt.store, tt.api)
			if tt.resume {
				s.SetResumeSince(jobNow())
			}

			err := s.SyncBills(t.Context(), 119, 0)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("SyncBills = %v, want an error containing %q", err, tt.wantErr)
			}
			if len(tt.store.upserted) != 0 || tt.store.success != nil {
				t.Errorf("upserted %v, success %+v; want nothing", tt.store.upserted, tt.store.success)
			}
		})
	}
}

// A limited run stops listing once it has enough bills, syncs only those, and leaves the
// watermark alone.
func TestSyncBills_LimitStopsTheListing(t *testing.T) {
	store := &billSyncStore{}
	api := &billAPI{n: 5}
	s := newBillSyncService(t, store, api)

	if err := s.SyncBills(t.Context(), 119, 2); err != nil {
		t.Fatal(err)
	}
	if len(api.details) != 2 || len(store.marked) != 2 {
		t.Errorf("details %v, marked %v; want 2 bills", api.details, store.marked)
	}
	if store.success != nil {
		t.Errorf("a limited run recorded success %+v", store.success)
	}
}

func TestSyncOneBill_DetailAndUpsertFailures(t *testing.T) {
	tests := []struct {
		name      string
		api       *billAPI
		failWrite string
		wantErr   string
		unknown   bool
	}{
		{name: "detail fails", api: &billAPI{n: 1, failPaths: map[string]bool{"/bill/119/hr/1": true}},
			wantErr: "get bill detail"},
		{name: "detail missing", api: &billAPI{n: 1, missingPaths: map[string]bool{"/bill/119/hr/1": true}},
			wantErr: "get bill detail", unknown: true},
		{name: "upsert fails", api: &billAPI{n: 1}, failWrite: "bill", wantErr: "upsert bill"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &billSyncStore{failWrites: map[string]bool{tt.failWrite: true}}
			s := newBillSyncService(t, store, tt.api)

			err := s.syncOneBill(t.Context(), hrSummary(1), 119)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("syncOneBill = %v, want an error containing %q", err, tt.wantErr)
			}
			if errors.Is(err, errUnknownBill) != tt.unknown {
				t.Errorf("errors.Is(err, errUnknownBill) = %v, want %v", !tt.unknown, tt.unknown)
			}
			if len(store.writes) != 0 {
				t.Errorf("writes = %v, want none before the bill row", store.writes)
			}
		})
	}
}

// Empty sub-resources write nothing, so the lists the last sync stored stay; the bill still
// counts as synced.
func TestSyncOneBill_EmptySubResourcesWriteNothing(t *testing.T) {
	store := &billSyncStore{}
	api := &billAPI{n: 1, bodies: map[string]string{
		"/bill/119/hr/1/cosponsors":   `{"cosponsors":[]}`,
		"/bill/119/hr/1/committees":   `{"committees":[]}`,
		"/bill/119/hr/1/subjects":     `{"subjects":{"legislativeSubjects":[]}}`,
		"/bill/119/hr/1/relatedbills": `{"relatedBills":[]}`,
		"/bill/119/hr/1/amendments":   `{"amendments":[]}`,
	}}
	s := newBillSyncService(t, store, api)

	if err := s.syncOneBill(t.Context(), hrSummary(1), 119); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"cosponsors", "committees", "committee_links", "subjects", "subject_links",
		"related_bills", "relation_links", "amendments"} {
		if slices.Contains(store.writes, w) {
			t.Errorf("wrote %s for an empty list (writes %v)", w, store.writes)
		}
	}
	if !slices.Equal(store.marked, []string{"hr-119-1"}) {
		t.Errorf("marked = %v, want [hr-119-1]", store.marked)
	}
}

// An amendment without a chamber takes it from its type, and keeps its latest action.
func TestSyncBillAmendments_ChamberFromType(t *testing.T) {
	store := &billSyncStore{}
	api := &billAPI{n: 1, bodies: map[string]string{"/bill/119/hr/1/amendments": `{"amendments":[
		{"number":"1","type":"HAMDT","congress":119},
		{"number":"2","type":"SAMDT","congress":119,"latestAction":{"actionDate":"2025-02-01","text":"Proposed."}},
		{"number":"3","type":"SAMDT","congress":119,"chamber":"House"}]}`}}
	s := newBillSyncService(t, store, api)

	if err := s.syncBillAmendments(t.Context(), "hr-119-1", 119, "hr", 1); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range store.amendments {
		got[a.ID] = a.Chamber
		if (a.ID == "samdt-119-2") != (len(a.LatestAction) > 0) {
			t.Errorf("%s latest action = %s", a.ID, a.LatestAction)
		}
	}
	want := map[string]string{"hamdt-119-1": chamberHouse, "samdt-119-2": chamberSenate, "samdt-119-3": chamberHouse}
	if len(got) != len(want) {
		t.Fatalf("amendments = %v, want %v", got, want)
	}
	for id, chamber := range want {
		if got[id] != chamber {
			t.Errorf("%s chamber = %q, want %q", id, got[id], chamber)
		}
	}
}

func TestMapParty(t *testing.T) {
	for in, want := range map[string]string{
		"Democratic": "D", "Republican": "R", "Independent": "I", "Libertarian": "O", "": "O",
	} {
		if got := mapParty(in); got != want {
			t.Errorf("mapParty(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	short := strings.Repeat("a", 60)
	if got := truncate(short); got != short {
		t.Errorf("truncate(60 chars) = %q, want it unchanged", got)
	}
	if got := truncate(short + "b"); got != short+"..." {
		t.Errorf("truncate(61 chars) = %q, want the first 60 and an ellipsis", got)
	}
}

func TestStateAbbrev(t *testing.T) {
	for in, want := range map[string]string{"Kansas": "KS", "District of Columbia": "DC", "Atlantis": "Atlantis"} {
		if got := stateAbbrev(in); got != want {
			t.Errorf("stateAbbrev(%q) = %q, want %q", in, got, want)
		}
	}
}

// membersPage is a Congress.gov member list of two members.
const membersPage = `{"members":[{"bioguideId":"A000001"},{"bioguideId":"B000002"}],"pagination":{"count":2}}`

// A failed member write is logged and the run goes on; a failed list or a cancel stops it.
func TestSyncMembers_Failures(t *testing.T) {
	const path = "/member/congress/119"
	t.Run("list fails", func(t *testing.T) {
		s := newBillSyncService(t, &billSyncStore{}, &billAPI{failPaths: map[string]bool{path: true}})
		_, err := s.syncMembers(t.Context(), 119, func(context.Context, congress.Member, int) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "list members") {
			t.Errorf("syncMembers = %v, want the list failure", err)
		}
	})
	t.Run("a member write fails", func(t *testing.T) {
		s := newBillSyncService(t, &billSyncStore{}, &billAPI{bodies: map[string]string{path: membersPage}})
		var tried []string
		total, err := s.syncMembers(t.Context(), 119, func(_ context.Context, m congress.Member, _ int) error {
			tried = append(tried, m.BioguideID)
			return errFakeStore
		})
		if err != nil || total != 2 || len(tried) != 2 {
			t.Errorf("syncMembers = %d, %v after trying %v; want 2, nil after both", total, err, tried)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		s := newBillSyncService(t, &billSyncStore{}, &billAPI{bodies: map[string]string{path: membersPage}})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var tried int
		_, err := s.syncMembers(ctx, 119, func(context.Context, congress.Member, int) error {
			tried++
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) || tried != 1 {
			t.Errorf("syncMembers = %v after %d members, want context.Canceled after 1", err, tried)
		}
	})
}
