package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	gosync "sync"
	"testing"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
)

// voteStore records what the vote sync and the Senate LIS ID sync write. Any other
// PipelineStore method panics through the nil embedded interface.
type voteStore struct {
	repository.PipelineStore

	mu          gosync.Mutex
	existing    map[string]bool
	incomplete  []string // IncompleteVoteIDs' answer
	votes       []repository.CongressionalVoteRow
	memberVotes map[string][]string  // vote ID -> member IDs
	success     *repository.SyncRun  // the last RecordSyncSuccess
	failures    []repository.SyncRun // every RecordSyncFailure

	roster   []string          // ListMemberIDs
	lis      map[string]string // LIS ID -> member, as members.lis_id holds them
	senators map[int][]repository.SenatorName
	lisSets  []string // every SetMemberLisID, as "bioguide=lis"

	existingErr error // ExistingVoteIDs
	lisErr      error // LISLookup
}

// newVoteStore knows the stored LIS IDs of the three senators in the trimmed Senate fixtures.
func newVoteStore(existing ...string) *voteStore {
	st := &voteStore{
		existing:    map[string]bool{},
		memberVotes: map[string][]string{},
		lis:         map[string]string{"S428": "A000382", "S354": "B001230", "S429": "B001299"},
	}
	for _, id := range existing {
		st.existing[id] = true
	}
	return st
}

func (f *voteStore) ExistingVoteIDs(_ context.Context, ids []string) (map[string]bool, error) {
	if f.existingErr != nil {
		return nil, f.existingErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	found := map[string]bool{}
	for _, id := range ids {
		if f.existing[id] {
			found[id] = true
		}
	}
	return found, nil
}

func (f *voteStore) UpsertCongressionalVote(_ context.Context, v repository.CongressionalVoteRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.votes = append(f.votes, v)
	return nil
}

func (f *voteStore) StoreRollCall(
	_ context.Context, v repository.CongressionalVoteRow, members []repository.MemberVoteRow,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.votes = append(f.votes, v)
	for _, m := range members {
		f.memberVotes[v.ID] = append(f.memberVotes[v.ID], m.MemberID)
	}
	return nil
}

// IncompleteVoteIDs answers with the incomplete IDs of the chamber's roll calls.
func (f *voteStore) IncompleteVoteIDs(_ context.Context, _ int, chamber string) ([]string, error) {
	var ids []string
	for _, id := range f.incomplete {
		if strings.HasPrefix(id, strings.ToLower(chamber)+"-") {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (f *voteStore) LISLookup(context.Context) (map[string]string, error) {
	if f.lisErr != nil {
		return nil, f.lisErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.lis), nil
}

func (f *voteStore) ListMemberIDs(context.Context) ([]string, error) {
	return f.roster, nil
}

func (f *voteStore) ListSenators(_ context.Context, congress int) ([]repository.SenatorName, error) {
	return f.senators[congress], nil
}

// SetMemberLisID moves the ID the way the Spanner transaction does.
func (f *voteStore) SetMemberLisID(_ context.Context, bioguideID, lisID string) (repository.LisIDChange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.roster, bioguideID) {
		return repository.LisIDChange{}, repository.ErrMemberNotFound
	}
	var change repository.LisIDChange
	for lis, member := range f.lis {
		if member == bioguideID {
			change.Previous = lis
			delete(f.lis, lis)
		}
	}
	if holder := f.lis[lisID]; holder != "" && holder != bioguideID {
		change.TakenFrom = holder
	}
	f.lis[lisID] = bioguideID
	f.lisSets = append(f.lisSets, bioguideID+"="+lisID)
	return change, nil
}

// GetSyncState reports a step that has never succeeded, so the members step never skips.
func (f *voteStore) GetSyncState(_ context.Context, step string, congress int) (*repository.SyncStateRow, error) {
	return &repository.SyncStateRow{Step: step, Congress: congress}, nil
}

func (f *voteStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.success = &run
	return nil
}

func (f *voteStore) RecordSyncFailure(_ context.Context, run repository.SyncRun) error {
	f.failures = append(f.failures, run)
	return nil
}

func (f *voteStore) voteIDs() []string {
	ids := make([]string, 0, len(f.votes))
	for _, v := range f.votes {
		ids = append(ids, v.ID)
	}
	slices.Sort(ids)
	return ids
}

// senateMembersPath is where voteFeedServer serves the Senate member feed.
const senateMembersPath = "/senators/cvc_member_data.xml"

// voteFeedServer stands in for the House Clerk and Senate LIS. Each session serves rolls
// 1..n, all with the same recorded document. A year or session missing from a map is a 404.
// The Senate member feed is the xmlparse fixture unless senateMembers is set.
type voteFeedServer struct {
	houseRolls    map[int]int    // year -> highest roll
	houseDocs     map[int]string // year -> testdata file served for every roll
	senateVotes   map[int]int    // session -> highest vote number
	senateDocs    map[int]string // session -> testdata file served for every vote
	senateMembers []byte
	membersStatus int // the member feed's HTTP status, when not 200

	mu      gosync.Mutex
	fetched []string
}

func (f *voteFeedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.fetched = append(f.fetched, r.URL.Path)
	f.mu.Unlock()

	var year, roll, dir, congress, session, number int
	switch {
	case scan(r.URL.Path, "/evs/%d/index.asp", &year):
		if n, ok := f.houseRolls[year]; ok {
			writeList(w, n, func(i int) string {
				return fmt.Sprintf("<A HREF=\"vote.asp?year=%d&rollnumber=%d\">%d</A>\n", year, i, i)
			})
			return
		}
	case scan(r.URL.Path, "/evs/%d/roll%d.xml", &year, &roll):
		if name, ok := f.houseDocs[year]; ok && roll <= f.houseRolls[year] {
			writeFixture(w, name)
			return
		}
	case scan(r.URL.Path, "/menu/vote_menu_%d_%d.xml", &congress, &session):
		if n, ok := f.senateVotes[session]; ok {
			writeList(w, n, func(i int) string {
				return fmt.Sprintf("<vote><vote_number>%05d</vote_number></vote>\n", i)
			})
			return
		}
	case scan(r.URL.Path, "/votes/vote%d/vote_%d_%d_%d.xml", &dir, &congress, &session, &number):
		if name, ok := f.senateDocs[session]; ok && number <= f.senateVotes[session] {
			writeFixture(w, name)
			return
		}
	case r.URL.Path == senateMembersPath:
		f.serveSenateMembers(w)
		return
	}
	http.NotFound(w, r)
}

func (f *voteFeedServer) serveSenateMembers(w http.ResponseWriter) {
	if f.membersStatus != 0 {
		w.WriteHeader(f.membersStatus)
		return
	}
	body := f.senateMembers
	if body == nil {
		var err error
		if body, err = os.ReadFile(
			filepath.Join("..", "xmlparse", "testdata", "senators", "cvc_member_data.xml"),
		); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	_, _ = w.Write(body)
}

func scan(path, format string, args ...any) bool {
	n, err := fmt.Sscanf(path, format, args...)
	return err == nil && n == len(args)
}

// writeList writes one line per item, newest first, like the real index pages.
func writeList(w http.ResponseWriter, n int, line func(int) string) {
	for i := n; i >= 1; i-- {
		_, _ = w.Write([]byte(line(i)))
	}
}

func writeFixture(w http.ResponseWriter, name string) {
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
}

func (f *voteFeedServer) paths(substr string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.fetched {
		if strings.Contains(p, substr) {
			out = append(out, p)
		}
	}
	return out
}

// the119th serves three roll calls per chamber in each session of the 119th, from the right
// recorded documents.
func the119th() *voteFeedServer {
	return &voteFeedServer{
		houseRolls:  map[int]int{2025: 3, 2026: 3},
		houseDocs:   map[int]string{2025: "house-2025-roll001.xml", 2026: "house-2026-roll001.xml"},
		senateVotes: map[int]int{1: 3, 2: 3},
		senateDocs:  map[int]string{1: "senate-119-1-vote00001.xml", 2: "senate-119-2-vote00001.xml"},
	}
}

// voteService returns a Service whose vote feeds are feeds. Warnings and errors go to the
// returned buffer.
func voteService(t *testing.T, store repository.PipelineStore, feeds *voteFeedServer) (*Service, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(feeds)
	t.Cleanup(srv.Close)
	var warnings bytes.Buffer
	return &Service{
		store:  store,
		http:   srv.Client(),
		logger: slog.New(slog.NewTextHandler(&warnings, &slog.HandlerOptions{Level: slog.LevelWarn})),
		feeds: voteFeeds{
			houseIndexURL:    srv.URL + "/evs/%d/index.asp",
			houseVoteURL:     srv.URL + "/evs/%d/roll%03d.xml",
			senateIndexURL:   srv.URL + "/menu/vote_menu_%d_%d.xml",
			senateVoteURL:    srv.URL + "/votes/vote%d%d/vote_%d_%d_%05d.xml",
			senateMembersURL: srv.URL + senateMembersPath,
		},
	}, &warnings
}

// voteID rebuilds a stored vote's ID from its chamber, session and roll number.
func voteID(v repository.CongressionalVoteRow) string {
	if v.Chamber == chamberHouse {
		return rollcall.HouseID(v.Congress, *v.Session, *v.RollNumber)
	}
	return rollcall.SenateID(v.Congress, *v.Session, *v.RollNumber)
}

func session1IDs() []string {
	var ids []string
	for n := 1; n <= 3; n++ {
		ids = append(ids, rollcall.HouseID(119, 1, n), rollcall.SenateID(119, 1, n))
	}
	return ids
}

func TestSyncVotes_Session2IsNotSkipped(t *testing.T) {
	store := newVoteStore(session1IDs()...)
	feeds := the119th()
	s, warnings := voteService(t, store, feeds)

	if err := s.SyncVotes(t.Context(), 119, []int{1, 2}); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"house-119-s2-roll001", "house-119-s2-roll002", "house-119-s2-roll003",
		"senate-119-s2-vote00001", "senate-119-s2-vote00002", "senate-119-s2-vote00003",
	}
	if got := store.voteIDs(); !slices.Equal(got, want) {
		t.Fatalf("stored votes %v, want %v", got, want)
	}
	for _, v := range store.votes {
		if v.Session == nil || *v.Session != 2 || v.RollNumber == nil || v.ID != voteID(v) {
			t.Errorf("vote %s has session %v, roll %v", v.ID, v.Session, v.RollNumber)
		}
		if v.Chamber == chamberHouse && v.VoteDate.Format("2006-01-02") != "2026-01-06" {
			t.Errorf("vote %s dated %s, want the 2026 document's 2026-01-06", v.ID, v.VoteDate)
		}
		if len(store.memberVotes[v.ID]) != 3 {
			t.Errorf("vote %s has member votes %v, want the fixture's 3", v.ID, store.memberVotes[v.ID])
		}
	}
	if got := feeds.paths("/evs/2025/roll"); len(got) != 0 {
		t.Errorf("fetched session 1 House rolls %v that were already stored", got)
	}
	if store.success == nil || store.success.ItemsSynced != 6 {
		t.Errorf("sync success %+v, want 6 items", store.success)
	}
	if warnings.Len() != 0 {
		t.Errorf("unexpected warnings:\n%s", warnings.String())
	}
}

func TestSyncVotes_ForceSession2WritesOnlySession2(t *testing.T) {
	store := newVoteStore(session1IDs()...)
	feeds := the119th()
	s, _ := voteService(t, store, feeds)
	s.SetForceSync(true)

	if err := s.SyncVotes(t.Context(), 119, []int{2}); err != nil {
		t.Fatal(err)
	}

	if len(store.votes) != 6 {
		t.Fatalf("stored %d votes, want 6", len(store.votes))
	}
	for _, v := range store.votes {
		if !strings.Contains(v.ID, "-s2-") || strings.Contains(v.ID, "-s1-") {
			t.Errorf("forced session 2 sync wrote %s", v.ID)
		}
	}
	if got := feeds.paths("/evs/2025/"); len(got) != 0 {
		t.Errorf("session 2 sync fetched the 2025 House folder: %v", got)
	}
}

func TestSyncVotes_RejectsDocumentsFromAnotherSession(t *testing.T) {
	store := newVoteStore()
	feeds := the119th()
	// A wrong year folder or menu: session 1's documents served at session 2's URLs.
	feeds.houseDocs[2026] = "house-2025-roll001.xml"
	feeds.senateDocs[2] = "senate-119-1-vote00001.xml"
	s, warnings := voteService(t, store, feeds)

	if err := s.SyncVotes(t.Context(), 119, []int{2}); err != nil {
		t.Fatal(err)
	}

	if len(store.votes) != 0 || len(store.memberVotes) != 0 {
		t.Fatalf("stored votes %v and member votes %v from mismatched documents", store.voteIDs(), store.memberVotes)
	}
	if got := strings.Count(warnings.String(), errSessionMismatch.Error()); got != 6 {
		t.Errorf("logged %d session mismatches, want 6:\n%s", got, warnings.String())
	}
}

func TestFetchAndStoreVote_SessionMismatchError(t *testing.T) {
	store := newVoteStore()
	feeds := the119th()
	feeds.houseDocs[2026] = "house-2025-roll001.xml"
	feeds.senateDocs[2] = "senate-119-1-vote00001.xml"
	s, _ := voteService(t, store, feeds)

	err := s.fetchAndStoreHouseVote(t.Context(), 119, 2, 2026, 1)
	if !errors.Is(err, errSessionMismatch) {
		t.Errorf("house error = %v, want errSessionMismatch", err)
	}
	if err != nil &&
		!strings.Contains(err.Error(), "requested congress 119 session 2, XML says congress 119 session 1") {
		t.Errorf("house error %q doesn't name both sessions", err)
	}

	senators := fixtureSenators()
	_, err = s.fetchAndStoreSenateVote(t.Context(), 119, 2, 1, senators)
	if !errors.Is(err, errSessionMismatch) {
		t.Errorf("senate error = %v, want errSessionMismatch", err)
	}

	// A different congress is rejected too.
	if _, err = s.fetchAndStoreSenateVote(t.Context(), 118, 1, 1, senators); !errors.Is(err, errSessionMismatch) {
		t.Errorf("senate error for congress 118 = %v, want errSessionMismatch", err)
	}
	if len(store.votes) != 0 {
		t.Errorf("stored %v", store.voteIDs())
	}
}

func TestSyncVotes_SessionWithoutIndexIsEmpty(t *testing.T) {
	store := newVoteStore()
	feeds := the119th()
	delete(feeds.houseRolls, 2026)
	delete(feeds.senateVotes, 2)
	s, warnings := voteService(t, store, feeds)

	if err := s.SyncVotes(t.Context(), 119, []int{2}); err != nil {
		t.Fatalf("SyncVotes = %v, want no error for a session with no roll calls yet", err)
	}

	if len(store.votes) != 0 {
		t.Errorf("stored %v", store.voteIDs())
	}
	if store.success == nil || store.success.ItemsSynced != 0 {
		t.Errorf("sync success %+v, want 0 items", store.success)
	}
	if warnings.Len() != 0 {
		t.Errorf("a 404 index should log at info, got warnings:\n%s", warnings.String())
	}
}

// A roll-call list that can't be read (not a 404) fails the step, after the other session
// has synced, instead of being logged and recorded as a success (#80, finding F10).
func TestSyncVotes_IndexErrorsFailTheStep(t *testing.T) {
	store := newVoteStore()
	feeds := the119th()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "2026") || strings.HasSuffix(r.URL.Path, "_2.xml") {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		feeds.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	s, _ := voteService(t, store, feeds)
	s.feeds.houseIndexURL = srv.URL + "/evs/%d/index.asp"
	s.feeds.senateIndexURL = srv.URL + "/menu/vote_menu_%d_%d.xml"

	err := s.SyncVotes(t.Context(), 119, []int{1, 2})
	if err == nil {
		t.Fatal("SyncVotes = nil, want the session 2 House and Senate errors")
	}
	for _, want := range []string{"session 2: house: discover house rolls", "senate: discover senate votes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q doesn't include %q", err, want)
		}
	}
	if len(store.votes) != 6 {
		t.Errorf("stored %v, want session 1's six votes", store.voteIDs())
	}
	if store.success != nil {
		t.Errorf("recorded success %+v for a failed step", store.success)
	}
	if len(store.failures) != 1 || store.failures[0].Step != stepVotes || store.failures[0].Error != err.Error() {
		t.Errorf("failures = %+v, want one votes failure with the step's error", store.failures)
	}
}

func TestSyncVotes_NoSessions(t *testing.T) {
	store := newVoteStore()
	feeds := the119th()
	s, _ := voteService(t, store, feeds)

	if err := s.SyncVotes(t.Context(), 120, nil); err != nil {
		t.Fatal(err)
	}
	if got := feeds.paths("/"); len(got) != 0 {
		t.Errorf("fetched %v for a congress with no sessions", got)
	}
	if store.success == nil || store.success.ItemsSynced != 0 {
		t.Errorf("sync success %+v, want 0 items", store.success)
	}
}

// The Clerk's index pages are gone (2026-10-01): with the index a 404, the House rolls are found
// from the roll files themselves, all of them are stored, and a year with no roll 1 is still
// "no roll calls yet".
func TestSyncHouseVotesWithoutTheIndex(t *testing.T) {
	store := newVoteStore()
	feeds := the119th()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var year, roll int
		switch {
		case strings.HasSuffix(r.URL.Path, "/index.asp"):
			http.NotFound(w, r)
			return
		case scan(r.URL.Path, "/evs/%d/roll%d.xml", &year, &roll) && year == 2025 && roll > feeds.houseRolls[2025]:
			// The Clerk's answer for a roll that doesn't exist: 200 and an error document.
			_, _ = fmt.Fprintf(w, `<xml>Error sanitizing file "roll%03d.xml". Please try again.</xml>`, roll)
			return
		}
		feeds.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	s, _ := voteService(t, store, feeds)
	s.feeds.houseIndexURL = srv.URL + "/evs/%d/index.asp"
	s.feeds.houseVoteURL = srv.URL + "/evs/%d/roll%03d.xml"

	got, err := s.discoverHouseRollCount(t.Context(), 2025)
	if err != nil || got != feeds.houseRolls[2025] {
		t.Fatalf("discoverHouseRollCount(2025) = %d, %v; want %d", got, err, feeds.houseRolls[2025])
	}
	if _, err = s.discoverHouseRollCount(t.Context(), 2027); !errors.Is(err, errNotFound) {
		t.Errorf("a year with no rolls: err = %v, want errNotFound", err)
	}
	n, err := s.syncHouseVotes(t.Context(), 119, 1, 2025)
	if err != nil || n != feeds.houseRolls[2025] {
		t.Errorf("syncHouseVotes = %d, %v; want %d stored", n, err, feeds.houseRolls[2025])
	}
}

// With the Senate's member feed down, the vote sync warns and resolves senators by the LIS IDs
// already stored.
func TestSyncVotes_MemberFeedDownUsesStoredLISIDs(t *testing.T) {
	store := newVoteStore()
	feeds := the119th()
	feeds.membersStatus = http.StatusServiceUnavailable
	s, warnings := voteService(t, store, feeds)

	if err := s.SyncVotes(t.Context(), 119, []int{1}); err != nil {
		t.Fatal(err)
	}
	if want := "senate lis ids not refreshed; using the stored ones"; !strings.Contains(warnings.String(), want) {
		t.Errorf("warnings lack %q:\n%s", want, warnings)
	}
	for n := 1; n <= 3; n++ {
		id := rollcall.SenateID(119, 1, n)
		if got := store.memberVotes[id]; len(got) != 3 {
			t.Errorf("%s member votes = %v, want the fixture's 3 by stored LIS ID", id, got)
		}
	}
}

// When the check of which roll calls are stored fails, every roll call is fetched again.
func TestSyncVotes_ExistingCheckFailureFetchesAll(t *testing.T) {
	store := newVoteStore(session1IDs()...)
	store.existingErr = errFakeStore
	s, warnings := voteService(t, store, the119th())

	if err := s.SyncVotes(t.Context(), 119, []int{1}); err != nil {
		t.Fatal(err)
	}
	if got, want := store.voteIDs(), slices.Sorted(slices.Values(session1IDs())); !slices.Equal(got, want) {
		t.Errorf("stored votes %v, want every session 1 roll call %v", got, want)
	}
	if want := "failed to check existing votes, fetching all"; !strings.Contains(warnings.String(), want) {
		t.Errorf("warnings lack %q:\n%s", want, warnings)
	}
}

// Without the stored LIS IDs, Senate votes can't be attributed: the step fails and stores nothing.
func TestSyncVotes_LISLookupFailureFailsTheStep(t *testing.T) {
	store := newVoteStore()
	store.lisErr = errFakeStore
	s, _ := voteService(t, store, the119th())

	err := s.SyncVotes(t.Context(), 119, []int{1})
	if err == nil || !strings.Contains(err.Error(), "read lis ids") {
		t.Fatalf("SyncVotes = %v, want the LIS lookup failure", err)
	}
	if len(store.votes) != 0 || len(store.failures) != 1 {
		t.Errorf("stored %v with failures %+v; want nothing stored and one failure", store.voteIDs(), store.failures)
	}
}
