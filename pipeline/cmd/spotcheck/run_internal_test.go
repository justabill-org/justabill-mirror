package main

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// officialServer serves the official lists and roll calls from xmlparse's testdata: House 2025
// has rolls 1 and 2, Senate session 1 has five votes, and session 2 has none yet in either
// chamber.
func officialServer(t *testing.T) sources {
	t.Helper()
	files := map[string]string{
		"/evs/2025/roll001.xml":          "house_2025_roll001_quorum.xml",
		"/evs/2025/roll002.xml":          "house_2025_roll002_speaker.xml",
		"/vote1191/vote_119_1_00001.xml": "real_senate_vote001.xml",
		"/vote1191/vote_119_1_00072.xml": "senate_vote_119_1_00072.xml",
		"/vote1191/vote_119_1_00372.xml": "senate_vote_119_1_00372.xml",
		"/vote1191/vote_119_1_00607.xml": "senate_vote_119_1_00607.xml",
		"/vote1191/vote_119_1_00616.xml": "senate_vote_119_1_00616.xml",
		"/vote1191/vote_119_1_00999.xml": "senate_vote_119_1_00072.xml", // the wrong file
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/evs/2025/index.asp":
			_, _ = w.Write([]byte(`<a href="?rollnumber=2">2</a><a href="?rollnumber=1">1</a>`))
			return
		case "/menu_119_1.xml":
			_, _ = w.Write([]byte(`<vote_summary><votes><vote><vote_number>00616</vote_number></vote>` +
				`<vote><vote_number>00607</vote_number></vote><vote><vote_number>00372</vote_number></vote>` +
				`<vote><vote_number>00072</vote_number></vote><vote><vote_number>00001</vote_number></vote>` +
				`</votes></vote_summary>`))
			return
		}
		name := files[r.URL.Path]
		if name == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(readTestdata(t, name))
	}))
	t.Cleanup(srv.Close)
	return sources{
		houseIndexURL: srv.URL + "/evs/%d/index.asp",
		houseRollURL:  srv.URL + "/evs/%d/roll%03d.xml",
		senateMenuURL: srv.URL + "/menu_%d_%d.xml",
		senateRollURL: srv.URL + "/vote%d%d/vote_%d_%d_%05d.xml",
		client:        srv.Client(),
	}
}

// fakeReader serves stored roll calls from a map and a fixed coverage.
type fakeReader struct {
	votes    map[string]*repository.StoredRollCall
	coverage *repository.StoredCoverage
	err      error
}

func (f *fakeReader) StoredRollCall(_ context.Context, id string) (*repository.StoredRollCall, error) {
	return f.votes[id], f.err
}

func (f *fakeReader) Coverage(context.Context, int) (*repository.StoredCoverage, error) {
	return f.coverage, f.err
}

func (f *fakeReader) Close() {}

type fixedCounts int

func (n fixedCounts) billCount(context.Context, int, string) (int, error) { return int(n), nil }

// loadedReader stores every testdata roll call the way the pipeline would, so they all match.
func loadedReader(t *testing.T, src sources) *fakeReader {
	t.Helper()
	f := &fakeReader{votes: map[string]*repository.StoredRollCall{}}
	house := stratum{Chamber: chamberHouse, Congress: 119, Session: 1}
	senate := stratum{Chamber: chamberSenate, Congress: 119, Session: 1}
	for _, n := range []int{1, 2} {
		rc, err := src.fetchRoll(t.Context(), house, n)
		if err != nil {
			t.Fatal(err)
		}
		f.votes[house.voteID(n)] = storedFrom(rc, nil)
	}
	for _, n := range []int{1, 72, 372, 607, 616} {
		rc, err := src.fetchRoll(t.Context(), senate, n)
		if err != nil {
			t.Fatal(err)
		}
		lis := map[string]string{}
		for id := range rc.Positions {
			lis[id] = "B" + id
		}
		f.votes[senate.voteID(n)] = storedFrom(rc, lis)
	}
	bills := map[string]int{}
	for _, t := range billTypes() {
		bills[t] = 50
	}
	f.coverage = &repository.StoredCoverage{
		BillsByType: bills,
		RollCalls: []repository.StoredRollCallCount{
			{Chamber: chamberHouse, Session: 1, Distinct: 2, Highest: 2},
			{Chamber: chamberSenate, Session: 1, Distinct: 5, Highest: 616},
		},
		MembersByChamber: map[string]int{chamberHouse: 441, chamberSenate: 100},
	}
	return f
}

func newTestChecker(t *testing.T, opts options) (*checker, *fakeReader) {
	t.Helper()
	src := officialServer(t)
	reader := loadedReader(t, src)
	return &checker{
		opts: opts, reader: reader, src: src, counter: fixedCounts(50), cmp: newComparer(),
		now: time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC),
	}, reader
}

func TestCheckerAllMatch(t *testing.T) {
	c, _ := newTestChecker(t, options{congress: 119, rolls: 20, coverage: true, seed: 7})
	var out bytes.Buffer
	r, err := c.run(t.Context(), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !r.passed() || verdict(r) != nil {
		t.Fatalf("failed:\n%s", out.String())
	}
	for _, want := range []string{
		"house-119-s1-roll001: matches",
		"house-119-s1-roll002: matches, 3 members",
		"senate-119-s1-vote00616: matches",
		"House 2026 session 2: no roll calls yet",
		"roll calls, Senate 2025 session 1: 5 of 5",
		"RESULT: PASS (0 failed, 0 warnings)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// The design's fixture test: one member's stored position flipped from Yea to Nay is reported
// as a mismatch, and the command exits non-zero.
func TestCheckerFlippedMember(t *testing.T) {
	c, reader := newTestChecker(t, options{congress: 119, rolls: 20, seed: 7})
	stored := reader.votes["senate-119-s1-vote00072"]
	for i, p := range stored.Positions {
		if p.LISID == "S428" {
			if p.Vote != model.VoteYea {
				t.Fatalf("fixture: S428 stored %q, want Yea", p.Vote)
			}
			stored.Positions[i].Vote = model.VoteNay
		}
	}

	var out bytes.Buffer
	r, err := c.run(t.Context(), &out)
	if err != nil {
		t.Fatal(err)
	}
	if r.failures != 1 || !errors.Is(verdict(r), errChecksFailed) {
		t.Errorf("failures = %d, verdict %v; want 1 and errChecksFailed", r.failures, verdict(r))
	}
	for _, want := range []string{
		"FAIL  senate-119-s1-vote00072: 1 differences",
		`member S428: official "Yea", stored "Nay"`,
		"RESULT: FAIL (1 failed, 0 warnings)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestCheckerFailures(t *testing.T) {
	c, reader := newTestChecker(t, options{congress: 119, rolls: 20, coverage: true, seed: 7})
	delete(reader.votes, "house-119-s1-roll002")
	reader.coverage.RollCalls[0].Distinct = 1
	// A menu entry whose file is for another vote.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(
			[]byte(`<vote_summary><votes><vote><vote_number>999</vote_number></vote></votes></vote_summary>`),
		)
	}))
	defer srv.Close()
	c.src.senateMenuURL = srv.URL + "/%d/%d"

	var out bytes.Buffer
	r, err := c.run(t.Context(), &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FAIL  house-119-s1-roll002: 1 differences",
		"not stored",
		"FAIL  senate-119-s1-vote00999: can't read the official XML",
		"is congress 119 session 1 roll 72",
		"FAIL  roll calls, House 2025 session 1: 1 of 2",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if r.passed() {
		t.Error("passed, want failed")
	}
}

func TestCheckerErrors(t *testing.T) {
	c, reader := newTestChecker(t, options{congress: 119, rolls: 4, coverage: true, seed: 7})
	reader.err = errors.New("spanner down")
	if _, err := c.run(t.Context(), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "spanner down") {
		t.Errorf("reader error: err = %v", err)
	}

	c, _ = newTestChecker(t, options{congress: 119, rolls: 4, seed: 7})
	c.src.houseIndexURL = "http://127.0.0.1:0/%d"
	if _, err := c.run(
		t.Context(),
		&bytes.Buffer{},
	); err == nil ||
		!strings.Contains(err.Error(), "House 2025 session 1") {
		t.Errorf("list error: err = %v", err)
	}
}

func TestRunNeedsSomethingToCheck(t *testing.T) {
	if err := run(t.Context(), nil, options{congress: 119}, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "nothing to check") {
		t.Errorf("err = %v", err)
	}
}

func TestSample(t *testing.T) {
	seq := func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i + 1
		}
		return out
	}
	strata := []stratum{{Numbers: seq(362)}, {Numbers: seq(3)}, {Numbers: seq(659)}, {}}
	rng := func() *rand.Rand { return rand.New(rand.NewPCG(42, 42)) }

	picks := sample(strata, 20, rng())
	sizes := []int{len(picks[0]), len(picks[1]), len(picks[2]), len(picks[3])}
	if !slices.Equal(sizes, []int{5, 3, 5, 0}) {
		t.Errorf("sizes = %v, want [5 3 5 0]", sizes)
	}
	for i, p := range picks {
		if !slices.IsSorted(p) || len(slices.Compact(slices.Clone(p))) != len(p) {
			t.Errorf("stratum %d picks %v: want sorted and distinct", i, p)
		}
	}
	if again := sample(strata, 20, rng()); !slices.EqualFunc(picks, again, slices.Equal) {
		t.Errorf("same seed, different sample: %v vs %v", picks, again)
	}

	// 22 over four strata: the first two take the remainder.
	picks = sample([]stratum{{Numbers: seq(9)}, {Numbers: seq(9)}, {Numbers: seq(9)}, {Numbers: seq(9)}}, 22, rng())
	if got := []int{len(picks[0]), len(picks[1]), len(picks[2]), len(picks[3])}; !slices.Equal(got, []int{6, 6, 5, 5}) {
		t.Errorf("sizes = %v, want [6 6 5 5]", got)
	}
	if picks = sample(nil, 20, rng()); len(picks) != 0 {
		t.Errorf("no strata: %v", picks)
	}
}

func TestConfigKey(t *testing.T) {
	if got := configKey("congress-api-key"); got != "congress_api_key" {
		t.Errorf("configKey = %q", got)
	}
}
