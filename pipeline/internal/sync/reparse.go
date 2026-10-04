package sync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

// Reparsing stored texts (#448): when the section parser changes, ReparseTexts rebuilds
// bill_texts.sections from the text already stored with each version, without downloading it
// again. The stored diffs still compare the old sections until they're recomputed.

const (
	stepReparseTexts = "reparse_texts"

	// DefaultReparsePageSize is how many stored texts ReparseTexts reads per page, and how often
	// it writes its checkpoint. Omnibus texts run to megabytes, so pages stay small.
	DefaultReparsePageSize = 25
)

// reparseCounts totals what reparsing stored texts did.
type reparseCounts struct {
	sections int
	failed   int
}

// ReparseTexts rebuilds the parsed sections of every bill text stored for one congress. It
// reads pageSize texts at a time (DefaultReparsePageSize when zero or less) and checkpoints
// after each page in sync_state (step "reparse_texts"), so an interrupted run resumes where it
// stopped. A text the parser can't read gets empty sections, as sync-texts would store. It
// makes no upstream calls and is safe to rerun.
func (s *Service) ReparseTexts(ctx context.Context, congressNum, pageSize int) error {
	if pageSize <= 0 {
		pageSize = DefaultReparsePageSize
	}
	return s.runStep(ctx, stepReparseTexts, congressNum, false, func(ctx context.Context) (int, error) {
		return s.reparseTexts(ctx, congressNum, pageSize)
	})
}

func (s *Service) reparseTexts(ctx context.Context, congressNum, pageSize int) (int, error) {
	var c reparseCounts
	n, err := s.eachStoredText(ctx, stepReparseTexts, congressNum, pageSize,
		func(ctx context.Context, src repository.LawRefSource) error {
			return s.reparseText(ctx, src, &c)
		})
	if err != nil {
		return n, err
	}
	s.logger.InfoContext(ctx, "reparse complete", "congress", congressNum,
		"versions", n, "top_level_sections", c.sections, "unparsed", c.failed)
	return n, nil
}

// reparseText parses one stored text and replaces its sections.
func (s *Service) reparseText(ctx context.Context, src repository.LawRefSource, c *reparseCounts) error {
	sections, err := parseSections([]byte(src.Content), src.Format)
	if err != nil {
		c.failed++
		s.logger.WarnContext(ctx, "parse sections failed, storing empty sections",
			"bill_id", src.BillID, "text_version_id", src.VersionID, "format", src.Format, "error", err)
		sections = []billtext.Section{}
	}
	sectionsJSON, err := json.Marshal(sections)
	if err != nil {
		return fmt.Errorf("marshal sections of %s: %w", src.VersionID, err)
	}
	if err = s.store.UpdateBillTextSections(ctx, src.VersionID, sectionsJSON); err != nil {
		return fmt.Errorf("store sections of %s: %w", src.VersionID, err)
	}
	c.sections += len(sections)
	return nil
}
