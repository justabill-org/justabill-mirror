package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// BillRepository implements repository.BillRepo with Spanner.
type BillRepository struct {
	client *spanner.Client
}

// List returns a paginated, filtered list of bills.
func (r *BillRepository) List(ctx context.Context, params model.ListParams) (*model.ListResult[model.Bill], error) {
	where, p := billListFilter(params)
	from, orderBy := billListSource(params)

	// Count
	var total int64
	countStmt := spanner.Statement{SQL: "SELECT COUNT(*) FROM " + billCountSource(params) + " " + where, Params: p}
	countIter := r.client.Single().Query(ctx, countStmt)
	defer countIter.Stop()
	countRow, err := countIter.Next()
	if err != nil {
		return nil, fmt.Errorf("count bills: %w", searchError(params, err))
	}
	if err = countRow.Columns(&total); err != nil {
		return nil, err
	}
	countIter.Stop()

	// Select
	p[paramLimit] = int64(params.Limit)
	p["off"] = int64(params.Offset)

	selectSQL := fmt.Sprintf(
		`SELECT bill_id, congress, bill_type, number, title, introduced_date, origin_chamber,
		        latest_action, current_status, status_date, policy_area,
		        sponsors, cosponsors, committees, subjects, related_bills, laws, updated_at, synced_at
		 FROM %s %s
		 %s
		 LIMIT @lim OFFSET @off`, from, where, orderBy)
	selectIter := r.client.Single().Query(ctx, spanner.Statement{SQL: selectSQL, Params: p})
	defer selectIter.Stop()

	var items []model.Bill
	for {
		row, iterErr := selectIter.Next()
		if errors.Is(iterErr, iterator.Done) {
			break
		}
		if iterErr != nil {
			return nil, fmt.Errorf("list bills: %w", searchError(params, iterErr))
		}
		b, scanErr := scanBill(row)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, b)
	}
	if items == nil {
		items = []model.Bill{}
	}
	return &model.ListResult[model.Bill]{
		Items:  items,
		Total:  int(total),
		Offset: params.Offset,
		Limit:  params.Limit,
	}, nil
}

// CountByStatus counts the bills the list's filters match, by current status: one GROUP BY under
// the WHERE [BillRepository.List] builds, so each count is the total List gives for that status
// (#713). The status filter, paging and sort are ignored: the counts are what the status views
// split. Bills with no status yet count in Total only. Within a congress the counts read
// idx_bills_list_status alone ([billCountSource], #868).
func (r *BillRepository) CountByStatus(ctx context.Context, params model.ListParams) (*model.BillCounts, error) {
	params.Statuses, params.StatusMode = nil, ""
	where, p := billListFilter(params)
	from := billCountSource(params)
	stmt := spanner.Statement{
		SQL:    "SELECT current_status, COUNT(*) FROM " + from + " " + where + " GROUP BY current_status",
		Params: p,
	}
	counts := &model.BillCounts{ByStatus: map[string]int{}}
	err := r.client.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var status spanner.NullString
		var n int64
		if err := row.Columns(&status, &n); err != nil {
			return err
		}
		if status.Valid && status.StringVal != "" {
			counts.ByStatus[status.StringVal] += int(n)
		}
		counts.Total += int(n)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("count bills by status: %w", searchError(params, err))
	}
	return counts, nil
}

// searchError marks err with [repository.ErrInvalidSearch] when the list has a search and Spanner
// rejected the query as an invalid argument, so the API answers 400 rather than 500 (#452). The
// 'words' dialect makes that rare, but a search is the one argument a visitor controls.
func searchError(params model.ListParams, err error) error {
	// Compared by name: importing grpc/codes would make grpc a direct dependency (db/CLAUDE.md).
	if params.Search != nil && spanner.ErrCode(err).String() == "InvalidArgument" {
		return fmt.Errorf("%w: %w", repository.ErrInvalidSearch, err)
	}
	return err
}

// billSearchIDs selects the IDs of the bills whose title or summary matches @search: two searches,
// each served by its own table's search index, joined by UNION DISTINCT (#619). Spanner runs
// SEARCH only through a search index, and one index can't serve `SEARCH(title) OR EXISTS (...
// SEARCH(summary))`, whose other half is in another table. dialect=>'words' reads the input as
// plain words, so punctuation people type ("H.R. 1", a stray quote or parenthesis, a lone OR)
// can't fail to parse as rquery syntax (#452).
const billSearchIDs = `SELECT t.bill_id FROM bills t WHERE SEARCH(t.title_tokens, @search, dialect=>'words')
		UNION DISTINCT
		SELECT s.bill_id FROM bill_summaries s WHERE SEARCH(s.summary_tokens, @search, dialect=>'words')`

// billListFilter builds the WHERE clause and its parameters for [BillRepository.List].
func billListFilter(params model.ListParams) (string, map[string]any) {
	where := "WHERE TRUE"
	p := map[string]any{}

	if params.Congress != nil {
		where += " AND congress = @congress"
		p[paramCongress] = int64(*params.Congress)
	}
	if params.BillType != nil {
		where += " AND bill_type = @billType"
		p["billType"] = *params.BillType
	}
	where += billStatusFilter(params, p)
	if params.Chamber != nil {
		where += " AND origin_chamber = @chamber"
		p[paramChamber] = canonicalChamber(*params.Chamber)
	}
	if params.PolicyArea != nil {
		// policy_area_id is the slug of policy_area (a generated column with its own index), so
		// the filter matches the name in any case and uses idx_bills_policy_area.
		where += " AND policy_area_id = @policyArea"
		p["policyArea"] = slug(*params.PolicyArea)
	}
	if params.Search != nil {
		where += " AND bills.bill_id IN (" + billSearchIDs + ")"
		p["search"] = *params.Search
	}
	if params.UnvotedBy != nil {
		where += " AND NOT EXISTS (SELECT 1 FROM user_votes uv WHERE uv.bill_id = bills.bill_id AND uv.user_id = @unvotedBy)"
		p["unvotedBy"] = *params.UnvotedBy
	}
	return where, p
}

// billStatusFilter returns the AND clause for the list's statuses, if any, and sets its parameter
// in p. One status keeps the plain equality; several match any of them in one list, with one total
// (#712). "at" (the default) is the bill's current status; "past" is a stage it ever reached, from
// the audit trail.
func billStatusFilter(params model.ListParams, p map[string]any) string {
	statuses := params.Statuses
	if len(statuses) == 0 {
		return ""
	}
	match := "= @status"
	if len(statuses) == 1 {
		p["status"] = statuses[0]
	} else {
		match = "IN UNNEST(@statuses)"
		p["statuses"] = statuses
	}
	if params.StatusMode == "past" {
		return " AND bills.bill_id IN (SELECT h.bill_id FROM bill_status_history h WHERE h.status " + match + ")"
	}
	return " AND current_status " + match
}

const (
	// newestFirst is the default bill order: newest introduced first (#453).
	newestFirst = "ORDER BY introduced_date DESC, bill_id"
	// latestActionFirst is sort=latest_action's order: the latest action's date, newest first.
	latestActionFirst = "ORDER BY latest_action_date DESC, bill_id"
	// updatedFirst is sort=updated_at's order: the bill's last update on Congress.gov, newest first.
	updatedFirst = "ORDER BY updated_at DESC, bill_id"
	// sortLatestAction is the sort key for the latest action's date, newest first (#712).
	sortLatestAction = "latest_action"
)

// The bill list's indexes, as FROM sources: each is keyed on congress first and stores every column
// the list filters on (#760, #868, docs/design/746-spanner-review.md), so Spanner filters in the
// index and joins back to bills only for the rows a page returns. idx_bills_list_status also stores
// updated_at.
const (
	billsTable          = "bills"
	billsByIntroduced   = "bills@{FORCE_INDEX=idx_bills_list_introduced}"
	billsByLatestAction = "bills@{FORCE_INDEX=idx_bills_list_latest_action}"
	billsByStatus       = "bills@{FORCE_INDEX=idx_bills_list_status}"
)

// billListIndexed reports whether a list can read the list indexes: within one congress, with no
// search and no past-status filter. Without a congress the indexes' leading key doesn't narrow the
// scan, a search starts from the search indexes, and a past-status filter starts from
// bill_status_history.
func billListIndexed(params model.ListParams) bool {
	return params.Congress != nil && params.Search == nil && params.StatusMode != "past"
}

// billListSource returns the FROM source and the ORDER BY clause of [BillRepository.List]'s page.
//
// An indexed list ([billListIndexed]) reads an index whose key is its order, so Spanner stops at
// offset + limit (#760): the default order reads idx_bills_list_introduced, and latest_action reads
// idx_bills_list_latest_action, or idx_bills_list_status when the list filters on current status
// (#868), which seeks to each status's bills, already in that order. A status-filtered list
// sorted by updated_at reads idx_bills_list_status too: it sorts only that status's rows, from the
// index. Each index's key order is the list's order, ties broken by bill_id, with NULL dates last,
// as ORDER BY sorts them. Anything else reads bills with no hint, as before.
func billListSource(params model.ListParams) (string, string) {
	orderBy := billListOrder(params.Sort)
	if !billListIndexed(params) {
		return billsTable, orderBy
	}
	byStatus := len(params.Statuses) > 0
	switch {
	case orderBy == newestFirst:
		return billsByIntroduced, orderBy
	case orderBy == latestActionFirst && byStatus, orderBy == updatedFirst && byStatus:
		return billsByStatus, orderBy
	case orderBy == latestActionFirst:
		return billsByLatestAction, orderBy
	}
	return billsTable, orderBy
}

// billCountSource returns the FROM source of a count under the list's filters: List's COUNT(*)
// and CountByStatus's GROUP BY. An indexed list ([billListIndexed]) counts from
// idx_bills_list_status whatever its order (#868): a status filter seeks to that status's rows
// rather than reading every bill of the congress, and the counts by status read the congress's
// rows from the index alone. Anything else counts from bills, as before.
func billCountSource(params model.ListParams) string {
	if billListIndexed(params) {
		return billsByStatus
	}
	return billsTable
}

// billListOrder returns the ORDER BY clause for a [model.ListParams] sort key. No sort, or one it
// doesn't know, is newest introduced first. Every clause ends in the unique bill_id, so LIMIT/OFFSET
// pages neither repeat nor skip bills that tie on the sort key.
func billListOrder(sort *string) string {
	if sort == nil {
		return newestFirst
	}
	switch *sort {
	case "updated_at":
		return updatedFirst
	case "number":
		return "ORDER BY congress DESC, number DESC, bill_type, bill_id"
	case sortLatestAction:
		// latest_action_date is JSON_VALUE(latest_action, '$.actionDate'), stored (#760).
		// Congress.gov's actionDate is YYYY-MM-DD, so the strings sort as dates. GoogleSQL orders
		// NULL lowest, so DESC puts bills with no latest action (or no date in it) last (#712).
		return latestActionFirst
	default:
		return newestFirst
	}
}

// canonicalChamber returns "House" or "Senate", as Congress.gov spells them in origin_chamber and
// member_terms.chamber, for any case of either name; anything else is returned as given (#453).
func canonicalChamber(chamber string) string {
	switch {
	case strings.EqualFold(chamber, "house"):
		return "House"
	case strings.EqualFold(chamber, "senate"):
		return "Senate"
	default:
		return chamber
	}
}

// billIndexRow is one row of Index's query.
type billIndexRow struct {
	ID        string           `spanner:"bill_id"`
	UpdatedAt spanner.NullTime `spanner:"updated_at"`
}

// Index returns every bill in the congress, ID and updated_at only, ordered by ID.
func (r *BillRepository) Index(ctx context.Context, congress int) ([]model.BillIndexEntry, error) {
	stmt := spanner.Statement{
		SQL:    `SELECT bill_id, updated_at FROM bills WHERE congress = @congress ORDER BY bill_id`,
		Params: map[string]any{paramCongress: int64(congress)},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	entries := []model.BillIndexEntry{}
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return entries, nil
		}
		if err != nil {
			return nil, fmt.Errorf("bill index: %w", err)
		}
		var br billIndexRow
		if err = row.ToStruct(&br); err != nil {
			return nil, fmt.Errorf("bill index: %w", err)
		}
		entries = append(entries, model.BillIndexEntry{ID: br.ID, UpdatedAt: nullTimePtr(br.UpdatedAt)})
	}
}

// statusIndexSQL lists a congress's bills past committee. It reads only
// idx_bills_list_latest_action, whose key starts with congress and which stores current_status,
// so it never joins back to bills (#853).
const statusIndexSQL = `SELECT bill_id, current_status
	FROM bills@{FORCE_INDEX=idx_bills_list_latest_action}
	WHERE congress = @congress
	  AND current_status IN ('passed_house', 'passed_senate', 'resolving_differences', 'to_president',
	                         'signed', 'vetoed', 'became_law')
	ORDER BY bill_id`

// billStatusIndexRow is one row of StatusIndex's query.
type billStatusIndexRow struct {
	ID     string `spanner:"bill_id"`
	Status string `spanner:"current_status"`
}

// StatusIndex returns the ID and current_status of every bill in the congress whose status is
// passed_house, passed_senate, resolving_differences, to_president, signed, vetoed or became_law,
// ordered by ID. Bills introduced, in committee, reported or with no status are left out: My
// votes shows them as in committee or earlier (#853).
func (r *BillRepository) StatusIndex(ctx context.Context, congress int) ([]model.BillStatusIndexEntry, error) {
	stmt := spanner.Statement{SQL: statusIndexSQL, Params: map[string]any{paramCongress: int64(congress)}}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	entries := []model.BillStatusIndexEntry{}
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return entries, nil
		}
		if err != nil {
			return nil, fmt.Errorf("bill status index: %w", err)
		}
		var br billStatusIndexRow
		if err = row.ToStruct(&br); err != nil {
			return nil, fmt.Errorf("bill status index: %w", err)
		}
		entries = append(entries, model.BillStatusIndexEntry{ID: br.ID, Status: br.Status})
	}
}

// PolicyAreas returns the name of every policy area in policy_areas, A to Z (ignoring case, then
// by name, so the order is the same on every read).
func (r *BillRepository) PolicyAreas(ctx context.Context) ([]string, error) {
	stmt := spanner.Statement{SQL: `SELECT name FROM policy_areas ORDER BY LOWER(name), name`}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	names := []string{}
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return names, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list policy areas: %w", err)
		}
		var name string
		if err = row.Columns(&name); err != nil {
			return nil, fmt.Errorf("list policy areas: %w", err)
		}
		names = append(names, name)
	}
}

// GetByID returns a single bill by ID.
func (r *BillRepository) GetByID(ctx context.Context, id string) (*model.Bill, error) {
	stmt := spanner.Statement{
		SQL: `SELECT bill_id, congress, bill_type, number, title, introduced_date, origin_chamber,
		        latest_action, current_status, status_date, policy_area,
		        sponsors, cosponsors, committees, subjects, related_bills, laws, updated_at, synced_at
		 FROM bills WHERE bill_id = @id`,
		Params: map[string]any{"id": id},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, nil //nolint:nilnil // not found returns nil
	}
	if err != nil {
		return nil, err
	}
	b, err := scanBill(row)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// GetActions returns all actions for a bill.
func (r *BillRepository) GetActions(ctx context.Context, billID string) ([]model.BillAction, error) {
	stmt := spanner.Statement{
		SQL: `SELECT action_id, bill_id, action_date, action_time, action_text, action_type, action_code,
		        source_system, committee_code, recorded_vote, sort_order
		 FROM bill_actions WHERE bill_id = @billID ORDER BY sort_order`,
		Params: map[string]any{paramBillID: billID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var actions []model.BillAction
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		var (
			id            string
			bid           string
			actionDate    spanner.NullDate
			actionTime    spanner.NullString
			actionText    string
			actionType    spanner.NullString
			actionCode    spanner.NullString
			sourceSystem  spanner.NullString
			committeeCode spanner.NullString
			recordedVote  spanner.NullJSON
			sortOrder     int64
		)
		if err = row.Columns(&id, &bid, &actionDate, &actionTime, &actionText,
			&actionType, &actionCode, &sourceSystem, &committeeCode,
			&recordedVote, &sortOrder); err != nil {
			return nil, err
		}
		a := model.BillAction{
			ID: id, BillID: bid, ActionText: actionText,
			ActionTime: nullStringPtr(actionTime), ActionType: nullStringPtr(actionType),
			ActionCode: nullStringPtr(actionCode), SourceSystem: nullStringPtr(sourceSystem),
			CommitteeCode: nullStringPtr(committeeCode), RecordedVote: nullJSONToRaw(recordedVote),
			SortOrder: int(sortOrder),
		}
		if actionDate.Valid {
			a.ActionDate = *nullDatePtr(actionDate)
		}
		actions = append(actions, a)
	}
	if actions == nil {
		actions = []model.BillAction{}
	}
	return actions, nil
}

// billSummaryColumns selects a summary from bill_summaries aliased s, with the name of the text
// version it was written from (bill_text_versions is unique on bill_id and version_code) and
// whether the prompt carried a CRS summary or a CRA resolution's disapproved rule. The why_it_matters column holds WhoItAffects (see
// billSummaryRecord).
const billSummaryColumns = `s.bill_id, s.short_summary, s.long_summary, s.why_it_matters, s.model_used,
	s.generated_at, s.source_version_code,
	(SELECT v.version_type FROM bill_text_versions v
	 WHERE v.bill_id = s.bill_id AND v.version_code = s.source_version_code) AS source_version_name,
	s.source_crs_hash IS NOT NULL AS with_crs, s.source_rule_hash IS NOT NULL AS with_rule`

// GetSummary returns the AI-generated summary for a bill.
func (r *BillRepository) GetSummary(ctx context.Context, billID string) (*model.BillSummary, error) {
	stmt := spanner.Statement{
		SQL:    `SELECT ` + billSummaryColumns + ` FROM bill_summaries s WHERE s.bill_id = @billID`,
		Params: map[string]any{paramBillID: billID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, nil //nolint:nilnil // not found returns nil
	}
	if err != nil {
		return nil, err
	}
	return scanBillSummary(row)
}

// GetSummaries returns the AI-generated summaries for the given bills in one read, keyed by bill
// ID. Bills without a summary are absent from the map.
func (r *BillRepository) GetSummaries(ctx context.Context, billIDs []string) (map[string]model.BillSummary, error) {
	out := make(map[string]model.BillSummary, len(billIDs))
	if len(billIDs) == 0 {
		return out, nil
	}
	stmt := spanner.Statement{
		SQL:    `SELECT ` + billSummaryColumns + ` FROM bill_summaries s WHERE s.bill_id IN UNNEST(@summaryBillIDs)`,
		Params: map[string]any{"summaryBillIDs": billIDs},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		s, err := scanBillSummary(row)
		if err != nil {
			return nil, err
		}
		out[s.BillID] = *s
	}
}

// scanBillSummary reads a row selected with billSummaryColumns.
func scanBillSummary(row *spanner.Row) (*model.BillSummary, error) {
	var (
		bid          string
		shortSummary spanner.NullString
		longSummary  spanner.NullString
		whoItAffects spanner.NullString
		modelUsed    spanner.NullString
		generatedAt  spanner.NullTime
		versionCode  spanner.NullString
		versionName  spanner.NullString
		withCRS      bool
		withRule     bool
	)
	if err := row.Columns(&bid, &shortSummary, &longSummary, &whoItAffects, &modelUsed, &generatedAt,
		&versionCode, &versionName, &withCRS, &withRule); err != nil {
		return nil, err
	}
	return &model.BillSummary{
		BillID: bid, ShortSummary: nullStringPtr(shortSummary),
		LongSummary: nullStringPtr(longSummary), WhoItAffects: nullStringPtr(whoItAffects),
		ModelUsed: nullStringPtr(modelUsed), GeneratedAt: nullTimePtr(generatedAt),
		SourceVersionCode: nullStringPtr(versionCode), SourceVersionName: nullStringPtr(versionName),
		WithCRSSummary: withCRS, WithRuleContext: withRule,
	}, nil
}

// GetTextVersions returns all text versions for a bill.
func (r *BillRepository) GetTextVersions(ctx context.Context, billID string) ([]model.BillTextVersion, error) {
	stmt := spanner.Statement{
		SQL: `SELECT version_id, bill_id, version_type, version_code, date, formats, sort_order, synced_at
		 FROM bill_text_versions WHERE bill_id = @billID ORDER BY sort_order`,
		Params: map[string]any{paramBillID: billID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var versions []model.BillTextVersion
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		var (
			id, bid, vType, vCode string
			date                  spanner.NullDate
			formats               spanner.NullJSON
			sortOrder             int64
			syncedAt              spanner.NullTime
		)
		if err = row.Columns(&id, &bid, &vType, &vCode, &date, &formats, &sortOrder, &syncedAt); err != nil {
			return nil, err
		}
		versions = append(versions, model.BillTextVersion{
			ID: id, BillID: bid, VersionType: vType, VersionCode: vCode,
			Date: nullDatePtr(date), Formats: nullJSONToRaw(formats),
			SortOrder: int(sortOrder), SyncedAt: nullTimePtr(syncedAt),
		})
	}
	if versions == nil {
		versions = []model.BillTextVersion{}
	}
	return versions, nil
}

// GetDiffs returns the metadata of every text diff of a bill: ids, versions, stats and
// generated_at, but not diff_content, which can run to megabytes on a big bill. GetDiffByID
// reads one diff's content. Like every reader of bill_text_diffs, it leaves out empty diffs
// (is_empty): rows that only tell the diff sweep a pair had no section changes.
func (r *BillRepository) GetDiffs(ctx context.Context, billID string) ([]model.BillTextDiff, error) {
	stmt := spanner.Statement{
		SQL: `SELECT diff_id, bill_id, from_version_id, to_version_id, diff_stats, generated_at
		 FROM bill_text_diffs WHERE bill_id = @billID AND NOT is_empty`,
		Params: map[string]any{paramBillID: billID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var diffs []model.BillTextDiff
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		var (
			id, bid, fromVID, toVID string
			diffStats               spanner.NullJSON
			generatedAt             spanner.NullTime
		)
		if err = row.Columns(&id, &bid, &fromVID, &toVID, &diffStats, &generatedAt); err != nil {
			return nil, err
		}
		diffs = append(diffs, model.BillTextDiff{
			ID: id, BillID: bid, FromVersionID: fromVID, ToVersionID: toVID,
			DiffStats: nullJSONToRaw(diffStats), GeneratedAt: nullTimePtr(generatedAt),
		})
	}
	if diffs == nil {
		diffs = []model.BillTextDiff{}
	}
	return diffs, nil
}

// GetTextContent returns the parsed text content for one of the bill's text versions. The
// version is read by bill_text_versions' primary key, so a version of another bill is not found.
func (r *BillRepository) GetTextContent(ctx context.Context, billID, versionID string) (*model.BillText, error) {
	stmt := spanner.Statement{
		SQL: `SELECT t.text_id, t.version_id, t.format, t.content, t.content_gz, t.content_hash, t.sections,
			t.fetched_at
		 FROM bill_text_versions v
		 JOIN bill_texts@{FORCE_INDEX=idx_bill_texts_version} t ON t.version_id = v.version_id
		 WHERE v.bill_id = @billID AND v.version_id = @versionID`,
		Params: map[string]any{paramBillID: billID, "versionID": versionID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, nil //nolint:nilnil // not found returns nil
	}
	if err != nil {
		return nil, err
	}
	var (
		id, tvid, format, content, hash string
		gz                              []byte
		sections                        spanner.NullJSON
		fetchedAt                       spanner.NullTime
	)
	if err = row.Columns(&id, &tvid, &format, &content, &gz, &hash, &sections, &fetchedAt); err != nil {
		return nil, err
	}
	if content, err = storedText(content, hash, gz); err != nil {
		return nil, fmt.Errorf("text %s: %w", id, err)
	}
	return &model.BillText{
		ID: id, TextVersionID: tvid, Format: format, Content: content,
		ContentHash: hash, Sections: nullJSONToRaw(sections), FetchedAt: nullTimePtr(fetchedAt),
	}, nil
}

// GetDiffByID returns one of the bill's diffs, read by bill_text_diffs' primary key, so a diff
// of another bill is not found. An empty diff (is_empty) is not found either.
func (r *BillRepository) GetDiffByID(ctx context.Context, billID, diffID string) (*model.BillTextDiff, error) {
	stmt := spanner.Statement{
		SQL: `SELECT diff_id, bill_id, from_version_id, to_version_id, diff_stats, diff_content, generated_at
		 FROM bill_text_diffs WHERE bill_id = @billID AND diff_id = @id AND NOT is_empty`,
		Params: map[string]any{paramBillID: billID, "id": diffID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, nil //nolint:nilnil // not found returns nil
	}
	if err != nil {
		return nil, err
	}
	var (
		id, bid, fromVID, toVID string
		diffStats               spanner.NullJSON
		diffContent             spanner.NullJSON
		generatedAt             spanner.NullTime
	)
	if err = row.Columns(&id, &bid, &fromVID, &toVID, &diffStats, &diffContent, &generatedAt); err != nil {
		return nil, err
	}
	return &model.BillTextDiff{
		ID: id, BillID: bid, FromVersionID: fromVID, ToVersionID: toVID,
		DiffStats: nullJSONToRaw(diffStats), DiffContent: nullJSONToRaw(diffContent),
		GeneratedAt: nullTimePtr(generatedAt),
	}, nil
}

// GetDiffSummary returns the AI summary for a diff.
func (r *BillRepository) GetDiffSummary(ctx context.Context, diffID string) (*model.BillTextDiffSummary, error) {
	stmt := spanner.Statement{
		SQL: `SELECT diff_id, summary, model_used, generated_at
		 FROM bill_text_diff_summaries WHERE diff_id = @diffID`,
		Params: map[string]any{"diffID": diffID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, nil //nolint:nilnil // not found returns nil
	}
	if err != nil {
		return nil, err
	}
	var (
		did, summary string
		modelUsed    spanner.NullString
		generatedAt  spanner.NullTime
	)
	if err = row.Columns(&did, &summary, &modelUsed, &generatedAt); err != nil {
		return nil, err
	}
	return &model.BillTextDiffSummary{
		DiffID: did, Summary: summary,
		ModelUsed: nullStringPtr(modelUsed), GeneratedAt: nullTimePtr(generatedAt),
	}, nil
}

// GetStatusHistory returns the lifecycle stage audit trail for a bill, ordered by rank.
func (r *BillRepository) GetStatusHistory(ctx context.Context, billID string) ([]model.BillStatusEntry, error) {
	stmt := spanner.Statement{
		SQL: `SELECT status, status_date, status_rank
		 FROM bill_status_history WHERE bill_id = @billID ORDER BY status_rank`,
		Params: map[string]any{paramBillID: billID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var entries []model.BillStatusEntry
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		var (
			status     string
			statusDate spanner.NullDate
			statusRank int64
		)
		if err = row.Columns(&status, &statusDate, &statusRank); err != nil {
			return nil, err
		}
		e := model.BillStatusEntry{Status: status, StatusRank: int(statusRank)}
		if statusDate.Valid {
			e.StatusDate = *nullDatePtr(statusDate)
		}
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []model.BillStatusEntry{}
	}
	return entries, nil
}

// GetAmendments returns all amendments for a bill.
func (r *BillRepository) GetAmendments(ctx context.Context, billID string) ([]model.Amendment, error) {
	stmt := spanner.Statement{
		SQL: `SELECT amendment_id, bill_id, congress, amendment_type, amendment_number, description, purpose,
		        sponsor_id, latest_action, submitted_date, chamber, synced_at
		 FROM amendments WHERE bill_id = @billID`,
		Params: map[string]any{paramBillID: billID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var amendments []model.Amendment
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		var (
			id, bid, aType, chamber string
			congress, aNumber       int64
			description, purpose    spanner.NullString
			sponsorID               spanner.NullString
			latestAction            spanner.NullJSON
			submittedDate           spanner.NullDate
			syncedAt                spanner.NullTime
		)
		if err = row.Columns(&id, &bid, &congress, &aType, &aNumber, &description, &purpose,
			&sponsorID, &latestAction, &submittedDate, &chamber, &syncedAt); err != nil {
			return nil, err
		}
		amendments = append(amendments, model.Amendment{
			ID: id, BillID: bid, Congress: int(congress), AmendmentType: aType,
			AmendmentNumber: int(aNumber), Description: nullStringPtr(description),
			Purpose: nullStringPtr(purpose), SponsorID: nullStringPtr(sponsorID),
			LatestAction: nullJSONToRaw(latestAction), SubmittedDate: nullDatePtr(submittedDate),
			Chamber: chamber, SyncedAt: nullTimePtr(syncedAt),
		})
	}
	if amendments == nil {
		amendments = []model.Amendment{}
	}
	return amendments, nil
}

// ListGAOReports returns GAO reports linked to a bill.
func (r *BillRepository) ListGAOReports(ctx context.Context, billID string) ([]model.GAOReport, error) {
	stmt := spanner.Statement{
		SQL: `SELECT g.report_id, g.title, g.report_number, g.report_type,
		        g.published_date, g.summary, g.pdf_url, g.html_url, g.synced_at
		 FROM bill_gao_reports bg
		 JOIN gao_reports g ON g.report_id = bg.report_id
		 WHERE bg.bill_id = @billID
		 ORDER BY g.published_date DESC`,
		Params: map[string]any{paramBillID: billID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var reports []model.GAOReport
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		var (
			id, title                string
			reportNumber, reportType spanner.NullString
			summary, pdfURL, htmlURL spanner.NullString
			publishedDate            spanner.NullDate
			syncedAt                 spanner.NullTime
		)
		if err = row.Columns(&id, &title, &reportNumber, &reportType,
			&publishedDate, &summary, &pdfURL, &htmlURL, &syncedAt); err != nil {
			return nil, err
		}
		reports = append(reports, model.GAOReport{
			ReportID: id, Title: title,
			ReportNumber:  nullStringPtr(reportNumber),
			ReportType:    nullStringPtr(reportType),
			PublishedDate: nullDatePtr(publishedDate),
			Summary:       nullStringPtr(summary),
			PDFURL:        nullStringPtr(pdfURL),
			HTMLURL:       nullStringPtr(htmlURL),
			SyncedAt:      nullTimePtr(syncedAt),
		})
	}
	if reports == nil {
		reports = []model.GAOReport{}
	}
	return reports, nil
}

func scanBill(row *spanner.Row) (model.Bill, error) {
	var (
		id, billType, title        string
		congress, number           int64
		introducedDate, statusDate spanner.NullDate
		originChamber              spanner.NullString
		latestAction               spanner.NullJSON
		currentStatus              spanner.NullString
		policyArea                 spanner.NullString
		sponsors, cosponsors       spanner.NullJSON
		committees, subjects       spanner.NullJSON
		relatedBills, laws         spanner.NullJSON
		updatedAt, syncedAt        spanner.NullTime
	)
	if err := row.Columns(
		&id, &congress, &billType, &number, &title,
		&introducedDate, &originChamber, &latestAction, &currentStatus,
		&statusDate, &policyArea, &sponsors, &cosponsors, &committees,
		&subjects, &relatedBills, &laws, &updatedAt, &syncedAt,
	); err != nil {
		return model.Bill{}, err
	}
	return model.Bill{
		ID: id, Congress: int(congress), BillType: billType, Number: int(number), Title: title,
		IntroducedDate: nullDatePtr(introducedDate), OriginChamber: nullStringPtr(originChamber),
		LatestAction: nullJSONToRaw(latestAction), CurrentStatus: nullStringPtr(currentStatus),
		StatusDate: nullDatePtr(statusDate), PolicyArea: nullStringPtr(policyArea),
		Sponsors: nullJSONToRaw(sponsors), Cosponsors: nullJSONToRaw(cosponsors),
		Committees: nullJSONToRaw(committees), Subjects: nullJSONToRaw(subjects),
		RelatedBills: nullJSONToRaw(relatedBills), Laws: nullJSONToLaws(laws),
		UpdatedAt: nullTimePtr(updatedAt), SyncedAt: nullTimePtr(syncedAt),
	}, nil
}
