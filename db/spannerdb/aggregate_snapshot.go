package spannerdb

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/scoring"
)

// voteStoredAt is when the server stored the vote aliased v (#458): the burst and new-since
// windows read it rather than voted_at, which an import takes from the browser.
const voteStoredAt = "COALESCE(v.recorded_at, v.voted_at)"

// newSince is the condition for a vote stored after the last publish of the cell aliased
// alias, or any vote when that cell was never published.
func newSince(alias string) string {
	return "(" + alias + ".published_at IS NULL OR " + voteStoredAt + " > " + alias + ".published_at)"
}

// aggregateGroupsSQL counts the eligible Yea and Nay votes by bill, state and district
// (docs/design/89-aggregate-analytics.md). An eligible vote is Yea or Nay, from an account
// that isn't excluded and is at least the minimum age, and passed App Check when that's
// required. The three LEFT JOINs find the last publish of the group's national, state and
// district cells, for the new-account share hold rule. user_votes is interleaved in users, so
// the join is local.
func aggregateGroupsSQL() string {
	n, s, d := newSince("n"), newSince("s"), newSince("d")
	young := " AND u.created_at > @youngFrom)"
	return `SELECT v.bill_id, u.state, u.district,
  COUNTIF(v.vote = 'yea') AS yea,
  COUNTIF(v.vote = 'nay') AS nay,
  COUNTIF(` + voteStoredAt + ` >= @recentFrom) AS recent,
  COUNTIF(` + voteStoredAt + ` >= @baselineFrom AND ` + voteStoredAt + ` < @recentFrom) AS baseline,
  COUNTIF(` + n + `) AS new_national,
  COUNTIF(` + n + young + ` AS young_national,
  COUNTIF(` + s + `) AS new_state,
  COUNTIF(` + s + young + ` AS young_state,
  COUNTIF(` + d + `) AS new_district,
  COUNTIF(` + d + young + ` AS young_district
FROM user_votes AS v
JOIN users AS u ON u.user_id = v.user_id
LEFT JOIN vote_aggregates AS n
  ON n.bill_id = v.bill_id AND n.scope = 'national' AND n.scope_key = ''
LEFT JOIN vote_aggregates AS s
  ON s.bill_id = v.bill_id AND s.scope = 'state' AND s.scope_key = u.state
LEFT JOIN vote_aggregates AS d
  ON d.bill_id = v.bill_id AND d.scope = 'district'
  AND d.scope_key = CONCAT(u.state, '-', CAST(u.district AS STRING))
WHERE v.vote IN ('yea', 'nay')
  AND u.agg_excluded_at IS NULL
  AND u.created_at <= @eligibleBefore
  AND (NOT @requireAppCheck OR v.app_check_ok = TRUE)
GROUP BY v.bill_id, u.state, u.district`
}

// aggregateIneligibleSQL counts the Yea and Nay votes the snapshot leaves out, each under the
// first reason that applies: excluded, then a young account, then no App Check.
const aggregateIneligibleSQL = `SELECT
  COUNTIF(u.agg_excluded_at IS NOT NULL) AS excluded,
  COUNTIF(u.agg_excluded_at IS NULL AND u.created_at > @eligibleBefore) AS young,
  COUNTIF(u.agg_excluded_at IS NULL AND u.created_at <= @eligibleBefore
    AND @requireAppCheck AND IFNULL(v.app_check_ok, FALSE) = FALSE) AS no_app_check
FROM user_votes AS v
JOIN users AS u ON u.user_id = v.user_id
WHERE v.vote IN ('yea', 'nay')`

// aggregateCellBillsSQL lists the bills with a cell that's shown.
const aggregateCellBillsSQL = `SELECT DISTINCT bill_id FROM vote_aggregates
WHERE status IN UNNEST(@statuses) ORDER BY bill_id`

// aggregateGroupRow is one row of aggregateGroupsSQL.
type aggregateGroupRow struct {
	BillID        string             `spanner:"bill_id"`
	State         spanner.NullString `spanner:"state"`
	District      spanner.NullInt64  `spanner:"district"`
	Yea           int64              `spanner:"yea"`
	Nay           int64              `spanner:"nay"`
	Recent        int64              `spanner:"recent"`
	Baseline      int64              `spanner:"baseline"`
	NewNational   int64              `spanner:"new_national"`
	YoungNational int64              `spanner:"young_national"`
	NewState      int64              `spanner:"new_state"`
	YoungState    int64              `spanner:"young_state"`
	NewDistrict   int64              `spanner:"new_district"`
	YoungDistrict int64              `spanner:"young_district"`
}

func (r *aggregateGroupRow) group() repository.AggregateVoteGroup {
	return repository.AggregateVoteGroup{
		BillID: r.BillID, State: r.State.StringVal, District: nullInt64Ptr(r.District),
		Yea: int(r.Yea), Nay: int(r.Nay), Recent: int(r.Recent), Baseline: int(r.Baseline),
		New: map[string]int{
			model.AggregateScopeNational: int(r.NewNational),
			model.AggregateScopeState:    int(r.NewState),
			model.AggregateScopeDistrict: int(r.NewDistrict),
		},
		Young: map[string]int{
			model.AggregateScopeNational: int(r.YoungNational),
			model.AggregateScopeState:    int(r.YoungState),
			model.AggregateScopeDistrict: int(r.YoungDistrict),
		},
	}
}

// AggregateSnapshot reads the eligible votes by bill, state and district, the ineligible ones
// by reason, and the bills with a shown cell, all in one read-only transaction.
func (s *PipelineStoreImpl) AggregateSnapshot(
	ctx context.Context,
	p repository.AggregateSnapshotParams,
) (*repository.AggregateSnapshot, error) {
	ro := s.client.ReadOnlyTransaction()
	defer ro.Close()

	recentFrom := p.AsOf.Add(-p.BurstWindow)
	params := map[string]any{
		"eligibleBefore":  p.AsOf.Add(-p.MinAccountAge),
		"youngFrom":       p.AsOf.Add(-p.YoungAccountAge),
		"recentFrom":      recentFrom,
		"baselineFrom":    recentFrom.Add(-p.BaselineWindow),
		"requireAppCheck": p.RequireAppCheck,
	}
	snap := &repository.AggregateSnapshot{Ineligible: map[string]int{}}
	err := eachRow(ctx, ro, spanner.Statement{SQL: aggregateGroupsSQL(), Params: params}, func(row *spanner.Row) error {
		var r aggregateGroupRow
		if err := row.ToStruct(&r); err != nil {
			return err
		}
		snap.Groups = append(snap.Groups, r.group())
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("aggregate snapshot: eligible votes: %w", err)
	}

	err = eachRow(
		ctx,
		ro,
		spanner.Statement{SQL: aggregateIneligibleSQL, Params: params},
		func(row *spanner.Row) error {
			var excluded, young, noAppCheck int64
			if colErr := row.Columns(&excluded, &young, &noAppCheck); colErr != nil {
				return colErr
			}
			snap.Ineligible[repository.IneligibleExcluded] = int(excluded)
			snap.Ineligible[repository.IneligibleYoung] = int(young)
			snap.Ineligible[repository.IneligibleNoAppCheck] = int(noAppCheck)
			return nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("aggregate snapshot: ineligible votes: %w", err)
	}

	stmt := spanner.Statement{
		SQL:    aggregateCellBillsSQL,
		Params: map[string]any{paramStatuses: servedAggregateStatuses()},
	}
	err = eachRow(ctx, ro, stmt, func(row *spanner.Row) error {
		var billID string
		if colErr := row.Columns(&billID); colErr != nil {
			return colErr
		}
		snap.CellBills = append(snap.CellBills, billID)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("aggregate snapshot: cell bills: %w", err)
	}
	return snap, nil
}

// senateChamber is member_terms and congressional_votes' name for the Senate.
const senateChamber = "Senate"

// seatCandidatesSQL reads the members' recorded roll calls on the bills, with the seat each
// member held in the roll call's congress and chamber.
const seatCandidatesSQL = `SELECT mv.member_id, cv.bill_id, cv.vote_id, cv.chamber, cv.congress,
       cv.session, cv.roll_number, cv.vote_date, cv.question, mv.vote, mt.state, mt.district
FROM congressional_votes@{FORCE_INDEX=idx_cv_bill} AS cv
JOIN member_votes AS mv ON mv.vote_id = cv.vote_id
JOIN member_terms AS mt
  ON mt.member_id = mv.member_id AND mt.congress = cv.congress AND mt.chamber = cv.chamber
WHERE cv.bill_id IN UNNEST(@billIDs) AND cv.roll_number IS NOT NULL`

// seatCandidateRow is one row of seatCandidatesSQL.
type seatCandidateRow struct {
	MemberID   string             `spanner:"member_id"`
	BillID     string             `spanner:"bill_id"`
	VoteID     string             `spanner:"vote_id"`
	Chamber    string             `spanner:"chamber"`
	Congress   int64              `spanner:"congress"`
	Session    spanner.NullInt64  `spanner:"session"`
	RollNumber int64              `spanner:"roll_number"`
	VoteDate   time.Time          `spanner:"vote_date"`
	Question   spanner.NullString `spanner:"question"`
	Vote       string             `spanner:"vote"`
	State      string             `spanner:"state"`
	District   spanner.NullInt64  `spanner:"district"`
}

// seatKey names one member's seat in one congress and chamber.
type seatKey struct {
	memberID string
	congress int
	scopeKey string
}

// scopeKey is the seat's constituency: the state for a senator, the district for the House.
func (r *seatCandidateRow) scopeKey() string {
	if r.Chamber == senateChamber || !r.District.Valid {
		return r.State
	}
	return r.State + "-" + strconv.FormatInt(r.District.Int64, 10)
}

// BillSeatPositions applies the scorecard rule (db/scoring) to the members' roll calls on the
// bills and returns each member's position with their seat, by member, congress and bill.
func (s *PipelineStoreImpl) BillSeatPositions(
	ctx context.Context,
	billIDs []string,
) ([]repository.SeatPosition, error) {
	if len(billIDs) == 0 {
		return nil, nil
	}
	candidates := map[seatKey][]scoring.Candidate{}
	stmt := spanner.Statement{SQL: seatCandidatesSQL, Params: map[string]any{paramBillIDs: billIDs}}
	err := eachRow(ctx, s.client.Single(), stmt, func(row *spanner.Row) error {
		var r seatCandidateRow
		if err := row.ToStruct(&r); err != nil {
			return err
		}
		k := seatKey{memberID: r.MemberID, congress: int(r.Congress), scopeKey: r.scopeKey()}
		candidates[k] = append(candidates[k], scoring.Candidate{
			BillID: r.BillID, VoteID: r.VoteID, Chamber: r.Chamber, Congress: int(r.Congress),
			Session: int(r.Session.Int64), RollNumber: int(r.RollNumber), VoteDate: r.VoteDate,
			Question: r.Question.StringVal, Vote: r.Vote,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bill seat positions: %w", err)
	}
	var positions []repository.SeatPosition
	for k := range candidates {
		for _, p := range scoring.PickPositions(candidates[k]) {
			positions = append(positions, repository.SeatPosition{
				MemberID: k.memberID, Congress: k.congress, ScopeKey: k.scopeKey, BillID: p.BillID, Vote: p.Vote,
			})
		}
	}
	slices.SortFunc(positions, func(a, b repository.SeatPosition) int {
		return cmp.Or(cmp.Compare(a.MemberID, b.MemberID), cmp.Compare(a.Congress, b.Congress),
			cmp.Compare(a.ScopeKey, b.ScopeKey), cmp.Compare(a.BillID, b.BillID))
	})
	return positions, nil
}
