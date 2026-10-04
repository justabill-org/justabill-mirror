package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// CRS bill summaries (docs/design/197-crs-summaries.md). The pipeline writes every version; the
// API reads the latest. The table is interleaved in bills without PARENT, so a summary can be
// written before its bill.

const (
	tableBillCRSSummaries = "bill_crs_summaries"

	// A summary row has 11 cells and is usually a few KB; H.R. 1 as enacted is about 175 KB of
	// HTML plus its rendering. 100 rows stay far below a commit's 80,000 cells and 100 MB.
	crsBatchRows = 100

	// paramCRSBills is the @crs_bills parameter: the bills StoredCRSSummaries reads.
	paramCRSBills = "crs_bills"
)

var errEmptyCRSKey = errors.New("empty bill id or version code")

type crsSummaryMut struct {
	BillID          string             `spanner:"bill_id"`
	VersionCode     string             `spanner:"version_code"`
	ActionDate      civil.Date         `spanner:"action_date"`
	ActionDesc      string             `spanner:"action_desc"`
	Chamber         spanner.NullString `spanner:"chamber"`
	TextHTML        string             `spanner:"text_html"`
	Text            string             `spanner:"text"`
	ContentHash     string             `spanner:"content_hash"`
	CRSUpdatedAt    time.Time          `spanner:"crs_updated_at"`
	SourceUpdatedAt time.Time          `spanner:"source_updated_at"`
	SyncedAt        time.Time          `spanner:"synced_at"`
}

type crsSummaryRow struct {
	BillID       string             `spanner:"bill_id"`
	VersionCode  string             `spanner:"version_code"`
	ActionDate   civil.Date         `spanner:"action_date"`
	ActionDesc   string             `spanner:"action_desc"`
	Chamber      spanner.NullString `spanner:"chamber"`
	Text         string             `spanner:"text"`
	CRSUpdatedAt time.Time          `spanner:"crs_updated_at"`
}

// Version codes aren't ordered (49 is Public Law), so "latest" is by the action described, then
// by when CRS wrote the summary.
const latestCRSSummarySQL = `SELECT bill_id, version_code, action_date, action_desc, chamber, text, crs_updated_at
FROM bill_crs_summaries
WHERE bill_id = @bill
ORDER BY action_date DESC, crs_updated_at DESC
LIMIT 1`

// GetCRSSummary implements [repository.BillRepo].
func (r *BillRepository) GetCRSSummary(ctx context.Context, billID string) (*model.CRSSummary, error) {
	stmt := spanner.Statement{SQL: latestCRSSummarySQL, Params: map[string]any{paramBill: billID}}
	rows, err := queryGraph(ctx, r.client, stmt, "crs summary", func(row crsSummaryRow) model.CRSSummary {
		return model.CRSSummary{
			BillID:      row.BillID,
			VersionCode: row.VersionCode,
			ActionDate:  row.ActionDate.In(time.UTC),
			ActionDesc:  row.ActionDesc,
			Chamber:     nullStringPtr(row.Chamber),
			Text:        row.Text,
			UpdatedAt:   row.CRSUpdatedAt,
		}
	})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// crsLeadsSQL reads each bill's latest CRS summary, ordered as latestCRSSummarySQL orders one
// bill's, through the table's (bill_id, version_code) key: one query for a page of bills.
const crsLeadsSQL = `SELECT bill_id, crs.version_code, crs.action_date, crs.action_desc, crs.text
FROM (
	SELECT id AS bill_id,
		(SELECT AS STRUCT c.version_code, c.action_date, c.action_desc, c.text FROM bill_crs_summaries c
		 WHERE c.bill_id = id
		 ORDER BY c.action_date DESC, c.crs_updated_at DESC LIMIT 1) AS crs
	FROM UNNEST(@billIDs) AS id
)
WHERE crs IS NOT NULL`

type crsLeadRow struct {
	BillID      string     `spanner:"bill_id"`
	VersionCode string     `spanner:"version_code"`
	ActionDate  civil.Date `spanner:"action_date"`
	ActionDesc  string     `spanner:"action_desc"`
	Text        string     `spanner:"text"`
}

// GetCRSLeads implements [repository.BillRepo]. The texts stay in the database's reply: only
// their leads ([model.CRSLead]) are returned.
func (r *BillRepository) GetCRSLeads(ctx context.Context, billIDs []string) (map[string]model.CardCRS, error) {
	out := map[string]model.CardCRS{}
	ids := slices.Compact(slices.Sorted(slices.Values(billIDs)))
	if len(ids) == 0 {
		return out, nil
	}
	stmt := spanner.Statement{SQL: crsLeadsSQL, Params: map[string]any{paramBillIDs: ids}}
	rows, err := queryGraph(ctx, r.client, stmt, "crs leads", func(row crsLeadRow) crsLeadRow { return row })
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.BillID] = model.CardCRS{
			VersionCode: row.VersionCode,
			ActionDate:  row.ActionDate.In(time.UTC),
			ActionDesc:  row.ActionDesc,
			Lead:        model.CRSLead(row.Text),
		}
	}
	return out, nil
}

// UpsertCRSSummaries implements [repository.PipelineStore]. It checks every row before it writes
// any, then commits 100 rows at a time.
func (s *PipelineStoreImpl) UpsertCRSSummaries(ctx context.Context, rows []repository.CRSSummaryRow) error {
	muts := make([]*spanner.Mutation, 0, len(rows))
	for _, r := range rows {
		if r.BillID == "" || r.VersionCode == "" {
			return fmt.Errorf("crs summary %q/%q: %w", r.BillID, r.VersionCode, errEmptyCRSKey)
		}
		m, err := spanner.InsertOrUpdateStruct(tableBillCRSSummaries, crsSummaryMut{
			BillID:          r.BillID,
			VersionCode:     r.VersionCode,
			ActionDate:      timeToCivilDate(r.ActionDate),
			ActionDesc:      r.ActionDesc,
			Chamber:         ptrToNullString(r.Chamber),
			TextHTML:        r.TextHTML,
			Text:            r.Text,
			ContentHash:     r.ContentHash,
			CRSUpdatedAt:    r.CRSUpdatedAt,
			SourceUpdatedAt: r.SourceUpdatedAt,
			SyncedAt:        spanner.CommitTimestamp,
		})
		if err != nil {
			return fmt.Errorf("crs summary mutation: %w", err)
		}
		muts = append(muts, m)
	}
	for batch := range slices.Chunk(muts, crsBatchRows) {
		if _, err := s.client.Apply(ctx, batch); err != nil {
			return fmt.Errorf("upsert crs summaries: %w", err)
		}
	}
	return nil
}

type storedCRSVersionRow struct {
	BillID       string    `spanner:"bill_id"`
	VersionCode  string    `spanner:"version_code"`
	ContentHash  string    `spanner:"content_hash"`
	CRSUpdatedAt time.Time `spanner:"crs_updated_at"`
}

// StoredCRSSummaries implements [repository.PipelineStore]. Both reads run in one read-only
// transaction, so they see the same snapshot.
func (s *PipelineStoreImpl) StoredCRSSummaries(
	ctx context.Context, billIDs []string,
) (repository.StoredCRSSummaries, error) {
	out := repository.StoredCRSSummaries{
		Versions: map[repository.CRSSummaryKey]repository.StoredCRSVersion{},
		Bills:    map[string]bool{},
	}
	if len(billIDs) == 0 {
		return out, nil
	}
	txn := s.client.ReadOnlyTransaction()
	defer txn.Close()
	params := map[string]any{paramCRSBills: billIDs}

	versions := txn.Query(ctx, spanner.Statement{
		SQL: `SELECT bill_id, version_code, content_hash, crs_updated_at
FROM bill_crs_summaries WHERE bill_id IN UNNEST(@crs_bills)`,
		Params: params,
	})
	err := versions.Do(func(row *spanner.Row) error {
		var v storedCRSVersionRow
		if convErr := row.ToStruct(&v); convErr != nil {
			return convErr
		}
		out.Versions[repository.CRSSummaryKey{BillID: v.BillID, VersionCode: v.VersionCode}] =
			repository.StoredCRSVersion{ContentHash: v.ContentHash, CRSUpdatedAt: v.CRSUpdatedAt}
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("read stored crs summaries: %w", err)
	}

	bills := txn.Query(
		ctx,
		spanner.Statement{SQL: "SELECT bill_id FROM bills WHERE bill_id IN UNNEST(@crs_bills)", Params: params},
	)
	err = bills.Do(func(row *spanner.Row) error {
		var id string
		if convErr := row.Columns(&id); convErr != nil {
			return convErr
		}
		out.Bills[id] = true
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("read bills for crs summaries: %w", err)
	}
	return out, nil
}
