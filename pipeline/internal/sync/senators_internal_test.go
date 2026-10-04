package sync

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
	"github.com/justabill-org/justabill/pipeline/internal/xmlparse"
)

// fixtureSenators places the four senators in the xmlparse Senate vote fixtures by LIS ID.
func fixtureSenators() *senatorResolver {
	return newResolver(slog.New(slog.DiscardHandler), map[string]string{
		"S428": "A000382", "S354": "B001230", "S429": "B001299", "S409": "L000570",
	}, nil)
}

// the119thSenators are senators of the 119th as Congress.gov names them ("Luján", "Ben Ray").
func the119thSenators() []repository.SenatorName {
	return []repository.SenatorName{
		{BioguideID: "A000382", FirstName: "Angela D.", LastName: "Alsobrooks", State: "MD"},
		{BioguideID: "B001299", FirstName: "Jim", LastName: "Banks", State: "IN"},
		{BioguideID: "L000570", FirstName: "Ben Ray", LastName: "Luján", State: "NM"},
		{BioguideID: "S000033", FirstName: "Bernard", LastName: "Sanders", State: "VT"},
		{BioguideID: "V000128", FirstName: "Chris", LastName: "Van Hollen", State: "MD"},
		{BioguideID: "B001303", FirstName: "Lisa", LastName: "Blunt Rochester", State: "DE"},
	}
}

func TestFoldName(t *testing.T) {
	for in, want := range map[string]string{
		"Luján":            "lujan",
		"  Ben   Ray ":     "ben ray",
		"CORTEZ MASTO":     "cortez masto",
		"Blunt-Rochester":  "blunt rochester",
		"Hyde-Smith":       "hyde smith",
		"Ñandú Müller-Øst": "nandu muller øst", // ø is a letter, not a letter plus a mark
		"":                 "",
	} {
		if got := foldName(in); got != want {
			t.Errorf("foldName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchSenator(t *testing.T) {
	senators := newResolver(slog.New(slog.DiscardHandler), nil, append(the119thSenators(),
		// Two senators with one last name in one state, as the Udalls nearly were.
		repository.SenatorName{BioguideID: "X000001", FirstName: "Tom", LastName: "Twin", State: "CO"},
		repository.SenatorName{BioguideID: "X000002", FirstName: "Tim", LastName: "Twin", State: "CO"},
	)).senators

	tests := []struct {
		name               string
		first, last, state string
		want               string
	}{
		{"accent folded", "Ben", "Lujan", "NM", "L000570"},
		{"first-name prefix", "Angela", "Alsobrooks", "MD", "A000382"},
		{"nickname falls back to last name and state", "Bernie", "Sanders", "VT", "S000033"},
		{"two-word last name", "Chris", "Van Hollen", "MD", "V000128"},
		{"case and spacing", "LISA", " blunt  rochester ", "de", "B001303"},
		{"same last name and state, first name decides", "Tom", "Twin", "CO", "X000001"},
		{"same last name and state, first name matches both", "T", "Twin", "CO", ""},
		{"same last name and state, first name matches neither", "Ted", "Twin", "CO", ""},
		{"wrong state", "Ben", "Lujan", "AZ", ""},
		{"unknown name", "Pat", "Nobody", "NM", ""},
		{"no last name", "Ben", "", "NM", ""},
		{"no state", "Ben", "Lujan", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchSenator(senators, tt.first, tt.last, tt.state); got != tt.want {
				t.Errorf("matchSenator(%q, %q, %q) = %q, want %q", tt.first, tt.last, tt.state, got, tt.want)
			}
		})
	}
}

// The ID wins over the name; a name match is cached for the run; a miss is logged once per LIS
// ID and reported by unmatched.
func TestSenatorResolver(t *testing.T) {
	var logs bytes.Buffer
	r := newResolver(slog.New(slog.NewTextHandler(&logs, nil)),
		map[string]string{"S428": "A000382"}, the119thSenators())
	ctx := t.Context()

	// An LIS ID beats the name: the XML's name here would match nobody.
	renamed := xmlparse.IndividualVote{MemberID: "S428", LastName: "Renamed", State: "MD"}
	if got := r.resolve(ctx, renamed); got != "A000382" {
		t.Errorf("by ID = %q, want A000382", got)
	}
	lujan := xmlparse.IndividualVote{MemberID: "S409", FirstName: "Ben", LastName: "Lujan", State: "NM"}
	nobody := xmlparse.IndividualVote{MemberID: "S999", FirstName: "Pat", LastName: "Nobody", State: "NM"}
	for range 3 {
		if got := r.resolve(ctx, lujan); got != "L000570" {
			t.Errorf("Lujan = %q, want L000570", got)
		}
		if got := r.resolve(ctx, nobody); got != "" {
			t.Errorf("unknown senator = %q, want no match", got)
		}
	}

	if got := r.unmatched(); !slices.Equal(got, []string{"S999"}) {
		t.Errorf("unmatched = %v, want [S999]", got)
	}
	if n := strings.Count(logs.String(), "could not place senator"); n != 1 {
		t.Errorf("logged %d misses, want one per LIS ID per run:\n%s", n, logs.String())
	}
	if n := strings.Count(logs.String(), "senator placed by name"); n != 1 {
		t.Errorf("logged %d name matches, want one:\n%s", n, logs.String())
	}
}

// senateMemberVotes returns the stored Senate votes' member IDs by vote ID.
func senateMemberVotes(store *voteStore) map[string][]string {
	votes := map[string][]string{}
	for id, members := range store.memberVotes {
		if strings.HasPrefix(id, "senate-") {
			votes[id] = members
		}
	}
	return votes
}

func lisSyncService(t *testing.T, store *voteStore, feeds *voteFeedServer) (*Service, *bytes.Buffer) {
	t.Helper()
	s, _ := voteService(t, store, feeds)
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewTextHandler(&logs, nil))
	return s, &logs
}

// The feed's IDs are stored for members we have: a wrong ID the old fallback wrote moves to
// its owner, an ID already right is left alone, and a senator not in members is skipped.
func TestSyncSenateLISIDs_StoresTheFeed(t *testing.T) {
	store := newVoteStore()
	store.roster = []string{"A000382", "L000570", "S000033", "X000009"}
	store.lis = map[string]string{"S428": "A000382", "S409": "X000009"} // S409 guessed wrong
	s, logs := lisSyncService(t, store, the119th())

	if err := s.syncSenateLISIDs(t.Context()); err != nil {
		t.Fatal(err)
	}

	if want := []string{"L000570=S409", "S000033=S313"}; !slices.Equal(store.lisSets, want) {
		t.Errorf("SetMemberLisID calls = %v, want %v", store.lisSets, want)
	}
	for _, want := range []string{
		`msg="set lis_id from senate feed" bioguide_id=L000570 lis_id=S409 old_lis_id="" taken_from=X000009`,
		`msg="set lis_id from senate feed" bioguide_id=S000033 lis_id=S313`,
		"listed=5 set=2 unchanged=1 invalid=0 not_stored=2 failed=0",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs don't include %q:\n%s", want, logs.String())
		}
	}

	// A second run finds nothing to change.
	store.lisSets = nil
	if err := s.syncSenateLISIDs(t.Context()); err != nil || len(store.lisSets) != 0 {
		t.Errorf("second run = %v, set %v; want nothing set", err, store.lisSets)
	}
}

func TestSyncSenateLISIDs_SkipsMalformedIDs(t *testing.T) {
	store := newVoteStore()
	store.roster = []string{"A000382", "L000570", "S000033"}
	feeds := the119th()
	feeds.senateMembers = []byte(`<senators>
<senator lis_member_id="S4280"><name><first>A</first><last>Long</last></name><bioguideId>A000382</bioguideId></senator>
<senator lis_member_id="S409"><name><first>B</first><last>Lower</last></name><bioguideId>l000570</bioguideId></senator>
<senator lis_member_id=""><name><first>C</first><last>Empty</last></name><bioguideId>S000033</bioguideId></senator>
<senator lis_member_id=" S313 "><name><first>Bernard</first><last>Sanders</last></name><bioguideId>S000033</bioguideId></senator>
</senators>`)
	s, logs := lisSyncService(t, store, feeds)

	if err := s.syncSenateLISIDs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"S000033=S313"}; !slices.Equal(store.lisSets, want) {
		t.Errorf("SetMemberLisID calls = %v, want %v (the padded ID trimmed, the rest skipped)", store.lisSets, want)
	}
	if n := strings.Count(logs.String(), "malformed id"); n != 3 {
		t.Errorf("logged %d malformed entries, want 3:\n%s", n, logs.String())
	}
}

func TestSyncSenateLISIDs_FeedErrors(t *testing.T) {
	for name, feed := range map[string]*voteFeedServer{
		"HTTP 503": {membersStatus: http.StatusServiceUnavailable},
		"not XML":  {senateMembers: []byte("<html>")},
		"empty":    {senateMembers: []byte("<senators></senators>")},
	} {
		t.Run(name, func(t *testing.T) {
			store := newVoteStore()
			s, _ := lisSyncService(t, store, feed)
			if err := s.syncSenateLISIDs(t.Context()); err == nil {
				t.Error("syncSenateLISIDs = nil, want an error")
			}
			if len(store.lisSets) != 0 {
				t.Errorf("set %v from a bad feed", store.lisSets)
			}
		})
	}
}

func TestHTTPGetMax_RejectsOversizedBodies(t *testing.T) {
	feeds := the119th()
	feeds.senateMembers = bytes.Repeat([]byte("x"), 101)
	s, _ := voteService(t, newVoteStore(), feeds)

	if _, err := s.httpGetMax(t.Context(), s.feeds.senateMembersURL, 100); err == nil ||
		!strings.Contains(err.Error(), "larger than 100 bytes") {
		t.Errorf("err = %v, want a size error", err)
	}
	if body, err := s.httpGetMax(t.Context(), s.feeds.senateMembersURL, 101); err != nil || len(body) != 101 {
		t.Errorf("at the cap: %d bytes, %v; want the body", len(body), err)
	}
}

// The member sync ends with the Senate feed; a feed failure doesn't fail it.
func TestSyncMembers_RefreshesSenateLISIDs(t *testing.T) {
	for _, status := range []int{0, http.StatusBadGateway} {
		t.Run(fmt.Sprint("feed status ", status), func(t *testing.T) {
			store := newVoteStore()
			store.roster = []string{"L000570"}
			feeds := the119th()
			feeds.membersStatus = status
			s, _ := voteService(t, store, feeds)
			s.api = serviceWithAPI(t, store, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"members": [], "pagination": {"count": 0}}`)
			}).api

			if err := s.SyncMembers(t.Context(), 119); err != nil {
				t.Fatal(err)
			}
			wantSets := []string{"L000570=S409"}
			if status != 0 {
				wantSets = nil
			}
			if !slices.Equal(store.lisSets, wantSets) {
				t.Errorf("SetMemberLisID calls = %v, want %v", store.lisSets, wantSets)
			}
			if store.success == nil {
				t.Error("members step not recorded as a success")
			}
		})
	}
}

// A senator neither the stored IDs nor the name fallback places is dropped from each vote,
// logged once, counted, and named in sync_state.last_error; the step still succeeds.
func TestSyncVotes_UnmatchedSenators(t *testing.T) {
	store := newVoteStore()
	delete(store.lis, "S429") // Banks, in every fixture vote, with no stored ID and no name match
	feeds := the119th()
	s, logs := lisSyncService(t, store, feeds)

	if err := s.SyncVotes(t.Context(), 119, []int{1, 2}); err != nil {
		t.Fatal(err)
	}

	for id, members := range senateMemberVotes(store) {
		if len(members) != 2 || slices.Contains(members, "B001299") {
			t.Errorf("vote %s has members %v, want the two placed senators", id, members)
		}
	}
	if store.success == nil || store.success.Warning != "unmatched senators: S429" {
		t.Errorf("sync success %+v, want warning %q", store.success, "unmatched senators: S429")
	}
	out := logs.String()
	if n := strings.Count(out, "could not place senator"); n != 1 {
		t.Errorf("logged %d misses for S429, want 1:\n%s", n, out)
	}
	for _, want := range []string{
		"unmatched_senators=1", // a vote's line and each session's
		`msg="vote sync finished" congress=119 stored=12 unmatched_senators=1`, // the run total
	} {
		if !strings.Contains(out, want) {
			t.Errorf("logs don't include %q:\n%s", want, out)
		}
	}
}

// The name fallback places a senator without a stored ID for the run, from that congress's
// senators only, and doesn't write the ID back.
func TestSyncVotes_NameFallback(t *testing.T) {
	store := newVoteStore()
	delete(store.lis, "S429")
	store.senators = map[int][]repository.SenatorName{
		118: {{BioguideID: "B999999", FirstName: "Jim", LastName: "Banks", State: "IN"}},
		119: the119thSenators(),
	}
	s, _ := lisSyncService(t, store, the119th())

	if err := s.SyncVotes(t.Context(), 119, []int{2}); err != nil {
		t.Fatal(err)
	}
	for id, members := range senateMemberVotes(store) {
		if !slices.Contains(members, "B001299") || slices.Contains(members, "B999999") {
			t.Errorf("vote %s has members %v, want B001299 placed by name", id, members)
		}
	}
	if slices.ContainsFunc(store.lisSets, func(set string) bool { return strings.HasSuffix(set, "=S429") }) {
		t.Errorf("name match written back: %v", store.lisSets)
	}
	if store.success == nil || store.success.Warning != "" {
		t.Errorf("sync success %+v, want no warning", store.success)
	}
}

// A stored Senate vote with fewer member rows than its totals is fetched again; complete ones
// aren't.
func TestSyncVotes_RefetchesIncompleteSenateVotes(t *testing.T) {
	var stored []string
	for n := 1; n <= 3; n++ {
		stored = append(stored, rollcall.HouseID(119, 2, n), rollcall.SenateID(119, 2, n))
	}
	store := newVoteStore(stored...)
	store.incomplete = []string{"senate-119-s1-vote00009", rollcall.SenateID(119, 2, 2)}
	feeds := the119th()
	s, logs := lisSyncService(t, store, feeds)

	if err := s.SyncVotes(t.Context(), 119, []int{2}); err != nil {
		t.Fatal(err)
	}
	if got, want := store.voteIDs(), []string{"senate-119-s2-vote00002"}; !slices.Equal(got, want) {
		t.Errorf("stored %v, want only the incomplete vote %v", got, want)
	}
	if !strings.Contains(logs.String(), "new=0 incomplete=1") {
		t.Errorf("logs don't count the re-fetch:\n%s", logs.String())
	}
}

// A House roll call stored before #455 with only some of its members (a run that stopped part
// way through its one-row-at-a-time writes) is fetched again and stored whole.
func TestSyncVotes_RefetchesIncompleteHouseVotes(t *testing.T) {
	var stored []string
	for n := 1; n <= 3; n++ {
		stored = append(stored, rollcall.HouseID(119, 2, n), rollcall.SenateID(119, 2, n))
	}
	store := newVoteStore(stored...)
	store.incomplete = []string{"house-119-s1-roll009", rollcall.HouseID(119, 2, 3)}
	feeds := the119th()
	s, logs := lisSyncService(t, store, feeds)

	if err := s.SyncVotes(t.Context(), 119, []int{2}); err != nil {
		t.Fatal(err)
	}
	id := rollcall.HouseID(119, 2, 3)
	if got := store.voteIDs(); !slices.Equal(got, []string{id}) {
		t.Errorf("stored %v, want only the incomplete vote %s", got, id)
	}
	if len(store.memberVotes[id]) != 3 {
		t.Errorf("vote %s has member votes %v, want the fixture's 3", id, store.memberVotes[id])
	}
	if got := feeds.paths("/evs/2026/roll"); !slices.Equal(got, []string{"/evs/2026/roll003.xml"}) {
		t.Errorf("fetched House rolls %v, want only roll 3", got)
	}
	if !strings.Contains(logs.String(), "new=0 incomplete=1") {
		t.Errorf("logs don't count the re-fetch:\n%s", logs.String())
	}
}
