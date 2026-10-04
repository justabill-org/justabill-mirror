package sync

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

const testBillID = "hr-119-1"

var errFakeStore = errors.New("fake store failure")

// linkStore records the relationship writes of the sync functions. Any other
// PipelineStore method panics through the nil embedded interface.
type linkStore struct {
	repository.PipelineStore

	mu           gosync.Mutex
	failJSON     bool
	failLinks    bool
	jsonCols     map[string]json.RawMessage
	sponsorships map[string][]repository.BillSponsorshipRow
	committees   []repository.BillCommitteeRow
	subjects     []string
	relations    []repository.BillRelationRow
	linkCalls    int
}

func newLinkStore() *linkStore {
	return &linkStore{
		jsonCols:     map[string]json.RawMessage{},
		sponsorships: map[string][]repository.BillSponsorshipRow{},
	}
}

func (f *linkStore) UpdateBillJSON(_ context.Context, _, column string, value json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failJSON {
		return errFakeStore
	}
	f.jsonCols[column] = value
	return nil
}

func (f *linkStore) link(write func()) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkCalls++
	if f.failLinks {
		return errFakeStore
	}
	write()
	return nil
}

func (f *linkStore) ReplaceBillSponsorships(
	_ context.Context, _, role string, rows []repository.BillSponsorshipRow,
) error {
	return f.link(func() { f.sponsorships[role] = rows })
}

func (f *linkStore) ReplaceBillCommittees(_ context.Context, _ string, rows []repository.BillCommitteeRow) error {
	return f.link(func() { f.committees = rows })
}

func (f *linkStore) ReplaceBillSubjects(_ context.Context, _ string, names []string) error {
	return f.link(func() { f.subjects = names })
}

func (f *linkStore) ReplaceBillRelations(_ context.Context, _ string, rows []repository.BillRelationRow) error {
	return f.link(func() { f.relations = rows })
}

// Trimmed Congress.gov responses for HR 1 (119th).
func congressFixtures() map[string]string {
	return map[string]string{
		"/bill/119/hr/1/cosponsors": `{"cosponsors": [
		{"bioguideId": "A000001", "fullName": "Rep. A", "sponsorshipDate": "2025-01-05",
		 "isOriginalCosponsor": true},
		{"bioguideId": "B000002", "fullName": "Rep. B", "sponsorshipDate": "2025-02-10"},
		{"bioguideId": "C000003", "fullName": "Rep. C", "sponsorshipDate": "2025-02-11",
		 "sponsorshipWithdrawnDate": "2025-03-01"},
		{"bioguideId": "", "fullName": "Unknown"}]}`,
		"/bill/119/hr/1/committees": `{"committees": [
		{"name": "Ways and Means Committee", "systemCode": "hswm00", "chamber": "House",
		 "type": "Standing", "activities": [
			{"name": "Referred To", "date": "2025-01-03T15:03:43Z"},
			{"name": "Unknown", "date": "2025-05-22T10:48:46Z"},
			{"name": "Unknown", "date": "2025-05-22T10:22:11Z"},
			{"name": "Markup By", "date": "2025-02-01T10:00:00Z"}]}]}`,
		"/bill/119/hr/1/subjects": `{"subjects": {"legislativeSubjects": [
		{"name": "Taxation"}, {"name": "Health care costs and insurance"}]}}`,
		"/bill/119/hr/1/relatedbills": `{"relatedBills": [
		{"congress": 119, "number": 7, "type": "S", "relationshipDetails": [
			{"type": "Related bill", "identifiedBy": "CRS"}]},
		{"congress": 118, "number": 12, "type": "HR", "relationshipDetails": []}]}`,
	}
}

func newTestService(t *testing.T, store repository.PipelineStore) *Service {
	t.Helper()
	fixtures := congressFixtures()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := fixtures[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Service{
		store:  store,
		api:    congress.NewClientWithBaseURL(srv.Client(), srv.URL),
		logger: slog.New(slog.DiscardHandler),
	}
}

func syncRelationships(ctx context.Context, s *Service) error {
	return errors.Join(
		s.syncBillCosponsors(ctx, testBillID, 119, "hr", 1),
		s.syncBillCommittees(ctx, testBillID, 119, "hr", 1),
		s.syncBillSubjects(ctx, testBillID, 119, "hr", 1),
		s.syncBillRelatedBills(ctx, testBillID, 119, "hr", 1),
	)
}

func date(t *testing.T, layout, s string) *time.Time {
	t.Helper()
	d, err := time.Parse(layout, s)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func TestSyncRelationshipsDualWrite(t *testing.T) {
	store := newLinkStore()
	if err := syncRelationships(t.Context(), newTestService(t, store)); err != nil {
		t.Fatalf("syncRelationships: %v", err)
	}

	for _, col := range []string{"cosponsors", "committees", "subjects", "related_bills"} {
		if len(store.jsonCols[col]) == 0 {
			t.Errorf("JSON column %s not written", col)
		}
	}

	wantCosponsors := []repository.BillSponsorshipRow{
		{MemberID: "A000001", SponsoredDate: date(t, time.DateOnly, "2025-01-05"), IsOriginal: true},
		{MemberID: "B000002", SponsoredDate: date(t, time.DateOnly, "2025-02-10")},
	}
	if got := store.sponsorships[repository.SponsorRoleCosponsor]; !reflect.DeepEqual(got, wantCosponsors) {
		t.Errorf("cosponsor rows = %+v, want %+v", got, wantCosponsors)
	}

	wantCommittees := []repository.BillCommitteeRow{
		{
			CommitteeID: "hswm00", CommitteeName: "Ways and Means Committee",
			Chamber: new("House"), CommitteeType: new("Standing"),
			Activity: "Referred To", ActivityDate: date(t, time.RFC3339, "2025-01-03T15:03:43Z"),
		},
		{
			CommitteeID: "hswm00", CommitteeName: "Ways and Means Committee",
			Chamber: new("House"), CommitteeType: new("Standing"),
			Activity: "Unknown", ActivityDate: date(t, time.RFC3339, "2025-05-22T10:22:11Z"),
		},
		{
			CommitteeID: "hswm00", CommitteeName: "Ways and Means Committee",
			Chamber: new("House"), CommitteeType: new("Standing"),
			Activity: "Markup By", ActivityDate: date(t, time.RFC3339, "2025-02-01T10:00:00Z"),
		},
	}
	if !reflect.DeepEqual(store.committees, wantCommittees) {
		t.Errorf("committee rows = %+v, want %+v", store.committees, wantCommittees)
	}

	if want := []string{"Taxation", "Health care costs and insurance"}; !reflect.DeepEqual(store.subjects, want) {
		t.Errorf("subjects = %v, want %v", store.subjects, want)
	}

	// The 118th-Congress entry has no relationship detail, so it has no row.
	wantRelations := []repository.BillRelationRow{
		{RelatedBillID: "s-119-7", RelationType: "Related bill", IdentifiedBy: new("CRS")},
	}
	if !reflect.DeepEqual(store.relations, wantRelations) {
		t.Errorf("relation rows = %+v, want %+v", store.relations, wantRelations)
	}
}

func TestSyncRelationshipsSkipsLinksWhenJSONFails(t *testing.T) {
	store := newLinkStore()
	store.failJSON = true
	if err := syncRelationships(t.Context(), newTestService(t, store)); !errors.Is(err, errFakeStore) {
		t.Errorf("syncRelationships error = %v, want the store failure", err)
	}

	if store.linkCalls != 0 {
		t.Errorf("link writes = %d after failed JSON writes, want 0", store.linkCalls)
	}
}

func TestSyncRelationshipsKeepsJSONWhenLinksFail(t *testing.T) {
	store := newLinkStore()
	store.failLinks = true
	if err := syncRelationships(t.Context(), newTestService(t, store)); !errors.Is(err, errFakeStore) {
		t.Errorf("syncRelationships error = %v, want the store failure", err)
	}

	if store.linkCalls != 4 {
		t.Errorf("link writes = %d, want 4", store.linkCalls)
	}
	if len(store.jsonCols) != 4 {
		t.Errorf("JSON columns written = %d, want 4", len(store.jsonCols))
	}
}

func TestSyncBillSponsors(t *testing.T) {
	store := newLinkStore()
	s := newTestService(t, store)
	if err := s.syncBillSponsors(t.Context(), testBillID, &congress.BillDetail{
		IntroducedDate: "2025-01-03",
		Sponsors:       []congress.Sponsor{{BioguideID: "S000001"}, {FullName: "no id"}},
	}); err != nil {
		t.Fatalf("syncBillSponsors: %v", err)
	}

	want := []repository.BillSponsorshipRow{{MemberID: "S000001", SponsoredDate: date(t, time.DateOnly, "2025-01-03")}}
	if got := store.sponsorships[repository.SponsorRoleSponsor]; !reflect.DeepEqual(got, want) {
		t.Errorf("sponsor rows = %+v, want %+v", got, want)
	}
}

func TestSyncBillSponsorsClearsWhenNone(t *testing.T) {
	store := newLinkStore()
	s := newTestService(t, store)
	if err := s.syncBillSponsors(t.Context(), testBillID, &congress.BillDetail{}); err != nil {
		t.Fatalf("syncBillSponsors: %v", err)
	}

	got, called := store.sponsorships[repository.SponsorRoleSponsor]
	if !called || len(got) != 0 {
		t.Errorf("sponsor rows = %+v (written %v), want an empty replace", got, called)
	}
}

func TestLinkRowsSkipMissingIDs(t *testing.T) {
	committees := []congress.Committee{
		{Name: "No code", Activities: []congress.CommitteeActivity{{Name: "Referred To"}}},
		{Name: "No activity", SystemCode: "ssfi00"},
		{Name: "Blank activity", SystemCode: "hsju00", Activities: []congress.CommitteeActivity{{Name: ""}}},
	}
	if rows, skipped := committeeRows(committees); len(rows) != 0 || skipped != 3 {
		t.Errorf("committeeRows = %d rows, %d skipped; want 0, 3", len(rows), skipped)
	}

	related := []congress.RelatedBillEntry{
		{Congress: 119, Type: "HR", RelationshipDetails: []congress.RelatedBillRelationship{{Type: "Identical bill"}}},
		{Congress: 119, Number: 3, Type: "HR", RelationshipDetails: []congress.RelatedBillRelationship{{}}},
	}
	if rows, skipped := relationRows(related); len(rows) != 0 || skipped != 2 {
		t.Errorf("relationRows = %d rows, %d skipped; want 0, 2", len(rows), skipped)
	}
}

func TestParseAPITime(t *testing.T) {
	cases := map[string]string{
		"2025-01-03":           "2025-01-03T00:00:00Z",
		"2025-01-03T15:03:43Z": "2025-01-03T15:03:43Z",
		"":                     "",
		"January 3":            "",
	}
	for in, want := range cases {
		got := ""
		if p := parseAPITime(in); p != nil {
			got = p.Format(time.RFC3339)
		}
		if got != want {
			t.Errorf("parseAPITime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRelatedBillIDMatchesSyncedBillID(t *testing.T) {
	got := relatedBillID(congress.RelatedBillEntry{Congress: 118, Number: 42, Type: "HJRES"})
	if want := "hjres-118-42"; got != want {
		t.Errorf("relatedBillID = %q, want %q", got, want)
	}
}
