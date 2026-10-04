package spannerdb_test

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// ndaaSizedText is a download the size of S. 2296 es (9 MB of XML), stored the way sync-texts
// stores it (#451): gzipped, with its plain text in content and about 5 MB of sections.
func ndaaSizedText(t *testing.T, versionID string) (repository.BillTextRow, string) {
	t.Helper()
	var b strings.Builder
	for i := 0; b.Len() < 9_000_000; i++ {
		fmt.Fprintf(&b, `<section id="H%08X"><enum>%d.</enum><text>The Secretary shall act.</text></section>`, i, i)
	}
	raw := b.String()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sections, err := json.Marshal([]map[string]string{{"id": "s1", "content": strings.Repeat("a", 5_000_000)}})
	if err != nil {
		t.Fatal(err)
	}
	return repository.BillTextRow{
		TextVersionID: versionID, Format: "Formatted XML",
		Content:     strings.Repeat("The Secretary shall act.\n", repository.MaxTextChars/26),
		ContentGz:   gz.Bytes(),
		ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte(raw))),
		Sections:    sections, FetchedAt: time.Now(),
	}, raw
}

func TestBillText_LargeTextRoundTripsThroughGzip(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	ctx := t.Context()

	row, raw := ndaaSizedText(t, ids["ih"])
	if err := store.InsertBillText(ctx, row); err != nil {
		t.Fatalf("InsertBillText: %v", err)
	}
	assertRows(t, unfetched(t, store), []string{"rh|new", "eh|new"}) // ih isn't fetched again

	text, err := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client}).GetTextContent(ctx, bill, ids["ih"])
	if err != nil || text == nil {
		t.Fatalf("GetTextContent = %v, %v", text, err)
	}
	if text.Content != raw {
		t.Errorf("GetTextContent content is %d chars, want the %d-char download", len(text.Content), len(raw))
	}
	bc, err := store.LoadBillContext(ctx, bill, ids["ih"])
	if err != nil || bc.Text != raw {
		t.Errorf("LoadBillContext text is the download = %v (%v)", bc != nil && bc.Text == raw, err)
	}
	srcs, err := store.ListLawRefSources(ctx, testdb.FixtureCongress, "", "", 10)
	if err != nil || len(srcs) != 2 || srcs[0].BillID != bill || srcs[0].Content != raw {
		t.Errorf("ListLawRefSources = %d sources (%v), want the download and the fixture's HR 808", len(srcs), err)
	}
}

func TestBillText_SmallTextReadsContent(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	const raw = "<bill>short</bill>"
	// content_gz holds something else: a reader must not unzip it when content is the download.
	if err := store.InsertBillText(t.Context(), repository.BillTextRow{
		TextVersionID: ids["ih"], Format: "Formatted XML", Content: raw, ContentGz: []byte("not gzip"),
		ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte(raw))), FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("InsertBillText: %v", err)
	}
	text, err := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client}).GetTextContent(t.Context(), bill, ids["ih"])
	if err != nil || text.Content != raw {
		t.Errorf("GetTextContent = %+v, %v", text, err)
	}
}

func TestBillText_CorruptGzipIsAnError(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	if err := store.InsertBillText(t.Context(), repository.BillTextRow{
		TextVersionID: ids["ih"], Format: "Formatted XML", Content: "plain text", ContentGz: []byte("not gzip"),
		ContentHash: "hash-of-the-download", FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("InsertBillText: %v", err)
	}
	if _, err := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client}).GetTextContent(
		t.Context(), bill, ids["ih"]); err == nil {
		t.Error("GetTextContent of a corrupt gzip: want an error")
	}
	if _, err := store.LoadBillContext(t.Context(), bill, ids["ih"]); err == nil {
		t.Error("LoadBillContext of a corrupt gzip: want an error")
	}
	if _, err := store.ListLawRefSources(t.Context(), testdb.FixtureCongress, "", "", 10); err == nil {
		t.Error("ListLawRefSources of a corrupt gzip: want an error")
	}
}

func TestRefetchBillText_StoresTheGzip(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	addTextAndDiff(t, store, client, bill, ids["ih"], ids["rh"], ids["eh"])
	upsertVersions(t, store, bill, textVersion(bill, typeEngrossed, "eh", 3),
		corrected(bill, typeReported, "rh", 2), textVersion(bill, typeIntroduced, "ih", 1))

	row, raw := ndaaSizedText(t, ids["rh"])
	res, err := store.RefetchBillText(t.Context(), row)
	if err != nil || !res.Changed {
		t.Fatalf("RefetchBillText = %+v, %v", res, err)
	}
	text, err := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client}).GetTextContent(t.Context(), bill, ids["rh"])
	if err != nil || text.Content != raw {
		t.Errorf("GetTextContent after refetch is the download = %v (%v)", text != nil && text.Content == raw, err)
	}
	assertRows(t, unfetched(t, store), nil)
}
