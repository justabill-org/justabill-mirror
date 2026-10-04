package spannerdb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/repository"
)

// A diff is valid when it compares two consecutive fetched versions of its bill, oldest to
// newest (docs/design/79-text-version-upsert.md, item 3). A version is fetched when it has a
// bill_texts row; "consecutive" means no other fetched version of the bill sorts between them.
// The queries below alias the from version f and the to version t.
const (
	// fetchedBetween is true when a fetched version of the bill sorts strictly between f and t.
	fetchedBetween = `EXISTS (SELECT 1 FROM bill_text_versions m
		JOIN bill_texts mt ON mt.version_id = m.version_id
		WHERE m.bill_id = f.bill_id AND m.sort_order > f.sort_order AND m.sort_order < t.sort_order)`

	// paramDiffIDs is the @ids parameter: the diffs a statement is restricted to.
	paramDiffIDs = "ids"

	// paramAfter is the @after parameter: the last bill of the page before.
	paramAfter = "after"

	// limitClause ends a query whose limit is optional.
	limitClause = " LIMIT @lim"

	// diffNotConsecutive is true for a diff d that no consecutive fetched pair of its bill matches.
	diffNotConsecutive = `NOT EXISTS (SELECT 1 FROM bill_text_versions f
		JOIN bill_texts ft ON ft.version_id = f.version_id
		JOIN bill_text_versions t ON t.bill_id = f.bill_id
		JOIN bill_texts tt ON tt.version_id = t.version_id
		WHERE f.bill_id = d.bill_id AND f.version_id = d.from_version_id AND t.version_id = d.to_version_id
			AND f.sort_order < t.sort_order AND NOT ` + fetchedBetween + `)`
)

// DeleteNonConsecutiveDiffs finds the invalid diffs a page of bills at a time, and deletes each
// page's bill by bill. Each bill's transaction checks its diffs again, so a diff that a
// concurrent sync made consecutive (by pruning the version between its ends) is kept.
func (s *PipelineStoreImpl) DeleteNonConsecutiveDiffs(ctx context.Context) (repository.DeletedDiffs, error) {
	return s.deleteNonConsecutiveDiffs(ctx, diffSweepPageBills)
}

func (s *PipelineStoreImpl) deleteNonConsecutiveDiffs(
	ctx context.Context, pageBills int,
) (repository.DeletedDiffs, error) {
	var total repository.DeletedDiffs
	err := s.eachDiffSweepPage(ctx, pageBills, func(versions []fetchedVersion, diffs []sweptDiff) (bool, error) {
		byBill, order := nonConsecutiveDiffs(versions, diffs)
		for _, billID := range order {
			deleted, delErr := s.deleteBillDiffs(ctx, billID, byBill[billID])
			if delErr != nil {
				return false, delErr
			}
			total.Diffs += deleted.Diffs
			total.Summaries += deleted.Summaries
			total.Attempts += deleted.Attempts
		}
		return true, nil
	})
	return total, err
}

// nonConsecutiveDiffs returns a page's invalid diffs' IDs by bill, and the bills in bill_id order.
func nonConsecutiveDiffs(versions []fetchedVersion, diffs []sweptDiff) (map[string][]string, []string) {
	consecutive := map[versionPair]bool{}
	for _, p := range consecutivePairs(versions) {
		consecutive[p.versionPair] = true
	}
	slices.SortFunc(diffs, func(a, b sweptDiff) int {
		return cmp.Or(cmp.Compare(a.BillID, b.BillID), cmp.Compare(a.DiffID, b.DiffID))
	})
	byBill := map[string][]string{}
	var order []string
	for _, d := range diffs {
		if consecutive[versionPair{billID: d.BillID, from: d.FromVersionID, to: d.ToVersionID}] {
			continue
		}
		if _, seen := byBill[d.BillID]; !seen {
			order = append(order, d.BillID)
		}
		byBill[d.BillID] = append(byBill[d.BillID], d.DiffID)
	}
	return byBill, order
}

// deleteBillDiffs deletes those of a bill's candidate diffs that are still invalid, with their
// summaries and summary attempts, in one read-write transaction.
func (s *PipelineStoreImpl) deleteBillDiffs(
	ctx context.Context, billID string, candidates []string,
) (repository.DeletedDiffs, error) {
	var deleted repository.DeletedDiffs
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		deleted = repository.DeletedDiffs{}
		ids, readErr := stillNonConsecutive(ctx, txn, billID, candidates)
		if readErr != nil || len(ids) == 0 {
			return readErr
		}
		params := map[string]any{paramBillID: billID, paramDiffIDs: ids}
		counts, updErr := txn.BatchUpdate(ctx, []spanner.Statement{
			{SQL: "DELETE FROM bill_text_diff_summaries WHERE diff_id IN UNNEST(@ids)", Params: params},
			{SQL: "DELETE FROM diff_summary_attempts WHERE diff_id IN UNNEST(@ids)", Params: params},
			{SQL: "DELETE FROM bill_text_diffs WHERE bill_id = @billID AND diff_id IN UNNEST(@ids)", Params: params},
		})
		if updErr != nil {
			return updErr
		}
		deleted = repository.DeletedDiffs{Diffs: int(counts[2]), Summaries: int(counts[0]), Attempts: int(counts[1])}
		return nil
	})
	if err != nil {
		return repository.DeletedDiffs{}, err
	}
	return deleted, nil
}

func stillNonConsecutive(
	ctx context.Context, txn *spanner.ReadWriteTransaction, billID string, candidates []string,
) ([]string, error) {
	iter := txn.Query(ctx, spanner.Statement{
		SQL: `SELECT d.diff_id FROM bill_text_diffs d
			WHERE d.bill_id = @billID AND d.diff_id IN UNNEST(@ids) AND ` + diffNotConsecutive,
		Params: map[string]any{paramBillID: billID, paramDiffIDs: candidates},
	})
	defer iter.Stop()
	var ids []string
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return ids, nil
		}
		if err != nil {
			return nil, err
		}
		var id string
		if err = row.Columns(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
}

// QueryMissingDiffPairs implements [repository.PipelineStore]. Pairs whose texts have the same
// content hash are left out, since their diff is empty. A pair with a stored empty diff
// (is_empty) has a row, so it isn't missing either. Pairs come by bill, then by the from and to
// versions' sort_order and version_id. It reads a page of bills at a time and stops at limit.
func (s *PipelineStoreImpl) QueryMissingDiffPairs(ctx context.Context, limit int) ([]repository.DiffPair, error) {
	return s.queryMissingDiffPairs(ctx, limit, diffSweepPageBills)
}

func (s *PipelineStoreImpl) queryMissingDiffPairs(
	ctx context.Context, limit, pageBills int,
) ([]repository.DiffPair, error) {
	var pairs []repository.DiffPair
	err := s.eachDiffSweepPage(ctx, pageBills, func(versions []fetchedVersion, diffs []sweptDiff) (bool, error) {
		stored := make(map[versionPair]bool, len(diffs))
		for _, d := range diffs {
			stored[versionPair{billID: d.BillID, from: d.FromVersionID, to: d.ToVersionID}] = true
		}
		for _, p := range consecutivePairs(versions) {
			if limit > 0 && len(pairs) == limit {
				return false, nil
			}
			if p.fromHash == p.toHash || stored[p.versionPair] {
				continue
			}
			pairs = append(pairs, repository.DiffPair{BillID: p.billID, FromVersionID: p.from, ToVersionID: p.to})
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return pairs, nil
}

// diffSweepPageBills is how many bills a page of the diff sweep covers. The two congresses hold
// about 40,000 bills, so a run reads about 40 pages of a few thousand versions and diffs each.
const diffSweepPageBills = 1000

// sweepPageSQL finds the last bill of the page after @after, from the bills table's key alone.
// Versions and diffs are interleaved in bills, so the range (@after, last] holds all of the
// page's bills' rows.
const sweepPageSQL = `SELECT MAX(bill_id) FROM (
SELECT bill_id FROM bills WHERE bill_id > @after ORDER BY bill_id LIMIT @lim)`

// sweepRangeFilter keeps a page's rows: those of the bills in (@after, @last].
const sweepRangeFilter = ` WHERE %s.bill_id > @after AND %s.bill_id <= @last`

// sweepVersionsSQL is fetchedVersionsSQL for a page. It looks each version's hash up in
// idx_bill_texts_version instead of hash-joining the whole index, which every page would scan.
const sweepVersionsSQL = `SELECT v.bill_id, v.version_id, v.version_code, v.sort_order, t.content_hash
FROM bill_text_versions v
JOIN@{JOIN_METHOD=APPLY_JOIN} bill_texts@{FORCE_INDEX=idx_bill_texts_version} t ON t.version_id = v.version_id`

// storedDiffsSQL reads diffs' bills and versions, but not diff_content. It reads the table, not
// idx_btd_versions: the index is keyed by the versions, so a page's bill range would scan all of
// it, while the table is interleaved in bills and the range is a key range.
const storedDiffsSQL = `SELECT d.bill_id, d.diff_id, d.from_version_id, d.to_version_id
FROM bill_text_diffs d`

// sweptDiff is a storedDiffsSQL row.
type sweptDiff struct {
	BillID        string `spanner:"bill_id"`
	DiffID        string `spanner:"diff_id"`
	FromVersionID string `spanner:"from_version_id"`
	ToVersionID   string `spanner:"to_version_id"`
}

// eachDiffSweepPage calls fn with the fetched versions and stored diffs of each page of
// pageBills bills, in bill_id order, until fn returns false. Each page is read in one snapshot,
// so a bill's versions and diffs agree. The sweep used to find consecutive pairs in SQL by
// joining each pair of a bill's versions to their texts and testing for a fetched version
// between them: about 149,000 rows and 70 s a run in production (#860). Each page now reads
// each of its rows once, and the pairs are found here (#869).
func (s *PipelineStoreImpl) eachDiffSweepPage(
	ctx context.Context, pageBills int, fn func([]fetchedVersion, []sweptDiff) (bool, error),
) error {
	after := ""
	for {
		versions, diffs, last, err := s.readDiffSweepPage(ctx, after, pageBills)
		if err != nil || last == "" {
			return err
		}
		more, err := fn(versions, diffs)
		if err != nil || !more {
			return err
		}
		after = last
	}
}

// readDiffSweepPage reads the page of bills after after: its versions, its diffs, and its last
// bill, which is "" when no bill is left.
func (s *PipelineStoreImpl) readDiffSweepPage(
	ctx context.Context, after string, pageBills int,
) ([]fetchedVersion, []sweptDiff, string, error) {
	ro := s.client.ReadOnlyTransaction()
	defer ro.Close()
	var last spanner.NullString
	err := eachRow(ctx, ro, spanner.Statement{
		SQL: sweepPageSQL, Params: map[string]any{paramAfter: after, paramLimit: int64(pageBills)},
	}, func(row *spanner.Row) error { return row.Columns(&last) })
	if err != nil {
		return nil, nil, "", fmt.Errorf("diff sweep page: %w", err)
	}
	if !last.Valid {
		return nil, nil, "", nil
	}
	params := map[string]any{paramAfter: after, "last": last.StringVal}
	versions, err := queryRows(ctx, ro, spanner.Statement{
		SQL: sweepVersionsSQL + fmt.Sprintf(sweepRangeFilter, "v", "v"), Params: params,
	}, "fetched text versions", func(r fetchedVersion) fetchedVersion { return r })
	if err != nil {
		return nil, nil, "", err
	}
	diffs, err := queryRows(ctx, ro, spanner.Statement{
		SQL: storedDiffsSQL + fmt.Sprintf(sweepRangeFilter, "d", "d"), Params: params,
	}, "stored diffs", func(r sweptDiff) sweptDiff { return r })
	if err != nil {
		return nil, nil, "", err
	}
	return versions, diffs, last.StringVal, nil
}

// versionPair is a bill's from and to versions.
type versionPair struct {
	billID, from, to string
}

// consecutivePair is a pair of consecutive fetched versions with their texts' hashes.
type consecutivePair struct {
	versionPair

	fromHash, toHash string
}

// consecutivePairs returns every pair of consecutive fetched versions: f before t (a lower
// sort_order) with no fetched version of the bill sorting strictly between them. Versions that
// share a sort_order pair with each version of the next sort_order, and not with each other.
// Pairs come by bill, then f's sort_order and version_id, then t's.
func consecutivePairs(versions []fetchedVersion) []consecutivePair {
	slices.SortFunc(versions, func(a, b fetchedVersion) int {
		return cmp.Or(cmp.Compare(a.BillID, b.BillID), cmp.Compare(a.SortOrder, b.SortOrder),
			cmp.Compare(a.VersionID, b.VersionID))
	})
	var pairs []consecutivePair
	var prev []fetchedVersion // the bill's versions at the sort_order before cur's
	for start := 0; start < len(versions); {
		end := start + 1
		for end < len(versions) && versions[end].BillID == versions[start].BillID &&
			versions[end].SortOrder == versions[start].SortOrder {
			end++
		}
		cur := versions[start:end]
		if len(prev) == 0 || prev[0].BillID != cur[0].BillID {
			prev = nil
		}
		for _, f := range prev {
			for _, t := range cur {
				pairs = append(pairs, consecutivePair{
					billID:   f.BillID,
					from:     f.VersionID,
					to:       t.VersionID,
					fromHash: f.ContentHash,
					toHash:   t.ContentHash,
				})
			}
		}
		prev, start = cur, end
	}
	return pairs
}
