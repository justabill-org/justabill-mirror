package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
)

// Default and maximum result sizes for the civic_graph queries. Every query has a LIMIT so a
// well-connected bill or member can't turn one request into an unbounded read.
const (
	defaultRelatedBills  = 10
	maxRelatedBills      = 50
	defaultCollaborators = 20
	maxCollaborators     = 100
	// A roll call has up to 435 House or 100 Senate votes, and a companion can have several.
	defaultCompanionVotes = 500
	maxCompanionVotes     = 2000
	defaultSectionBills   = 10
	maxSectionBills       = 50
	// An omnibus bill references thousands of sections, once per text version.
	defaultLawRefs = 1000
	maxLawRefs     = 5000

	// minSharedSubjects is how many legislative subjects a bill must share to count as related.
	minSharedSubjects = 2
	// companionRelation is the Congress.gov relation type for a bill's other-chamber companion.
	companionRelation = "Identical bill"

	// houseSeats and senateSeats count a roll call's member votes when it has no tally.
	houseSeats  = 435
	senateSeats = 100

	paramBill     = "bill"
	paramCongress = "congress"
	paramLimit    = "lim"
)

// relatedBillsSQL combines explicit RELATED_TO edges (either direction, since Congress.gov
// usually lists a relation on both bills) with bills sharing subjects through ABOUT edges.
// Joining bills drops relations to bills that aren't synced yet.
const relatedBillsSQL = `WITH explicit AS (
	SELECT bill_id, ARRAY_AGG(DISTINCT relation_type) AS relation_types
	FROM GRAPH_TABLE(civic_graph
		MATCH (b:Bill {bill_id: @bill})-[r:RELATED_TO]-(o:Bill)
		WHERE o.bill_id <> b.bill_id
		RETURN o.bill_id AS bill_id, r.relation_type AS relation_type)
	GROUP BY bill_id
), shared AS (
	SELECT bill_id, COUNT(DISTINCT subject_id) AS shared_subjects
	FROM GRAPH_TABLE(civic_graph
		MATCH (b:Bill {bill_id: @bill})-[:ABOUT]->(s:Subject)<-[:ABOUT]-(o:Bill)
		WHERE o.bill_id <> b.bill_id
		RETURN o.bill_id AS bill_id, s.subject_id AS subject_id)
	GROUP BY bill_id
), ids AS (
	SELECT bill_id FROM explicit
	UNION DISTINCT
	SELECT bill_id FROM shared WHERE shared_subjects >= @minShared
)
SELECT b.bill_id, b.congress, b.bill_type, b.number, b.title, b.current_status,
	COALESCE(e.relation_types, ARRAY<STRING>[]) AS relation_types,
	COALESCE(sh.shared_subjects, 0) AS shared_subjects
FROM ids
JOIN bills b ON b.bill_id = ids.bill_id
LEFT JOIN explicit e ON e.bill_id = ids.bill_id
LEFT JOIN shared sh ON sh.bill_id = ids.bill_id
ORDER BY e.bill_id IS NULL, shared_subjects DESC, b.congress DESC, b.bill_id
LIMIT @lim`

// collaboratorsSQL counts the bills of one congress that two members both sponsored or
// cosponsored. A member can hold two terms in a congress (a House member appointed to the
// Senate), so the term shown is the latest one.
const collaboratorsSQL = `SELECT g.peer_id, m.first_name, m.last_name, g.shared_bills,
	ARRAY(SELECT AS STRUCT t.party, t.state, t.chamber FROM member_terms t
		WHERE t.member_id = g.peer_id AND t.congress = @congress
		ORDER BY t.start_date DESC, t.chamber LIMIT 1) AS terms
FROM (
	SELECT peer_id, COUNT(DISTINCT bill_id) AS shared_bills
	FROM GRAPH_TABLE(civic_graph
		MATCH (a:Member {bioguide_id: @member})-[:SPONSORED]->(b:Bill {congress: @congress})
			<-[:SPONSORED]-(peer:Member)
		WHERE peer.bioguide_id <> a.bioguide_id
		RETURN peer.bioguide_id AS peer_id, b.bill_id AS bill_id)
	GROUP BY peer_id
) g
JOIN members m ON m.bioguide_id = g.peer_id
ORDER BY g.shared_bills DESC, g.peer_id
LIMIT @lim`

// companionRollCallsSQL finds a bill's companions, its "Identical bill" relations (listed on
// either bill) to a bill of the other chamber (House bill types start with "h"), and their roll
// calls, newest first, with each roll call's tally. Voice votes (no roll_number) are left out:
// their member rows are synthetic, not recorded positions.
const companionRollCallsSQL = `SELECT cv.bill_id AS companion_bill_id, cv.vote_id, cv.congress, cv.chamber,
	cv.vote_date, cv.question, cv.result,
	IFNULL(cv.yeas, 0) + IFNULL(cv.nays, 0) + IFNULL(cv.present, 0) + IFNULL(cv.not_voting, 0) AS tally
FROM (
	SELECT related_bill_id AS companion_id FROM bill_relations
	WHERE bill_id = @bill AND relation_type = @relation
	UNION DISTINCT
	SELECT bill_id AS companion_id FROM bill_relations@{FORCE_INDEX=idx_bill_relations_related}
	WHERE related_bill_id = @bill AND relation_type = @relation
) rel
JOIN bills b ON b.bill_id = @bill
JOIN bills c ON c.bill_id = rel.companion_id
JOIN congressional_votes@{FORCE_INDEX=idx_cv_bill} cv ON cv.bill_id = c.bill_id
WHERE STARTS_WITH(c.bill_type, 'h') <> STARTS_WITH(b.bill_type, 'h') AND cv.roll_number IS NOT NULL
ORDER BY cv.vote_date DESC, cv.vote_id`

// companionMemberVotesSQL reads the member votes of the roll calls companionRollCallsSQL chose,
// in its order, with each member's party in the roll call's chamber and congress.
const companionMemberVotesSQL = `SELECT cv.vote_id, m.bioguide_id AS member_id, m.first_name, m.last_name,
	t.party, mv.vote
FROM congressional_votes cv
JOIN member_votes mv ON mv.vote_id = cv.vote_id
JOIN members m ON m.bioguide_id = mv.member_id
LEFT JOIN member_terms t ON t.member_id = mv.member_id AND t.congress = cv.congress AND t.chamber = cv.chamber
WHERE cv.vote_id IN UNNEST(@votes)
ORDER BY cv.vote_date DESC, cv.vote_id, mv.member_id
LIMIT @lim`

// billsChangingSectionSQL follows CHANGES_LAW edges backwards from a LawSection to the bills of
// one congress whose text changes it. A bill that only cites the section isn't a change.
const billsChangingSectionSQL = `SELECT b.bill_id, b.congress, b.bill_type, b.number, b.title, b.current_status,
	g.ref_kinds
FROM (
	SELECT bill_id, ARRAY_AGG(DISTINCT ref_kind) AS ref_kinds
	FROM GRAPH_TABLE(civic_graph
		MATCH (b:Bill {congress: @congress})-[r:CHANGES_LAW]->(s:LawSection {section_id: @section})
		WHERE b.bill_id <> @exclude AND r.ref_kind <> @cites
		RETURN b.bill_id AS bill_id, r.ref_kind AS ref_kind)
	GROUP BY bill_id
) g
JOIN bills b ON b.bill_id = g.bill_id
ORDER BY b.introduced_date DESC, b.bill_id
LIMIT @lim`

// lawChangedByBillSQL lists a bill's CHANGES_LAW edges in text version order. Edges to sections
// that aren't loaded, including "nonusc:" references, don't match.
const lawChangedByBillSQL = `SELECT g.version_id, g.section_id, g.ref_kind, g.subsection_path, g.title_number,
	g.section_number, g.heading
FROM GRAPH_TABLE(civic_graph
	MATCH (b:Bill {bill_id: @bill})-[r:CHANGES_LAW]->(s:LawSection)
	RETURN r.version_id AS version_id, s.section_id AS section_id, r.ref_kind AS ref_kind,
		r.subsection_path AS subsection_path, s.title_number AS title_number,
		s.section_number AS section_number, s.heading AS heading) g
LEFT JOIN bill_text_versions v ON v.bill_id = @bill AND v.version_id = g.version_id
ORDER BY v.sort_order, g.version_id, g.title_number, g.section_number, g.ref_kind
LIMIT @lim`

// GraphRepository implements repository.GraphRepo with GQL over the civic_graph property graph.
type GraphRepository struct {
	client *spanner.Client
}

type relatedBillRow struct {
	BillID         string             `spanner:"bill_id"`
	Congress       int64              `spanner:"congress"`
	BillType       string             `spanner:"bill_type"`
	Number         int64              `spanner:"number"`
	Title          string             `spanner:"title"`
	CurrentStatus  spanner.NullString `spanner:"current_status"`
	RelationTypes  []string           `spanner:"relation_types"`
	SharedSubjects int64              `spanner:"shared_subjects"`
}

type sectionBillRow struct {
	BillID        string             `spanner:"bill_id"`
	Congress      int64              `spanner:"congress"`
	BillType      string             `spanner:"bill_type"`
	Number        int64              `spanner:"number"`
	Title         string             `spanner:"title"`
	CurrentStatus spanner.NullString `spanner:"current_status"`
	RefKinds      []string           `spanner:"ref_kinds"`
}

type lawRefRow struct {
	VersionID      string             `spanner:"version_id"`
	SectionID      string             `spanner:"section_id"`
	RefKind        string             `spanner:"ref_kind"`
	SubsectionPath spanner.NullString `spanner:"subsection_path"`
	TitleNumber    int64              `spanner:"title_number"`
	SectionNumber  string             `spanner:"section_number"`
	Heading        spanner.NullString `spanner:"heading"`
}

type termRow struct {
	Party   string `spanner:"party"`
	State   string `spanner:"state"`
	Chamber string `spanner:"chamber"`
}

type collaboratorRow struct {
	PeerID      string     `spanner:"peer_id"`
	FirstName   string     `spanner:"first_name"`
	LastName    string     `spanner:"last_name"`
	SharedBills int64      `spanner:"shared_bills"`
	Terms       []*termRow `spanner:"terms"`
}

type companionRollCallRow struct {
	CompanionBillID string             `spanner:"companion_bill_id"`
	VoteID          string             `spanner:"vote_id"`
	Congress        int64              `spanner:"congress"`
	Chamber         string             `spanner:"chamber"`
	VoteDate        spanner.NullTime   `spanner:"vote_date"`
	Question        spanner.NullString `spanner:"question"`
	Result          spanner.NullString `spanner:"result"`
	Tally           int64              `spanner:"tally"`
}

type companionMemberVoteRow struct {
	VoteID    string             `spanner:"vote_id"`
	MemberID  string             `spanner:"member_id"`
	FirstName string             `spanner:"first_name"`
	LastName  string             `spanner:"last_name"`
	Party     spanner.NullString `spanner:"party"`
	Vote      string             `spanner:"vote"`
}

// clampLimit returns def for a non-positive limit and caps it at maxLimit.
func clampLimit(limit, def, maxLimit int) int64 {
	if limit <= 0 {
		return int64(def)
	}
	return int64(min(limit, maxLimit))
}

// RelatedBills returns bills explicitly related to billID and bills sharing subjects with it.
func (r *GraphRepository) RelatedBills(ctx context.Context, billID string, limit int) ([]model.RelatedBill, error) {
	stmt := spanner.Statement{SQL: relatedBillsSQL, Params: map[string]any{
		paramBill:   billID,
		"minShared": int64(minSharedSubjects),
		paramLimit:  clampLimit(limit, defaultRelatedBills, maxRelatedBills),
	}}
	return queryGraph(ctx, r.client, stmt, "related bills", func(row relatedBillRow) model.RelatedBill {
		types := row.RelationTypes
		if types == nil {
			types = []string{}
		}
		slices.Sort(types)
		return model.RelatedBill{
			BillID:         row.BillID,
			Congress:       int(row.Congress),
			BillType:       row.BillType,
			Number:         int(row.Number),
			Title:          row.Title,
			CurrentStatus:  nullStringPtr(row.CurrentStatus),
			RelationTypes:  types,
			SharedSubjects: int(row.SharedSubjects),
		}
	})
}

// Collaborators returns the members who share the most sponsored bills with memberID in congress.
func (r *GraphRepository) Collaborators(
	ctx context.Context, memberID string, congress, limit int,
) ([]model.Collaborator, error) {
	stmt := spanner.Statement{SQL: collaboratorsSQL, Params: map[string]any{
		"member":      memberID,
		paramCongress: int64(congress),
		paramLimit:    clampLimit(limit, defaultCollaborators, maxCollaborators),
	}}
	return queryGraph(ctx, r.client, stmt, "collaborators", func(row collaboratorRow) model.Collaborator {
		c := model.Collaborator{
			BioguideID:  row.PeerID,
			FirstName:   row.FirstName,
			LastName:    row.LastName,
			SharedBills: int(row.SharedBills),
		}
		if len(row.Terms) > 0 {
			t := row.Terms[0]
			c.Party, c.State, c.Chamber = &t.Party, &t.State, &t.Chamber
		}
		return c
	})
}

// CompanionVotes returns member votes on recorded roll calls about billID's other-chamber
// identical bills, newest roll call first, at most limit of them. It reads the roll calls first
// and then the member votes of only the newest ones that fill limit, so a companion with dozens of
// roll calls doesn't join and sort every member vote on each (#784).
func (r *GraphRepository) CompanionVotes(
	ctx context.Context, billID string, limit int,
) ([]model.CompanionVote, error) {
	txn := r.client.ReadOnlyTransaction()
	defer txn.Close()

	lim := clampLimit(limit, defaultCompanionVotes, maxCompanionVotes)
	rollCalls, err := queryRows(ctx, txn, spanner.Statement{SQL: companionRollCallsSQL, Params: map[string]any{
		paramBill: billID, "relation": companionRelation,
	}}, "companion roll calls", func(row companionRollCallRow) companionRollCallRow { return row })
	if err != nil {
		return nil, err
	}
	if len(rollCalls) == 0 {
		return []model.CompanionVote{}, nil
	}
	rollCalls = newestRollCalls(rollCalls, lim)
	byID := make(map[string]companionRollCallRow, len(rollCalls))
	ids := make([]string, 0, len(rollCalls))
	for _, rc := range rollCalls {
		byID[rc.VoteID] = rc
		ids = append(ids, rc.VoteID)
	}
	stmt := spanner.Statement{SQL: companionMemberVotesSQL, Params: map[string]any{"votes": ids, paramLimit: lim}}
	return queryRows(ctx, txn, stmt, "companion votes", func(row companionMemberVoteRow) model.CompanionVote {
		rc := byID[row.VoteID]
		return model.CompanionVote{
			CompanionBillID: rc.CompanionBillID,
			VoteID:          row.VoteID,
			Chamber:         rc.Chamber,
			VoteDate:        rc.VoteDate.Time,
			Question:        nullStringPtr(rc.Question),
			Result:          nullStringPtr(rc.Result),
			MemberID:        row.MemberID,
			FirstName:       row.FirstName,
			LastName:        row.LastName,
			Party:           nullStringPtr(row.Party),
			Vote:            row.Vote,
		}
	})
}

// newestRollCalls keeps the newest roll calls (rollCalls is newest first) whose member votes
// reach limit, counting each by its tally or, without one, a full chamber. The member-vote read's
// LIMIT still cuts the last one short when the tallies undercount.
func newestRollCalls(rollCalls []companionRollCallRow, limit int64) []companionRollCallRow {
	var votes int64
	for i, rc := range rollCalls {
		n := rc.Tally
		if n <= 0 {
			n = chamberSeats(rc.Chamber)
		}
		if votes += n; votes >= limit {
			return rollCalls[:i+1]
		}
	}
	return rollCalls
}

// chamberSeats is how many members vote in chamber ("House" or "Senate").
func chamberSeats(chamber string) int64 {
	if chamber == senateChamber {
		return senateSeats
	}
	return houseSeats
}

// BillsChangingSection returns the other bills of congress whose text changes sectionID.
func (r *GraphRepository) BillsChangingSection(
	ctx context.Context, sectionID, excludeBillID string, congress, limit int,
) ([]model.SectionBill, error) {
	stmt := spanner.Statement{SQL: billsChangingSectionSQL, Params: map[string]any{
		"section":     sectionID,
		"exclude":     excludeBillID,
		"cites":       model.LawRefCites,
		paramCongress: int64(congress),
		paramLimit:    clampLimit(limit, defaultSectionBills, maxSectionBills),
	}}
	return queryGraph(ctx, r.client, stmt, "bills changing section", func(row sectionBillRow) model.SectionBill {
		kinds := row.RefKinds
		if kinds == nil {
			kinds = []string{}
		}
		slices.Sort(kinds)
		return model.SectionBill{
			BillID:        row.BillID,
			Congress:      int(row.Congress),
			BillType:      row.BillType,
			Number:        int(row.Number),
			Title:         row.Title,
			CurrentStatus: nullStringPtr(row.CurrentStatus),
			RefKinds:      kinds,
		}
	})
}

// LawChangedByBill returns billID's CHANGES_LAW edges with each section's heading.
func (r *GraphRepository) LawChangedByBill(ctx context.Context, billID string, limit int) ([]model.LawRef, error) {
	stmt := spanner.Statement{SQL: lawChangedByBillSQL, Params: map[string]any{
		paramBill:  billID,
		paramLimit: clampLimit(limit, defaultLawRefs, maxLawRefs),
	}}
	return queryGraph(ctx, r.client, stmt, "law changed by bill", func(row lawRefRow) model.LawRef {
		return model.LawRef{
			VersionID:      row.VersionID,
			SectionID:      row.SectionID,
			RefKind:        row.RefKind,
			SubsectionPath: nullStringPtr(row.SubsectionPath),
			TitleNumber:    int(row.TitleNumber),
			SectionNumber:  row.SectionNumber,
			Heading:        nullStringPtr(row.Heading),
		}
	})
}

// queryGraph runs a single-use read, decodes each row into R with ToStruct and converts it.
// It returns an empty, non-nil slice when nothing matches.
func queryGraph[R, T any](
	ctx context.Context, client *spanner.Client, stmt spanner.Statement, what string, convert func(R) T,
) ([]T, error) {
	return queryRows(ctx, client.Single(), stmt, what, convert)
}

// queryRows is queryGraph on a transaction the caller chose.
func queryRows[R, T any](
	ctx context.Context, txn querier, stmt spanner.Statement, what string, convert func(R) T,
) ([]T, error) {
	iter := txn.Query(ctx, stmt)
	defer iter.Stop()

	out := []T{}
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", what, err)
		}
		var r R
		if err = row.ToStruct(&r); err != nil {
			return nil, fmt.Errorf("read %s: %w", what, err)
		}
		out = append(out, convert(r))
	}
}
