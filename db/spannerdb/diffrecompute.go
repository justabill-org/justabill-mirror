package spannerdb

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/repository"
)

// Recomputing stored diffs (#449): when the diff algorithm changes, `pipeline-backfill diffs
// --recompute` computes every stored diff again and replaces it in place, so the Changes tab never
// goes blank and a diff whose content didn't change keeps its summary. A diff whose content changed
// keeps its diff_id but loses its summary and its summary attempt (#529), since both describe the
// old content: a block or backoff earned by the old content mustn't hold the new content back.

// Parameters of the statements below.
const (
	paramDiffID      = "diffID"
	paramFromVID     = "fromVID"
	paramToVID       = "toVID"
	paramDiffContent = "content"
)

// diffByPair selects the diff of a version pair; @billID, @fromVID and @toVID name it.
const diffByPair = `SELECT diff_id, diff_content, is_empty FROM bill_text_diffs
	WHERE bill_id = @billID AND from_version_id = @fromVID AND to_version_id = @toVID`

// QueryStoredDiffPairs implements [repository.PipelineStore].
func (s *PipelineStoreImpl) QueryStoredDiffPairs(ctx context.Context) ([]repository.DiffPair, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT d.bill_id, d.from_version_id, d.to_version_id FROM bill_text_diffs d
			LEFT JOIN bill_text_versions f ON f.bill_id = d.bill_id AND f.version_id = d.from_version_id
			ORDER BY d.bill_id, f.sort_order, d.diff_id`,
	})
	defer iter.Stop()
	var pairs []repository.DiffPair
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return pairs, nil
		}
		if err != nil {
			return nil, err
		}
		var p repository.DiffPair
		if err = row.Columns(&p.BillID, &p.FromVersionID, &p.ToVersionID); err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
}

// ReplaceBillTextDiff implements [repository.PipelineStore].
func (s *PipelineStoreImpl) ReplaceBillTextDiff(
	ctx context.Context, d repository.BillTextDiffRow,
) (repository.DiffReplacement, error) {
	var out repository.DiffReplacement
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		out = repository.DiffReplacement{}
		stored, found, readErr := readDiffByPair(ctx, txn,
			repository.DiffPair{BillID: d.BillID, FromVersionID: d.FromVersionID, ToVersionID: d.ToVersionID})
		if readErr != nil || !found {
			return readErr
		}
		out.Found = true
		same, cmpErr := sameJSON(stored.content, d.DiffContent)
		if cmpErr != nil || (same && stored.isEmpty == d.IsEmpty) {
			return cmpErr
		}
		out.Changed = true
		params := map[string]any{
			paramBillID: d.BillID, paramDiffID: stored.id, "stats": rawToNullJSON(d.DiffStats),
			paramDiffContent: rawToNullJSON(d.DiffContent), "generatedAt": d.GeneratedAt, "isEmpty": d.IsEmpty,
		}
		counts, updErr := txn.BatchUpdate(ctx, []spanner.Statement{
			{SQL: "DELETE FROM bill_text_diff_summaries WHERE diff_id = @diffID", Params: params},
			{SQL: "DELETE FROM diff_summary_attempts WHERE diff_id = @diffID", Params: params},
			{SQL: `UPDATE bill_text_diffs SET diff_stats = @stats, diff_content = @content,
				generated_at = @generatedAt, is_empty = @isEmpty WHERE bill_id = @billID AND diff_id = @diffID`, Params: params},
		})
		if updErr != nil {
			return updErr
		}
		out.SummaryDeleted, out.AttemptDeleted = counts[0] > 0, counts[1] > 0
		return nil
	})
	if err != nil {
		return repository.DiffReplacement{}, err
	}
	return out, nil
}

// storedDiff is a pair's stored diff, as ReplaceBillTextDiff compares it.
type storedDiff struct {
	id      string
	content spanner.NullJSON
	isEmpty bool
}

// readDiffByPair returns a pair's stored diff, and whether it exists.
func readDiffByPair(
	ctx context.Context, txn *spanner.ReadWriteTransaction, p repository.DiffPair,
) (storedDiff, bool, error) {
	iter := txn.Query(ctx, spanner.Statement{SQL: diffByPair, Params: map[string]any{
		paramBillID: p.BillID, paramFromVID: p.FromVersionID, paramToVID: p.ToVersionID,
	}})
	defer iter.Stop()
	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return storedDiff{}, false, nil
	}
	if err != nil {
		return storedDiff{}, false, err
	}
	var d storedDiff
	if err = row.Columns(&d.id, &d.content, &d.isEmpty); err != nil {
		return storedDiff{}, false, err
	}
	return d, true, nil
}

// sameJSON reports whether a stored JSON value and raw JSON hold the same data, whatever the key
// order or number formatting.
func sameJSON(stored spanner.NullJSON, raw json.RawMessage) (bool, error) {
	if !stored.Valid {
		return len(raw) == 0, nil
	}
	storedRaw, err := json.Marshal(stored.Value)
	if err != nil {
		return false, err
	}
	var a, b any
	if err = json.Unmarshal(storedRaw, &a); err != nil {
		return false, err
	}
	if err = json.Unmarshal(raw, &b); err != nil {
		return false, err
	}
	return reflect.DeepEqual(a, b), nil
}
