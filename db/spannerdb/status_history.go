package spannerdb

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
)

// ReplaceBillStatus sets a bill's current status and its date and replaces its status history
// with entries, in one transaction, so a stage the bill no longer has (one an earlier rule gave
// it) is dropped.
func (s *PipelineStoreImpl) ReplaceBillStatus(
	ctx context.Context, billID, status string, date *time.Time, entries []repository.BillStatusRow,
) error {
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		if _, err := txn.Update(ctx, billStatusUpdate(billID, status, date)); err != nil {
			return fmt.Errorf("update status: %w", err)
		}
		if _, err := txn.Update(ctx, spanner.Statement{
			SQL:    "DELETE FROM bill_status_history WHERE bill_id = @billID",
			Params: map[string]any{paramBillID: billID},
		}); err != nil {
			return fmt.Errorf("delete status history: %w", err)
		}
		mutations := make([]*spanner.Mutation, 0, len(entries))
		for _, e := range entries {
			mutations = append(mutations, spanner.Insert("bill_status_history",
				[]string{colBillID, colStatus, "status_date", "status_rank"},
				[]any{billID, e.Status, timeToCivilDate(e.StatusDate), int64(e.StatusRank)}))
		}
		return txn.BufferWrite(mutations)
	})
	return err
}

// ListStoredBillActions pages through a congress's bills in bill_id order, starting after
// afterBillID ("" for the first page), each with its stored actions in sort_order. Both reads
// share one snapshot.
func (s *PipelineStoreImpl) ListStoredBillActions(
	ctx context.Context, congressNum int, afterBillID string, limit int,
) ([]repository.StoredBillActions, error) {
	txn := s.client.ReadOnlyTransaction()
	defer txn.Close()

	ids, err := readStrings(ctx, txn, spanner.Statement{
		SQL: `SELECT bill_id FROM bills WHERE congress = @congress AND bill_id > @after
			ORDER BY bill_id LIMIT @lim`,
		Params: map[string]any{paramCongress: int64(congressNum), paramAfter: afterBillID, paramLimit: int64(limit)},
	})
	if err != nil {
		return nil, fmt.Errorf("list bills: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	actions := make(map[string][]repository.BillActionRow, len(ids))
	err = eachRow(ctx, txn, spanner.Statement{
		SQL: `SELECT bill_id, action_date, action_text, action_type, action_code, source_system, sort_order
			FROM bill_actions WHERE bill_id IN UNNEST(@billIDs) ORDER BY bill_id, sort_order`,
		Params: map[string]any{paramBillIDs: ids},
	}, func(row *spanner.Row) error {
		billID, a, scanErr := scanStoredAction(row)
		if scanErr != nil {
			return scanErr
		}
		actions[billID] = append(actions[billID], a)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list bill actions: %w", err)
	}

	out := make([]repository.StoredBillActions, 0, len(ids))
	for _, id := range ids {
		out = append(out, repository.StoredBillActions{BillID: id, Actions: actions[id]})
	}
	return out, nil
}

// scanStoredAction reads one row of ListStoredBillActions' action query.
func scanStoredAction(row *spanner.Row) (string, repository.BillActionRow, error) {
	var (
		billID                   string
		date                     spanner.NullDate
		text                     string
		actionType, code, source spanner.NullString
		sortOrder                int64
	)
	if err := row.Columns(&billID, &date, &text, &actionType, &code, &source, &sortOrder); err != nil {
		return "", repository.BillActionRow{}, fmt.Errorf("read bill action: %w", err)
	}
	a := repository.BillActionRow{
		ActionText:   text,
		ActionType:   nullStringPtr(actionType),
		ActionCode:   nullStringPtr(code),
		SourceSystem: nullStringPtr(source),
		SortOrder:    int(sortOrder),
	}
	if d := nullDatePtr(date); d != nil {
		a.ActionDate = *d
	}
	return billID, a, nil
}
