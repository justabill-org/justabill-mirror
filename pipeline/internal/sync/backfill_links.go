package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// Link-table backfill (docs/design/31-bill-ontology.md, item 4): rebuild the
// link tables from the relationship JSON already stored on each bill, with
// the same converters the dual-write uses. It makes no Congress.gov calls.

const (
	stepLinks = "links"

	// DefaultLinksPageSize is how many bills BackfillLinks reads per page. The
	// sync_state checkpoint is written after every page.
	DefaultLinksPageSize = 200
)

var errBadLinkJSON = errors.New("unreadable relationship json")

// linkCounts totals one backfill run.
type linkCounts struct {
	bills   int
	rows    int
	skipped int
	badJSON int
}

// linkBackfiller rebuilds one link table for a bill from its JSON column.
type linkBackfiller func(ctx context.Context, src repository.BillLinkSource) (rows, skipped int, err error)

// BackfillLinks rebuilds a congress's link tables from the stored JSON. It is
// idempotent: every write replaces the bill's links. It pages by bill_id and
// checkpoints in sync_state (step "links"), so an interrupted run resumes
// after the last finished page. A finished run records success, which clears the
// checkpoint, so the next run starts over.
func (s *Service) BackfillLinks(ctx context.Context, congressNum, pageSize int) error {
	if pageSize <= 0 {
		pageSize = DefaultLinksPageSize
	}
	return s.runStep(ctx, stepLinks, congressNum, false, func(ctx context.Context) (int, error) {
		return s.backfillLinks(ctx, congressNum, pageSize)
	})
}

func (s *Service) backfillLinks(ctx context.Context, congressNum, pageSize int) (int, error) {
	state, err := s.store.GetSyncState(ctx, stepLinks, congressNum)
	if err != nil {
		return 0, fmt.Errorf("read links checkpoint: %w", err)
	}
	var c linkCounts
	after := ""
	if state != nil && state.LastOffset != nil {
		after = *state.LastOffset
		c.bills = state.ItemsSynced
	}
	s.logger.InfoContext(ctx, "backfilling link tables", "congress", congressNum, "resume_after", after)

	for {
		page, listErr := s.store.ListBillLinkSources(ctx, congressNum, after, pageSize)
		if listErr != nil {
			return c.bills, fmt.Errorf("list bills after %q: %w", after, listErr)
		}
		for _, src := range page {
			if err = s.backfillBillLinks(ctx, src, &c); err != nil {
				return c.bills, err
			}
		}
		if len(page) < pageSize {
			break
		}
		after = page[len(page)-1].BillID
		if err = s.store.SaveSyncCheckpoint(ctx, stepLinks, congressNum, &after, c.bills); err != nil {
			return c.bills, fmt.Errorf("save links checkpoint: %w", err)
		}
		s.logger.InfoContext(ctx, "link backfill progress", "congress", congressNum, "bills", c.bills, "after", after)
	}

	s.logger.InfoContext(ctx, "link backfill complete", "congress", congressNum,
		"bills", c.bills, "rows", c.rows, "skipped_without_id", c.skipped, "unreadable_json", c.badJSON)
	return c.bills, nil
}

// backfillBillLinks rebuilds every link table of one bill. A NULL column was
// never synced and is left alone. Unreadable JSON is logged and skipped; a
// failed write stops the run so it can resume from the last checkpoint.
func (s *Service) backfillBillLinks(ctx context.Context, src repository.BillLinkSource, c *linkCounts) error {
	for _, col := range []struct {
		name  string
		raw   json.RawMessage
		write linkBackfiller
	}{
		{"sponsors", src.Sponsors, s.backfillSponsors},
		{"cosponsors", src.Cosponsors, s.backfillCosponsors},
		{"committees", src.Committees, s.backfillCommittees},
		{"subjects", src.Subjects, s.backfillSubjects},
		{"related_bills", src.RelatedBills, s.backfillRelations},
	} {
		if len(col.raw) == 0 || bytes.Equal(col.raw, []byte("null")) {
			continue
		}
		rows, skipped, err := col.write(ctx, src)
		if errors.Is(err, errBadLinkJSON) {
			c.badJSON++
			s.logger.WarnContext(ctx, "skipped unreadable link json", "bill_id", src.BillID, "column", col.name,
				"error", err)
			continue
		}
		if err != nil {
			return fmt.Errorf("backfill %s for %s: %w", col.name, src.BillID, err)
		}
		c.rows += rows
		c.skipped += skipped
		if skipped > 0 {
			s.logger.WarnContext(ctx, "skipped link rows without an id",
				"bill_id", src.BillID, "column", col.name, "skipped", skipped)
		}
	}
	c.bills++
	return nil
}

func decodeLinkJSON[T any](raw json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("%w: %w", errBadLinkJSON, err)
	}
	return v, nil
}

func (s *Service) backfillSponsors(ctx context.Context, src repository.BillLinkSource) (int, int, error) {
	sponsors, err := decodeLinkJSON[[]congress.Sponsor](src.Sponsors)
	if err != nil {
		return 0, 0, err
	}
	rows, skipped := sponsorshipRows(sponsors, src.IntroducedDate)
	return len(rows), skipped, s.store.ReplaceBillSponsorships(ctx, src.BillID, repository.SponsorRoleSponsor, rows)
}

func (s *Service) backfillCosponsors(ctx context.Context, src repository.BillLinkSource) (int, int, error) {
	cosponsors, err := decodeLinkJSON[[]congress.Cosponsor](src.Cosponsors)
	if err != nil {
		return 0, 0, err
	}
	rows, skipped := cosponsorshipRows(cosponsors)
	return len(rows), skipped, s.store.ReplaceBillSponsorships(ctx, src.BillID, repository.SponsorRoleCosponsor, rows)
}

func (s *Service) backfillCommittees(ctx context.Context, src repository.BillLinkSource) (int, int, error) {
	committees, err := decodeLinkJSON[[]congress.Committee](src.Committees)
	if err != nil {
		return 0, 0, err
	}
	rows, skipped := committeeRows(committees)
	return len(rows), skipped, s.store.ReplaceBillCommittees(ctx, src.BillID, rows)
}

func (s *Service) backfillSubjects(ctx context.Context, src repository.BillLinkSource) (int, int, error) {
	names, err := decodeLinkJSON[[]string](src.Subjects)
	if err != nil {
		return 0, 0, err
	}
	return len(names), 0, s.store.ReplaceBillSubjects(ctx, src.BillID, names)
}

func (s *Service) backfillRelations(ctx context.Context, src repository.BillLinkSource) (int, int, error) {
	related, err := decodeLinkJSON[[]congress.RelatedBillEntry](src.RelatedBills)
	if err != nil {
		return 0, 0, err
	}
	rows, skipped := relationRows(related)
	return len(rows), skipped, s.store.ReplaceBillRelations(ctx, src.BillID, rows)
}
