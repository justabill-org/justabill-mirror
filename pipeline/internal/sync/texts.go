package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/billtext"
	"github.com/justabill-org/justabill/pipeline/internal/diff"
)

const (
	formatXML  = "Formatted XML"
	formatText = "Formatted Text"
)

// textFormat represents a single format entry in the formats JSONB.
type textFormat struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// SyncBillTexts fetches bill text content for versions that haven't been fetched yet, and
// again for those whose corrected print a bill sync queued for a refetch (#302), parses
// sections, and computes diffs between consecutive versions. It then sweeps the
// stored diffs, so that each compares two consecutive fetched versions, oldest to newest.
// The limit is a batch size over the unfetched queue, not a cut of a listing, so a limited run
// still records success.
func (s *Service) SyncBillTexts(ctx context.Context, congressNum, limit int) error {
	return s.runStep(ctx, stepTexts, congressNum, false, func(ctx context.Context) (int, error) {
		return s.syncBillTexts(ctx, congressNum, limit)
	})
}

func (s *Service) syncBillTexts(ctx context.Context, congressNum, limit int) (int, error) {
	s.logger.InfoContext(ctx, "syncing bill texts", "congress", congressNum, "limit", limit)

	versions, err := s.store.QueryUnfetchedTextVersions(ctx, limit)
	if err != nil {
		return 0, fmt.Errorf("query unfetched text versions: %w", err)
	}

	s.logger.InfoContext(ctx, "found unfetched text versions", "count", len(versions))

	// Bills run defaultTextWorkers at a time; one bill's versions run in order, so a version's
	// diff reads the stored text of the one before it (#361).
	var stored syncCounter
	workerPool(ctx, groupByBill(versions), defaultTextWorkers,
		func(ctx context.Context, bill []repository.TextVersionRef) {
			for _, v := range bill {
				if ctx.Err() != nil {
					return
				}
				if s.syncTextVersion(ctx, v) {
					stored.inc()
				}
			}
		})
	total := stored.get()
	if ctx.Err() != nil {
		return total, ctx.Err() // runStep records it as a failure
	}

	s.logger.InfoContext(ctx, "bill texts synced", "congress", congressNum, "count", total)
	return total, s.sweepDiffs(ctx, limit)
}

// groupByBill splits versions, which QueryUnfetchedTextVersions orders by bill and then
// oldest first, into one run per bill, keeping that order.
func groupByBill(versions []repository.TextVersionRef) [][]repository.TextVersionRef {
	var bills [][]repository.TextVersionRef
	for _, v := range versions {
		if n := len(bills); n > 0 && bills[n-1][0].BillID == v.BillID {
			bills[n-1] = append(bills[n-1], v)
			continue
		}
		bills = append(bills, []repository.TextVersionRef{v})
	}
	return bills
}

// syncTextVersion fetches and stores one version's text, then diffs it against the bill's
// previous fetched version, and reports whether the text was stored. A failure is logged,
// and the version stays queued for the next run.
func (s *Service) syncTextVersion(ctx context.Context, v repository.TextVersionRef) bool {
	err := s.fetchAndStoreText(ctx, v)
	countItem(ctx, err)
	if err != nil {
		s.logger.WarnContext(ctx, "fetch text failed",
			"text_version_id", v.ID, "bill_id", v.BillID, "error", err)
		return false
	}
	if v.Refetch {
		return true // a changed text lost the diffs at both ends; the sweep recomputes them
	}
	if err = s.computeAndStoreDiff(ctx, v); err != nil {
		s.logger.WarnContext(ctx, "compute diff failed",
			"text_version_id", v.ID, "bill_id", v.BillID, "error", err)
	}
	return true
}

// selectTextURL picks the best URL from the formats list, preferring XML over plain text.
func selectTextURL(formatsJSON json.RawMessage) (string, string, error) {
	var formats []textFormat
	if unmarshalErr := json.Unmarshal(formatsJSON, &formats); unmarshalErr != nil {
		return "", "", fmt.Errorf("parsing formats JSONB: %w", unmarshalErr)
	}

	// Prefer XML, fall back to text
	for _, f := range formats {
		if f.Type == formatXML {
			return f.URL, formatXML, nil
		}
	}
	for _, f := range formats {
		if f.Type == formatText {
			return f.URL, formatText, nil
		}
	}

	return "", "", fmt.Errorf("no supported text format found in %d formats", len(formats))
}

// fetchAndStoreText downloads the text content, parses sections, and stores it. A refetched
// text that didn't change is only marked fetched.
func (s *Service) fetchAndStoreText(ctx context.Context, v repository.TextVersionRef) error {
	text, raw, err := s.downloadText(ctx, v)
	if err != nil {
		return err
	}
	if v.Refetch {
		changed, refetchErr := s.storeRefetchedText(ctx, v, text)
		if refetchErr != nil || !changed {
			return refetchErr
		}
	} else if err = s.store.InsertBillText(ctx, text); err != nil {
		return fmt.Errorf("insert bill text: %w", err)
	}
	s.storeTextLawRefs(ctx, v, billsPackageID(v.Formats), text, raw)
	return nil
}

// downloadText fetches a version's text in the best format and builds its row with textRow.
func (s *Service) downloadText(
	ctx context.Context, v repository.TextVersionRef,
) (repository.BillTextRow, string, error) {
	textURL, format, err := selectTextURL(v.Formats)
	if err != nil {
		return repository.BillTextRow{}, "", fmt.Errorf("select text URL: %w", err)
	}

	s.logger.InfoContext(ctx, "fetching bill text",
		"text_version_id", v.ID, "bill_id", v.BillID, "format", format, "refetch", v.Refetch)

	data, err := s.httpGet(ctx, textURL)
	if err != nil {
		return repository.BillTextRow{}, "", fmt.Errorf("fetch text content: %w", err)
	}
	return s.textRow(ctx, v, format, data)
}

// textRow parses a downloaded text's sections and builds its bill_texts row with newTextRow, for
// sync-texts and sync-govinfo alike. It returns the row and the text as downloaded. A text too
// large to store is logged and gets a row without ContentGz or content, which marks the version
// fetched, so it isn't downloaded again.
func (s *Service) textRow(
	ctx context.Context, v repository.TextVersionRef, format string, data []byte,
) (repository.BillTextRow, string, error) {
	sections, err := parseSections(data, format)
	if err != nil {
		s.logger.WarnContext(ctx, "parse sections failed, storing with empty sections",
			"text_version_id", v.ID, "error", err)
		sections = []billtext.Section{}
	}

	row, stored, err := newTextRow(v.ID, format, data, sections, time.Now())
	if err != nil {
		return row, "", err
	}
	if !stored {
		s.logger.WarnContext(ctx, "bill text too large to store, marked fetched without text",
			"text_version_id", v.ID, "bill_id", v.BillID, "version_code", v.VersionCode)
		return row, "", nil
	}
	return row, string(data), nil
}

// storeTextLawRefs writes the references to law of a text just stored, row as built by textRow
// and raw as downloaded, with the MODS of GovInfo package packageID ("" for none). A text too
// large to store has none. The text stays stored when they fail to store: the failure is
// logged, and backfill-law-refs writes them later.
func (s *Service) storeTextLawRefs(
	ctx context.Context, v repository.TextVersionRef, packageID string, row repository.BillTextRow, raw string,
) {
	if row.ContentGz == nil {
		return
	}
	src := repository.LawRefSource{
		BillID: v.BillID, VersionID: v.ID, VersionCode: v.VersionCode, Formats: v.Formats,
		Format: row.Format, Content: raw,
	}
	if err := s.storePackageLawRefs(ctx, src, packageID, &lawRefCounts{}); err != nil {
		s.logger.WarnContext(ctx, "store law references failed",
			"text_version_id", v.ID, "bill_id", v.BillID, "error", err)
	}
}

// storeRefetchedText stores a corrected print's text and reports whether its content changed.
// A changed text drops the diffs at either end, and their summaries, which the diff sweep and
// sync-summaries recompute; the bill's own summary follows the new content hash.
func (s *Service) storeRefetchedText(
	ctx context.Context, v repository.TextVersionRef, text repository.BillTextRow,
) (bool, error) {
	res, err := s.store.RefetchBillText(ctx, text)
	if err != nil {
		return false, fmt.Errorf("store refetched bill text: %w", err)
	}
	if !res.Changed {
		s.logger.InfoContext(ctx, "refetched text unchanged", "text_version_id", v.ID, "bill_id", v.BillID)
		return false, nil
	}
	markBill(ctx, v.BillID)
	level := slog.LevelInfo
	if res.Deleted.Summaries > 0 {
		level = slog.LevelWarn // paid-for summaries are regenerated
	}
	s.logger.Log(ctx, level, "refetched text changed", "text_version_id", v.ID, "bill_id", v.BillID,
		"version_code", v.VersionCode, "deleted_diffs", res.Deleted.Diffs,
		"deleted_summaries", res.Deleted.Summaries)
	return true, nil
}

// parseSections dispatches to the appropriate parser based on format.
func parseSections(data []byte, format string) ([]billtext.Section, error) {
	if strings.Contains(format, "XML") {
		return billtext.ParseXML(data)
	}
	return billtext.ParsePlainText(string(data))
}

// computeAndStoreDiff diffs a newly fetched version against the bill's previous fetched
// version, if there is one.
func (s *Service) computeAndStoreDiff(ctx context.Context, v repository.TextVersionRef) error {
	prev, err := s.store.FindPreviousVersion(ctx, v.BillID, v.ID)
	if err != nil {
		return fmt.Errorf("find previous version: %w", err)
	}
	if prev == nil {
		return nil // the oldest fetched version: nothing to diff
	}
	current, err := s.store.LoadSections(ctx, v.ID)
	if err != nil {
		return fmt.Errorf("load current sections: %w", err)
	}
	pair := repository.DiffPair{BillID: v.BillID, FromVersionID: prev.VersionID, ToVersionID: v.ID}
	_, err = s.storeDiff(ctx, pair, prev.Sections, current)
	return err
}

// sweepDiffs makes the stored diffs match the fetched versions (docs/design/79-text-version-upsert.md,
// item 3). It deletes diffs, and their summaries, that don't compare consecutive fetched versions
// oldest to newest, then computes the diffs missing between consecutive fetched versions, at most
// limit of them when limit is positive. The summaries of the new diffs come from sync-summaries.
// A version fetched out of order leaves both: a diff that skips it and a missing pair after it.
// Running it again changes nothing.
func (s *Service) sweepDiffs(ctx context.Context, limit int) error {
	deleted, err := s.store.DeleteNonConsecutiveDiffs(ctx)
	if err != nil {
		return fmt.Errorf("delete non-consecutive diffs: %w", err)
	}
	pairs, err := s.store.QueryMissingDiffPairs(ctx, limit)
	if err != nil {
		return fmt.Errorf("query missing diff pairs: %w", err)
	}
	computed, empty, failed := 0, 0, 0
	for _, p := range pairs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		stored, diffErr := s.diffPair(ctx, p)
		switch {
		case diffErr != nil:
			failed++
			s.logger.WarnContext(ctx, "compute missing diff failed", "bill_id", p.BillID,
				"from_version", p.FromVersionID, "to_version", p.ToVersionID, "error", diffErr)
		case stored:
			computed++
		default:
			empty++
		}
	}
	level := slog.LevelInfo
	if deleted.Summaries > 0 {
		level = slog.LevelWarn // paid-for summaries are regenerated
	}
	s.logger.Log(ctx, level, "diff sweep done", "deleted_diffs", deleted.Diffs,
		"deleted_summaries", deleted.Summaries, "missing_pairs", len(pairs), "computed", computed,
		"empty", empty, "failed", failed)
	return nil
}

// diffPair loads both versions' sections and stores their diff.
func (s *Service) diffPair(ctx context.Context, p repository.DiffPair) (bool, error) {
	from, err := s.store.LoadSections(ctx, p.FromVersionID)
	if err != nil {
		return false, fmt.Errorf("load from sections: %w", err)
	}
	to, err := s.store.LoadSections(ctx, p.ToVersionID)
	if err != nil {
		return false, fmt.Errorf("load to sections: %w", err)
	}
	return s.storeDiff(ctx, p, from, to)
}

// storeDiff computes the section diff between two versions and stores it. It reports false when
// no section was added, removed or modified: the diff is then stored flagged empty, so the sweep
// doesn't diff the pair again and no reader shows it (docs/design/401-remember-empty-diffs.md).
func (s *Service) storeDiff(
	ctx context.Context, p repository.DiffPair, fromJSON, toJSON json.RawMessage,
) (bool, error) {
	row, stats, err := computeDiffRow(p, fromJSON, toJSON)
	if err != nil {
		return false, err
	}
	if err = s.store.InsertBillTextDiff(ctx, row); err != nil {
		return false, fmt.Errorf("insert bill text diff: %w", err)
	}
	if row.IsEmpty {
		s.logger.DebugContext(ctx, "stored empty diff",
			"bill_id", p.BillID, "from_version", p.FromVersionID, "to_version", p.ToVersionID)
		return false, nil
	}

	s.logger.InfoContext(ctx, "computed diff",
		"bill_id", p.BillID,
		"from_version", p.FromVersionID,
		"to_version", p.ToVersionID,
		"sections_added", stats.SectionsAdded,
		"sections_removed", stats.SectionsRemoved,
		"sections_modified", stats.SectionsModified)
	return true, nil
}

// computeDiffRow computes the section diff between two versions' stored sections, as the row
// that stores it, flagged IsEmpty when no section changed.
func computeDiffRow(
	p repository.DiffPair, fromJSON, toJSON json.RawMessage,
) (repository.BillTextDiffRow, diff.Stats, error) {
	var oldSections, newSections []billtext.Section
	if err := json.Unmarshal(fromJSON, &oldSections); err != nil {
		return repository.BillTextDiffRow{}, diff.Stats{}, fmt.Errorf("unmarshal old sections: %w", err)
	}
	if err := json.Unmarshal(toJSON, &newSections); err != nil {
		return repository.BillTextDiffRow{}, diff.Stats{}, fmt.Errorf("unmarshal new sections: %w", err)
	}

	diffs, stats, err := diff.ComputeDiff(oldSections, newSections)
	if err != nil {
		return repository.BillTextDiffRow{}, diff.Stats{}, fmt.Errorf("compute diff: %w", err)
	}
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return repository.BillTextDiffRow{}, diff.Stats{}, fmt.Errorf("marshal diff stats: %w", err)
	}
	if diffs == nil {
		diffs = []diff.SectionDiff{} // diff_content is NOT NULL
	}
	diffContentJSON, err := json.Marshal(diffs)
	if err != nil {
		return repository.BillTextDiffRow{}, diff.Stats{}, fmt.Errorf("marshal diff content: %w", err)
	}
	return repository.BillTextDiffRow{
		BillID:        p.BillID,
		FromVersionID: p.FromVersionID,
		ToVersionID:   p.ToVersionID,
		DiffStats:     statsJSON,
		DiffContent:   diffContentJSON,
		GeneratedAt:   time.Now(),
		IsEmpty:       stats.Empty(),
	}, stats, nil
}
