package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/legislators"
)

// pastTermsStore records member and term writes and applies UpdateMemberLisID the way Spanner
// does (only when lis_id is NULL). Sync-state methods come from stepStore.
type pastTermsStore struct {
	*stepStore

	roster    []string
	lis       map[string]string // bioguide -> stored LIS ID
	members   []repository.MemberRow
	terms     []repository.MemberTermRow
	lisWrites []string // "bioguide=lis"
	failTerm  string   // UpsertMemberTerm fails for this bioguide ID
}

func newPastTermsStore(roster []string, lis map[string]string) *pastTermsStore {
	if lis == nil {
		lis = map[string]string{}
	}
	return &pastTermsStore{stepStore: &stepStore{}, roster: roster, lis: lis}
}

func (f *pastTermsStore) ListMemberIDs(context.Context) ([]string, error) { return f.roster, nil }

func (f *pastTermsStore) LISLookup(context.Context) (map[string]string, error) {
	byLIS := make(map[string]string, len(f.lis))
	for member, lis := range f.lis {
		byLIS[lis] = member
	}
	return byLIS, nil
}

func (f *pastTermsStore) UpsertMember(_ context.Context, m repository.MemberRow) error {
	f.members = append(f.members, m)
	return nil
}

func (f *pastTermsStore) UpsertMemberTerm(_ context.Context, t repository.MemberTermRow) error {
	if t.MemberID == f.failTerm {
		return errors.New("spanner: deadline exceeded")
	}
	f.terms = append(f.terms, t)
	return nil
}

func (f *pastTermsStore) UpdateMemberLisID(_ context.Context, bioguideID, lisID string) error {
	f.lisWrites = append(f.lisWrites, bioguideID+"="+lisID)
	if f.lis[bioguideID] == "" {
		f.lis[bioguideID] = lisID
	}
	return nil
}

// pastTermsService returns a service reading congress-legislators from handler (nil: the
// fixture files in ../legislators/testdata) and logging to the returned buffer.
func pastTermsService(t *testing.T, store repository.PipelineStore, handler http.Handler) (*Service, *bytes.Buffer) {
	t.Helper()
	if handler == nil {
		handler = http.FileServer(http.Dir("../legislators/testdata"))
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	s := &Service{store: store, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	s.SetLegislators(legislators.New(srv.URL))
	return s, &logs
}

func utcDay(y int, m time.Month, d int) *time.Time {
	return new(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
}

func TestSyncPastMemberTerms_118(t *testing.T) {
	// Norton isn't in the roster. Manchin's LIS ID is stored already, and Ricketts's belongs to
	// another member (a bad earlier match), so only Schiff's is set.
	store := newPastTermsStore(
		[]string{"M001183", "M001212", "R000618", "S001150", "T000461", "Q000001"},
		map[string]string{"M001183": "S338", "Q000001": "S423"},
	)
	s, logs := pastTermsService(t, store, nil)

	if err := s.SyncPastMemberTerms(t.Context(), 118); err != nil {
		t.Fatalf("SyncPastMemberTerms: %v", err)
	}

	start, end := utcDay(2023, time.January, 3), utcDay(2025, time.January, 3)
	want := []repository.MemberTermRow{
		{MemberID: "M001183", Congress: 118, Chamber: chamberSenate, State: "WV", Party: "I",
			StartDate: start, EndDate: end},
		{MemberID: "M001212", Congress: 118, Chamber: chamberHouse, State: "AL", District: new(2), Party: "R",
			StartDate: start, EndDate: end},
		{MemberID: "R000618", Congress: 118, Chamber: chamberSenate, State: "NE", Party: "R",
			StartDate: utcDay(2023, time.January, 23), EndDate: end},
		{MemberID: "S001150", Congress: 118, Chamber: chamberHouse, State: "CA", District: new(30), Party: "D",
			StartDate: start, EndDate: utcDay(2024, time.December, 8)},
		{MemberID: "S001150", Congress: 118, Chamber: chamberSenate, State: "CA", Party: "D",
			StartDate: utcDay(2024, time.December, 9), EndDate: end},
	}
	if !reflect.DeepEqual(store.terms, want) {
		t.Errorf("terms =\n%+v\nwant\n%+v", store.terms, want)
	}
	if want := []string{"S001150=S427"}; !reflect.DeepEqual(store.lisWrites, want) {
		t.Errorf("lis_id writes = %v, want %v (once per member, never over a stored or taken ID)",
			store.lisWrites, want)
	}
	if store.lis["R000618"] != "" {
		t.Errorf("Ricketts got lis_id %q, which another member holds", store.lis["R000618"])
	}

	wantRun := repository.SyncRun{Step: stepMemberTerms, Congress: 118, ItemsSynced: 5}
	if len(store.successes) != 1 {
		t.Fatalf("successes = %+v, want one", store.successes)
	}
	if got := store.successes[0]; got.Step != wantRun.Step || got.Congress != wantRun.Congress ||
		got.ItemsSynced != wantRun.ItemsSynced {
		t.Errorf("success = %+v, want %+v", got, wantRun)
	}
	for _, line := range []string{
		"set lis_id from congress-legislators", "lis_id=S427",
		"member not in roster", "bioguide_id=N000147",
		"lis_id already belongs to another member",
		"not_in_roster=1", "invalid=5", "lis_ids_set=1",
	} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("logs lack %q:\n%s", line, logs.String())
		}
	}
}

// A stored LIS ID that disagrees with the dataset is kept and logged.
func TestSyncPastMemberTerms_KeepsAStoredLisID(t *testing.T) {
	store := newPastTermsStore([]string{"S001150"}, map[string]string{"S001150": "S999"})
	s, logs := pastTermsService(t, store, nil)
	if err := s.SyncPastMemberTerms(t.Context(), 118); err != nil {
		t.Fatal(err)
	}
	if len(store.lisWrites) != 0 {
		t.Errorf("lis_id writes = %v, want none", store.lisWrites)
	}
	if !strings.Contains(logs.String(), "stored lis_id differs") {
		t.Errorf("the mismatch wasn't logged:\n%s", logs.String())
	}
}

func TestSyncPastMemberTerms_FetchFailureWritesNothing(t *testing.T) {
	store := newPastTermsStore([]string{"S001150"}, nil)
	s, _ := pastTermsService(t, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "historical.json") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.FileServer(http.Dir("../legislators/testdata")).ServeHTTP(w, r)
	}))

	if err := s.SyncPastMemberTerms(t.Context(), 118); err == nil {
		t.Fatal("SyncPastMemberTerms returned no error when the historical file failed")
	}
	if len(store.terms) != 0 || len(store.lisWrites) != 0 {
		t.Errorf("wrote %d terms and %d LIS IDs after a failed fetch", len(store.terms), len(store.lisWrites))
	}
	if len(store.failures) != 1 || store.failures[0].Step != stepMemberTerms {
		t.Errorf("failures = %+v, want one member_terms failure", store.failures)
	}
}

// A term that fails to write doesn't stop the others, but the step fails so it isn't recorded
// as done.
func TestSyncPastMemberTerms_WriteFailureFailsTheStep(t *testing.T) {
	store := newPastTermsStore([]string{"M001183", "M001212", "S001150"}, nil)
	store.failTerm = "M001212"
	s, _ := pastTermsService(t, store, nil)

	err := s.SyncPastMemberTerms(t.Context(), 118)
	if err == nil || !strings.Contains(err.Error(), "1 of 6 member terms failed") {
		t.Fatalf("error = %v, want 1 of 6 member terms failed", err)
	}
	if len(store.terms) != 3 {
		t.Errorf("wrote %d terms, want 3 (Manchin, and Schiff's two)", len(store.terms))
	}
	if len(store.successes) != 0 || len(store.failures) != 1 {
		t.Errorf("successes = %d, failures = %d; want 0 and 1", len(store.successes), len(store.failures))
	}
}

// The roster-only mode writes names and photos, and no terms or senator detail calls.
func TestSyncMemberRoster_WritesNamesOnly(t *testing.T) {
	store := newPastTermsStore(nil, nil)
	var paths []string
	s := serviceWithAPI(t, store, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		fmt.Fprint(w, `{"members": [
			{"bioguideId": "S001150", "name": "Schiff, Adam B.", "state": "California",
			 "partyName": "Democratic", "depiction": {"imageUrl": "https://example.test/s001150.jpg"}},
			{"bioguideId": "M001212", "name": "Moore, Barry", "state": "Alabama", "district": 1,
			 "partyName": "Republican"}
		], "pagination": {"count": 2}}`)
	})

	if err := s.SyncMemberRoster(t.Context(), 118); err != nil {
		t.Fatal(err)
	}
	want := []repository.MemberRow{
		{BioguideID: "S001150", FirstName: "Adam B.", LastName: "Schiff",
			PhotoURL: new("https://example.test/s001150.jpg")},
		{BioguideID: "M001212", FirstName: "Barry", LastName: "Moore"},
	}
	if !reflect.DeepEqual(store.members, want) {
		t.Errorf("members = %+v, want %+v", store.members, want)
	}
	if len(store.terms) != 0 || len(store.lisWrites) != 0 {
		t.Errorf("roster-only wrote %d terms and %d LIS IDs, want none", len(store.terms), len(store.lisWrites))
	}
	if want := []string{"/member/congress/118"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("requests = %v, want only the member list %v", paths, want)
	}
	if len(store.successes) != 1 || store.successes[0].Step != stepMembers {
		t.Errorf("successes = %+v, want one members run", store.successes)
	}
}

func TestCongressDates(t *testing.T) {
	for _, tt := range []struct {
		congress   int
		start, end string
	}{
		{118, "2023-01-03", "2025-01-03"},
		{119, "2025-01-03", "2027-01-03"},
	} {
		start, end := congressDates(tt.congress)
		if got := start.Format(time.DateOnly) + " " + end.Format(time.DateOnly); got != tt.start+" "+tt.end {
			t.Errorf("congressDates(%d) = %s, want %s %s", tt.congress, got, tt.start, tt.end)
		}
	}
}
