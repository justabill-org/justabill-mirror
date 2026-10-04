package spannerdb

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
)

// MemberRepository implements repository.MemberRepo with Spanner.
type MemberRepository struct {
	client *spanner.Client
}

// List returns a paginated list of members by name, with bioguide_id breaking ties between
// members who share one so pages don't overlap.
func (r *MemberRepository) List(
	ctx context.Context,
	params model.ListParams,
) (*model.ListResult[model.Member], error) {
	where := "WHERE TRUE"
	join := ""
	p := map[string]any{}

	if params.Congress != nil || params.Chamber != nil || params.State != nil || params.District != nil {
		join = "JOIN member_terms mt ON m.bioguide_id = mt.member_id"
		if params.Congress != nil {
			where += " AND mt.congress = @congress"
			p[paramCongress] = int64(*params.Congress)
		}
		if params.Chamber != nil {
			where += " AND mt.chamber = @chamber"
			p[paramChamber] = canonicalChamber(*params.Chamber)
		}
		if params.State != nil {
			where += " AND mt.state = @state"
			p[paramState] = *params.State
		}
		if params.District != nil {
			where += " AND mt.district = @district"
			p["district"] = int64(*params.District)
		}
	}

	// Count
	var total int64
	countSQL := fmt.Sprintf("SELECT COUNT(DISTINCT m.bioguide_id) FROM members m %s %s", join, where)
	countIter := r.client.Single().Query(ctx, spanner.Statement{SQL: countSQL, Params: p})
	defer countIter.Stop()
	countRow, err := countIter.Next()
	if err != nil {
		return nil, err
	}
	if err = countRow.Columns(&total); err != nil {
		return nil, err
	}
	countIter.Stop()

	// Select
	p[paramLimit] = int64(params.Limit)
	p["off"] = int64(params.Offset)
	selectSQL := fmt.Sprintf(
		`SELECT DISTINCT m.bioguide_id, m.first_name, m.last_name, m.birth_year, m.photo_url, m.official_url
		 FROM members m %s %s
		 ORDER BY m.last_name, m.first_name, m.bioguide_id
		 LIMIT @lim OFFSET @off`, join, where)
	selectIter := r.client.Single().Query(ctx, spanner.Statement{SQL: selectSQL, Params: p})
	defer selectIter.Stop()

	var items []model.Member
	for {
		row, iterErr := selectIter.Next()
		if errors.Is(iterErr, iterator.Done) {
			break
		}
		if iterErr != nil {
			return nil, iterErr
		}
		m, scanErr := scanMember(row)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, m)
	}
	if items == nil {
		items = []model.Member{}
	}
	return &model.ListResult[model.Member]{
		Items:  items,
		Total:  int(total),
		Offset: params.Offset,
		Limit:  params.Limit,
	}, nil
}

// GetByID returns a single member by bioguide ID.
func (r *MemberRepository) GetByID(ctx context.Context, bioguideID string) (*model.MemberDetail, error) {
	stmt := spanner.Statement{
		SQL: `SELECT bioguide_id, first_name, last_name, birth_year, photo_url, official_url
		 FROM members WHERE bioguide_id = @id`,
		Params: map[string]any{"id": bioguideID},
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
		bid, firstName, lastName string
		birthYear                spanner.NullInt64
		photoURL, officialURL    spanner.NullString
	)
	if err = row.Columns(&bid, &firstName, &lastName, &birthYear, &photoURL, &officialURL); err != nil {
		return nil, err
	}
	iter.Stop()

	m := &model.MemberDetail{
		BioguideID: bid, FirstName: firstName, LastName: lastName,
		BirthYear: nullInt64Ptr(birthYear), PhotoURL: nullStringPtr(photoURL),
		OfficialURL: nullStringPtr(officialURL),
	}

	// Fetch terms
	termStmt := spanner.Statement{
		SQL: `SELECT member_id, congress, chamber, state, district, party, start_date, end_date
		 FROM member_terms WHERE member_id = @id ORDER BY congress DESC`,
		Params: map[string]any{"id": bioguideID},
	}
	termIter := r.client.Single().Query(ctx, termStmt)
	defer termIter.Stop()

	for {
		tRow, tErr := termIter.Next()
		if errors.Is(tErr, iterator.Done) {
			break
		}
		if tErr != nil {
			return nil, tErr
		}
		var (
			memberID, chamber, state, party string
			congress                        int64
			district                        spanner.NullInt64
			startDate, endDate              spanner.NullDate
		)
		if tErr = tRow.Columns(&memberID, &congress, &chamber, &state, &district, &party,
			&startDate, &endDate); tErr != nil {
			return nil, tErr
		}
		m.Terms = append(m.Terms, model.MemberTerm{
			MemberID: memberID, Congress: int(congress), Chamber: chamber,
			State: state, District: nullInt64Ptr(district), Party: party,
			StartDate: nullDatePtr(startDate), EndDate: nullDatePtr(endDate),
		})
	}
	if m.Terms == nil {
		m.Terms = []model.MemberTerm{}
	}
	return m, nil
}

// GetByDistrict returns the members who hold a state's House seat in the current congress. A
// member whose term has an end_date has left and isn't returned.
func (r *MemberRepository) GetByDistrict(ctx context.Context, state string, district int) ([]model.Member, error) {
	stmt := spanner.Statement{
		SQL: `SELECT m.bioguide_id, m.first_name, m.last_name, m.birth_year, m.photo_url, m.official_url
		 FROM members m
		 JOIN member_terms mt ON m.bioguide_id = mt.member_id
		 WHERE mt.state = @state AND mt.district = @district AND mt.chamber = 'House'
		   AND mt.congress = (SELECT number FROM congresses WHERE is_current = TRUE LIMIT 1)
		   AND mt.end_date IS NULL`,
		Params: map[string]any{paramState: state, "district": int64(district)},
	}
	return r.queryMembers(ctx, stmt)
}

// GetSenators returns the senators who represent a state in the current congress, leaving out
// those whose term has an end_date.
func (r *MemberRepository) GetSenators(ctx context.Context, state string) ([]model.Member, error) {
	stmt := spanner.Statement{
		SQL: `SELECT m.bioguide_id, m.first_name, m.last_name, m.birth_year, m.photo_url, m.official_url
		 FROM members m
		 JOIN member_terms mt ON m.bioguide_id = mt.member_id
		 WHERE mt.state = @state AND mt.chamber = 'Senate'
		   AND mt.congress = (SELECT number FROM congresses WHERE is_current = TRUE LIMIT 1)
		   AND mt.end_date IS NULL
		 ORDER BY m.last_name, m.first_name`,
		Params: map[string]any{paramState: state},
	}
	return r.queryMembers(ctx, stmt)
}

// GetRecentVotes returns a member's most recent roll call votes, newest first. Votes on the same
// day are ordered by session, roll number and vote ID, as scoring.later breaks ties, so the
// order is the same on every read (#788). BillTitle is nil when the vote's bill isn't stored.
func (r *MemberRepository) GetRecentVotes(
	ctx context.Context,
	bioguideID string,
	limit int,
) ([]model.MemberVoteSummary, error) {
	stmt := spanner.Statement{
		SQL: `SELECT cv.vote_id, cv.bill_id, b.title, cv.vote_date, cv.question, cv.result, mv.vote, cv.chamber
		 FROM member_votes mv
		 JOIN congressional_votes cv ON mv.vote_id = cv.vote_id
		 LEFT JOIN bills b ON cv.bill_id = b.bill_id
		 WHERE mv.member_id = @memberID
		 ORDER BY cv.vote_date DESC, cv.session DESC, cv.roll_number DESC, cv.vote_id DESC
		 LIMIT @lim`,
		Params: map[string]any{"memberID": bioguideID, paramLimit: int64(limit)},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var votes []model.MemberVoteSummary
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		var (
			voteID, memberVote, chamber string
			billID                      spanner.NullString
			billTitle                   spanner.NullString
			voteDate                    spanner.NullTime
			question                    spanner.NullString
			result                      spanner.NullString
		)
		if err = row.Columns(&voteID, &billID, &billTitle, &voteDate, &question, &result,
			&memberVote, &chamber); err != nil {
			return nil, err
		}
		v := model.MemberVoteSummary{
			VoteID: voteID, MemberVote: memberVote, Chamber: chamber,
			BillID: nullStringPtr(billID), BillTitle: nullStringPtr(billTitle),
			Question: nullStringPtr(question), Result: nullStringPtr(result),
		}
		if voteDate.Valid {
			v.VoteDate = voteDate.Time
		}
		votes = append(votes, v)
	}
	if votes == nil {
		votes = []model.MemberVoteSummary{}
	}
	return votes, nil
}

func (r *MemberRepository) queryMembers(ctx context.Context, stmt spanner.Statement) ([]model.Member, error) {
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var members []model.Member
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		m, scanErr := scanMember(row)
		if scanErr != nil {
			return nil, scanErr
		}
		members = append(members, m)
	}
	if members == nil {
		members = []model.Member{}
	}
	return members, nil
}

func scanMember(row *spanner.Row) (model.Member, error) {
	var (
		bid, firstName, lastName string
		birthYear                spanner.NullInt64
		photoURL, officialURL    spanner.NullString
	)
	if err := row.Columns(&bid, &firstName, &lastName, &birthYear, &photoURL, &officialURL); err != nil {
		return model.Member{}, err
	}
	return model.Member{
		BioguideID: bid, FirstName: firstName, LastName: lastName,
		BirthYear: nullInt64Ptr(birthYear), PhotoURL: nullStringPtr(photoURL),
		OfficialURL: nullStringPtr(officialURL),
	}, nil
}
