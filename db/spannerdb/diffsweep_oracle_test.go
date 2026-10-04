package spannerdb_test

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// The diff sweep's statements before #869, which found consecutive pairs by joining each pair
// of a bill's versions to their texts and testing for a fetched version between them. They're
// kept verbatim as the oracle the new sweep must agree with.
const (
	oldNonConsecutiveDiffsSQL = `SELECT d.bill_id, d.diff_id FROM bill_text_diffs d WHERE NOT EXISTS (SELECT 1 FROM bill_text_versions f
		JOIN bill_texts ft ON ft.version_id = f.version_id
		JOIN bill_text_versions t ON t.bill_id = f.bill_id
		JOIN bill_texts tt ON tt.version_id = t.version_id
		WHERE f.bill_id = d.bill_id AND f.version_id = d.from_version_id AND t.version_id = d.to_version_id
			AND f.sort_order < t.sort_order AND NOT EXISTS (SELECT 1 FROM bill_text_versions m
		JOIN bill_texts mt ON mt.version_id = m.version_id
		WHERE m.bill_id = f.bill_id AND m.sort_order > f.sort_order AND m.sort_order < t.sort_order)) ORDER BY d.bill_id, d.diff_id`

	oldMissingDiffPairsSQL = `SELECT f.bill_id, f.version_id, t.version_id
		FROM bill_text_versions f
		JOIN bill_texts ft ON ft.version_id = f.version_id
		JOIN bill_text_versions t ON t.bill_id = f.bill_id AND t.sort_order > f.sort_order
		JOIN bill_texts tt ON tt.version_id = t.version_id
		WHERE ft.content_hash != tt.content_hash
			AND NOT EXISTS (SELECT 1 FROM bill_text_versions m
		JOIN bill_texts mt ON mt.version_id = m.version_id
		WHERE m.bill_id = f.bill_id AND m.sort_order > f.sort_order AND m.sort_order < t.sort_order)
			AND NOT EXISTS (SELECT 1 FROM bill_text_diffs d
				WHERE d.bill_id = f.bill_id AND d.from_version_id = f.version_id AND d.to_version_id = t.version_id)
		ORDER BY f.bill_id, f.sort_order`
)

// sweepCorpus collects a random diff sweep corpus: each version's sort_order, every version so
// far, and the (from, to) pairs that have a diff, which idx_btd_versions keeps unique.
type sweepCorpus struct {
	corpus

	sortOf   map[string]int64
	versions []string
	diffs    map[[2]string]bool
}

// seedSweepCorpus writes n bills of up to five versions each, some sharing a sort_order and
// about two in three fetched with a text hash from a small pool, and diffs between random
// versions: consecutive, backwards, skipping, to unfetched or missing versions, and filed under
// another bill. It returns each version's sort_order.
func seedSweepCorpus(t *testing.T, client *spanner.Client, n int) map[string]int64 {
	t.Helper()
	c := &sweepCorpus{r: rand.New(rand.NewPCG(869, 2)), now: time.Now(),
		sortOf: map[string]int64{}, diffs: map[[2]string]bool{}}
	for i := range n {
		id := "hr-" + strconv.Itoa(testdb.FixtureCongress) + "-" + strconv.Itoa(7000+i)
		c.ins("bills", map[string]any{"bill_id": id,
			"congress": int64(testdb.FixtureCongress), "bill_type": "hr", "number": int64(7000 + i), "title": id})
		ids := c.sweepVersions(id, i)
		if len(ids) == 0 {
			continue
		}
		for range c.r.IntN(5) {
			from, to := pick(c.r, ids...), pick(c.r, ids...)
			switch c.r.IntN(8) {
			case 0:
				to = "gone-" + to // a pruned version
			case 1:
				to = pick(c.r, c.versions...) // likely another bill's
			}
			c.addDiff(id, from, to)
		}
	}
	c.apply(t, client)
	return c.sortOf
}

// sweepVersions adds up to five versions of a bill and returns their IDs.
func (c *sweepCorpus) sweepVersions(id string, i int) []string {
	var ids []string
	sort := int64(0)
	for j := range c.r.IntN(6) {
		if j == 0 || c.r.IntN(5) > 0 {
			sort++ // otherwise a tie with the version before
		}
		vid := fmt.Sprintf("v%04d-%c%d", i, 'a'+rune(c.r.IntN(26)), j)
		ids = append(ids, vid)
		c.sortOf[vid] = sort
		c.ins("bill_text_versions", map[string]any{"bill_id": id,
			"version_id": vid, "version_type": "Version", "version_code": "c" + strconv.Itoa(j), "sort_order": sort})
		if c.r.IntN(3) > 0 {
			c.ins("bill_texts", map[string]any{"text_id": "t-" + vid,
				"version_id": vid, "format": "xml", "content": "x", "content_hash": pick(c.r, "h1", "h2", "h3")})
		}
	}
	c.versions = append(c.versions, ids...)
	return ids
}

// addDiff adds a diff from → to filed under bill, unless the pair already has one.
func (c *sweepCorpus) addDiff(bill, from, to string) {
	if c.diffs[[2]string{from, to}] {
		return
	}
	c.diffs[[2]string{from, to}] = true
	c.ins("bill_text_diffs", map[string]any{
		"bill_id": bill, "diff_id": fmt.Sprintf("d%05d", len(c.diffs)), "from_version_id": from, "to_version_id": to,
		"diff_content": spanner.NullJSON{Value: []any{}, Valid: true}, "generated_at": c.now,
	})
}

// TestDiffSweep_MatchesOldStatements checks the sweep's missing pairs and the diffs it deletes
// against the statements they replaced (#869), on a random corpus.
func TestDiffSweep_MatchesOldStatements(t *testing.T) {
	store, client := newLinkStore(t)
	sortOf := seedSweepCorpus(t, client, 60)

	wantPairs := queryRowStrings(t, client, oldMissingDiffPairsSQL, nil)
	full := missingPairs(t, store, 0)
	gotPairs := make([]string, 0, len(full))
	for _, p := range full {
		gotPairs = append(gotPairs, p.BillID+"|"+p.FromVersionID+"|"+p.ToVersionID)
	}
	// The old statement ordered by bill and the from version's sort_order only, so compare the
	// pairs as sets and that order as a sequence.
	if len(wantPairs) < 20 {
		t.Fatalf("the corpus has %d missing pairs: widen it", len(wantPairs))
	}
	if !reflect.DeepEqual(slices.Sorted(slices.Values(gotPairs)), slices.Sorted(slices.Values(wantPairs))) {
		t.Errorf("missing pairs differ from the old statement's\n got %q\nwant %q", gotPairs, wantPairs)
	}
	order := func(rows []string) []string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			bill, rest, _ := strings.Cut(row, "|")
			from, _, _ := strings.Cut(rest, "|")
			out = append(out, bill+"|"+strconv.FormatInt(sortOf[from], 10))
		}
		return out
	}
	if got, want := order(gotPairs), order(wantPairs); !reflect.DeepEqual(got, want) {
		t.Errorf("missing pairs' order = %q, the old statement's = %q", got, want)
	}
	if got := missingPairs(t, store, 7); !reflect.DeepEqual(got, full[:7]) {
		t.Errorf("limit 7 = %v, want the first 7 of %v", got, full)
	}
	checkPagedPairs(t, store, full)

	wantDeleted := queryRowStrings(t, client, oldNonConsecutiveDiffsSQL, nil)
	if len(wantDeleted) < 20 {
		t.Fatalf("the corpus has %d non-consecutive diffs: widen it", len(wantDeleted))
	}
	t.Logf("%d missing pairs, %d non-consecutive diffs", len(wantPairs), len(wantDeleted))
	before := queryRowStrings(t, client, "SELECT bill_id, diff_id FROM bill_text_diffs ORDER BY bill_id, diff_id", nil)
	deleted, err := store.DeleteNonConsecutiveDiffsInPages(t.Context(), 7)
	if err != nil {
		t.Fatalf("DeleteNonConsecutiveDiffsInPages: %v", err)
	}
	after := queryRowStrings(t, client, "SELECT bill_id, diff_id FROM bill_text_diffs ORDER BY bill_id, diff_id", nil)
	gone := slices.DeleteFunc(slices.Clone(before), func(row string) bool { return slices.Contains(after, row) })
	if !reflect.DeepEqual(gone, wantDeleted) || deleted.Diffs != len(wantDeleted) {
		t.Errorf("deleted %d diffs %q, the old statement found %q", deleted.Diffs, gone, wantDeleted)
	}
}

// checkPagedPairs checks that reading in small pages returns the same pairs as one page. Small
// pages cross many page boundaries, a page of one bill has no neighbor in it, and a limit can stop
// mid-page or right at a page's end.
func checkPagedPairs(t *testing.T, store *spannerdb.PipelineStoreImpl, full []repository.DiffPair) {
	t.Helper()
	for _, page := range []int{1, 7} {
		for _, limit := range []int{0, 7, len(full)} {
			got, err := store.QueryMissingDiffPairsInPages(t.Context(), limit, page)
			if err != nil {
				t.Fatalf("QueryMissingDiffPairsInPages(%d, %d): %v", limit, page, err)
			}
			want := full
			if limit > 0 {
				want = full[:limit]
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("pages of %d bills, limit %d = %v, want %v", page, limit, got, want)
			}
		}
	}
}
