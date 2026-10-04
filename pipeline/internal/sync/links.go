package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// Ontology dual-write (docs/design/31-bill-ontology.md, item 3). Each bill
// relationship is stored twice: as the JSON column the web app reads today,
// and as rows in its link table. The link rows are written only after the
// JSON column, so they never get ahead of it, and backfill-links can rebuild
// them from the JSON if a link write fails.
//
// The converters below take the Congress.gov types, which are also what the
// JSON columns hold, so the backfill can reuse them on the stored JSON.

// sponsorshipRows converts a bill's primary sponsors. They take the bill's
// introduced date as their sponsorship date.
func sponsorshipRows(
	sponsors []congress.Sponsor, introduced *time.Time,
) ([]repository.BillSponsorshipRow, int) {
	rows := make([]repository.BillSponsorshipRow, 0, len(sponsors))
	skipped := 0
	for _, sp := range sponsors {
		if sp.BioguideID == "" {
			skipped++
			continue
		}
		rows = append(rows, repository.BillSponsorshipRow{MemberID: sp.BioguideID, SponsoredDate: introduced})
	}
	return rows, skipped
}

// cosponsorshipRows converts a bill's cosponsors. Members who withdrew their
// cosponsorship stay in the JSON column but get no link row.
func cosponsorshipRows(cosponsors []congress.Cosponsor) ([]repository.BillSponsorshipRow, int) {
	rows := make([]repository.BillSponsorshipRow, 0, len(cosponsors))
	skipped := 0
	for _, c := range cosponsors {
		if c.WithdrawnAt != "" {
			continue
		}
		if c.BioguideID == "" {
			skipped++
			continue
		}
		rows = append(rows, repository.BillSponsorshipRow{
			MemberID:      c.BioguideID,
			SponsoredDate: parseAPITime(c.SponsoredAt),
			IsOriginal:    c.IsOriginal,
		})
	}
	return rows, skipped
}

// committeeRows returns one row per committee activity. The link table is
// keyed on the activity, so a committee without one has no row, and an
// activity listed more than once (Congress.gov repeats "Unknown") keeps its
// earliest date.
func committeeRows(committees []congress.Committee) ([]repository.BillCommitteeRow, int) {
	var rows []repository.BillCommitteeRow
	seen := map[[2]string]int{}
	skipped := 0
	for _, c := range committees {
		if strings.TrimSpace(c.SystemCode) == "" || len(c.Activities) == 0 {
			skipped++
			continue
		}
		for _, a := range c.Activities {
			if a.Name == "" {
				skipped++
				continue
			}
			date := parseAPITime(a.Date)
			key := [2]string{c.SystemCode, a.Name}
			if i, dup := seen[key]; dup {
				if prev := rows[i].ActivityDate; prev == nil || (date != nil && date.Before(*prev)) {
					rows[i].ActivityDate = date
				}
				continue
			}
			seen[key] = len(rows)
			rows = append(rows, repository.BillCommitteeRow{
				CommitteeID:   c.SystemCode,
				CommitteeName: c.Name,
				Chamber:       nilIfEmpty(c.Chamber),
				CommitteeType: nilIfEmpty(c.Type),
				Activity:      a.Name,
				ActivityDate:  date,
			})
		}
	}
	return rows, skipped
}

// relationRows returns one row per relationship detail of each related bill.
func relationRows(related []congress.RelatedBillEntry) ([]repository.BillRelationRow, int) {
	var rows []repository.BillRelationRow
	skipped := 0
	for _, rb := range related {
		id := relatedBillID(rb)
		if id == "" || len(rb.RelationshipDetails) == 0 {
			skipped++
			continue
		}
		for _, d := range rb.RelationshipDetails {
			if d.Type == "" {
				skipped++
				continue
			}
			rows = append(rows, repository.BillRelationRow{
				RelatedBillID: id,
				RelationType:  d.Type,
				IdentifiedBy:  nilIfEmpty(d.IdentifiedBy),
			})
		}
	}
	return rows, skipped
}

// relatedBillID builds the bill ID (<type>-<congress>-<number>) that syncOneBill
// would give the related bill.
func relatedBillID(rb congress.RelatedBillEntry) string {
	if rb.Type == "" || rb.Congress <= 0 || rb.Number <= 0 {
		return ""
	}
	return fmt.Sprintf("%s-%d-%d", strings.ToLower(rb.Type), rb.Congress, rb.Number)
}

// parseAPITime reads the Congress.gov date formats: RFC 3339 timestamps for
// committee activities and plain dates for sponsorships.
func parseAPITime(s string) *time.Time {
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// writeBillJSONAndLinks stores one relationship JSON column, then its link
// rows. A failed JSON write skips the link write.
func (s *Service) writeBillJSONAndLinks(
	ctx context.Context, billID, column string, value any, skipped int,
	replaceLinks func(context.Context) error,
) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal %s json: %w", column, err)
	}
	if err = s.store.UpdateBillJSON(ctx, billID, column, raw); err != nil {
		return fmt.Errorf("update %s json: %w", column, err)
	}
	return s.replaceLinks(ctx, billID, column, skipped, replaceLinks)
}

func (s *Service) replaceLinks(
	ctx context.Context, billID, column string, skipped int, replace func(context.Context) error,
) error {
	if skipped > 0 {
		s.logger.WarnContext(ctx, "skipped link rows without an id",
			"bill_id", billID, "column", column, "skipped", skipped)
	}
	if err := replace(ctx); err != nil {
		return fmt.Errorf("replace %s links: %w", column, err)
	}
	return nil
}

// syncBillSponsors writes the primary sponsors' link rows. Their JSON column
// is written by upsertBill.
func (s *Service) syncBillSponsors(ctx context.Context, billID string, detail *congress.BillDetail) error {
	rows, skipped := sponsorshipRows(detail.Sponsors, parseAPITime(detail.IntroducedDate))
	return s.replaceLinks(ctx, billID, "sponsors", skipped, func(ctx context.Context) error {
		return s.store.ReplaceBillSponsorships(ctx, billID, repository.SponsorRoleSponsor, rows)
	})
}
