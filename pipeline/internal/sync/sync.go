// Package sync orchestrates data synchronization from Congress.gov API into the database.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/semconv"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/fedreg"
	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
	"github.com/justabill-org/justabill/pipeline/internal/legislators"
)

// syncCounter provides an atomic counter for concurrent worker pools.
type syncCounter struct{ n atomic.Int64 }

func (c *syncCounter) inc() { c.n.Add(1) }

func (c *syncCounter) get() int { return int(c.n.Load()) }

const (
	pageSize       = 250 // the Congress.gov maximum
	nameSplitParts = 2
	chamberSenate  = "Senate"
	chamberHouse   = "House"
)

// Service coordinates pipeline sync operations.
type Service struct {
	store   repository.PipelineStore
	api     *congress.Client
	govinfo *govinfo.Client
	// fedreg is the Federal Register client SyncCRARules uses; nil leaves that step off.
	fedreg *fedreg.Client
	// http is the pipeline's upstream client, for vote XML and bill text downloads.
	http       *http.Client
	logger     *slog.Logger
	summarizer summarizer
	// lawExplainer is the summarizer, for law-change explanations; nil when AI is off.
	lawExplainer lawExplainer
	summaryJob   SummaryJobConfig
	// batches is the Vertex AI batch path; nil when AI_BATCH_BUCKET is unset.
	batches *summaryBatches
	// clock is what the summary job counts its daily cap and retries by; nil means [time.Now].
	clock       func() time.Time
	feeds       voteFeeds
	legislators *legislators.Client
	tally       *Tally
	// revalidator tells the web app which bills a step run stored; nil when the hook is off.
	revalidator revalidator
	// apiCache deletes the API's cached copies of a step run's bills; nil when REDIS_URL is unset.
	apiCache    apiCacheClearer
	forceSync   bool
	resumeSince time.Time
}

// SetGovInfo sets the GovInfo client on the service.
func (s *Service) SetGovInfo(g *govinfo.Client) { s.govinfo = g }

// SetForceSync disables skip-existing optimizations, forcing a full re-sync.
func (s *Service) SetForceSync(force bool) { s.forceSync = force }

// SetResumeSince makes SyncBills skip bills marked synced at or after t, so a rerun of an
// interrupted load continues where it stopped, and record t as the watermark when it succeeds.
// The zero time continues the interrupted run sync_state saved, if any (billsResumeSince).
func (s *Service) SetResumeSince(t time.Time) { s.resumeSince = t }

// New creates a new sync Service. httpClient is the pipeline's upstream client (the one api
// uses too), for vote XML and bill text downloads.
func New(store repository.PipelineStore, api *congress.Client, httpClient *http.Client) *Service {
	return &Service{
		store:       store,
		api:         api,
		http:        httpClient,
		logger:      slog.Default(),
		summaryJob:  DefaultSummaryJobConfig(),
		feeds:       defaultVoteFeeds(),
		legislators: legislators.New(""),
	}
}

const memberSyncInterval = 24 * time.Hour

// SyncMembers fetches all members for a congress and upserts them, then stores the sitting
// senators' LIS IDs from the Senate's member feed. It skips itself when the last successful
// run started less than 24 hours ago.
func (s *Service) SyncMembers(ctx context.Context, congressNum int) error {
	return s.syncMemberStep(ctx, congressNum, s.upsertMember, true)
}

// syncMemberStep runs the members step with upsert unless it succeeded in the last 24 hours.
// With senateIDs, a run that stored the members then refreshes LIS IDs from the Senate's
// feed; a feed failure is logged and doesn't fail the step.
func (s *Service) syncMemberStep(ctx context.Context, congressNum int, upsert memberUpsert, senateIDs bool) error {
	if !s.forceSync {
		if st, err := s.store.GetSyncState(ctx, stepMembers, congressNum); err == nil && st != nil &&
			!st.LastSyncedAt.IsZero() && time.Since(st.LastSyncedAt) < memberSyncInterval {
			s.logger.InfoContext(ctx, "members synced recently, skipping",
				"congress", congressNum, "last_synced", st.LastSyncedAt)
			return nil
		}
	}
	return s.runStep(ctx, stepMembers, congressNum, false, func(ctx context.Context) (int, error) {
		n, err := s.syncMembers(ctx, congressNum, upsert)
		if err == nil && senateIDs {
			if lisErr := s.syncSenateLISIDs(ctx); lisErr != nil {
				s.logger.WarnContext(ctx, "senate lis ids not refreshed", "error", lisErr)
			}
		}
		return n, err
	})
}

// memberUpsert stores one member from the Congress.gov member list.
type memberUpsert func(ctx context.Context, m congress.Member, congressNum int) error

func (s *Service) syncMembers(ctx context.Context, congressNum int, upsert memberUpsert) (int, error) {
	s.logger.InfoContext(ctx, "syncing members", "congress", congressNum)
	offset := 0
	total := 0

	for {
		resp, err := s.api.ListMembers(ctx, congressNum, offset, pageSize)
		if err != nil {
			return total, fmt.Errorf("list members: %w", err)
		}

		for _, m := range resp.Members {
			if ctx.Err() != nil {
				return total, ctx.Err()
			}
			err = upsert(ctx, m, congressNum)
			if err != nil {
				s.logger.WarnContext(ctx, "upsert member failed", "bioguide_id", m.BioguideID, "error", err)
			}
			countItem(ctx, err)
		}

		total += len(resp.Members)
		offset += pageSize
		if offset >= resp.Pagination.Count || len(resp.Members) == 0 {
			break
		}
	}

	s.logger.InfoContext(ctx, "members synced", "congress", congressNum, "count", total)
	return total, nil
}

// SyncBills fetches bills for a congress and upserts them with actions and text versions.
// A run that continues an interrupted one (a timeout, a crash, or SetResumeSince) skips the
// bills marked synced since that run started and, when it succeeds, records that start as the
// watermark (billsResumeSince). A limited run (limit > 0) never moves the bills watermark: it
// didn't list everything.
//
// Bills that failed on earlier runs and are due again in sync_retry go first, ahead of the
// listing (deduplicated). A bill that fails is recorded there, with a permanent error given
// up at once, and doesn't fail the run; a bill that syncs has its row cleared. The run fails,
// keeping the watermark, only if a failure couldn't be recorded
// (docs/design/67-upstream-quota-retries.md).
func (s *Service) SyncBills(ctx context.Context, congressNum, limit int) error {
	return s.runStepOutcome(ctx, stepBills, congressNum, limit > 0, func(ctx context.Context) (stepOutcome, error) {
		return s.syncBills(ctx, congressNum, limit)
	})
}

func (s *Service) syncBills(ctx context.Context, congressNum, limit int) (stepOutcome, error) {
	s.logger.InfoContext(ctx, "syncing bills", "congress", congressNum, "limit", limit)

	resume, err := s.billsResumeSince(ctx, congressNum, limit > 0)
	if err != nil {
		return stepOutcome{}, err
	}
	retries := s.newRetryList(repository.RetryStepBills, congressNum)
	due, err := retries.due(ctx)
	if err != nil {
		return stepOutcome{}, err
	}
	listed, err := s.collectBillSummaries(ctx, congressNum, limit)
	if err != nil {
		return stepOutcome{}, err
	}
	bills := s.mergeDueBills(ctx, retries, due, listed, congressNum)
	if limit > 0 && len(bills) > limit {
		bills = bills[:limit]
	}
	bills, skipped, err := s.skipResumed(ctx, congressNum, bills, resume)
	if err != nil {
		return stepOutcome{}, err
	}

	progress := newBillProgress(s.logger, s.requestCount, len(bills), skipped)
	obs.Items(ctx, semconv.ItemOutcomeSkipped, skipped)

	workerPool(ctx, bills, defaultCongressWorkers, func(ctx context.Context, bs congress.BillSummary) {
		id := billID(bs, congressNum)
		syncErr := s.syncOneBill(ctx, bs, congressNum)
		if syncErr != nil {
			s.logger.WarnContext(ctx, "sync bill failed", "bill", id, "error", syncErr)
			retries.failed(ctx, id, syncErr)
		} else {
			retries.succeeded(id)
		}
		progress.add(ctx, syncErr == nil)
	})

	progress.log(ctx, "bills synced")
	return stepOutcome{items: progress.done.get(), watermark: resume}, retries.finish(ctx)
}

// mergeDueBills puts the due retries ahead of the listed bills, each bill once. A due bill
// that is also listed keeps its list entry. A due ID that isn't a bill ID can never sync, so
// its row is cleared.
func (s *Service) mergeDueBills(
	ctx context.Context, retries *retryList, due []string, listed []congress.BillSummary, congressNum int,
) []congress.BillSummary {
	if len(due) == 0 {
		return listed
	}
	byID := make(map[string]congress.BillSummary, len(listed))
	for _, bs := range listed {
		byID[billID(bs, congressNum)] = bs
	}
	bills := make([]congress.BillSummary, 0, len(due)+len(listed))
	queued := make(map[string]bool, len(due)+len(listed))
	for _, id := range due {
		bs, ok := byID[id]
		if !ok {
			var err error
			if bs, err = billSummaryFromID(id); err != nil || bs.Congress != congressNum {
				s.logger.WarnContext(ctx, "dropping unusable bill retry", "bill", id, "error", err)
				retries.succeeded(id)
				continue
			}
		}
		if !queued[id] {
			queued[id] = true
			bills = append(bills, bs)
		}
	}
	for _, bs := range listed {
		if id := billID(bs, congressNum); !queued[id] {
			queued[id] = true
			bills = append(bills, bs)
		}
	}
	return bills
}

// skipResumed drops the bills marked synced at or after since and returns the rest with the
// number skipped. With the zero time it returns bills unchanged.
func (s *Service) skipResumed(
	ctx context.Context, congressNum int, bills []congress.BillSummary, since time.Time,
) ([]congress.BillSummary, int, error) {
	if since.IsZero() {
		return bills, 0, nil
	}
	ids, err := s.store.ListBillIDsSyncedSince(ctx, congressNum, since)
	if err != nil {
		return nil, 0, fmt.Errorf("list bills synced since %s: %w", since.Format(time.RFC3339), err)
	}
	synced := make(map[string]bool, len(ids))
	for _, id := range ids {
		synced[id] = true
	}
	remaining := make([]congress.BillSummary, 0, len(bills))
	for _, bs := range bills {
		if !synced[billID(bs, congressNum)] {
			remaining = append(remaining, bs)
		}
	}
	skipped := len(bills) - len(remaining)
	s.logger.InfoContext(ctx, "resuming bills", "congress", congressNum,
		"resume_since", since.Format(time.RFC3339), "skipped", skipped, "remaining", len(remaining))
	return remaining, skipped, nil
}

// requestCount is how many Congress.gov requests the service has made so far.
func (s *Service) requestCount() int64 {
	if s.api == nil {
		return 0
	}
	return s.api.Requests()
}

const syncTimeBuffer = 5 * time.Minute

func (s *Service) collectBillSummaries(
	ctx context.Context, congressNum, limit int,
) ([]congress.BillSummary, error) {
	// Use fromDateTime if a previous run succeeded and we're not forcing.
	var since *time.Time
	if !s.forceSync {
		if st, err := s.store.GetSyncState(ctx, stepBills, congressNum); err == nil && st != nil &&
			!st.LastSyncedAt.IsZero() {
			buffered := st.LastSyncedAt.Add(-syncTimeBuffer)
			since = &buffered
			s.logger.InfoContext(ctx, "fetching bills updated since", "since", buffered)
		}
	}

	return s.paginateBills(ctx, congressNum, limit, since)
}

func (s *Service) paginateBills(
	ctx context.Context, congressNum, limit int, since *time.Time,
) ([]congress.BillSummary, error) {
	var bills []congress.BillSummary
	offset := 0

	for {
		var (
			resp *congress.BillsResponse
			err  error
		)
		if since != nil {
			resp, err = s.api.ListBillsUpdatedSince(ctx, congressNum, *since, offset, pageSize)
		} else {
			resp, err = s.api.ListBills(ctx, congressNum, offset, pageSize)
		}
		if err != nil {
			return nil, fmt.Errorf("list bills: %w", err)
		}

		for _, bs := range resp.Bills {
			if limit > 0 && len(bills) >= limit {
				return bills, nil
			}
			bills = append(bills, bs)
		}

		if limit > 0 && len(bills) >= limit {
			return bills, nil
		}
		offset += pageSize
		if offset >= resp.Pagination.Count || len(resp.Bills) == 0 {
			break
		}
	}

	return bills, nil
}

// billID is the stored ID of a listed bill: <type>-<congress>-<number>.
func billID(bs congress.BillSummary, congressNum int) string {
	num, _ := strconv.Atoi(bs.Number)
	return fmt.Sprintf("%s-%d-%d", strings.ToLower(bs.Type), congressNum, num)
}

// syncOneBill fetches and stores a bill and its sub-resources. Only when all of them succeed
// does it mark the bill synced (updated_at), so a bill that failed partway keeps its old
// updated_at and a resumed run retries it. It returns every failure, joined.
func (s *Service) syncOneBill(ctx context.Context, bs congress.BillSummary, congressNum int) error {
	num, _ := strconv.Atoi(bs.Number)
	billType := strings.ToLower(bs.Type)
	id := billID(bs, congressNum)

	// Fetch detail first (required for upsert).
	detail, err := s.api.GetBill(ctx, congressNum, billType, num)
	if notFound(err) {
		return fmt.Errorf("get bill detail: %w: %w", errUnknownBill, err)
	}
	if err != nil {
		return fmt.Errorf("get bill detail: %w", err)
	}

	if err = s.upsertBill(ctx, id, detail, &bs); err != nil {
		return fmt.Errorf("upsert bill: %w", err)
	}

	if err = errors.Join(
		s.syncBillSponsors(ctx, id, detail),
		s.syncBillDetailsConcurrently(ctx, id, congressNum, billType, num),
	); err != nil {
		return err
	}
	if err = s.store.MarkBillSynced(ctx, id); err != nil {
		return fmt.Errorf("mark bill synced: %w", err)
	}
	markBill(ctx, id)
	s.logger.InfoContext(ctx, "synced bill", "id", id, "title", truncate(detail.Title))
	return nil
}

// syncBillDetailsConcurrently runs the seven sub-resource syncs, which are independent of each
// other, in parallel and returns their errors joined.
func (s *Service) syncBillDetailsConcurrently(
	ctx context.Context, billID string, congressNum int, billType string, num int,
) error {
	var wg sync.WaitGroup

	type syncFn func(context.Context, string, int, string, int) error

	fns := []syncFn{
		s.syncBillActions,
		s.syncBillTextVersions,
		s.syncBillCosponsors,
		s.syncBillCommittees,
		s.syncBillSubjects,
		s.syncBillRelatedBills,
		s.syncBillAmendments,
	}

	errs := make([]error, len(fns))
	wg.Add(len(fns))
	for i, fn := range fns {
		go func() {
			defer wg.Done()
			errs[i] = fn(ctx, billID, congressNum, billType, num)
		}()
	}

	wg.Wait()
	return errors.Join(errs...)
}

func (s *Service) upsertMember(ctx context.Context, m congress.Member, congressNum int) error {
	if err := s.upsertMemberRoster(ctx, m, congressNum); err != nil {
		return err
	}

	party := mapParty(m.PartyName)
	chamber := chamberSenate
	if m.District != nil {
		chamber = chamberHouse
	}
	stateCode := stateAbbrev(m.State)

	return s.store.UpsertMemberTerm(ctx, repository.MemberTermRow{
		MemberID: m.BioguideID,
		Congress: congressNum,
		Chamber:  chamber,
		State:    stateCode,
		District: m.District,
		Party:    party,
		EndDate:  termEndDate(m.Terms.Item, chamber, congressNum),
	})
}

// upsertMemberRoster stores a member's name and photo, and nothing about their terms.
func (s *Service) upsertMemberRoster(ctx context.Context, m congress.Member, _ int) error {
	parts := strings.SplitN(m.Name, ", ", nameSplitParts)
	lastName := parts[0]
	firstName := ""
	if len(parts) > 1 {
		firstName = parts[1]
	}

	var photoURL *string
	if m.Depiction != nil && m.Depiction.ImageURL != "" {
		photoURL = &m.Depiction.ImageURL
	}

	return s.store.UpsertMember(ctx, repository.MemberRow{
		BioguideID: m.BioguideID,
		FirstName:  firstName,
		LastName:   lastName,
		PhotoURL:   photoURL,
	})
}

func (s *Service) upsertBill(
	ctx context.Context, billID string,
	detail *congress.BillDetail, bs *congress.BillSummary,
) error {
	num, _ := strconv.Atoi(detail.Number)
	billType := strings.ToLower(detail.Type)

	var introducedDate *time.Time
	if detail.IntroducedDate != "" {
		if t, err := time.Parse("2006-01-02", detail.IntroducedDate); err == nil {
			introducedDate = &t
		}
	}

	var policyArea *string
	if detail.PolicyArea != nil {
		policyArea = &detail.PolicyArea.Name
	}

	var sponsorsJSON []byte
	if len(detail.Sponsors) > 0 {
		sponsorsJSON, _ = json.Marshal(detail.Sponsors)
	}

	// A bill synced by ID (SyncBillsByID) has no list entry; the detail carries the same field.
	var latestAction json.RawMessage
	switch {
	case bs.LatestAction != nil:
		latestAction, _ = json.Marshal(bs.LatestAction)
	case detail.LatestAction != nil:
		latestAction, _ = json.Marshal(detail.LatestAction)
	}

	return s.store.UpsertBill(ctx, repository.BillRow{
		ID:             billID,
		Congress:       detail.Congress,
		BillType:       billType,
		Number:         num,
		Title:          detail.Title,
		IntroducedDate: introducedDate,
		OriginChamber:  &detail.OriginChamber,
		PolicyArea:     policyArea,
		LatestAction:   latestAction,
		Sponsors:       sponsorsJSON,
		Laws:           BillLaws(detail),
	})
}

func (s *Service) syncBillActions(
	ctx context.Context, billID string, congressNum int, billType string, number int,
) error {
	resp, err := s.api.GetBillActions(ctx, congressNum, billType, number)
	if err != nil {
		return fetchError("actions", err)
	}

	actions := make([]repository.BillActionRow, 0, len(resp.Actions))
	for i, a := range resp.Actions {
		actionDate, _ := time.Parse("2006-01-02", a.ActionDate)
		var sourceSystem *string
		if a.SourceSystem != nil && a.SourceSystem.Name != "" {
			sourceSystem = &a.SourceSystem.Name
		}

		actions = append(actions, repository.BillActionRow{
			ActionDate:   actionDate,
			ActionText:   a.Text,
			ActionType:   nilIfEmpty(a.Type),
			ActionCode:   nilIfEmpty(a.ActionCode),
			SourceSystem: sourceSystem,
			SortOrder:    i + 1,
		})
	}

	if err = s.store.ReplaceBillActions(ctx, billID, actions); err != nil {
		return fmt.Errorf("replace bill actions: %w", err)
	}

	// Detect and store voice votes from action text.
	s.syncVoiceVotes(ctx, billID, congressNum, resp.Actions)

	return s.storeStatusHistory(ctx, billID, apiLifecycleActions(resp.Actions))
}

func (s *Service) syncBillTextVersions(
	ctx context.Context, billID string, congressNum int, billType string, number int,
) error {
	resp, err := s.api.GetBillTextVersions(ctx, congressNum, billType, number)
	if err != nil {
		return fetchError("text_versions", err)
	}

	versions := TextVersionRows(ctx, s.logger, billID, resp.TextVersions)

	res, err := s.store.UpsertBillTextVersions(ctx, billID, versions)
	if err != nil {
		return fmt.Errorf("upsert text versions: %w", err)
	}
	s.logTextVersionSync(ctx, billID, res)
	return nil
}

// logTextVersionSync logs an upsert's counts. Pruning deletes texts and paid-for diff
// summaries, and duplicate codes mean a version was dropped, so both are warnings.
func (s *Service) logTextVersionSync(ctx context.Context, billID string, res repository.TextVersionSyncResult) {
	if res.Pruned > 0 {
		s.logger.WarnContext(ctx, "pruned text versions no longer listed",
			"bill_id", billID, "pruned", res.Pruned, "version_codes", res.PrunedCodes)
	}
	if res.Duplicates > 0 {
		s.logger.WarnContext(ctx, "dropped text versions with duplicate codes",
			"bill_id", billID, "duplicates", res.Duplicates)
	}
	if len(res.RefetchCodes) > 0 {
		s.logger.InfoContext(ctx, "queued corrected text versions for a refetch",
			"bill_id", billID, "version_codes", res.RefetchCodes)
	}
	s.logger.DebugContext(ctx, "text versions synced", "bill_id", billID,
		"inserted", res.Inserted, "updated", res.Updated, "pruned", res.Pruned, "refetch", len(res.RefetchCodes))
}

func (s *Service) syncBillCosponsors(
	ctx context.Context, billID string, congressNum int, billType string, number int,
) error {
	resp, err := s.api.GetBillCosponsors(ctx, congressNum, billType, number)
	if err != nil {
		return fetchError("cosponsors", err)
	}
	if len(resp.Cosponsors) == 0 {
		return nil
	}
	rows, skipped := cosponsorshipRows(resp.Cosponsors)
	return s.writeBillJSONAndLinks(ctx, billID, "cosponsors", resp.Cosponsors, skipped,
		func(ctx context.Context) error {
			return s.store.ReplaceBillSponsorships(ctx, billID, repository.SponsorRoleCosponsor, rows)
		})
}

func (s *Service) syncBillCommittees(
	ctx context.Context, billID string, congressNum int, billType string, number int,
) error {
	resp, err := s.api.GetBillCommittees(ctx, congressNum, billType, number)
	if err != nil {
		return fetchError("committees", err)
	}
	if len(resp.Committees) == 0 {
		return nil
	}
	rows, skipped := committeeRows(resp.Committees)
	return s.writeBillJSONAndLinks(ctx, billID, "committees", resp.Committees, skipped,
		func(ctx context.Context) error {
			return s.store.ReplaceBillCommittees(ctx, billID, rows)
		})
}

func (s *Service) syncBillSubjects(
	ctx context.Context, billID string, congressNum int, billType string, number int,
) error {
	resp, err := s.api.GetBillSubjects(ctx, congressNum, billType, number)
	if err != nil {
		return fetchError("subjects", err)
	}
	names := make([]string, 0, len(resp.Subjects.LegislativeSubjects))
	for _, ls := range resp.Subjects.LegislativeSubjects {
		names = append(names, ls.Name)
	}
	if len(names) == 0 {
		return nil
	}
	// ReplaceBillSubjects slugs the names and drops any that slug to nothing.
	return s.writeBillJSONAndLinks(ctx, billID, "subjects", names, 0, func(ctx context.Context) error {
		return s.store.ReplaceBillSubjects(ctx, billID, names)
	})
}

func (s *Service) syncBillRelatedBills(
	ctx context.Context, billID string, congressNum int, billType string, number int,
) error {
	resp, err := s.api.GetBillRelatedBills(ctx, congressNum, billType, number)
	if err != nil {
		return fetchError("related_bills", err)
	}
	if len(resp.RelatedBills) == 0 {
		return nil
	}
	rows, skipped := relationRows(resp.RelatedBills)
	return s.writeBillJSONAndLinks(ctx, billID, "related_bills", resp.RelatedBills, skipped,
		func(ctx context.Context) error {
			return s.store.ReplaceBillRelations(ctx, billID, rows)
		})
}

func (s *Service) syncBillAmendments(
	ctx context.Context, billID string, congressNum int, billType string, number int,
) error {
	resp, err := s.api.GetBillAmendments(ctx, congressNum, billType, number)
	if err != nil {
		return fetchError("amendments", err)
	}
	var errs []error
	for _, a := range resp.Amendments {
		amendNum, _ := strconv.Atoi(a.Number)
		amendType := strings.ToLower(a.Type)
		amendID := fmt.Sprintf("%s-%d-%d", amendType, congressNum, amendNum)

		var latestAction json.RawMessage
		if a.LatestAction != nil {
			latestAction, _ = json.Marshal(a.LatestAction)
		}

		chamber := a.Chamber
		if chamber == "" {
			if strings.HasPrefix(amendType, "s") {
				chamber = chamberSenate
			} else {
				chamber = chamberHouse
			}
		}

		if execErr := s.store.UpsertAmendment(ctx, repository.AmendmentRow{
			ID:              amendID,
			BillID:          billID,
			Congress:        congressNum,
			AmendmentType:   amendType,
			AmendmentNumber: amendNum,
			Description:     nilIfEmpty(a.Description),
			Purpose:         nilIfEmpty(a.Purpose),
			LatestAction:    latestAction,
			Chamber:         chamber,
		}); execErr != nil {
			errs = append(errs, fmt.Errorf("upsert amendment %s: %w", amendID, execErr))
		}
	}
	return errors.Join(errs...)
}

// fetchError wraps a failed sub-resource fetch. The caller writes nothing, so the list stored
// by the last successful sync stays, and the bill isn't marked synced.
func fetchError(subResource string, err error) error {
	return fmt.Errorf("fetch %s: %w", subResource, err)
}

// --- Helpers ---

func mapParty(partyName string) string {
	switch {
	case strings.Contains(partyName, "Democrat"):
		return "D"
	case strings.Contains(partyName, "Republican"):
		return "R"
	case strings.Contains(partyName, "Independent"):
		return "I"
	default:
		return "O"
	}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func truncate(s string) string {
	const maxLen = 60
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

//nolint:gochecknoglobals // lookup table
var stateMap = map[string]string{
	"Alabama": "AL", "Alaska": "AK", "Arizona": "AZ", "Arkansas": "AR",
	"California": "CA", "Colorado": "CO", "Connecticut": "CT", "Delaware": "DE",
	"Florida": "FL", "Georgia": "GA", "Hawaii": "HI", "Idaho": "ID",
	"Illinois": "IL", "Indiana": "IN", "Iowa": "IA", "Kansas": "KS",
	"Kentucky": "KY", "Louisiana": "LA", "Maine": "ME", "Maryland": "MD",
	"Massachusetts": "MA", "Michigan": "MI", "Minnesota": "MN", "Mississippi": "MS",
	"Missouri": "MO", "Montana": "MT", "Nebraska": "NE", "Nevada": "NV",
	"New Hampshire": "NH", "New Jersey": "NJ", "New Mexico": "NM", "New York": "NY",
	"North Carolina": "NC", "North Dakota": "ND", "Ohio": "OH", "Oklahoma": "OK",
	"Oregon": "OR", "Pennsylvania": "PA", "Rhode Island": "RI", "South Carolina": "SC",
	"South Dakota": "SD", "Tennessee": "TN", "Texas": "TX", "Utah": "UT",
	"Vermont": "VT", "Virginia": "VA", "Washington": "WA", "West Virginia": "WV",
	"Wisconsin": "WI", "Wyoming": "WY", "District of Columbia": "DC",
	"American Samoa": "AS", "Guam": "GU", "Northern Mariana Islands": "MP",
	"Puerto Rico": "PR", "Virgin Islands": "VI",
}

func stateAbbrev(fullName string) string {
	if abbr, ok := stateMap[fullName]; ok {
		return abbr
	}
	return fullName
}
