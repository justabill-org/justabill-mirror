package spannerdb_test

import (
	"bytes"
	"compress/gzip"
	"testing"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// Bills of the text-queue tests (docs/design/746-spanner-review.md, Review Focus 5): each has the
// versions ih < rh, both with text; their rh text is stored the way a queue must not trip on.
const (
	gzOnlyBill    = 41 // rh's download is only in content_gz, content is empty
	refetchedBill = 42 // rh's fetched_at is NULL: queued for a refetch
	plainBill     = 43 // rh's text is in content, fetched
)

// seedQueueTexts seeds the three bills above, plus 44 with an untexted rh (ih fetched), and
// returns each bill's version IDs by code.
func seedQueueTexts(
	t *testing.T, store *spannerdb.PipelineStoreImpl, client *spanner.Client,
) map[int]map[string]string {
	t.Helper()
	ctx := t.Context()
	ids := map[int]map[string]string{}
	for _, n := range []int{gzOnlyBill, refetchedBill, plainBill, 44} {
		ids[n] = seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 10, []string{"rh", "ih"}, "ih")
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte("<bill>rh</bill>")); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	texts := []repository.BillTextRow{
		{TextVersionID: ids[gzOnlyBill]["rh"], ContentGz: gz.Bytes()},
		{TextVersionID: ids[refetchedBill]["rh"], Content: "<bill>rh</bill>"},
		{TextVersionID: ids[plainBill]["rh"], Content: "<bill>rh</bill>"},
	}
	for _, row := range texts {
		row.Format, row.ContentHash, row.FetchedAt = "xml", "hash-rh", summaryNow()
		if err := store.InsertBillText(ctx, row); err != nil {
			t.Fatalf("insert text: %v", err)
		}
	}
	if _, err := client.Apply(ctx, []*spanner.Mutation{spanner.Update("bill_texts",
		[]string{"text_id", "fetched_at"}, []any{textID(t, client, ids[refetchedBill]["rh"]), nil})}); err != nil {
		t.Fatalf("clear fetched_at: %v", err)
	}
	return ids
}

func TestQueryUnfetchedTextVersions_StoredTexts(t *testing.T) {
	store, client := newSummaryStore(t)
	seedQueueTexts(t, store, client)

	refs, err := store.QueryUnfetchedTextVersions(t.Context(), 0)
	if err != nil {
		t.Fatalf("QueryUnfetchedTextVersions: %v", err)
	}
	var got []string
	for _, r := range refs {
		state := "new"
		if r.Refetch {
			state = "refetch"
		}
		got = append(got, r.BillID+"|"+r.VersionCode+"|"+state)
	}
	// A content_gz-only text is fetched, so it isn't queued; a NULL fetched_at is a refetch; a
	// version with no text at all is new.
	assertRows(t, got, []string{billID(refetchedBill) + "|rh|refetch", billID(44) + "|rh|new"})
}

func TestQueryBillsToSummarize_StoredTexts(t *testing.T) {
	store, client := newSummaryStore(t)
	seedQueueTexts(t, store, client)

	// Any stored text counts, wherever its download lives and whether or not it's queued for a
	// refetch; bill 44's latest version with text is ih. All are recent (tier 1).
	assertRows(t, queue(t, store, nil), []string{
		billID(gzOnlyBill) + "|rh|hash-rh|1",
		billID(refetchedBill) + "|rh|hash-rh|1",
		billID(plainBill) + "|rh|hash-rh|1",
		billID(44) + "|ih|hash-ih|1",
	})
}

func TestQueryBillsToExplainLaw_StoredTexts(t *testing.T) {
	store, client := newLawChangeStore(t)
	ids := seedQueueTexts(t, store, client)
	for n, v := range ids {
		lawRefs(t, store, billID(n), v["rh"], ref(secPhysicians, model.LawRefAmends))
		lawRefs(t, store, billID(n), v["ih"], ref(secFlag, model.LawRefAmends))
	}

	assertRows(t, lawQueue(t, store, nil), []string{
		billID(gzOnlyBill) + "|hash-rh",
		billID(refetchedBill) + "|hash-rh",
		billID(plainBill) + "|hash-rh",
		billID(44) + "|hash-ih",
	})
}
