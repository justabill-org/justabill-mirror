package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
	"github.com/justabill-org/justabill/pipeline/internal/xmlparse"
)

const (
	senateVoteWorkers = 5

	houseDateFormat  = "2-Jan-2006"
	senateDateFormat = "January 2, 2006, 03:04 PM"
)

// voteFeeds holds the House Clerk and Senate LIS URL templates. Tests point them at an
// httptest server.
type voteFeeds struct {
	houseIndexURL    string // year
	houseVoteURL     string // year, roll
	senateIndexURL   string // congress, session
	senateVoteURL    string // congress, session, congress, session, vote number
	senateMembersURL string // the Senate's member feed: LIS ID to bioguide ID
}

func defaultVoteFeeds() voteFeeds {
	return voteFeeds{
		houseIndexURL:    "https://clerk.house.gov/evs/%d/index.asp",
		houseVoteURL:     "https://clerk.house.gov/evs/%d/roll%03d.xml",
		senateIndexURL:   "https://www.senate.gov/legislative/LIS/roll_call_lists/vote_menu_%d_%d.xml",
		senateVoteURL:    "https://www.senate.gov/legislative/LIS/roll_call_votes/vote%d%d/vote_%d_%d_%05d.xml",
		senateMembersURL: "https://www.senate.gov/legislative/LIS_MEMBER/cvc_member_data.xml",
	}
}

var (
	// errNotFound is an HTTP 404 from a vote feed. For a session's index or menu it means the
	// session has no roll calls yet.
	errNotFound = errors.New("not found")
	// errSessionMismatch means a vote document names a different congress or session than the
	// one requested, e.g. a wrong year folder. Nothing from it is written.
	errSessionMismatch = errors.New("vote XML is for a different congress or session")
)

// senateVoteNumberRe extracts vote numbers from the Senate vote menu XML.
var senateVoteNumberRe = regexp.MustCompile(`<vote_number>\s*(\d+)\s*</vote_number>`)

// billRefRe splits a compacted measure reference ("hr144", "sconres7") into type and number.
var billRefRe = regexp.MustCompile(`^([a-z]+)(\d+)$`)

// rollNumberRe matches roll call numbers in the House index page.
// The index page uses links like "rollnumber=362" rather than "roll362.xml".
var rollNumberRe = regexp.MustCompile(`rollnumber=(\d+)`)

// SyncVotes fetches the House and Senate roll call votes of the given sessions of a congress
// and upserts them into the database. Callers pass rollcall.Sessions for every session that has
// started. A chamber whose roll-call list can't be read fails the step after the other chamber
// and the other sessions have synced; a single roll call that fails is logged.
//
// Senators are placed by the LIS IDs the Senate's member feed maps to bioguide IDs (refreshed
// at the start of the run), then by a strict name match. Senators neither places are logged,
// counted, and named in sync_state.last_error ("unmatched senators: S409") without failing the
// step, and their votes are fetched again on the next run.
func (s *Service) SyncVotes(ctx context.Context, congressNum int, sessions []int) error {
	return s.runStepOutcome(ctx, stepVotes, congressNum, false, func(ctx context.Context) (stepOutcome, error) {
		return s.syncVotes(ctx, congressNum, sessions)
	})
}

// sessionVotes counts what one session's vote sync stored.
type sessionVotes struct {
	house, senate, senateLinked int
	// unmatchedSenators counts the distinct senators in the session's stored Senate votes that
	// couldn't be placed.
	unmatchedSenators int
}

func (s *Service) syncVotes(ctx context.Context, congressNum int, sessions []int) (stepOutcome, error) {
	if len(sessions) == 0 {
		s.logger.InfoContext(ctx, "no session of this congress has started", "congress", congressNum)
		return stepOutcome{}, nil
	}

	if err := s.syncSenateLISIDs(ctx); err != nil {
		s.logger.WarnContext(ctx, "senate lis ids not refreshed; using the stored ones", "error", err)
	}
	senators, err := s.newSenatorResolver(ctx, congressNum)
	if err != nil {
		return stepOutcome{}, err
	}

	total := 0
	var errs []error
	for _, session := range sessions {
		counts, sessionErr := s.syncSessionVotes(ctx, congressNum, session, senators)
		s.logger.InfoContext(ctx, "votes synced", "congress", congressNum, "session", session,
			"house", counts.house, "senate", counts.senate, "senate_linked", counts.senateLinked,
			"unmatched_senators", counts.unmatchedSenators)
		s.tally.addVotes(session, counts.house, counts.senate)
		total += counts.house + counts.senate
		if sessionErr != nil {
			errs = append(errs, fmt.Errorf("session %d: %w", session, sessionErr))
		}
	}

	out := stepOutcome{items: total}
	unmatched := senators.unmatched()
	for _, lis := range unmatched {
		s.tally.addUnmatchedSenator(lis)
	}
	if len(unmatched) > 0 {
		out.warning = "unmatched senators: " + strings.Join(unmatched, ", ")
	}
	s.logger.InfoContext(ctx, "vote sync finished", "congress", congressNum, "stored", total,
		"unmatched_senators", len(unmatched))
	return out, errors.Join(errs...)
}

// syncSessionVotes syncs one session, House and Senate in parallel, and returns what it stored
// and the chambers' errors, joined.
func (s *Service) syncSessionVotes(
	ctx context.Context, congressNum, session int, senators *senatorResolver,
) (sessionVotes, error) {
	year := rollcall.Year(congressNum, session)
	s.logger.InfoContext(ctx, "syncing votes", "congress", congressNum, "session", session, "year", year)

	var (
		counts              sessionVotes
		houseErr, senateErr error
		wg                  sync.WaitGroup
	)
	wg.Add(2) //nolint:mnd // house + senate

	go func() {
		defer wg.Done()
		counts.house, houseErr = s.syncHouseVotes(ctx, congressNum, session, year)
	}()

	go func() {
		defer wg.Done()
		var senate senateCounts
		senate, senateErr = s.syncSenateVotes(ctx, congressNum, session, senators)
		counts.senate, counts.senateLinked, counts.unmatchedSenators = senate.stored, senate.linked, senate.unmatched
	}()

	wg.Wait()

	if houseErr != nil {
		houseErr = fmt.Errorf("house: %w", houseErr)
	}
	if senateErr != nil {
		senateErr = fmt.Errorf("senate: %w", senateErr)
	}
	return counts, errors.Join(houseErr, senateErr)
}

const houseVoteWorkers = 5

func (s *Service) syncHouseVotes(ctx context.Context, congressNum, session, year int) (int, error) {
	maxRoll, err := s.discoverHouseRollCount(ctx, year)
	if errors.Is(err, errNotFound) {
		s.logger.InfoContext(ctx, "no house roll calls yet", "session", session, "year", year)
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("discover house rolls: %w", err)
	}

	s.logger.InfoContext(ctx, "discovered house rolls", "year", year, "max_roll", maxRoll)

	rolls, incomplete := s.filterNewHouseRolls(ctx, congressNum, session, maxRoll)

	s.logger.InfoContext(ctx, "house votes to fetch", "total", maxRoll, "new", len(rolls)-incomplete,
		"incomplete", incomplete)

	counter := &syncCounter{}

	workerPool(ctx, rolls, houseVoteWorkers, func(ctx context.Context, roll int) {
		fetchErr := s.fetchAndStoreHouseVote(ctx, congressNum, session, year, roll)
		countItem(ctx, fetchErr)
		if fetchErr != nil {
			s.logger.WarnContext(ctx, "house vote fetch failed", "roll", roll, "error", fetchErr)
			return
		}
		counter.inc()
	})

	return counter.get(), nil
}

func (s *Service) discoverHouseRollCount(ctx context.Context, year int) (int, error) {
	url := fmt.Sprintf(s.feeds.houseIndexURL, year)

	body, err := s.httpGet(ctx, url)
	if errors.Is(err, errNotFound) {
		// The Clerk took its per-year index pages down (404 for 2024 and 2025 on 2026-10-01), so
		// the roll files are the listing: find the highest by probing them.
		return s.probeHouseRollCount(ctx, year)
	}
	if err != nil {
		return 0, err
	}

	matches := rollNumberRe.FindAllStringSubmatch(string(body), -1)
	maxRoll := 0
	for _, m := range matches {
		n, _ := strconv.Atoi(m[1])
		if n > maxRoll {
			maxRoll = n
		}
	}
	if maxRoll == 0 {
		// An index with no roll numbers in it is no listing either: probe the roll files.
		return s.probeHouseRollCount(ctx, year)
	}

	return maxRoll, nil
}

// maxHouseRolls bounds probeHouseRollCount: the House has never held 1,000 roll calls in a year.
const maxHouseRolls = 2000

// probeHouseRollCount finds a year's highest House roll by fetching its roll files
// (rollcall.HighestRoll), about 20 requests to the Clerk and none to Congress.gov. It returns
// errNotFound when the year has no roll 1, as the index's 404 used to mean.
func (s *Service) probeHouseRollCount(ctx context.Context, year int) (int, error) {
	n, err := rollcall.HighestRoll(func(roll int) (bool, error) {
		body, getErr := s.httpGet(ctx, fmt.Sprintf(s.feeds.houseVoteURL, year, roll))
		if errors.Is(getErr, errNotFound) {
			return false, nil
		}
		return getErr == nil && rollcall.IsHouseRoll(body), getErr
	}, maxHouseRolls)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, errNotFound
	}
	s.logger.InfoContext(ctx, "house rolls found from the roll files", "year", year, "max_roll", n)
	return n, nil
}

// filterNewHouseRolls returns the roll numbers 1..maxRoll to fetch: those not stored yet, and
// those stored with fewer member rows than their totals (written before #455 made each roll
// call one commit, by a run that stopped part way). It also returns how many are the latter.
func (s *Service) filterNewHouseRolls(ctx context.Context, congressNum, session, maxRoll int) ([]int, int) {
	allRolls := make([]int, 0, maxRoll)
	for i := 1; i <= maxRoll; i++ {
		allRolls = append(allRolls, i)
	}
	return s.filterStoredVotes(ctx, congressNum, chamberHouse, allRolls, func(n int) string {
		return rollcall.HouseID(congressNum, session, n)
	})
}

// filterNewSenateVotes returns the vote numbers to fetch: those not stored yet, and those
// stored with fewer member rows than the roll call's totals (a senator couldn't be placed), so
// they're fetched again once the mapping exists. It also returns how many are the latter.
func (s *Service) filterNewSenateVotes(ctx context.Context, congressNum, session int, voteNums []int) ([]int, int) {
	return s.filterStoredVotes(ctx, congressNum, chamberSenate, voteNums, func(n int) string {
		return rollcall.SenateID(congressNum, session, n)
	})
}

// filterStoredVotes keeps the roll calls in nums that aren't stored or are stored incomplete
// (IncompleteVoteIDs), and counts the latter. A forced sync keeps them all; so does a failed
// check of which are stored.
func (s *Service) filterStoredVotes(
	ctx context.Context, congressNum int, chamber string, nums []int, voteID func(int) string,
) ([]int, int) {
	if s.forceSync {
		return nums, 0
	}

	voteIDs := make([]string, 0, len(nums))
	for _, n := range nums {
		voteIDs = append(voteIDs, voteID(n))
	}

	existing, err := s.store.ExistingVoteIDs(ctx, voteIDs)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to check existing votes, fetching all", "chamber", chamber, "error", err)
		return nums, 0
	}
	incompleteIDs, err := s.store.IncompleteVoteIDs(ctx, congressNum, chamber)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to check incomplete votes", "chamber", chamber, "error", err)
	}
	incomplete := make(map[string]bool, len(incompleteIDs))
	for _, id := range incompleteIDs {
		incomplete[id] = true
	}

	var fetch []int
	refetch := 0
	for _, n := range nums {
		id := voteID(n)
		switch {
		case !existing[id]:
			fetch = append(fetch, n)
		case incomplete[id]:
			fetch = append(fetch, n)
			refetch++
		}
	}
	return fetch, refetch
}

func (s *Service) fetchAndStoreHouseVote(
	ctx context.Context, congressNum, session, year, roll int,
) error {
	url := fmt.Sprintf(s.feeds.houseVoteURL, year, roll)

	data, err := s.httpGet(ctx, url)
	if err != nil {
		return err
	}

	result, err := xmlparse.ParseHouseVote(data)
	if err != nil {
		return fmt.Errorf("parse house vote: %w", err)
	}

	gotSession, err := rollcall.ParseOrdinalSession(result.Session)
	if err != nil {
		return fmt.Errorf("%w: %w", errSessionMismatch, err)
	}
	if err = checkSession(congressNum, session, result.Congress, gotSession); err != nil {
		return err
	}

	return s.storeHouseVote(ctx, congressNum, session, year, roll, result)
}

// storeHouseVote writes a parsed House roll call and its members' positions.
func (s *Service) storeHouseVote(
	ctx context.Context, congressNum, session, year, roll int, result *xmlparse.HouseVoteResult,
) error {
	voteID := rollcall.HouseID(congressNum, session, roll)

	voteDate, err := time.Parse(houseDateFormat, result.VoteDate)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to parse house vote date, using fallback",
			"vote_date", result.VoteDate, "error", err)
		voteDate = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	}

	// The bill may not be synced yet; congressional_votes.bill_id has no foreign key, and
	// the link is in place once it is.
	billID := optionalID(billIDFromRef(congressNum, result.LegisNum))

	vote := repository.CongressionalVoteRow{
		ID:         voteID,
		BillID:     billID,
		Congress:   congressNum,
		Chamber:    chamberHouse,
		Session:    &session,
		RollNumber: &roll,
		VoteDate:   voteDate,
		Question:   &result.Question,
		Result:     &result.Result,
		Yeas:       &result.YeaTotal,
		Nays:       &result.NayTotal,
		Present:    &result.PresentTotal,
		NotVoting:  &result.NotVotingTotal,
	}

	positions := make([]memberPosition, 0, len(result.Votes))
	for _, v := range result.Votes {
		if v.MemberID != "" {
			positions = append(positions, memberPosition{memberID: v.MemberID, vote: v.Vote})
		}
	}
	if err = s.storeRollCall(ctx, vote, positions, 0); err != nil {
		return err
	}
	markVotedBill(ctx, billID)

	return nil
}

// senateCounts counts what one session's Senate vote sync stored.
type senateCounts struct {
	stored, linked int
	// unmatched counts the distinct senators in the stored votes that couldn't be placed.
	unmatched int
}

// syncSenateVotes stores a session's new and incomplete Senate roll calls.
func (s *Service) syncSenateVotes(
	ctx context.Context, congressNum, session int, senators *senatorResolver,
) (senateCounts, error) {
	voteNums, err := s.discoverSenateVoteNumbers(ctx, congressNum, session)
	if errors.Is(err, errNotFound) {
		s.logger.InfoContext(ctx, "no senate roll calls yet", "congress", congressNum, "session", session)
		return senateCounts{}, nil
	}
	if err != nil {
		return senateCounts{}, fmt.Errorf("discover senate votes: %w", err)
	}

	s.logger.InfoContext(ctx, "discovered senate votes", "congress", congressNum, "session", session,
		"count", len(voteNums))

	discovered := len(voteNums)
	voteNums, incomplete := s.filterNewSenateVotes(ctx, congressNum, session, voteNums)
	s.logger.InfoContext(ctx, "senate votes to fetch", "total_discovered", discovered, "new", len(voteNums)-incomplete,
		"incomplete", incomplete)

	counter := &syncCounter{}
	linked := &syncCounter{}
	var (
		mu        sync.Mutex
		unmatched = map[string]bool{}
	)

	workerPool(ctx, voteNums, senateVoteWorkers, func(ctx context.Context, voteNum int) {
		stored, fetchErr := s.fetchAndStoreSenateVote(ctx, congressNum, session, voteNum, senators)
		countItem(ctx, fetchErr)
		if fetchErr != nil {
			s.logger.WarnContext(ctx, "senate vote fetch failed", "vote_number", voteNum, "error", fetchErr)
			return
		}
		counter.inc()
		if stored.linked {
			linked.inc()
		}
		mu.Lock()
		for _, lis := range stored.unmatched {
			unmatched[lis] = true
		}
		mu.Unlock()
	})

	return senateCounts{stored: counter.get(), linked: linked.get(), unmatched: len(unmatched)}, nil
}

func (s *Service) discoverSenateVoteNumbers(ctx context.Context, congressNum, session int) ([]int, error) {
	url := fmt.Sprintf(s.feeds.senateIndexURL, congressNum, session)
	body, err := s.httpGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch senate vote index: %w", err)
	}

	matches := senateVoteNumberRe.FindAllStringSubmatch(string(body), -1)
	nums := make([]int, 0, len(matches))
	for _, m := range matches {
		n, parseErr := strconv.Atoi(m[1])
		if parseErr == nil {
			nums = append(nums, n)
		}
	}

	return nums, nil
}

// senateVoteStored is what storing one Senate roll call found.
type senateVoteStored struct {
	// linked is whether the vote is linked to a bill.
	linked bool
	// unmatched lists the LIS IDs of the senators in the vote that couldn't be placed.
	unmatched []string
}

// fetchAndStoreSenateVote fetches and stores one Senate roll call.
func (s *Service) fetchAndStoreSenateVote(
	ctx context.Context, congressNum, session, voteNum int, senators *senatorResolver,
) (senateVoteStored, error) {
	url := fmt.Sprintf(s.feeds.senateVoteURL, congressNum, session, congressNum, session, voteNum)

	data, err := s.httpGet(ctx, url)
	if err != nil {
		return senateVoteStored{}, err
	}

	result, err := xmlparse.ParseSenateVote(data)
	if err != nil {
		return senateVoteStored{}, fmt.Errorf("parse senate vote: %w", err)
	}
	if err = checkSession(congressNum, session, result.Congress, result.Session); err != nil {
		return senateVoteStored{}, err
	}

	return s.storeSenateVote(ctx, congressNum, session, voteNum, result, senators)
}

// storeSenateVote writes a parsed Senate roll call and the positions of the senators it can
// place.
func (s *Service) storeSenateVote(
	ctx context.Context, congressNum, session, voteNum int, result *xmlparse.SenateVoteResult,
	senators *senatorResolver,
) (senateVoteStored, error) {
	voteID := rollcall.SenateID(congressNum, session, voteNum)

	// Some files pad the time with a second space ("July 1, 2025,  11:56 AM").
	voteDate, err := time.Parse(senateDateFormat, strings.Join(strings.Fields(result.VoteDate), " "))
	if err != nil {
		s.logger.WarnContext(ctx, "failed to parse senate vote date, using fallback",
			"vote_date", result.VoteDate, "error", err)
		voteDate = time.Now().Truncate(time.Hour)
	}

	billID := optionalID(senateBillID(congressNum, result))

	vote := repository.CongressionalVoteRow{
		ID:         voteID,
		BillID:     billID,
		Congress:   congressNum,
		Chamber:    chamberSenate,
		Session:    &session,
		RollNumber: &voteNum,
		VoteDate:   voteDate,
		Question:   &result.Question,
		Result:     &result.Result,
		Yeas:       &result.YeaTotal,
		Nays:       &result.NayTotal,
		Present:    &result.PresentTotal,
		NotVoting:  &result.AbsentTotal,
	}

	stored := senateVoteStored{linked: billID != nil}
	positions := make([]memberPosition, 0, len(result.Votes))
	for _, v := range result.Votes {
		if v.MemberID == "" {
			continue
		}

		memberID := senators.resolve(ctx, v)
		if memberID == "" {
			stored.unmatched = append(stored.unmatched, v.MemberID)
			continue
		}

		positions = append(positions, memberPosition{memberID: memberID, vote: v.Vote})
	}
	if err = s.storeRollCall(ctx, vote, positions, len(stored.unmatched)); err != nil {
		return senateVoteStored{}, err
	}
	markVotedBill(ctx, billID)

	return stored, nil
}

// markVotedBill marks the bill a stored roll call names, if it names one: the bill's page lists
// its roll calls.
func markVotedBill(ctx context.Context, billID *string) {
	if billID != nil {
		markBill(ctx, *billID)
	}
}

// memberPosition is one member's position on a roll call, as the clerk recorded it.
type memberPosition struct {
	memberID string
	vote     string
}

// storeRollCall writes the roll call's row and its members' positions in one commit
// (StoreRollCall), each position in its canonical form (model.NormalizeMemberVote). Values that
// aren't a yes/no/present/absent, such as Speaker candidates, are stored trimmed and counted in
// the vote's log line, as are unmatched senators: members the vote named that couldn't be placed
// (always 0 for the House, whose XML carries bioguide IDs). On an error nothing is stored, so
// the next sync fetches the roll call again.
func (s *Service) storeRollCall(
	ctx context.Context, vote repository.CongressionalVoteRow, positions []memberPosition, unmatched int,
) error {
	unrecognized := 0
	members := make([]repository.MemberVoteRow, 0, len(positions))
	for _, p := range positions {
		value, ok := model.NormalizeMemberVote(p.vote)
		if !ok {
			unrecognized++
		}
		members = append(members, repository.MemberVoteRow{MemberID: p.memberID, Vote: value})
	}
	if err := s.store.StoreRollCall(ctx, vote, members); err != nil {
		return fmt.Errorf("store roll call: %w", err)
	}

	level := slog.LevelDebug
	if unrecognized > 0 || unmatched > 0 {
		level = slog.LevelInfo
	}
	linkedBill := ""
	if vote.BillID != nil {
		linkedBill = *vote.BillID
	}
	s.logger.Log(ctx, level, "vote stored", "vote_id", vote.ID, "bill_id", linkedBill,
		"members", len(positions), "unrecognized_values", unrecognized, "unmatched_senators", unmatched)
	return nil
}

// senateBillID returns the bill a Senate roll call is about: the measure itself, or for an
// amendment vote the measure it amends. Nominations, treaties and votes with no document
// have none.
func senateBillID(congressNum int, r *xmlparse.SenateVoteResult) (string, bool) {
	congress := r.Document.Congress
	if congress == 0 {
		congress = congressNum
	}

	if id, ok := billIDFromRef(congress, r.Document.Type+" "+r.Document.Number); ok {
		return id, true
	}
	switch compactRef(r.Document.Type) {
	case "samdt", "hamdt":
		return billIDFromRef(congress, r.AmendmentToDocument)
	default:
		return "", false
	}
}

// billIDFromRef turns a measure reference in either chamber's spelling ("H R 144",
// "H.R. 1", "S.J.Res. 82", "H CON RES 14") into a bill ID such as "hr-119-144". It accepts
// only the eight bill and resolution types; nominations ("PN25-37"), amendments
// ("S.Amdt. 3937"), "QUORUM" and empty references return false.
func billIDFromRef(congress int, ref string) (string, bool) {
	m := billRefRe.FindStringSubmatch(compactRef(ref))
	if m == nil || congress <= 0 {
		return "", false
	}

	if !knownBillType(m[1]) {
		return "", false
	}

	num, err := strconv.Atoi(m[2])
	if err != nil || num <= 0 {
		return "", false
	}

	return fmt.Sprintf("%s-%d-%d", m[1], congress, num), true
}

// compactRef lowercases a reference and drops its dots and whitespace: "S.Con.Res. 7"
// becomes "sconres7".
func compactRef(ref string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(ref, ".", "")), ""))
}

// optionalID returns a pointer to id when ok, and nil otherwise.
func optionalID(id string, ok bool) *string {
	if !ok {
		return nil
	}
	return &id
}

// checkSession returns errSessionMismatch unless a vote document's congress and session are the
// ones requested.
func checkSession(wantCongress, wantSession, gotCongress, gotSession int) error {
	if gotCongress != wantCongress || gotSession != wantSession {
		return fmt.Errorf("%w: requested congress %d session %d, XML says congress %d session %d",
			errSessionMismatch, wantCongress, wantSession, gotCongress, gotSession)
	}
	return nil
}

// httpGet fetches a vote XML or bill text URL with the upstream client and returns the body
// bytes. A 404 wraps errNotFound. The upstream client returns final HTTP errors as
// *upstream.StatusError; the status checks below cover a plain client too.
func (s *Service) httpGet(ctx context.Context, url string) ([]byte, error) {
	return s.httpGetMax(ctx, url, 0)
}

// httpGetMax is httpGet with the body capped at maxBytes; a longer body is an error. Zero
// means no cap.
func (s *Service) httpGetMax(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.http.Do(req)
	if se, ok := errors.AsType[*upstream.StatusError](err); ok && se.Status == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %w", errNotFound, se)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: HTTP 404 for %s", errNotFound, url)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	if maxBytes <= 0 {
		return io.ReadAll(resp.Body)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", url, maxBytes)
	}
	return body, nil
}
