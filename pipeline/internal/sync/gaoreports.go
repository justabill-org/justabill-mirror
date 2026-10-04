package sync

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

// SyncGAOReports searches GovInfo for GAO reports that cite up to limit bills of a congress and
// links them. It picks bills with [repository.PipelineStore.ListBillsForGAOCheck] (never checked
// first, then those not checked for [repository.GAORecheckAfter]) and marks each one checked
// once its search succeeds and every hit is linked, so each run makes progress.
func (s *Service) SyncGAOReports(ctx context.Context, congressNum, limit int) error {
	if s.govinfo == nil {
		s.logger.InfoContext(ctx, "govinfo client not configured, skipping GAO report sync")
		return nil
	}

	return s.runStep(ctx, stepGAOReports, congressNum, false, func(ctx context.Context) (int, error) {
		return s.syncGAOReports(ctx, congressNum, limit)
	})
}

func (s *Service) syncGAOReports(ctx context.Context, congressNum, limit int) (int, error) {
	s.logger.InfoContext(ctx, "syncing GAO reports", "congress", congressNum, "limit", limit)

	billIDs, err := s.store.ListBillsForGAOCheck(ctx, congressNum, limit)
	if err != nil {
		return 0, fmt.Errorf("list bills for GAO sync: %w", err)
	}

	counter := &syncCounter{}
	checked := &syncCounter{}

	workerPool(ctx, billIDs, defaultGovInfoWorkers, func(ctx context.Context, billID string) {
		count, ok := s.syncGAOForBill(ctx, billID)
		for range count {
			counter.inc()
		}
		if ok {
			checked.inc()
		}
	})

	total := counter.get()

	s.logger.InfoContext(ctx, "GAO reports synced", "congress", congressNum, "count", total,
		"bills", len(billIDs), "bills_checked", checked.get())
	return total, nil
}

// syncGAOForBill searches for the bill's GAO reports and links each hit. It returns how many it
// linked, and whether it marked the bill checked: only when the search and every link succeeded,
// so a failure is retried on the next run rather than in 30 days.
func (s *Service) syncGAOForBill(ctx context.Context, billID string) (int, bool) {
	_, bt, n, err := parseBillID(billID)
	if err != nil {
		s.logger.WarnContext(ctx, "invalid bill ID for GAO sync", "bill_id", billID, "error", err)
		return 0, false
	}

	citation := billTypeToCitation(bt, n)
	resp, err := s.govinfo.SearchGAOReports(ctx, citation)
	if err != nil {
		s.logger.WarnContext(ctx, "GAO search failed", "bill_id", billID, "error", err)
		return 0, false
	}

	count := 0
	for _, hit := range resp.Results {
		if s.linkGAOReport(ctx, billID, hit.PackageID, hit.Title) {
			count++
		}
	}
	if count < len(resp.Results) {
		return count, false
	}

	if err = s.store.MarkGAOChecked(ctx, billID); err != nil {
		s.logger.WarnContext(ctx, "mark GAO checked failed", "bill_id", billID, "error", err)
		return count, false
	}
	return count, true
}

func (s *Service) linkGAOReport(ctx context.Context, billID, packageID, title string) bool {
	summary, err := s.govinfo.FetchPackageSummary(ctx, packageID)
	if err != nil {
		s.logger.WarnContext(ctx, "fetch GAO summary failed",
			"package_id", packageID, "error", err)
		return false
	}

	var pdfURL, htmlURL *string
	if summary.Download != nil {
		pdfURL = nilIfEmpty(summary.Download.PDFLink)
		if summary.Download.TxtLink != "" {
			htmlURL = nilIfEmpty(summary.Download.TxtLink)
		}
	}

	var pubDate *time.Time
	if summary.DateIssued != "" {
		if t, parseErr := time.Parse("2006-01-02", summary.DateIssued); parseErr == nil {
			pubDate = &t
		}
	}

	if err = s.store.UpsertGAOReport(ctx, repository.GAOReportRow{
		ReportID:      packageID,
		Title:         title,
		ReportNumber:  nilIfEmpty(summary.DocClass),
		PublishedDate: pubDate,
		PDFURL:        pdfURL,
		HTMLURL:       htmlURL,
	}); err != nil {
		s.logger.WarnContext(ctx, "upsert GAO report failed",
			"report_id", packageID, "error", err)
		return false
	}

	if err = s.store.LinkBillGAOReport(ctx, billID, packageID); err != nil {
		s.logger.WarnContext(ctx, "link bill GAO report failed",
			"bill_id", billID, "report_id", packageID, "error", err)
		return false
	}

	s.logger.InfoContext(ctx, "linked GAO report", "bill_id", billID, "report_id", packageID)
	return true
}

// parseBillID splits a bill ID ("hr-119-1") into congress, type and number.
func parseBillID(billID string) (int, string, int, error) {
	parts := strings.Split(billID, "-")
	const expectedParts = 3
	if len(parts) != expectedParts {
		return 0, "", 0, fmt.Errorf("invalid bill ID format: %s", billID)
	}

	congressNum, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, "", 0, fmt.Errorf("invalid congress number in bill ID: %s", parts[1])
	}

	number, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, "", 0, fmt.Errorf("invalid bill number in bill ID: %s", parts[2])
	}

	return congressNum, parts[0], number, nil
}

var billTypeCitations = map[string]string{ //nolint:gochecknoglobals // lookup table
	"hr":      "h.r.",
	"s":       "s.",
	"hjres":   "h.j.res.",
	"sjres":   "s.j.res.",
	"hconres": "h.con.res.",
	"sconres": "s.con.res.",
	"hres":    "h.res.",
	"sres":    "s.res.",
}

func billTypeToCitation(billType string, number int) string {
	prefix := billTypeCitations[billType]
	if prefix == "" {
		prefix = billType
	}
	return fmt.Sprintf("%s %d", prefix, number)
}
