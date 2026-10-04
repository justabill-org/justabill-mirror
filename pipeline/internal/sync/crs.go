package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/htmltext"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
)

// CRS summaries (docs/design/197-crs-summaries.md, "Sync"). One list, /summaries/{congress},
// carries every summary with its text, so a congress's full load is about 24 requests and a run
// after that usually one.
const (
	// crsRereadWindow is how far before the last successful run's start the next run begins, so a
	// summary Congress.gov timestamped late is still listed. Re-reading is harmless: unchanged
	// summaries aren't rewritten.
	crsRereadWindow = time.Hour
	// crsMaxPages stops a runaway loop: 100,000 summaries, where the 119th has about 6,000.
	crsMaxPages = 400
	// maxCRSVersionCode is bill_crs_summaries.version_code's length.
	maxCRSVersionCode = 8
)

var errMalformedCRSSummary = errors.New("malformed CRS summary")

// crsRun counts what one run did, for its log line.
type crsRun struct {
	pages, fetched, changed, orphans, skipped int
}

// SyncCRSSummaries stores the CRS summaries of a congress's bills that changed since the step's
// last successful run (from the congress's start on the first run, or with SetForceSync). A
// summary is rewritten only when its HTML or its lastSummaryUpdateDate changed, and one that
// can't be read is logged and skipped. The bills whose summaries changed are revalidated once,
// when the run ends.
func (s *Service) SyncCRSSummaries(ctx context.Context, congressNum int) error {
	return s.runStep(ctx, stepCRSSummaries, congressNum, false, func(ctx context.Context) (int, error) {
		return s.syncCRSSummaries(ctx, congressNum)
	})
}

func (s *Service) syncCRSSummaries(ctx context.Context, congressNum int) (int, error) {
	to := s.now()
	from, err := s.crsWindowStart(ctx, congressNum)
	if err != nil {
		return 0, err
	}
	s.logger.InfoContext(ctx, "syncing crs summaries", "congress", congressNum, "from", from, "to", to)

	var run crsRun
	for {
		if run.pages == crsMaxPages {
			return run.changed, fmt.Errorf("crs summaries: more than %d pages", crsMaxPages)
		}
		if err = ctx.Err(); err != nil {
			return run.changed, err
		}
		page, listErr := s.api.ListSummaries(ctx, congressNum, from, to, run.pages*pageSize)
		run.pages++
		if listErr != nil {
			return run.changed, fmt.Errorf("list crs summaries: %w", listErr)
		}
		if err = s.storeCRSPage(ctx, congressNum, page, &run); err != nil {
			return run.changed, err
		}
		// pagination.count isn't reliable, and a short page can still have a next one.
		if !page.HasNext || page.Items() == 0 {
			break
		}
	}

	s.logger.InfoContext(ctx, "crs summaries synced", "congress", congressNum, "fetched", run.fetched,
		"changed", run.changed, "orphans", run.orphans, "skipped", run.skipped, "requests", run.pages)
	return run.changed, nil
}

// crsWindowStart is where a run's window begins: an hour before the last successful run started,
// or the congress's first day on the first run and with SetForceSync.
func (s *Service) crsWindowStart(ctx context.Context, congressNum int) (time.Time, error) {
	start := rollcall.Start(congressNum)
	if s.forceSync {
		return start, nil
	}
	st, err := s.store.GetSyncState(ctx, stepCRSSummaries, congressNum)
	if err != nil {
		return time.Time{}, fmt.Errorf("read crs summaries sync state: %w", err)
	}
	if st == nil || st.LastSyncedAt.IsZero() {
		return start, nil
	}
	if from := st.LastSyncedAt.Add(-crsRereadWindow); from.After(start) {
		return from, nil
	}
	return start, nil
}

// storeCRSPage writes the page's summaries that are new or changed. A malformed summary is
// logged and skipped; a store error fails the run, so the watermark stays put and the next run
// lists the page again.
func (s *Service) storeCRSPage(
	ctx context.Context, congressNum int, page *congress.SummariesPage, run *crsRun,
) error {
	for _, err := range page.Malformed {
		s.logger.WarnContext(ctx, "skipping malformed crs summary", "congress", congressNum, "error", err)
		run.skipped++
	}
	rows := make([]repository.CRSSummaryRow, 0, len(page.Summaries))
	billIDs := make([]string, 0, len(page.Summaries))
	for _, cs := range page.Summaries {
		row, err := crsSummaryRow(cs, congressNum)
		if err != nil {
			s.logger.WarnContext(ctx, "skipping malformed crs summary", "congress", congressNum,
				"bill_type", cs.Bill.Type, "bill_number", cs.Bill.Number, "version_code", cs.VersionCode, "error", err)
			run.skipped++
			continue
		}
		rows = append(rows, row)
		billIDs = append(billIDs, row.BillID)
	}
	if len(rows) == 0 {
		return nil
	}

	stored, err := s.store.StoredCRSSummaries(ctx, billIDs)
	if err != nil {
		return fmt.Errorf("read stored crs summaries: %w", err)
	}
	var writes []repository.CRSSummaryRow
	for _, row := range rows {
		run.fetched++
		if !stored.Bills[row.BillID] {
			run.orphans++
		}
		prev, ok := stored.Versions[repository.CRSSummaryKey{BillID: row.BillID, VersionCode: row.VersionCode}]
		if ok && prev.ContentHash == row.ContentHash && prev.CRSUpdatedAt.Equal(row.CRSUpdatedAt) {
			continue
		}
		writes = append(writes, row)
	}
	if len(writes) == 0 {
		return nil
	}
	if err = s.store.UpsertCRSSummaries(ctx, writes); err != nil {
		return fmt.Errorf("store crs summaries: %w", err)
	}
	run.changed += len(writes)
	for _, row := range writes {
		if stored.Bills[row.BillID] {
			markBill(ctx, row.BillID)
		}
	}
	return nil
}

// crsSummaryRow checks a listed summary and builds its row: the bill ID the way billID builds
// it, the text rendered to plain text, and the sha256 of the HTML.
func crsSummaryRow(cs congress.CRSSummary, congressNum int) (repository.CRSSummaryRow, error) {
	billType := strings.ToLower(cs.Bill.Type)
	num, err := strconv.Atoi(cs.Bill.Number)
	switch {
	case !knownBillType(billType):
		return repository.CRSSummaryRow{}, fmt.Errorf("%w: bill type %q", errMalformedCRSSummary, cs.Bill.Type)
	case err != nil || num <= 0:
		return repository.CRSSummaryRow{}, fmt.Errorf("%w: bill number %q", errMalformedCRSSummary, cs.Bill.Number)
	case cs.Bill.Congress != congressNum:
		return repository.CRSSummaryRow{}, fmt.Errorf("%w: congress %d", errMalformedCRSSummary, cs.Bill.Congress)
	case cs.VersionCode == "" || len(cs.VersionCode) > maxCRSVersionCode:
		return repository.CRSSummaryRow{}, fmt.Errorf("%w: version code %q", errMalformedCRSSummary, cs.VersionCode)
	}

	actionDate, err := time.Parse(time.DateOnly, cs.ActionDate)
	if err != nil {
		return repository.CRSSummaryRow{}, fmt.Errorf("%w: action date: %w", errMalformedCRSSummary, err)
	}
	crsUpdated, err := time.Parse(time.RFC3339, cs.LastSummaryUpdateDate)
	if err != nil {
		return repository.CRSSummaryRow{}, fmt.Errorf("%w: lastSummaryUpdateDate: %w", errMalformedCRSSummary, err)
	}
	sourceUpdated, err := time.Parse(time.RFC3339, cs.UpdateDate)
	if err != nil {
		return repository.CRSSummaryRow{}, fmt.Errorf("%w: updateDate: %w", errMalformedCRSSummary, err)
	}
	text := htmltext.Render(cs.Text)
	if text == "" {
		return repository.CRSSummaryRow{}, fmt.Errorf("%w: no text", errMalformedCRSSummary)
	}

	var chamber *string
	if cs.CurrentChamber == chamberHouse || cs.CurrentChamber == chamberSenate {
		chamber = &cs.CurrentChamber
	}
	hash := sha256.Sum256([]byte(cs.Text))
	return repository.CRSSummaryRow{
		BillID:          fmt.Sprintf("%s-%d-%d", billType, congressNum, num),
		VersionCode:     cs.VersionCode,
		ActionDate:      actionDate,
		ActionDesc:      strings.TrimSpace(cs.ActionDesc),
		Chamber:         chamber,
		TextHTML:        cs.Text,
		Text:            text,
		ContentHash:     hex.EncodeToString(hash[:]),
		CRSUpdatedAt:    crsUpdated.UTC(),
		SourceUpdatedAt: sourceUpdated.UTC(),
	}, nil
}

// knownBillType reports whether t is one of the eight bill types (lowercase).
func knownBillType(t string) bool {
	switch t {
	case "hr", "s", "hjres", "sjres", "hconres", "sconres", "hres", "sres":
		return true
	}
	return false
}
