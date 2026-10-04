package spannerdb_test

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

const crsOrphanBill = "s-119-999"

type crsFixture struct {
	store  *spannerdb.PipelineStoreImpl
	bills  *spannerdb.BillRepository
	client *spanner.Client
}

func newCRSFixture(t *testing.T) crsFixture {
	t.Helper()
	store, client := newLinkStore(t)
	return crsFixture{store: store, bills: spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client}), client: client}
}

func crsTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func crsDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// crsRow builds a summary row; the text and hash name the bill and version so they're told apart.
func crsRow(t *testing.T, bill, code, actionDate, desc, chamber, crsUpdated string) repository.CRSSummaryRow {
	t.Helper()
	row := repository.CRSSummaryRow{
		BillID:          bill,
		VersionCode:     code,
		ActionDate:      crsDate(t, actionDate),
		ActionDesc:      desc,
		TextHTML:        "<p><strong>" + desc + "</strong></p><p>Summary " + bill + "/" + code + ".</p>",
		Text:            desc + "\n\nSummary " + bill + "/" + code + ".",
		ContentHash:     fmt.Sprintf("%064s", bill+code),
		CRSUpdatedAt:    crsTime(t, crsUpdated),
		SourceUpdatedAt: crsTime(t, crsUpdated).Add(time.Minute),
	}
	if chamber != "" {
		row.Chamber = &chamber
	}
	return row
}

// hr1CRSVersions are H.R. 1's five CRS summaries (119th) as Congress.gov listed them on
// 2026-09-28, in the list's updateDate order. Version codes aren't ordered: 49 is Public Law.
// 00 and 07 share an action date, and CRS wrote 07 later.
func hr1CRSVersions(t *testing.T) []repository.CRSSummaryRow {
	t.Helper()
	bill := testdb.FixtureHouseBill
	return []repository.CRSSummaryRow{
		crsRow(t, bill, "00", "2025-05-20", "Introduced in House", "House", "2025-05-22T17:15:29Z"),
		crsRow(t, bill, "53", "2025-05-22", "Passed House", "House", "2025-06-16T13:42:01Z"),
		crsRow(t, bill, "07", "2025-05-20", "Reported to House", "House", "2025-09-22T12:34:08Z"),
		crsRow(t, bill, "49", "2025-07-04", "Public Law", "", "2025-10-06T21:40:39Z"),
		crsRow(t, bill, "55", "2025-07-01", "Passed Senate", "Senate", "2025-10-06T21:56:04Z"),
	}
}

func TestGetCRSSummary_LatestIsTheLatestAction(t *testing.T) {
	f := newCRSFixture(t)
	ctx := t.Context()
	if err := f.store.UpsertCRSSummaries(ctx, hr1CRSVersions(t)); err != nil {
		t.Fatalf("UpsertCRSSummaries: %v", err)
	}

	got, err := f.bills.GetCRSSummary(ctx, testdb.FixtureHouseBill)
	if err != nil {
		t.Fatalf("GetCRSSummary: %v", err)
	}
	want := &model.CRSSummary{
		BillID:      testdb.FixtureHouseBill,
		VersionCode: "49",
		ActionDate:  crsDate(t, "2025-07-04"),
		ActionDesc:  "Public Law",
		Text:        "Public Law\n\nSummary hr-119-1/49.",
		UpdatedAt:   crsTime(t, "2025-10-06T21:40:39Z"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetCRSSummary = %+v, want %+v", got, want)
	}
}

func TestGetCRSSummary_SameActionDateLaterSummaryWins(t *testing.T) {
	f := newCRSFixture(t)
	ctx := t.Context()
	versions := hr1CRSVersions(t)
	// Written in either order, "Reported" (07, written later) beats "Introduced" (00).
	for _, rows := range [][]repository.CRSSummaryRow{
		{versions[0], versions[2]},
		{versions[2], versions[0]},
	} {
		for i := range rows {
			rows[i].BillID = testdb.FixtureSenateBill
		}
		if err := f.store.UpsertCRSSummaries(ctx, rows); err != nil {
			t.Fatalf("UpsertCRSSummaries: %v", err)
		}
		got, err := f.bills.GetCRSSummary(ctx, testdb.FixtureSenateBill)
		if err != nil {
			t.Fatalf("GetCRSSummary: %v", err)
		}
		if got == nil || got.VersionCode != "07" || got.Chamber == nil || *got.Chamber != "House" {
			t.Errorf("GetCRSSummary = %+v, want version 07 in the House", got)
		}
	}
}

func TestGetCRSSummary_NoneIsNil(t *testing.T) {
	f := newCRSFixture(t)
	got, err := f.bills.GetCRSSummary(t.Context(), testdb.FixtureHouseBill)
	if err != nil || got != nil {
		t.Errorf("GetCRSSummary = %+v, %v; want nil, nil", got, err)
	}
}

func TestGetCRSLeads_APageInOneRead(t *testing.T) {
	f := newCRSFixture(t)
	ctx := t.Context()
	versions := hr1CRSVersions(t)
	// The Senate bill has two versions on one action date: the one CRS wrote later wins.
	senate := []repository.CRSSummaryRow{
		crsRow(t, testdb.FixtureSenateBill, "07", "2025-05-20", "Reported to Senate", "Senate", "2025-09-22T12:34:08Z"),
		crsRow(
			t,
			testdb.FixtureSenateBill,
			"00",
			"2025-05-20",
			"Introduced in Senate",
			"Senate",
			"2025-05-22T17:15:29Z",
		),
	}
	// The orphan's summary opens with its short title, and the lead is the paragraph after it.
	orphan := crsRow(t, crsOrphanBill, "00", "2025-01-03", "Introduced in Senate", "", "2025-02-01T10:00:00Z")
	orphan.Text = hr187CRS
	rows := append(append(versions, senate...), orphan)
	if err := f.store.UpsertCRSSummaries(ctx, rows); err != nil {
		t.Fatalf("UpsertCRSSummaries: %v", err)
	}

	t.Run("bills with and without a summary, one repeated", func(t *testing.T) {
		got, err := f.bills.GetCRSLeads(ctx, []string{
			testdb.FixtureHouseBill, testdb.FixtureSenateBill, "hr-119-777", crsOrphanBill, testdb.FixtureHouseBill,
		})
		if err != nil {
			t.Fatalf("GetCRSLeads: %v", err)
		}
		want := map[string]model.CardCRS{
			testdb.FixtureHouseBill: {
				VersionCode: "49", ActionDate: crsDate(t, "2025-07-04"), ActionDesc: "Public Law",
				// No paragraph of crsRow's text reads as a sentence, so the lead is the first.
				Lead: "Public Law",
			},
			testdb.FixtureSenateBill: {
				VersionCode: "07", ActionDate: crsDate(t, "2025-05-20"), ActionDesc: "Reported to Senate",
				Lead: "Reported to Senate",
			},
			crsOrphanBill: {
				VersionCode: "00", ActionDate: crsDate(t, "2025-01-03"), ActionDesc: "Introduced in Senate",
				Lead: hr187CRSLead,
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("GetCRSLeads =\n%+v\nwant\n%+v", got, want)
		}
	})

	t.Run("no ids", func(t *testing.T) {
		for _, ids := range [][]string{nil, {}} {
			got, err := f.bills.GetCRSLeads(ctx, ids)
			if err != nil || got == nil || len(got) != 0 {
				t.Errorf("GetCRSLeads(%v) = %v, %v; want an empty map", ids, got, err)
			}
		}
	})
}

func TestUpsertCRSSummaries_OrphanBillIsStored(t *testing.T) {
	f := newCRSFixture(t)
	ctx := t.Context()
	row := crsRow(t, crsOrphanBill, "00", "2025-03-04", "Introduced in Senate", "Senate", "2025-06-01T10:00:00Z")
	if err := f.store.UpsertCRSSummaries(ctx, []repository.CRSSummaryRow{row}); err != nil {
		t.Fatalf("UpsertCRSSummaries for a bill with no bills row: %v", err)
	}
	got, err := f.bills.GetCRSSummary(ctx, crsOrphanBill)
	if err != nil {
		t.Fatalf("GetCRSSummary: %v", err)
	}
	if got == nil || got.VersionCode != "00" || got.Text != row.Text {
		t.Errorf("GetCRSSummary = %+v, want the orphan's version 00", got)
	}
}

func TestUpsertCRSSummaries_SecondWriteReplacesTheRow(t *testing.T) {
	f := newCRSFixture(t)
	ctx := t.Context()
	first := crsRow(t, testdb.FixtureHouseBill, "00", "2025-05-20", "Introduced in House", "House",
		"2025-05-22T17:15:29Z")
	second := first
	second.TextHTML = "<p>Corrected.</p>"
	second.Text = "Corrected."
	second.ContentHash = strings.Repeat("c", 64)
	second.CRSUpdatedAt = crsTime(t, "2025-05-30T18:00:00Z")
	second.SourceUpdatedAt = crsTime(t, "2025-05-30T18:28:45Z")
	second.Chamber = nil
	for _, row := range []repository.CRSSummaryRow{first, second} {
		if err := f.store.UpsertCRSSummaries(ctx, []repository.CRSSummaryRow{row}); err != nil {
			t.Fatalf("UpsertCRSSummaries: %v", err)
		}
	}

	got := queryStrings(t, f.client, `SELECT version_code, CAST(action_date AS STRING), action_desc,
		COALESCE(chamber, '-'), text_html, text, content_hash,
		FORMAT_TIMESTAMP('%FT%TZ', crs_updated_at, 'UTC'), FORMAT_TIMESTAMP('%FT%TZ', source_updated_at, 'UTC'),
		CAST(synced_at IS NOT NULL AS STRING)
		FROM bill_crs_summaries WHERE bill_id = @bill`, map[string]any{"bill": testdb.FixtureHouseBill})
	want := []string{strings.Join([]string{"00", "2025-05-20", "Introduced in House", "-", "<p>Corrected.</p>",
		"Corrected.", second.ContentHash, "2025-05-30T18:00:00Z", "2025-05-30T18:28:45Z", "true"}, "|")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
}

func TestUpsertCRSSummaries_EmptyKeyWritesNothing(t *testing.T) {
	f := newCRSFixture(t)
	ctx := t.Context()
	good := crsRow(t, crsOrphanBill, "00", "2025-03-04", "Introduced in Senate", "", "2025-06-01T10:00:00Z")
	for _, bad := range []repository.CRSSummaryRow{
		{BillID: crsOrphanBill},
		{VersionCode: "00"},
	} {
		err := f.store.UpsertCRSSummaries(ctx, []repository.CRSSummaryRow{good, bad})
		if err == nil || !strings.Contains(err.Error(), "empty bill id or version code") {
			t.Errorf("UpsertCRSSummaries(%+v) error = %v, want empty key", bad, err)
		}
	}
	got, err := f.bills.GetCRSSummary(ctx, crsOrphanBill)
	if err != nil || got != nil {
		t.Errorf("after a rejected batch GetCRSSummary = %+v, %v; want nothing written", got, err)
	}
}

func TestUpsertCRSSummaries_ManyRowsCommitInBatches(t *testing.T) {
	f := newCRSFixture(t)
	ctx := t.Context()
	const n = 250 // one full list page: three commits of at most 100
	rows := make([]repository.CRSSummaryRow, n)
	for i := range rows {
		rows[i] = crsRow(t, fmt.Sprintf("hres-119-%d", i+1), "00", "2025-04-01", "Introduced in House", "House",
			"2025-06-01T10:00:00Z")
	}
	if err := f.store.UpsertCRSSummaries(ctx, rows); err != nil {
		t.Fatalf("UpsertCRSSummaries: %v", err)
	}
	got := queryStrings(t, f.client, `SELECT CAST(COUNT(*) AS STRING) FROM bill_crs_summaries`, nil)
	if want := []string{strconv.Itoa(n)}; !reflect.DeepEqual(got, want) {
		t.Errorf("row count = %v, want %v", got, want)
	}
}

func TestGetCRSSummary_ReadError(t *testing.T) {
	f := newCRSFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.bills.GetCRSSummary(ctx, testdb.FixtureHouseBill); err == nil {
		t.Error("GetCRSSummary on a canceled context: want an error")
	}
	if err := f.store.UpsertCRSSummaries(ctx, hr1CRSVersions(t)); err == nil {
		t.Error("UpsertCRSSummaries on a canceled context: want an error")
	}
}

func TestStoredCRSSummaries_HashesAndBills(t *testing.T) {
	f := newCRSFixture(t)
	ctx := t.Context()
	orphan := crsRow(t, crsOrphanBill, "00", "2025-03-04", "Introduced in Senate", "Senate", "2025-06-01T10:00:00Z")
	rows := append(hr1CRSVersions(t), orphan)
	if err := f.store.UpsertCRSSummaries(ctx, rows); err != nil {
		t.Fatalf("UpsertCRSSummaries: %v", err)
	}

	got, err := f.store.StoredCRSSummaries(ctx, []string{testdb.FixtureHouseBill, crsOrphanBill, "hr-119-424242"})
	if err != nil {
		t.Fatalf("StoredCRSSummaries: %v", err)
	}
	if len(got.Versions) != len(rows) {
		t.Errorf("versions = %d, want %d", len(got.Versions), len(rows))
	}
	for _, r := range rows {
		v, ok := got.Versions[repository.CRSSummaryKey{BillID: r.BillID, VersionCode: r.VersionCode}]
		if !ok || v.ContentHash != r.ContentHash || !v.CRSUpdatedAt.Equal(r.CRSUpdatedAt) {
			t.Errorf("version %s/%s = %+v (found %v), want hash %s at %s",
				r.BillID, r.VersionCode, v, ok, r.ContentHash, r.CRSUpdatedAt)
		}
	}
	// The fixture has H.R. 1; the orphan's summary is stored but its bill isn't.
	if want := map[string]bool{testdb.FixtureHouseBill: true}; !reflect.DeepEqual(got.Bills, want) {
		t.Errorf("bills = %v, want %v", got.Bills, want)
	}
}

func TestStoredCRSSummaries_NoBillsReadsNothing(t *testing.T) {
	f := newCRSFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // no bills means no query, so even a canceled context succeeds
	got, err := f.store.StoredCRSSummaries(ctx, nil)
	if err != nil || len(got.Versions) != 0 || len(got.Bills) != 0 {
		t.Errorf("StoredCRSSummaries(nil) = %+v, %v; want empty maps and no error", got, err)
	}
	if _, err = f.store.StoredCRSSummaries(ctx, []string{testdb.FixtureHouseBill}); err == nil {
		t.Error("StoredCRSSummaries on a canceled context: want an error")
	}
}
