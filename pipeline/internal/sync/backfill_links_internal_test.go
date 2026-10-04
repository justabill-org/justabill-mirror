package sync

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

// backfillStore serves bills to BackfillLinks and records its link writes and
// checkpoints. Any other PipelineStore method panics through the nil embedded
// interface.
type backfillStore struct {
	repository.PipelineStore

	bills       []repository.BillLinkSource
	state       *repository.SyncStateRow
	checkpoints []string // LastOffset of every checkpoint and success; "" for nil
	listAfters  []string
	failBill    string
	errors      int

	sponsorships map[string][]repository.BillSponsorshipRow // key: bill|role
	committees   map[string][]repository.BillCommitteeRow
	subjects     map[string][]string
	relations    map[string][]repository.BillRelationRow
}

func newBackfillStore(bills ...repository.BillLinkSource) *backfillStore {
	return &backfillStore{
		bills:        bills,
		sponsorships: map[string][]repository.BillSponsorshipRow{},
		committees:   map[string][]repository.BillCommitteeRow{},
		subjects:     map[string][]string{},
		relations:    map[string][]repository.BillRelationRow{},
	}
}

func (f *backfillStore) GetSyncState(_ context.Context, _ string, _ int) (*repository.SyncStateRow, error) {
	return f.state, nil
}

func (f *backfillStore) SaveSyncCheckpoint(
	_ context.Context,
	step string,
	congress int,
	offset *string,
	items int,
) error {
	f.setState(repository.SyncStateRow{Step: step, Congress: congress, LastOffset: offset, ItemsSynced: items})
	return nil
}

// RecordSyncSuccess clears the checkpoint, like the Spanner store.
func (f *backfillStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.setState(repository.SyncStateRow{Step: run.Step, Congress: run.Congress, ItemsSynced: run.ItemsSynced})
	return nil
}

func (f *backfillStore) setState(st repository.SyncStateRow) {
	f.state = &st
	offset := ""
	if st.LastOffset != nil {
		offset = *st.LastOffset
	}
	f.checkpoints = append(f.checkpoints, offset)
}

func (f *backfillStore) RecordSyncFailure(context.Context, repository.SyncRun) error {
	f.errors++
	return nil
}

func (f *backfillStore) ListBillLinkSources(
	_ context.Context, _ int, after string, limit int,
) ([]repository.BillLinkSource, error) {
	f.listAfters = append(f.listAfters, after)
	i := 0
	for i < len(f.bills) && f.bills[i].BillID <= after {
		i++
	}
	return f.bills[i:min(i+limit, len(f.bills))], nil
}

func (f *backfillStore) write(billID string, store func()) error {
	if billID == f.failBill {
		return errFakeStore
	}
	store()
	return nil
}

func (f *backfillStore) ReplaceBillSponsorships(
	_ context.Context, billID, role string, rows []repository.BillSponsorshipRow,
) error {
	return f.write(billID, func() { f.sponsorships[billID+"|"+role] = rows })
}

func (f *backfillStore) ReplaceBillCommittees(
	_ context.Context, billID string, rows []repository.BillCommitteeRow,
) error {
	return f.write(billID, func() { f.committees[billID] = rows })
}

func (f *backfillStore) ReplaceBillSubjects(_ context.Context, billID string, names []string) error {
	return f.write(billID, func() { f.subjects[billID] = names })
}

func (f *backfillStore) ReplaceBillRelations(
	_ context.Context, billID string, rows []repository.BillRelationRow,
) error {
	return f.write(billID, func() { f.relations[billID] = rows })
}

func newBackfillService(store repository.PipelineStore) *Service {
	return &Service{store: store, logger: slog.New(slog.DiscardHandler)}
}

// linkSource builds a bill whose only stored JSON is a subject list.
func linkSource(billID string) repository.BillLinkSource {
	return repository.BillLinkSource{BillID: billID, Subjects: json.RawMessage(`["Taxation"]`)}
}

func TestBackfillLinksRebuildsFromStoredJSON(t *testing.T) {
	introduced := date(t, time.DateOnly, "2025-01-03")
	store := newBackfillStore(repository.BillLinkSource{
		BillID:         testBillID,
		IntroducedDate: introduced,
		Sponsors:       json.RawMessage(`[{"bioguideId":"A000001","fullName":"Rep. A"}]`),
		Cosponsors: json.RawMessage(`[
			{"bioguideId":"B000002","sponsorshipDate":"2025-01-05","isOriginalCosponsor":true},
			{"bioguideId":"C000003","sponsorshipDate":"2025-02-11","sponsorshipWithdrawnDate":"2025-03-01"},
			{"bioguideId":""}]`),
		Committees: json.RawMessage(`[{"name":"Ways and Means Committee","systemCode":"hswm00",
			"activities":[{"name":"Referred To","date":"2025-01-03T15:03:43Z"}]}]`),
		Subjects: json.RawMessage(`["Taxation"]`),
		RelatedBills: json.RawMessage(
			`[{"congress":119,"number":7,"type":"S","relationshipDetails":[{"type":"Related bill"}]}]`,
		),
	})

	if err := newBackfillService(store).BackfillLinks(t.Context(), 119, 10); err != nil {
		t.Fatalf("BackfillLinks: %v", err)
	}

	wantSponsors := []repository.BillSponsorshipRow{{MemberID: "A000001", SponsoredDate: introduced}}
	if got := store.sponsorships[testBillID+"|"+repository.SponsorRoleSponsor]; !reflect.DeepEqual(got, wantSponsors) {
		t.Errorf("sponsors = %+v, want %+v", got, wantSponsors)
	}
	wantCosponsors := []repository.BillSponsorshipRow{
		{MemberID: "B000002", SponsoredDate: date(t, time.DateOnly, "2025-01-05"), IsOriginal: true},
	}
	if got := store.sponsorships[testBillID+"|"+repository.SponsorRoleCosponsor]; !reflect.DeepEqual(
		got,
		wantCosponsors,
	) {
		t.Errorf("cosponsors = %+v, want %+v (withdrawn and ID-less cosponsors dropped)", got, wantCosponsors)
	}
	if got := store.committees[testBillID]; len(got) != 1 || got[0].CommitteeID != "hswm00" {
		t.Errorf("committees = %+v, want one hswm00 row", got)
	}
	if got := store.subjects[testBillID]; !slices.Equal(got, []string{"Taxation"}) {
		t.Errorf("subjects = %q, want [Taxation]", got)
	}
	if got := store.relations[testBillID]; len(got) != 1 || got[0].RelatedBillID != "s-119-7" {
		t.Errorf("relations = %+v, want one s-119-7 row", got)
	}
	if store.state == nil || store.state.LastOffset != nil || store.state.ItemsSynced != 1 {
		t.Errorf("final state = %+v, want no offset and 1 bill", store.state)
	}
}

func TestBackfillLinksSkipsNullAndUnreadableColumns(t *testing.T) {
	store := newBackfillStore(repository.BillLinkSource{
		BillID:       testBillID,
		Sponsors:     json.RawMessage(`null`),
		Cosponsors:   json.RawMessage(`{"not":"a list"}`),
		Subjects:     json.RawMessage(`["Taxation"]`),
		RelatedBills: nil,
	})

	if err := newBackfillService(store).BackfillLinks(t.Context(), 119, 10); err != nil {
		t.Fatalf("BackfillLinks: %v", err)
	}
	if len(store.sponsorships) != 0 || len(store.committees) != 0 || len(store.relations) != 0 {
		t.Errorf("wrote links for NULL or unreadable columns: sponsorships %v, committees %v, relations %v",
			store.sponsorships, store.committees, store.relations)
	}
	if got := store.subjects[testBillID]; !slices.Equal(got, []string{"Taxation"}) {
		t.Errorf("subjects = %q, want [Taxation] despite the bad cosponsors column", got)
	}
}

func TestBackfillLinksPagesAndCheckpoints(t *testing.T) {
	store := newBackfillStore(linkSource("hr-119-1"), linkSource("hr-119-2"), linkSource("s-119-1"),
		linkSource("s-119-2"))

	if err := newBackfillService(store).BackfillLinks(t.Context(), 119, 2); err != nil {
		t.Fatalf("BackfillLinks: %v", err)
	}
	if want := []string{"", "hr-119-2", "s-119-2"}; !slices.Equal(store.listAfters, want) {
		t.Errorf("list calls after %q, want %q", store.listAfters, want)
	}
	if want := []string{"hr-119-2", "s-119-2", ""}; !slices.Equal(store.checkpoints, want) {
		t.Errorf("checkpoints = %q, want %q (cleared when done)", store.checkpoints, want)
	}
	if len(store.subjects) != len(store.bills) || store.state.ItemsSynced != len(store.bills) {
		t.Errorf("backfilled %d bills, state %d; want %d", len(store.subjects), store.state.ItemsSynced,
			len(store.bills))
	}
}

func TestBackfillLinksResumesFromCheckpoint(t *testing.T) {
	store := newBackfillStore(linkSource("hr-119-1"), linkSource("hr-119-2"), linkSource("s-119-1"))
	after := "hr-119-2"
	store.state = &repository.SyncStateRow{Step: stepLinks, Congress: 119, LastOffset: &after, ItemsSynced: 2}

	if err := newBackfillService(store).BackfillLinks(t.Context(), 119, 2); err != nil {
		t.Fatalf("BackfillLinks: %v", err)
	}
	if got := slices.Sorted(maps.Keys(store.subjects)); !slices.Equal(got, []string{"s-119-1"}) {
		t.Errorf("backfilled %q, want only the bill after the checkpoint", got)
	}
	if store.state.LastOffset != nil || store.state.ItemsSynced != 3 {
		t.Errorf("final state = %+v, want no offset and 3 bills", store.state)
	}
}

func TestBackfillLinksStopsOnWriteFailure(t *testing.T) {
	store := newBackfillStore(linkSource("hr-119-1"), linkSource("hr-119-2"), linkSource("s-119-1"))
	store.failBill = "s-119-1"

	err := newBackfillService(store).BackfillLinks(t.Context(), 119, 2)
	if !errors.Is(err, errFakeStore) || !strings.Contains(err.Error(), "s-119-1") {
		t.Fatalf("err = %v, want the store failure for s-119-1", err)
	}
	if want := []string{"hr-119-2"}; !slices.Equal(store.checkpoints, want) {
		t.Errorf("checkpoints = %q, want %q so a rerun resumes after the last full page", store.checkpoints, want)
	}
	if store.errors != 1 {
		t.Errorf("recorded %d sync errors, want 1", store.errors)
	}
}
