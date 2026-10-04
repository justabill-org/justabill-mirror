package sync

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/justabill-org/justabill/db/repository"
)

// Recomputing stored diffs (#449): when the diff algorithm or the section parser changes,
// SweepDiffs with recompute computes every stored diff again from the stored sections.

// recomputeCounts totals what recomputing stored diffs did.
type recomputeCounts struct {
	unchanged, replaced, emptied, failed, summaries int
}

// SweepDiffs runs sync-texts' diff sweep on its own: it deletes the diffs that don't compare
// consecutive fetched versions and computes the missing ones, with no downloads. With recompute
// it first computes every stored diff again and replaces those that changed, deleting their
// summaries for sync-summaries to write again; a diff that's now empty is replaced by an empty
// diff, which no reader shows. Running it again changes nothing.
func (s *Service) SweepDiffs(ctx context.Context, recompute bool) error {
	if recompute {
		if err := s.recomputeDiffs(ctx); err != nil {
			return err
		}
	}
	return s.sweepDiffs(ctx, 0)
}

func (s *Service) recomputeDiffs(ctx context.Context) error {
	pairs, err := s.store.QueryStoredDiffPairs(ctx)
	if err != nil {
		return fmt.Errorf("query stored diff pairs: %w", err)
	}
	var c recomputeCounts
	for _, p := range pairs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = s.recomputeDiff(ctx, p, &c); err != nil {
			c.failed++
			s.logger.WarnContext(ctx, "recompute diff failed", "bill_id", p.BillID,
				"from_version", p.FromVersionID, "to_version", p.ToVersionID, "error", err)
		}
	}
	level := slog.LevelInfo
	if c.summaries > 0 {
		level = slog.LevelWarn // paid-for summaries are regenerated
	}
	s.logger.Log(ctx, level, "diffs recomputed", "stored_diffs", len(pairs), "unchanged", c.unchanged,
		"replaced", c.replaced, "emptied", c.emptied, "failed", c.failed, "deleted_summaries", c.summaries)
	return nil
}

// recomputeDiff computes one stored diff again and replaces it, flagged empty when no section
// changed (#503), so the sweep doesn't diff the pair again.
func (s *Service) recomputeDiff(ctx context.Context, p repository.DiffPair, c *recomputeCounts) error {
	from, err := s.store.LoadSections(ctx, p.FromVersionID)
	if err != nil {
		return fmt.Errorf("load from sections: %w", err)
	}
	to, err := s.store.LoadSections(ctx, p.ToVersionID)
	if err != nil {
		return fmt.Errorf("load to sections: %w", err)
	}
	row, _, err := computeDiffRow(p, from, to)
	if err != nil {
		return err
	}
	replaced, err := s.store.ReplaceBillTextDiff(ctx, row)
	if err != nil {
		return fmt.Errorf("replace diff: %w", err)
	}
	switch {
	case replaced.Changed && row.IsEmpty:
		c.emptied++
	case replaced.Changed:
		c.replaced++
	case replaced.Found:
		c.unchanged++
	}
	if replaced.SummaryDeleted {
		c.summaries++
	}
	return nil
}
