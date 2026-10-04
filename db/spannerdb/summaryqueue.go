package spannerdb

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
)

// The summary queue (#869). A due bill is one whose summary is missing or of other text than its
// latest fetched version (or, with q.CRSContext, written with another CRS summary than the
// latest; with q.RuleContext, with other disapproved rule data than the bill's bill_cra_rules row;
// with q.ResummarizeOnPromptChange, of another prompt), and that no attempt for the same text,
// prompt and model holds back. A CRS or rule change never makes a bill due whose attempt was
// blocked for its current text, whatever the prompt: the model refused that text
// (docs/design/197-crs-summaries.md, docs/design/590-cra-disapproved-rules.md).
//
// The queue used to be one statement that ran, for every bill of the congress, a correlated
// EXISTS over congressional_votes and a struct subquery joining each text version to its text
// through a global index: about 207,000 rows and tens of CPU-seconds a run in production (#860).
// It now reads four flat sets in one snapshot, each a scan of one table or index, and joins them
// here. Only the passed-chamber filter (q.PassedChamber, passedChamberSQL) still tests each bill,
// on its interleaved actions and status history.

// summaryQueueBillsSQL reads each bill of the congress with its summary, attempt and disapproved
// rule rows, all interleaved in bills and keyed by bill_id, so each join is a merge on the key.
const summaryQueueBillsSQL = `SELECT b.bill_id, b.status_date,
	bs.bill_id IS NOT NULL AS summarized, bs.source_content_hash, bs.source_crs_hash, bs.source_rule_hash,
	bs.prompt_version,
	sa.outcome AS attempt_outcome, sa.content_hash AS attempt_hash, sa.prompt_version AS attempt_prompt,
	sa.model AS attempt_model, sa.next_attempt_at AS attempt_next_at,
	r.context_hash AS rule_hash
FROM bills b
LEFT JOIN bill_summaries bs ON bs.bill_id = b.bill_id
LEFT JOIN summary_attempts sa ON sa.bill_id = b.bill_id
LEFT JOIN bill_cra_rules r ON r.bill_id = b.bill_id
WHERE b.congress = @congress
	AND (NOT @passedChamber OR ` + passedChamberSQL + `)`

// fetchedVersionsSQL reads every text version that has a stored text, with its text's hash from
// idx_bill_texts_version, which stores content_hash (migration 30): no text row, whose content
// runs to megabytes, is read. The index is global and keyed by a random version_id, so the hash
// join scans it once instead of seeking it once per version. The summary queue adds a congress
// filter on the bill ID's middle part; the diff sweep reads by pages of bills (sweepVersionsSQL).
const fetchedVersionsSQL = `SELECT v.bill_id, v.version_id, v.version_code, v.sort_order, t.content_hash
FROM bill_text_versions v
JOIN@{JOIN_METHOD=HASH_JOIN} bill_texts@{FORCE_INDEX=idx_bill_texts_version} t ON t.version_id = v.version_id`

// congressPartFilter keeps the rows whose bill_id (type-congress-number) is of @congressPart.
const congressPartFilter = ` WHERE SPLIT(%s.bill_id, '-')[SAFE_OFFSET(1)] = @congressPart`

// summaryQueueCRSSQL reads the congress's CRS summaries' ordering columns and hashes; the latest
// per bill is picked here, as latestCRSSummarySQL orders them.
const summaryQueueCRSSQL = `SELECT c.bill_id, c.version_code, c.action_date, c.crs_updated_at, c.content_hash
FROM bill_crs_summaries c`

// votedBillsSQL reads the congress's bills with a congressional_votes row from idx_cv_bill alone.
const votedBillsSQL = `SELECT DISTINCT bill_id FROM congressional_votes@{FORCE_INDEX=idx_cv_bill}
WHERE bill_id IS NOT NULL AND SPLIT(bill_id, '-')[SAFE_OFFSET(1)] = @congressPart`

// summaryQueueBill is a summaryQueueBillsSQL row.
type summaryQueueBill struct {
	BillID            string             `spanner:"bill_id"`
	StatusDate        spanner.NullDate   `spanner:"status_date"`
	Summarized        bool               `spanner:"summarized"`
	SourceContentHash spanner.NullString `spanner:"source_content_hash"`
	SourceCRSHash     spanner.NullString `spanner:"source_crs_hash"`
	SourceRuleHash    spanner.NullString `spanner:"source_rule_hash"`
	PromptVersion     spanner.NullString `spanner:"prompt_version"`
	AttemptOutcome    spanner.NullString `spanner:"attempt_outcome"`
	AttemptHash       spanner.NullString `spanner:"attempt_hash"`
	AttemptPrompt     spanner.NullString `spanner:"attempt_prompt"`
	AttemptModel      spanner.NullString `spanner:"attempt_model"`
	AttemptNextAt     spanner.NullTime   `spanner:"attempt_next_at"`
	RuleHash          spanner.NullString `spanner:"rule_hash"`
}

// fetchedVersion is a fetchedVersionsSQL row.
type fetchedVersion struct {
	BillID      string `spanner:"bill_id"`
	VersionID   string `spanner:"version_id"`
	VersionCode string `spanner:"version_code"`
	SortOrder   int64  `spanner:"sort_order"`
	ContentHash string `spanner:"content_hash"`
}

// crsHashRow is a summaryQueueCRSSQL row.
type crsHashRow struct {
	BillID       string     `spanner:"bill_id"`
	VersionCode  string     `spanner:"version_code"`
	ActionDate   civil.Date `spanner:"action_date"`
	CRSUpdatedAt time.Time  `spanner:"crs_updated_at"`
	ContentHash  string     `spanner:"content_hash"`
}

// dueSummary is a due bill with the status date the queue orders by.
type dueSummary struct {
	item       repository.SummaryQueueItem
	statusDate spanner.NullDate
}

// QueryBillsToSummarize returns the bills due for a summary; see [repository.PipelineStore].
func (s *PipelineStoreImpl) QueryBillsToSummarize(
	ctx context.Context, q repository.SummaryQueueQuery,
) ([]repository.SummaryQueueItem, error) {
	due, err := s.dueSummaries(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query bills to summarize: %w", err)
	}
	slices.SortFunc(due, func(a, b dueSummary) int {
		if c := cmp.Compare(a.item.Tier, b.item.Tier); c != 0 {
			return c
		}
		if c := compareNullDatesDesc(a.statusDate, b.statusDate); c != 0 {
			return c
		}
		return cmp.Compare(a.item.BillID, b.item.BillID)
	})
	items := make([]repository.SummaryQueueItem, 0, min(len(due), max(q.Limit, 0)))
	for _, d := range due[:min(len(due), max(q.Limit, 0))] {
		items = append(items, d.item)
	}
	return items, nil
}

// CountBillsToSummarize counts the due bills per tier; see [repository.PipelineStore].
func (s *PipelineStoreImpl) CountBillsToSummarize(
	ctx context.Context, q repository.SummaryQueueQuery,
) (map[int]int, error) {
	due, err := s.dueSummaries(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("count bills to summarize: %w", err)
	}
	counts := make(map[int]int)
	for _, d := range due {
		counts[d.item.Tier]++
	}
	return counts, nil
}

// dueSummaries reads the queue's four sets in one snapshot and returns the due bills, unordered.
func (s *PipelineStoreImpl) dueSummaries(ctx context.Context, q repository.SummaryQueueQuery) ([]dueSummary, error) {
	ro := s.client.ReadOnlyTransaction()
	defer ro.Close()

	congressPart := map[string]any{"congressPart": strconv.Itoa(q.Congress)}
	bills, err := queryRows(ctx, ro, spanner.Statement{SQL: summaryQueueBillsSQL, Params: map[string]any{
		paramCongress: int64(q.Congress), "passedChamber": q.PassedChamber,
		"passedCodes": passedChamberCodes(), "passedRank": int64(passedHouseRank),
	}}, "summary queue bills", func(r summaryQueueBill) summaryQueueBill { return r })
	if err != nil {
		return nil, err
	}
	versions, err := queryRows(ctx, ro, spanner.Statement{
		SQL: fetchedVersionsSQL + fmt.Sprintf(congressPartFilter, "v"), Params: congressPart,
	}, "fetched text versions", func(r fetchedVersion) fetchedVersion { return r })
	if err != nil {
		return nil, err
	}
	var crs map[string]string
	if q.CRSContext {
		if crs, err = latestCRSHashes(ctx, ro, congressPart); err != nil {
			return nil, err
		}
	}
	voted := map[string]bool{}
	err = eachRow(ctx, ro, spanner.Statement{SQL: votedBillsSQL, Params: congressPart}, func(row *spanner.Row) error {
		var id string
		if colErr := row.Columns(&id); colErr != nil {
			return colErr
		}
		voted[id] = true
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("query voted bills: %w", err)
	}
	return summaryDue(q, bills, latestFetchedVersions(versions), crs, voted), nil
}

// latestCRSHashes returns the content hash of each bill's latest CRS summary in the congress,
// ordered as latestCRSSummarySQL orders them (ties by version_code, which that query leaves open).
func latestCRSHashes(ctx context.Context, txn querier, congressPart map[string]any) (map[string]string, error) {
	rows, err := queryRows(ctx, txn, spanner.Statement{
		SQL: summaryQueueCRSSQL + fmt.Sprintf(congressPartFilter, "c"), Params: congressPart,
	}, "crs summary hashes", func(r crsHashRow) crsHashRow { return r })
	if err != nil {
		return nil, err
	}
	latest := map[string]crsHashRow{}
	for _, r := range rows {
		cur, ok := latest[r.BillID]
		if !ok || crsLater(r, cur) {
			latest[r.BillID] = r
		}
	}
	out := make(map[string]string, len(latest))
	for id, r := range latest {
		out[id] = r.ContentHash
	}
	return out, nil
}

// crsLater reports whether a sorts before b in ORDER BY action_date DESC, crs_updated_at DESC,
// version_code.
func crsLater(a, b crsHashRow) bool {
	if c := a.ActionDate.Compare(b.ActionDate); c != 0 {
		return c > 0
	}
	if c := a.CRSUpdatedAt.Compare(b.CRSUpdatedAt); c != 0 {
		return c > 0
	}
	return a.VersionCode < b.VersionCode
}

// latestFetchedVersions returns each bill's latest fetched version, by latestVersionOrder:
// the highest sort_order, then the lowest version_id.
func latestFetchedVersions(versions []fetchedVersion) map[string]fetchedVersion {
	latest := make(map[string]fetchedVersion, len(versions))
	for _, v := range versions {
		cur, ok := latest[v.BillID]
		if !ok || v.SortOrder > cur.SortOrder || (v.SortOrder == cur.SortOrder && v.VersionID < cur.VersionID) {
			latest[v.BillID] = v
		}
	}
	return latest
}

// summaryDue returns the bills of q's congress that are due, with their tier.
func summaryDue(
	q repository.SummaryQueueQuery, bills []summaryQueueBill, latest map[string]fetchedVersion,
	crs map[string]string, voted map[string]bool,
) []dueSummary {
	recentSince := timeToCivilDate(q.Now.UTC().AddDate(0, 0, -q.RecentDays))
	var due []dueSummary
	for _, b := range bills {
		v, ok := latest[b.BillID]
		if !ok || heldBack(q, b, v.ContentHash) {
			continue
		}
		var tier int
		switch {
		case !inputsCurrent(q, b, v.ContentHash, crs):
			tier = priorityTier(b, voted[b.BillID], recentSince)
		case q.ResummarizeOnPromptChange && b.PromptVersion.StringVal != q.PromptVersion:
			tier = repository.SummaryTierPromptChange
		default:
			continue
		}
		due = append(due, dueSummary{
			item: repository.SummaryQueueItem{
				BillID: b.BillID, VersionID: v.VersionID, VersionCode: v.VersionCode, ContentHash: v.ContentHash,
				Tier: tier,
			},
			statusDate: b.StatusDate,
		})
	}
	return due
}

// priorityTier is the tier of a bill due for its inputs: voted, then recent by status date.
func priorityTier(b summaryQueueBill, voted bool, recentSince civil.Date) int {
	switch {
	case voted:
		return repository.SummaryTierVoted
	case b.StatusDate.Valid && !b.StatusDate.Date.Before(recentSince):
		return repository.SummaryTierRecent
	default:
		return repository.SummaryTierOther
	}
}

// heldBack reports whether the bill's attempt for its latest text, q's prompt and model failed
// and waits: blocked (no next attempt) or backing off until after q.Now.
func heldBack(q repository.SummaryQueueQuery, b summaryQueueBill, latestHash string) bool {
	return b.AttemptOutcome.Valid && b.AttemptOutcome.StringVal != repository.SummaryOutcomeOK &&
		b.AttemptHash.StringVal == latestHash && b.AttemptPrompt.StringVal == q.PromptVersion &&
		b.AttemptModel.StringVal == q.Model &&
		(!b.AttemptNextAt.Valid || b.AttemptNextAt.Time.After(q.Now))
}

// inputsCurrent reports whether the bill's summary was written from its latest text (whose hash
// is latestHash) and, as q asks, its latest CRS summary and disapproved rule data. A changed CRS
// summary or rule doesn't count while the bill's attempt for that text is blocked.
func inputsCurrent(q repository.SummaryQueueQuery, b summaryQueueBill, latestHash string, crs map[string]string) bool {
	if !b.Summarized || !b.SourceContentHash.Valid || b.SourceContentHash.StringVal != latestHash {
		return false
	}
	crsHash, hasCRS := crs[b.BillID]
	crsChanged := q.CRSContext && hasCRS && b.SourceCRSHash.StringVal != crsHash
	ruleChanged := q.RuleContext && b.RuleHash.Valid && b.SourceRuleHash.StringVal != b.RuleHash.StringVal
	blocked := b.AttemptOutcome.StringVal == repository.SummaryOutcomeBlocked && b.AttemptHash.StringVal == latestHash
	return !crsChanged && !ruleChanged || blocked
}
