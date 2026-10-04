package spannerdb

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/repository"
)

// storedTextVersion is the part of a bill_text_versions row that matching needs. Fetched is
// true when the version has a bill_texts row.
type storedTextVersion struct {
	ID      string           `spanner:"version_id"`
	Code    string           `spanner:"version_code"`
	Type    string           `spanner:"version_type"`
	Formats spanner.NullJSON `spanner:"formats"`
	Fetched bool             `spanner:"fetched"`
}

// textVersionUpdate is an input row matched to a stored version it keeps the ID of. Refetch
// is true when the version's text was fetched and its formats now list other URLs.
type textVersionUpdate struct {
	id      string
	row     repository.TextVersionRow
	refetch bool
}

// textVersionPlan is what one upsert does to a bill's versions.
type textVersionPlan struct {
	updates    []textVersionUpdate
	inserts    []repository.TextVersionRow
	pruneIDs   []string
	pruneCodes []string
	duplicates int
}

// planTextVersions matches each input row to a stored version, first by version_code and
// then, for rows still unmatched, by version_type, so a version stored under an old code
// mapping is recoded in place rather than replaced. Rows with a code already seen are
// dropped (the input is newest first, so the newest wins). Stored versions left unmatched
// are pruned, but only when the input is non-empty: an empty list is more likely a bad
// response than a bill with no text.
//
// No write in the plan can collide on idx_btv_bill_version_code: input codes are unique, a
// code-matched row keeps its code, and a type-matched or inserted row takes a code that no
// stored row holds (a stored row with that code would have been code-matched).
func planTextVersions(stored []storedTextVersion, versions []repository.TextVersionRow) textVersionPlan {
	var plan textVersionPlan
	rows, duplicates := dedupeTextVersions(versions)
	plan.duplicates = duplicates
	match, claimed := matchTextVersions(stored, rows)
	for i, r := range rows {
		if match[i] < 0 {
			plan.inserts = append(plan.inserts, r)
			continue
		}
		sv := stored[match[i]]
		plan.updates = append(plan.updates, textVersionUpdate{
			id: sv.ID, row: r, refetch: sv.Fetched && formatsChanged(sv.Formats, r.Formats),
		})
	}
	if len(rows) == 0 {
		return plan
	}
	for j, sv := range stored {
		if !claimed[j] {
			plan.pruneIDs = append(plan.pruneIDs, sv.ID)
			plan.pruneCodes = append(plan.pruneCodes, sv.Code)
		}
	}
	return plan
}

// formatsChanged reports whether the new formats list URLs, and not the same ones as the
// stored formats: Congress.gov publishing a corrected print (#302). Order and format types
// don't matter, and a list with no URLs never counts, since there would be nothing to fetch.
func formatsChanged(stored spanner.NullJSON, formats json.RawMessage) bool {
	next := formatURLs(formats)
	if len(next) == 0 {
		return false
	}
	return !slices.Equal(formatURLs(nullJSONToRaw(stored)), next)
}

// formatURLs returns the sorted, distinct URLs of a formats list (`[{"type":…,"url":…}]`), or
// nil when it isn't one.
func formatURLs(formats json.RawMessage) []string {
	var entries []struct {
		URL string `json:"url"`
	}
	if len(formats) == 0 || json.Unmarshal(formats, &entries) != nil {
		return nil
	}
	urls := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.URL != "" {
			urls = append(urls, e.URL)
		}
	}
	slices.Sort(urls)
	return slices.Compact(urls)
}

// dedupeTextVersions keeps the first row for each version code and counts the rest.
func dedupeTextVersions(versions []repository.TextVersionRow) ([]repository.TextVersionRow, int) {
	rows := make([]repository.TextVersionRow, 0, len(versions))
	seen := make(map[string]bool, len(versions))
	for _, v := range versions {
		if !seen[v.VersionCode] {
			seen[v.VersionCode] = true
			rows = append(rows, v)
		}
	}
	return rows, len(versions) - len(rows)
}

// matchTextVersions returns, for each row, the index of the stored version it updates (-1
// for a new version), and which stored versions were matched.
func matchTextVersions(stored []storedTextVersion, rows []repository.TextVersionRow) ([]int, []bool) {
	byCode := make(map[string]int, len(stored))
	for i, sv := range stored {
		byCode[sv.Code] = i
	}
	claimed := make([]bool, len(stored))
	match := make([]int, len(rows))
	for i, r := range rows {
		match[i] = -1
		if j, ok := byCode[r.VersionCode]; ok {
			match[i] = j
			claimed[j] = true
		}
	}
	for i, r := range rows {
		if match[i] >= 0 {
			continue
		}
		for j, sv := range stored {
			if !claimed[j] && sv.Type == r.VersionType {
				match[i] = j
				claimed[j] = true
				break
			}
		}
	}
	return match, claimed
}

// statements returns the plan's DML in execution order: the prune cascade (diff summaries and
// attempts, diffs at either end, texts, law references, versions), then updates, then inserts. An update whose version
// needs a refetch also clears its text's fetched_at, which queues the text for sync-texts while
// it, and the diffs at either end, stay readable (see RefetchBillText).
func (p textVersionPlan) statements(billID string) []spanner.Statement {
	var stmts []spanner.Statement
	if len(p.pruneIDs) > 0 {
		params := map[string]any{paramBillID: billID, "ids": p.pruneIDs}
		const diffs = `SELECT diff_id FROM bill_text_diffs WHERE bill_id = @billID
			AND (from_version_id IN UNNEST(@ids) OR to_version_id IN UNNEST(@ids))`
		stmts = append(
			stmts,
			spanner.Statement{
				SQL:    "DELETE FROM bill_text_diff_summaries WHERE diff_id IN (" + diffs + ")",
				Params: params,
			},
			spanner.Statement{
				SQL:    "DELETE FROM diff_summary_attempts WHERE diff_id IN (" + diffs + ")",
				Params: params,
			},
			spanner.Statement{SQL: `DELETE FROM bill_text_diffs WHERE bill_id = @billID
				AND (from_version_id IN UNNEST(@ids) OR to_version_id IN UNNEST(@ids))`, Params: params},
			spanner.Statement{SQL: "DELETE FROM bill_texts WHERE version_id IN UNNEST(@ids)", Params: params},
			spanner.Statement{
				SQL:    "DELETE FROM bill_law_refs WHERE bill_id = @billID AND version_id IN UNNEST(@ids)",
				Params: params,
			},
			spanner.Statement{
				SQL:    "DELETE FROM bill_text_versions WHERE bill_id = @billID AND version_id IN UNNEST(@ids)",
				Params: params,
			},
		)
	}
	for _, u := range p.updates {
		params := textVersionParams(billID, u.row)
		params["versionID"] = u.id
		stmts = append(stmts, spanner.Statement{
			SQL: `UPDATE bill_text_versions SET version_type = @versionType, version_code = @versionCode,
				date = @date, formats = @formats, sort_order = @sortOrder, synced_at = CURRENT_TIMESTAMP()
				WHERE bill_id = @billID AND version_id = @versionID`,
			Params: params,
		})
		if u.refetch {
			stmts = append(stmts, spanner.Statement{
				SQL: "UPDATE bill_texts SET fetched_at = NULL WHERE version_id = @versionID", Params: params,
			})
		}
	}
	// version_id and synced_at come from their column defaults.
	for _, r := range p.inserts {
		stmts = append(stmts, spanner.Statement{
			SQL: `INSERT INTO bill_text_versions (bill_id, version_type, version_code, date, formats, sort_order)
				VALUES (@billID, @versionType, @versionCode, @date, @formats, @sortOrder)`,
			Params: textVersionParams(billID, r),
		})
	}
	return stmts
}

func (p textVersionPlan) result() repository.TextVersionSyncResult {
	var refetch []string
	for _, u := range p.updates {
		if u.refetch {
			refetch = append(refetch, u.row.VersionCode)
		}
	}
	return repository.TextVersionSyncResult{
		Inserted:     len(p.inserts),
		Updated:      len(p.updates),
		RefetchCodes: refetch,
		Pruned:       len(p.pruneIDs),
		PrunedCodes:  p.pruneCodes,
		Duplicates:   p.duplicates,
	}
}

func textVersionParams(billID string, r repository.TextVersionRow) map[string]any {
	return map[string]any{
		paramBillID: billID, "versionType": r.VersionType, "versionCode": r.VersionCode,
		"date": ptrTimeToCivilDate(r.Date), "formats": rawToNullJSON(r.Formats), "sortOrder": int64(r.SortOrder),
	}
}

// UpsertBillTextVersions writes a bill's text versions in one read-write transaction,
// keeping the version_id of every version that is still listed (see planTextVersions).
func (s *PipelineStoreImpl) UpsertBillTextVersions(
	ctx context.Context, billID string, versions []repository.TextVersionRow,
) (repository.TextVersionSyncResult, error) {
	var result repository.TextVersionSyncResult
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		stored, readErr := readTextVersions(ctx, txn, billID)
		if readErr != nil {
			return readErr
		}
		plan := planTextVersions(stored, versions)
		if stmts := plan.statements(billID); len(stmts) > 0 {
			if _, updErr := txn.BatchUpdate(ctx, stmts); updErr != nil {
				return updErr
			}
		}
		result = plan.result()
		return nil
	})
	if err != nil {
		return repository.TextVersionSyncResult{}, err
	}
	return result, nil
}

func readTextVersions(ctx context.Context, txn *spanner.ReadWriteTransaction, billID string) (
	[]storedTextVersion, error,
) {
	iter := txn.Query(ctx, spanner.Statement{
		SQL: `SELECT v.version_id, v.version_code, v.version_type, v.formats, t.text_id IS NOT NULL AS fetched
			FROM bill_text_versions v LEFT JOIN bill_texts t ON t.version_id = v.version_id
			WHERE v.bill_id = @billID ORDER BY v.sort_order, v.version_id`,
		Params: map[string]any{paramBillID: billID},
	})
	defer iter.Stop()
	var out []storedTextVersion
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		var sv storedTextVersion
		if err = row.ToStruct(&sv); err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
}

// RefetchBillText implements [repository.PipelineStore] in one read-write transaction. The
// diffs it deletes come back from the diff sweep at the end of sync-texts, which diffs
// consecutive fetched versions that have none, and their summaries from sync-summaries.
func (s *PipelineStoreImpl) RefetchBillText(
	ctx context.Context, t repository.BillTextRow,
) (repository.TextRefetch, error) {
	var out repository.TextRefetch
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		out = repository.TextRefetch{}
		billID, hash, queued, readErr := queuedText(ctx, txn, t.TextVersionID)
		if readErr != nil || !queued {
			return readErr
		}
		params := map[string]any{
			paramTextVersionID: t.TextVersionID,
			paramBillID:        billID,
			"fetchedAt":        t.FetchedAt,
			"format":           t.Format,
			"text":             t.Content,
			paramContentGz:     t.ContentGz,
			"contentHash":      t.ContentHash,
			paramSections:      rawToNullJSON(t.Sections),
		}
		if hash == t.ContentHash {
			_, updErr := txn.Update(ctx, spanner.Statement{
				SQL: "UPDATE bill_texts SET fetched_at = @fetchedAt WHERE version_id = @vid", Params: params,
			})
			return updErr
		}
		const atEitherEnd = `bill_id = @billID AND (from_version_id = @vid OR to_version_id = @vid)`
		counts, updErr := txn.BatchUpdate(ctx, []spanner.Statement{
			{SQL: `DELETE FROM bill_text_diff_summaries
				WHERE diff_id IN (SELECT diff_id FROM bill_text_diffs WHERE ` + atEitherEnd + `)`, Params: params},
			{SQL: `DELETE FROM diff_summary_attempts
				WHERE diff_id IN (SELECT diff_id FROM bill_text_diffs WHERE ` + atEitherEnd + `)`, Params: params},
			{SQL: "DELETE FROM bill_text_diffs WHERE " + atEitherEnd, Params: params},
			{SQL: `UPDATE bill_texts SET format = @format, content = @text, content_gz = @contentGz,
				content_hash = @contentHash,
				sections = @sections, fetched_at = @fetchedAt WHERE version_id = @vid`, Params: params},
		})
		if updErr != nil {
			return updErr
		}
		deleted := repository.DeletedDiffs{Diffs: int(counts[2]), Summaries: int(counts[0]), Attempts: int(counts[1])}
		out = repository.TextRefetch{Changed: true, Deleted: deleted}
		return nil
	})
	if err != nil {
		return repository.TextRefetch{}, err
	}
	return out, nil
}

// queuedText returns the bill and content hash of a version's text when it is queued for a
// refetch (fetched_at NULL), and false otherwise.
func queuedText(ctx context.Context, txn *spanner.ReadWriteTransaction, versionID string) (
	string, string, bool, error,
) {
	iter := txn.Query(ctx, spanner.Statement{
		SQL: `SELECT v.bill_id, t.content_hash FROM bill_texts t
			JOIN bill_text_versions v ON v.version_id = t.version_id
			WHERE t.version_id = @vid AND t.fetched_at IS NULL`,
		Params: map[string]any{paramTextVersionID: versionID},
	})
	defer iter.Stop()
	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	var billID, hash string
	if err = row.Columns(&billID, &hash); err != nil {
		return "", "", false, err
	}
	return billID, hash, true, nil
}
