package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
)

const (
	httpTimeout = 30 * time.Second
	// maxBody caps one download; the largest roll-call XML is well under 1 MB.
	maxBody = 16 << 20
)

// errNotFound is an HTTP 404. For a session's roll-call list it means the session has none yet.
var errNotFound = errors.New("not found")

// sources fetches the official record: the House Clerk's and the Senate's roll-call lists and
// XML files. Tests point the URL templates at an httptest server.
type sources struct {
	houseIndexURL string // year
	houseRollURL  string // year, roll
	senateMenuURL string // congress, session
	senateRollURL string // congress, session, congress, session, vote number
	client        *http.Client
}

func defaultSources() sources {
	return sources{
		houseIndexURL: "https://clerk.house.gov/evs/%d/index.asp",
		houseRollURL:  "https://clerk.house.gov/evs/%d/roll%03d.xml",
		senateMenuURL: "https://www.senate.gov/legislative/LIS/roll_call_lists/vote_menu_%d_%d.xml",
		senateRollURL: "https://www.senate.gov/legislative/LIS/roll_call_votes/vote%d%d/vote_%d_%d_%05d.xml",
		client:        &http.Client{Timeout: httpTimeout},
	}
}

// stratum is one chamber and session's roll calls, numbered as the official list has them.
type stratum struct {
	Chamber  string
	Congress int
	Session  int
	Numbers  []int // ascending
}

func (st stratum) String() string {
	return fmt.Sprintf("%s %d session %d", st.Chamber, rollcall.Year(st.Congress, st.Session), st.Session)
}

// highest is the largest roll number in the stratum, 0 when it has none.
func (st stratum) highest() int {
	if len(st.Numbers) == 0 {
		return 0
	}
	return st.Numbers[len(st.Numbers)-1]
}

// voteID is the ID the pipeline stores roll call n of this stratum under.
func (st stratum) voteID(n int) string {
	if st.Chamber == chamberHouse {
		return rollcall.HouseID(st.Congress, st.Session, n)
	}
	return rollcall.SenateID(st.Congress, st.Session, n)
}

// strata lists the roll calls of each chamber in each of the congress's two sessions, House
// first. A session that hasn't started, or has no roll calls yet, is an empty stratum.
func (s sources) strata(ctx context.Context, congress int, now time.Time) ([]stratum, error) {
	started := rollcall.Sessions(congress, now)
	var out []stratum
	for _, chamber := range []string{chamberHouse, chamberSenate} {
		for session := 1; session <= 2; session++ {
			st := stratum{Chamber: chamber, Congress: congress, Session: session}
			if slices.Contains(started, session) {
				nums, err := s.rollNumbers(ctx, chamber, congress, session)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", st, err)
				}
				st.Numbers = nums
			}
			out = append(out, st)
		}
	}
	return out, nil
}

// rollNumbers returns a session's roll numbers in ascending order: 1 to the highest on the
// clerk's index in the House, the vote menu's numbers in the Senate.
func (s sources) rollNumbers(ctx context.Context, chamber string, congress, session int) ([]int, error) {
	if chamber == chamberHouse {
		highest, err := s.highestHouseRoll(ctx, rollcall.Year(congress, session))
		if err != nil {
			return nil, err
		}
		nums := make([]int, highest)
		for i := range nums {
			nums[i] = i + 1
		}
		return nums, nil
	}
	body, err := s.get(ctx, fmt.Sprintf(s.senateMenuURL, congress, session))
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	nums, err := parseSenateMenu(body)
	if err != nil {
		return nil, err
	}
	slices.Sort(nums)
	return slices.Compact(nums), nil
}

// maxHouseRolls bounds the probe in highestHouseRoll: the House has never held 1,000 roll calls in
// a year.
const maxHouseRolls = 2000

// highestHouseRoll is a year's highest House roll: from the Clerk's index page, or, now that those
// pages are gone (a 404 for 2024 and 2025 on 2026-10-01), by probing the roll files
// (rollcall.HighestRoll). 0 means no roll calls yet.
func (s sources) highestHouseRoll(ctx context.Context, year int) (int, error) {
	body, err := s.get(ctx, fmt.Sprintf(s.houseIndexURL, year))
	if err != nil && !errors.Is(err, errNotFound) {
		return 0, err
	}
	if err == nil {
		if n := parseHouseIndex(body); n > 0 {
			return n, nil
		}
		// An index with no roll numbers in it is no listing either: probe.
	}
	return rollcall.HighestRoll(func(roll int) (bool, error) {
		rollBody, getErr := s.get(ctx, fmt.Sprintf(s.houseRollURL, year, roll))
		if errors.Is(getErr, errNotFound) {
			return false, nil
		}
		return getErr == nil && rollcall.IsHouseRoll(rollBody), getErr
	}, maxHouseRolls)
}

// fetchRoll downloads and parses roll call n of a stratum, and checks the file is the one asked
// for.
func (s sources) fetchRoll(ctx context.Context, st stratum, n int) (*officialRollCall, error) {
	var (
		url   string
		parse func([]byte) (*officialRollCall, error)
	)
	if st.Chamber == chamberHouse {
		url = fmt.Sprintf(s.houseRollURL, rollcall.Year(st.Congress, st.Session), n)
		parse = parseHouseRoll
	} else {
		url = fmt.Sprintf(s.senateRollURL, st.Congress, st.Session, st.Congress, st.Session, n)
		parse = parseSenateRoll
	}
	body, err := s.get(ctx, url)
	if err != nil {
		return nil, err
	}
	rc, err := parse(body)
	if err != nil {
		return nil, err
	}
	if rc.Congress != st.Congress || rc.Session != st.Session || rc.Number != n {
		return nil, fmt.Errorf("%s is congress %d session %d roll %d", url, rc.Congress, rc.Session, rc.Number)
	}
	return rc, nil
}

func (s sources) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
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
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

// sample picks total roll calls spread evenly over the strata (the first ones take any
// remainder), at random within each. A stratum with fewer roll calls than its share gives all
// it has. The picks of each stratum come back in ascending order.
func sample(strata []stratum, total int, rng *rand.Rand) [][]int {
	picks := make([][]int, len(strata))
	if len(strata) == 0 || total <= 0 {
		return picks
	}
	share, extra := total/len(strata), total%len(strata)
	for i, st := range strata {
		want := share
		if i < extra {
			want++
		}
		want = min(want, len(st.Numbers))
		chosen := make([]int, 0, want)
		for _, j := range rng.Perm(len(st.Numbers))[:want] {
			chosen = append(chosen, st.Numbers[j])
		}
		slices.Sort(chosen)
		picks[i] = chosen
	}
	return picks
}
