package spannerdb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
)

// The API's "Changes to current law" read (docs/design/149-law-aware-assistant.md, plan item 5):
// the references of a bill's latest stored text, joined with the explanations of that text.

// paramCitesKind names the parameter holding the reference kind that only mentions a section,
// which the law-change reads leave out.
const paramCitesKind = "citesKind"

// lawChangeVersionSQL reads the bill's congress and its latest version with stored text, the
// version the explainer reads (lawChangeDueSQL).
const lawChangeVersionSQL = `SELECT b.congress, v.version_id, v.version_code, v.content_hash
FROM bills b
LEFT JOIN (
	SELECT btv.bill_id, btv.version_id, btv.version_code, bt.content_hash
	FROM bill_text_versions btv JOIN bill_texts bt ON bt.version_id = btv.version_id
	WHERE btv.bill_id = @bill
	ORDER BY ` + latestVersionOrder + ` LIMIT 1
) v ON v.bill_id = b.bill_id
WHERE b.bill_id = @bill`

// lawChangeEntriesSQL reads the version's references that change the law, with each section's
// heading when it's loaded and the explanation of this text (matched on its content hash).
const lawChangeEntriesSQL = `SELECT r.section_id, r.ref_kind, r.cite_text, r.subsection_path, r.instruction,
	s.section_id IS NOT NULL AS loaded, s.heading,
	c.explanation, c.model_used, c.prompt_version, c.generated_at, c.release_point
FROM bill_law_refs r
LEFT JOIN usc_sections s ON s.section_id = r.section_id
LEFT JOIN bill_law_changes c ON c.bill_id = r.bill_id AND c.section_id = r.section_id
	AND c.source_content_hash = @hash
WHERE r.bill_id = @bill AND r.version_id = @version AND r.ref_kind != @citesKind
ORDER BY r.bill_section_ref, r.section_id, r.ref_kind`

// alsoChangedByPairsSQL lists, per section, the other bills of the congress whose text, in any
// version, changes it. It reads idx_bill_law_refs_section alone: the congress is the middle part of
// a bill ID (<type>-<congress>-<number>), so other congresses' bills drop out, and each bill counts
// once per section, before any bills row is read. Joining bills for every reference of every
// version timed out on bills changing hundreds of sections (#784).
const alsoChangedByPairsSQL = `SELECT r.section_id, r.bill_id, ARRAY_AGG(DISTINCT r.ref_kind) AS ref_kinds
FROM bill_law_refs@{FORCE_INDEX=idx_bill_law_refs_section} r
WHERE r.section_id IN UNNEST(@sections) AND r.ref_kind != @citesKind AND r.bill_id != @bill
	AND SPLIT(r.bill_id, '-')[SAFE_OFFSET(1)] = @congressPart
GROUP BY r.section_id, r.bill_id`

// alsoChangedByBillsSQL reads the bills alsoChangedByPairsSQL found, once each. The congress
// check makes the bill ID filter above an optimization only.
const alsoChangedByBillsSQL = `SELECT bill_id, congress, bill_type, number, title, current_status, introduced_date
FROM bills
WHERE bill_id IN UNNEST(@bills) AND congress = @congress`

type lawChangeEntryRow struct {
	SectionID      string             `spanner:"section_id"`
	RefKind        string             `spanner:"ref_kind"`
	CiteText       spanner.NullString `spanner:"cite_text"`
	SubsectionPath spanner.NullString `spanner:"subsection_path"`
	Instruction    spanner.NullString `spanner:"instruction"`
	Loaded         bool               `spanner:"loaded"`
	Heading        spanner.NullString `spanner:"heading"`
	Explanation    spanner.NullString `spanner:"explanation"`
	ModelUsed      spanner.NullString `spanner:"model_used"`
	PromptVersion  spanner.NullString `spanner:"prompt_version"`
	GeneratedAt    spanner.NullTime   `spanner:"generated_at"`
	ReleasePoint   spanner.NullString `spanner:"release_point"`
}

type alsoChangedByPairRow struct {
	SectionID string   `spanner:"section_id"`
	BillID    string   `spanner:"bill_id"`
	RefKinds  []string `spanner:"ref_kinds"`
}

type alsoChangedByBillRow struct {
	BillID         string             `spanner:"bill_id"`
	Congress       int64              `spanner:"congress"`
	BillType       string             `spanner:"bill_type"`
	Number         int64              `spanner:"number"`
	Title          string             `spanner:"title"`
	CurrentStatus  spanner.NullString `spanner:"current_status"`
	IntroducedDate spanner.NullDate   `spanner:"introduced_date"`
}

// lawChangeVersion is the bill a law-changes read is for, and its latest stored text.
type lawChangeVersion struct {
	congress    int64
	versionID   spanner.NullString
	versionCode spanner.NullString
	contentHash spanner.NullString
}

// BillLawChangeEntries reads the bill's latest stored text version, its references that change
// the law with their explanations, and the other bills changing each section, in one read-only
// transaction. It returns nil when the bill doesn't exist.
func (r *LawRepository) BillLawChangeEntries(
	ctx context.Context, billID string, alsoLimit int,
) (*model.BillLawChanges, error) {
	txn := r.client.ReadOnlyTransaction()
	defer txn.Close()

	v, err := readLawChangeVersion(ctx, txn, billID)
	if err != nil || v == nil {
		return nil, err
	}
	out := &model.BillLawChanges{
		BillID:      billID,
		VersionID:   nullStringPtr(v.versionID),
		VersionCode: nullStringPtr(v.versionCode),
		Changes:     []model.LawChangeEntry{},
	}
	if !v.versionID.Valid {
		return out, nil
	}
	stmt := spanner.Statement{SQL: lawChangeEntriesSQL, Params: map[string]any{
		paramBill: billID, "version": v.versionID.StringVal, paramHash: v.contentHash.StringVal,
		paramCitesKind: model.LawRefCites,
	}}
	rows, err := queryRows(ctx, txn, stmt, "bill law change entries", func(row lawChangeEntryRow) lawChangeEntryRow {
		return row
	})
	if err != nil {
		return nil, err
	}
	out.Changes, out.Explained = groupLawChangeEntries(rows)
	if len(out.Changes) == 0 || alsoLimit <= 0 {
		return out, nil
	}
	also, err := readAlsoChangedBy(ctx, txn, billID, v.congress, out.Changes, alsoLimit)
	if err != nil {
		return nil, err
	}
	for i := range out.Changes {
		if bills, ok := also[out.Changes[i].SectionID]; ok {
			out.Changes[i].AlsoChangedBy = bills
		}
	}
	return out, nil
}

func readLawChangeVersion(ctx context.Context, txn querier, billID string) (*lawChangeVersion, error) {
	iter := txn.Query(ctx, spanner.Statement{SQL: lawChangeVersionSQL, Params: map[string]any{paramBill: billID}})
	defer iter.Stop()
	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, nil //nolint:nilnil // nil is the documented "no such bill"
	}
	if err != nil {
		return nil, fmt.Errorf("read law change version of %s: %w", billID, err)
	}
	var v lawChangeVersion
	if err = row.Columns(&v.congress, &v.versionID, &v.versionCode, &v.contentHash); err != nil {
		return nil, fmt.Errorf("read law change version of %s: %w", billID, err)
	}
	return &v, nil
}

// groupLawChangeEntries folds the references into one entry per section, in the order the query
// returned them, as the explainer groups its input (aiLawChangeContext in pipeline/internal/sync).
// It returns the provenance of the first explanation found.
func groupLawChangeEntries(rows []lawChangeEntryRow) ([]model.LawChangeEntry, *model.LawChangeProvenance) {
	entries := []model.LawChangeEntry{}
	var explained *model.LawChangeProvenance
	index := map[string]int{}
	for _, row := range rows {
		i, seen := index[row.SectionID]
		if !seen {
			i = len(entries)
			index[row.SectionID] = i
			entries = append(entries, newLawChangeEntry(row))
		}
		e := &entries[i]
		if lawKindRank(row.RefKind) > lawKindRank(e.ChangeKind) {
			e.ChangeKind = row.RefKind
		}
		if e.CiteText == nil {
			e.CiteText = nullStringPtr(row.CiteText)
		}
		e.SubsectionPath = joinLawText(e.SubsectionPath, row.SubsectionPath, ", ")
		e.Instruction = joinLawText(e.Instruction, row.Instruction, "\n\n")
		if explained == nil && row.ModelUsed.Valid {
			explained = &model.LawChangeProvenance{
				ModelUsed:     row.ModelUsed.StringVal,
				PromptVersion: row.PromptVersion.StringVal,
				GeneratedAt:   nullTimeValue(row.GeneratedAt),
				ReleasePoint:  row.ReleasePoint.StringVal,
			}
		}
	}
	return entries, explained
}

func newLawChangeEntry(row lawChangeEntryRow) model.LawChangeEntry {
	e := model.LawChangeEntry{
		SectionID:     row.SectionID,
		Loaded:        row.Loaded,
		ChangeKind:    row.RefKind,
		AlsoChangedBy: []model.SectionBill{},
	}
	if row.Loaded {
		e.Heading = nullStringPtr(row.Heading)
	}
	if row.Explanation.Valid && strings.TrimSpace(row.Explanation.StringVal) != "" {
		e.Explanation = &row.Explanation.StringVal
	}
	title, section, ok := model.ParseUSCSectionID(row.SectionID)
	if !ok {
		title, section, ok = model.ParseUSCNoteID(row.SectionID)
		e.IsNote = ok
	}
	if ok {
		e.InUSCode = true
		e.TitleNumber = &title
		e.SectionNumber = &section
	}
	return e
}

// joinLawText adds next's parts (split on sep) that acc doesn't hold yet, joined with sep. The
// parser joins the subsection paths of one reference with ", " ("(t), (c)(2)(B)").
func joinLawText(acc *string, next spanner.NullString, sep string) *string {
	if !next.Valid {
		return acc
	}
	var parts []string
	if acc != nil {
		parts = strings.Split(*acc, sep)
	}
	for p := range strings.SplitSeq(next.StringVal, sep) {
		if p = strings.TrimSpace(p); p != "" && !slices.Contains(parts, p) {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return acc
	}
	joined := strings.Join(parts, sep)
	return &joined
}

// lawKindRank orders the kinds that change law, weakest first; any other kind ranks below them.
func lawKindRank(kind string) int {
	return slices.Index([]string{model.LawRefAmends, model.LawRefAdds, model.LawRefRepeals}, kind)
}

// readAlsoChangedBy returns, per section (or statutory note) of entries that's in the US Code, at
// most limit other bills of congress changing it, newest introduced first.
func readAlsoChangedBy(
	ctx context.Context, txn querier, billID string, congress int64, entries []model.LawChangeEntry, limit int,
) (map[string][]model.SectionBill, error) {
	sections := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.InUSCode {
			sections = append(sections, e.SectionID)
		}
	}
	if len(sections) == 0 {
		return map[string][]model.SectionBill{}, nil
	}
	stmt := spanner.Statement{SQL: alsoChangedByPairsSQL, Params: map[string]any{
		"sections": sections, paramCitesKind: model.LawRefCites, paramBill: billID,
		"congressPart": strconv.FormatInt(congress, 10),
	}}
	pairs, err := queryRows(ctx, txn, stmt, "bills also changing sections",
		func(row alsoChangedByPairRow) alsoChangedByPairRow { return row })
	if err != nil {
		return nil, err
	}
	bills, err := readAlsoChangedByBills(ctx, txn, congress, pairs)
	if err != nil {
		return nil, err
	}
	// Newest first; a bill with no introduced date sorts last, then by bill ID. A pair whose bill
	// isn't in the congress (or isn't synced) has no row and is left out.
	pairs = slices.DeleteFunc(pairs, func(p alsoChangedByPairRow) bool { _, ok := bills[p.BillID]; return !ok })
	slices.SortFunc(pairs, func(a, b alsoChangedByPairRow) int {
		if c := compareNullDatesDesc(bills[a.BillID].IntroducedDate, bills[b.BillID].IntroducedDate); c != 0 {
			return c
		}
		return cmp.Compare(a.BillID, b.BillID)
	})
	out := map[string][]model.SectionBill{}
	for _, p := range pairs {
		if len(out[p.SectionID]) >= limit {
			continue
		}
		b := bills[p.BillID]
		kinds := p.RefKinds
		if kinds == nil {
			kinds = []string{}
		}
		slices.Sort(kinds)
		out[p.SectionID] = append(out[p.SectionID], model.SectionBill{
			BillID:        b.BillID,
			Congress:      int(b.Congress),
			BillType:      b.BillType,
			Number:        int(b.Number),
			Title:         b.Title,
			CurrentStatus: nullStringPtr(b.CurrentStatus),
			RefKinds:      kinds,
		})
	}
	return out, nil
}

// readAlsoChangedByBills reads the bills of pairs that belong to congress, by bill ID.
func readAlsoChangedByBills(
	ctx context.Context, txn querier, congress int64, pairs []alsoChangedByPairRow,
) (map[string]alsoChangedByBillRow, error) {
	if len(pairs) == 0 {
		return map[string]alsoChangedByBillRow{}, nil
	}
	ids := make([]string, 0, len(pairs))
	for _, p := range pairs {
		ids = append(ids, p.BillID)
	}
	slices.Sort(ids)
	stmt := spanner.Statement{SQL: alsoChangedByBillsSQL, Params: map[string]any{
		"bills": slices.Compact(ids), paramCongress: congress,
	}}
	rows, err := queryRows(ctx, txn, stmt, "bills also changing sections (bills)",
		func(row alsoChangedByBillRow) alsoChangedByBillRow { return row })
	if err != nil {
		return nil, err
	}
	out := make(map[string]alsoChangedByBillRow, len(rows))
	for _, row := range rows {
		out[row.BillID] = row
	}
	return out, nil
}

// compareNullDatesDesc orders later dates first and NULL after every date.
func compareNullDatesDesc(a, b spanner.NullDate) int {
	switch {
	case a.Valid && b.Valid:
		return b.Date.Compare(a.Date)
	case a.Valid:
		return -1
	case b.Valid:
		return 1
	}
	return 0
}
