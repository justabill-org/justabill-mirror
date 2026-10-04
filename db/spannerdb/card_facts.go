package spannerdb

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/scoring"
)

// The /vote card's facts (#704): the latest CRS summary's lead, how each chamber passed the bill,
// when it became law and how many sections of law it changes, for a page of bills in two reads.

const (
	// statusBecameLaw is the bill status the pipeline records once a bill is law.
	statusBecameLaw = "became_law"
	// statusToPresident, statusSigned and statusVetoed are the statuses of a bill that passed both
	// chambers and isn't law yet.
	statusToPresident = "to_president"
	statusSigned      = "signed"
	statusVetoed      = "vetoed"

	paramBecameLaw = "becameLaw"
	paramLawAction = "lawAction"

	// voteIDMethodField is the field of an unrecorded passage's vote ID
	// ("house-119-voice-hr-119-1276-20251209", split on "-") that holds its method.
	voteIDMethodField = 2
)

// cardBillSQL reads each bill's latest CRS summary (the one GetCRSSummary serves), the laws
// Congress.gov says it became, its "Became Public Law No:" action, its became_law status and the
// number of sections its latest stored text changes, counted as GET /bills/{id}/law-changes groups
// them (lawChangeEntriesSQL).
const cardBillSQL = `SELECT bill_id, current_status, status_date, laws, law_status_date,
	law.action_date AS law_action_date, law.action_text AS law_action_text,
	crs.version_code AS crs_version_code, crs.action_date AS crs_action_date,
	crs.action_desc AS crs_action_desc, crs.text AS crs_text, law_change_count
FROM (
	SELECT b.bill_id, b.current_status, b.status_date, b.laws,
		(SELECT h.status_date FROM bill_status_history h
		 WHERE h.bill_id = b.bill_id AND h.status = @becameLaw) AS law_status_date,
		(SELECT AS STRUCT a.action_date, a.action_text FROM bill_actions a
		 WHERE a.bill_id = b.bill_id AND REGEXP_CONTAINS(a.action_text, @lawAction)
		 ORDER BY a.action_date DESC, a.sort_order DESC LIMIT 1) AS law,
		(SELECT AS STRUCT c.version_code, c.action_date, c.action_desc, c.text FROM bill_crs_summaries c
		 WHERE c.bill_id = b.bill_id
		 ORDER BY c.action_date DESC, c.crs_updated_at DESC LIMIT 1) AS crs,
		(SELECT COUNT(DISTINCT r.section_id) FROM bill_law_refs r
		 WHERE r.bill_id = b.bill_id AND r.ref_kind != @citesKind
			AND r.version_id = (
				SELECT btv.version_id
				FROM bill_text_versions btv JOIN bill_texts bt ON bt.version_id = btv.version_id
				WHERE btv.bill_id = b.bill_id
				ORDER BY ` + latestVersionOrder + ` LIMIT 1)) AS law_change_count
	FROM bills b
	WHERE b.bill_id IN UNNEST(@billIDs)
)`

// cardVotesSQL reads the bills' votes oldest first, through idx_cv_bill.
const cardVotesSQL = `SELECT bill_id, vote_id, chamber, roll_number, vote_date, question, result,
	yeas, nays, present, not_voting
FROM congressional_votes
WHERE bill_id IN UNNEST(@billIDs)
ORDER BY bill_id, vote_date, vote_id`

type cardBillRow struct {
	BillID         string             `spanner:"bill_id"`
	CurrentStatus  spanner.NullString `spanner:"current_status"`
	StatusDate     spanner.NullDate   `spanner:"status_date"`
	Laws           spanner.NullJSON   `spanner:"laws"`
	LawStatusDate  spanner.NullDate   `spanner:"law_status_date"`
	LawActionDate  spanner.NullDate   `spanner:"law_action_date"`
	LawActionText  spanner.NullString `spanner:"law_action_text"`
	CRSVersionCode spanner.NullString `spanner:"crs_version_code"`
	CRSActionDate  spanner.NullDate   `spanner:"crs_action_date"`
	CRSActionDesc  spanner.NullString `spanner:"crs_action_desc"`
	CRSText        spanner.NullString `spanner:"crs_text"`
	LawChangeCount int64              `spanner:"law_change_count"`
}

type cardVoteRow struct {
	BillID     string             `spanner:"bill_id"`
	VoteID     string             `spanner:"vote_id"`
	Chamber    string             `spanner:"chamber"`
	RollNumber spanner.NullInt64  `spanner:"roll_number"`
	VoteDate   time.Time          `spanner:"vote_date"`
	Question   spanner.NullString `spanner:"question"`
	Result     spanner.NullString `spanner:"result"`
	Yeas       spanner.NullInt64  `spanner:"yeas"`
	Nays       spanner.NullInt64  `spanner:"nays"`
	Present    spanner.NullInt64  `spanner:"present"`
	NotVoting  spanner.NullInt64  `spanner:"not_voting"`
}

// GetCardFacts implements [repository.BillRepo]. It reads the bills and their votes in one
// read-only transaction, two queries whatever the number of bills.
func (r *BillRepository) GetCardFacts(ctx context.Context, billIDs []string) (map[string]model.BillCardFacts, error) {
	out := map[string]model.BillCardFacts{}
	ids := slices.Compact(slices.Sorted(slices.Values(billIDs)))
	if len(ids) == 0 {
		return out, nil
	}
	txn := r.client.ReadOnlyTransaction()
	defer txn.Close()

	bills, err := queryRows(ctx, txn, spanner.Statement{SQL: cardBillSQL, Params: map[string]any{
		paramBillIDs: ids, paramBecameLaw: statusBecameLaw, paramLawAction: model.LawActionPattern(),
		paramCitesKind: model.LawRefCites,
	}}, "card facts", func(row cardBillRow) cardBillRow { return row })
	if err != nil {
		return nil, err
	}
	votes, err := queryRows(ctx, txn, spanner.Statement{SQL: cardVotesSQL, Params: map[string]any{paramBillIDs: ids}},
		"card votes", func(row cardVoteRow) cardVoteRow { return row })
	if err != nil {
		return nil, err
	}
	passage := passageEntries(votes)
	for _, row := range bills {
		f := model.BillCardFacts{
			CRS:            cardCRS(row),
			Passage:        passage[row.BillID],
			Enacted:        cardEnactment(row),
			LawChangeCount: int(row.LawChangeCount),
		}
		if f.Passage == nil {
			f.Passage = []model.PassageEntry{}
		}
		if !f.Empty() {
			out[row.BillID] = f
		}
	}
	return out, nil
}

func cardCRS(row cardBillRow) *model.CardCRS {
	if !row.CRSVersionCode.Valid {
		return nil
	}
	return &model.CardCRS{
		VersionCode: row.CRSVersionCode.StringVal,
		ActionDate:  row.CRSActionDate.Date.In(time.UTC),
		ActionDesc:  row.CRSActionDesc.StringVal,
		Lead:        model.CRSLead(row.CRSText.StringVal),
	}
}

// cardEnactment is when the bill became law and which law it became. The date comes from the
// "Became Public Law No:" action; without one, from the became_law status, in the status history
// or as the bill's current status. Without any date there's no enactment. The law's type and
// number come from bills.laws (#736), Congress.gov's own record; only for a row the pipeline
// hasn't synced since that column was added (or one that names no law the card knows) are they
// read from the action's text.
func cardEnactment(row cardBillRow) *model.Enactment {
	lawType, number, fromAction := model.ParseLawAction(row.LawActionText.StringVal)
	var e *model.Enactment
	switch {
	case fromAction && row.LawActionDate.Valid:
		e = &model.Enactment{Date: row.LawActionDate.Date.In(time.UTC), LawType: &lawType, LawNumber: &number}
	case row.LawStatusDate.Valid:
		e = &model.Enactment{Date: row.LawStatusDate.Date.In(time.UTC)}
	case row.CurrentStatus.StringVal == statusBecameLaw && row.StatusDate.Valid:
		e = &model.Enactment{Date: row.StatusDate.Date.In(time.UTC)}
	default:
		return nil
	}
	if lawsType, lawsNumber, ok := firstCardLaw(nullJSONToLaws(row.Laws)); ok {
		e.LawType, e.LawNumber = &lawsType, &lawsNumber
	}
	return e
}

// firstCardLaw is the first of a bill's laws that is a public or private law with a number, as
// the card's law type ("public" or "private") and number ("119-95"). A bill rarely becomes more
// than one law, and the card names one.
func firstCardLaw(laws []model.BillLaw) (string, string, bool) {
	for _, law := range laws {
		if law.Number == "" {
			continue
		}
		switch law.Type {
		case model.BillLawTypePublic:
			return model.LawTypePublic, law.Number, true
		case model.BillLawTypePrivate:
			return model.LawTypePrivate, law.Number, true
		}
	}
	return "", "", false
}

// passageEntries keeps, per bill, each chamber's latest vote that passed or failed the bill
// itself, oldest chamber first. votes are ordered by bill, date and vote ID, so of two on the
// same day the later ID wins.
func passageEntries(votes []cardVoteRow) map[string][]model.PassageEntry {
	latest := map[string]map[string]model.PassageEntry{}
	for _, v := range votes {
		method := passageMethod(v)
		if method == "" {
			continue
		}
		if latest[v.BillID] == nil {
			latest[v.BillID] = map[string]model.PassageEntry{}
		}
		latest[v.BillID][v.Chamber] = passageEntry(v, method)
	}
	out := make(map[string][]model.PassageEntry, len(latest))
	for billID, byChamber := range latest {
		entries := slices.Collect(maps.Values(byChamber))
		slices.SortFunc(entries, func(a, b model.PassageEntry) int {
			return cmp.Or(a.Date.Compare(b.Date), cmp.Compare(a.Chamber, b.Chamber))
		})
		out[billID] = entries
	}
	return out
}

// passageMethod is how the vote passed (or failed) the bill, or "" when it isn't a vote on
// passage: a roll call whose question is on the final-passage allowlist ([scoring.IsFinalVote]),
// or an unrecorded passage the pipeline stored from the bill's actions.
func passageMethod(v cardVoteRow) string {
	if v.RollNumber.Valid {
		if scoring.IsFinalVote(v.Chamber, v.Question.StringVal) {
			return model.PassageRoll
		}
		return ""
	}
	fields := strings.Split(v.VoteID, "-")
	if len(fields) <= voteIDMethodField {
		return ""
	}
	switch m := fields[voteIDMethodField]; m {
	case model.PassageVoice, model.PassageUC:
		return m
	default:
		return ""
	}
}

func passageEntry(v cardVoteRow, method string) model.PassageEntry {
	e := model.PassageEntry{
		Chamber:  v.Chamber,
		Method:   method,
		Date:     v.VoteDate,
		Question: nullStringPtr(v.Question),
		Result:   nullStringPtr(v.Result),
	}
	if method == model.PassageRoll {
		e.RollNumber = nullInt64Ptr(v.RollNumber)
		e.Yeas = nullInt64Ptr(v.Yeas)
		e.Nays = nullInt64Ptr(v.Nays)
		e.Present = nullInt64Ptr(v.Present)
		e.NotVoting = nullInt64Ptr(v.NotVoting)
	}
	return e
}
