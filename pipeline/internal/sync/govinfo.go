package sync

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

const (
	defaultLookbackHours   = 24
	govinfoBillsCollection = "BILLS"
)

var govinfoBillIDRe = regexp.MustCompile(`BILLS-(\d+)([a-z]+)(\d+)([a-z]+)$`)

// parseGovInfoBillID extracts bill info from a GovInfo package ID.
// Format: "BILLS-{congress}{type}{number}{version}" e.g. "BILLS-119hr144ih".
func parseGovInfoBillID(packageID string) (int, string, int, string, bool) {
	matches := govinfoBillIDRe.FindStringSubmatch(packageID)
	if matches == nil {
		return 0, "", 0, "", false
	}

	congress, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, "", 0, "", false
	}

	billType := matches[2]

	number, numErr := strconv.Atoi(matches[3])
	if numErr != nil {
		return 0, "", 0, "", false
	}

	versionCode := matches[4]

	return congress, billType, number, versionCode, true
}

// SyncGovInfoChanges polls the GovInfo Collections Service for recently modified
// bill text packages and fetches/stores any new or updated text content. Packages due again
// in sync_retry go first; failures are recorded there and successes cleared, as in SyncBills.
func (s *Service) SyncGovInfoChanges(ctx context.Context, congressNum int) error {
	if s.govinfo == nil {
		s.logger.InfoContext(ctx, "govinfo client not configured, skipping govinfo sync")
		return nil
	}
	return s.runStep(ctx, stepGovInfo, congressNum, false, func(ctx context.Context) (int, error) {
		return s.syncGovInfoChanges(ctx, congressNum)
	})
}

func (s *Service) syncGovInfoChanges(ctx context.Context, congressNum int) (int, error) {
	s.logger.InfoContext(ctx, "syncing govinfo changes", "congress", congressNum)

	lastSyncTime, err := s.getGovInfoLastSync(ctx, congressNum)
	if err != nil {
		return 0, fmt.Errorf("get govinfo last sync: %w", err)
	}

	retries := s.newRetryList(repository.RetryStepGovInfo, congressNum)
	packageIDs, err := retries.due(ctx)
	if err != nil {
		return 0, err
	}

	resp, err := s.govinfo.PollChanges(ctx, govinfoBillsCollection, lastSyncTime)
	if err != nil {
		return 0, fmt.Errorf("poll govinfo changes: %w", err)
	}

	s.logger.InfoContext(ctx, "govinfo packages found", "count", len(resp.Packages))

	queued := make(map[string]bool, len(packageIDs)+len(resp.Packages))
	for _, id := range packageIDs {
		queued[id] = true
	}
	for _, pkg := range resp.Packages {
		if !queued[pkg.PackageID] {
			queued[pkg.PackageID] = true
			packageIDs = append(packageIDs, pkg.PackageID)
		}
	}

	total := 0
	for _, id := range packageIDs {
		if ctx.Err() != nil {
			break // runStep records it as a failure
		}
		err = s.processGovInfoPackage(ctx, id, congressNum)
		countItem(ctx, err)
		if err != nil {
			s.logger.WarnContext(ctx, "process govinfo package failed", "package_id", id, "error", err)
			retries.failed(ctx, id, err)
			continue
		}
		retries.succeeded(id)
		total++
	}

	if err = errors.Join(ctx.Err(), retries.finish(ctx)); err != nil {
		return total, err
	}
	s.logger.InfoContext(ctx, "govinfo changes synced", "congress", congressNum, "count", total)
	return total, nil
}

// getGovInfoLastSync returns the govinfo watermark, or 24 hours ago when the step has never
// succeeded.
func (s *Service) getGovInfoLastSync(ctx context.Context, congressNum int) (time.Time, error) {
	st, err := s.store.GetSyncState(ctx, stepGovInfo, congressNum)
	if err != nil {
		return time.Time{}, err
	}
	if st != nil && !st.LastSyncedAt.IsZero() {
		return st.LastSyncedAt, nil
	}
	return time.Now().Add(-defaultLookbackHours * time.Hour), nil
}

func (s *Service) processGovInfoPackage(ctx context.Context, packageID string, congressNum int) error {
	congress, billType, number, versionCode, ok := parseGovInfoBillID(packageID)
	if !ok {
		s.logger.WarnContext(ctx, "could not parse govinfo package ID", "package_id", packageID)
		return nil
	}

	if congress != congressNum {
		return nil
	}

	summary, err := s.govinfo.FetchPackageSummary(ctx, packageID)
	if err != nil {
		return fmt.Errorf("fetch package summary: %w", err)
	}

	if summary.Download == nil {
		return nil
	}

	textURL := summary.Download.XMLLink
	format := formatXML
	if textURL == "" {
		textURL = summary.Download.TxtLink
		format = formatText
	}
	if textURL == "" {
		return nil
	}

	// Look up the bill_text_version matching this bill and version code
	billID := fmt.Sprintf("%s-%d-%d", billType, congress, number)
	versionID, findErr := s.store.FindUnfetchedVersion(ctx, billID, versionCode)
	if findErr != nil {
		// No matching unfetched version found; skip
		if strings.Contains(findErr.Error(), "no unfetched version found") {
			return nil
		}
		return fmt.Errorf("find unfetched version: %w", findErr)
	}

	s.logger.InfoContext(ctx, "fetching govinfo bill text",
		"package_id", packageID, "bill_id", billID, "version_code", versionCode)

	data, err := s.govinfo.FetchText(ctx, textURL)
	if err != nil {
		return fmt.Errorf("fetch govinfo text: %w", err)
	}

	// Built and stored as sync-texts stores a text: newTextRow's row, then its law references.
	v := repository.TextVersionRef{ID: versionID, BillID: billID, VersionCode: versionCode}
	text, raw, err := s.textRow(ctx, v, format, data)
	if err != nil {
		return err
	}
	if err = s.store.InsertBillText(ctx, text); err != nil {
		return fmt.Errorf("insert govinfo bill text: %w", err)
	}
	markBill(ctx, billID)
	s.storeTextLawRefs(ctx, v, packageID, text, raw)
	return nil
}
