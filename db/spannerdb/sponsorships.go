package spannerdb

import (
	"context"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// sponsorshipsSQL reads a bill's sponsor and cosponsors from the link table. The FK to members
// is NOT ENFORCED, so a sponsor who isn't synced yet still comes back, without a name. A member
// can hold two terms in a congress (a House member appointed to the Senate); the latest is used.
const sponsorshipsSQL = `SELECT s.member_id, COALESCE(m.first_name, '') AS first_name,
	COALESCE(m.last_name, '') AS last_name, s.role, s.sponsored_date,
	COALESCE(s.is_original, FALSE) AS is_original,
	ARRAY(SELECT AS STRUCT t.party, t.state, t.district FROM member_terms t
		WHERE t.member_id = s.member_id AND t.congress = b.congress
		ORDER BY t.start_date DESC, t.chamber LIMIT 1) AS terms
FROM bill_sponsorships s
JOIN bills b ON b.bill_id = s.bill_id
LEFT JOIN members m ON m.bioguide_id = s.member_id
WHERE s.bill_id = @bill
ORDER BY s.role = @cosponsor, s.sponsored_date IS NULL, s.sponsored_date, is_original DESC, last_name,
	s.member_id`

type sponsorTermRow struct {
	Party    string            `spanner:"party"`
	State    string            `spanner:"state"`
	District spanner.NullInt64 `spanner:"district"`
}

type sponsorshipRow struct {
	MemberID      string            `spanner:"member_id"`
	FirstName     string            `spanner:"first_name"`
	LastName      string            `spanner:"last_name"`
	Role          string            `spanner:"role"`
	SponsoredDate spanner.NullDate  `spanner:"sponsored_date"`
	IsOriginal    bool              `spanner:"is_original"`
	Terms         []*sponsorTermRow `spanner:"terms"`
}

// GetSponsorships returns the bill's sponsor first, then its cosponsors by sponsorship date
// (original cosponsors first on a tie, undated ones last).
func (r *BillRepository) GetSponsorships(ctx context.Context, billID string) ([]model.BillSponsorship, error) {
	stmt := spanner.Statement{SQL: sponsorshipsSQL, Params: map[string]any{
		paramBill:   billID,
		"cosponsor": repository.SponsorRoleCosponsor,
	}}
	return queryGraph(ctx, r.client, stmt, "sponsorships", func(row sponsorshipRow) model.BillSponsorship {
		s := model.BillSponsorship{
			BioguideID:    row.MemberID,
			FirstName:     row.FirstName,
			LastName:      row.LastName,
			Role:          row.Role,
			SponsoredDate: nullDatePtr(row.SponsoredDate),
			IsOriginal:    row.IsOriginal,
		}
		if len(row.Terms) > 0 {
			t := row.Terms[0]
			s.Party, s.State, s.District = &t.Party, &t.State, nullInt64Ptr(t.District)
		}
		return s
	})
}
