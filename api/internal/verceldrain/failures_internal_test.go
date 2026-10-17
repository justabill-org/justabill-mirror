package verceldrain

import (
	"strconv"
	"testing"
	"time"

	jsemconv "github.com/justabill-org/justabill/obs/semconv"
)

func TestClassify(t *testing.T) {
	const noProxy = 1000 // a sentinel: the line has no proxy object

	tests := []struct {
		name       string
		l, p       int
		wantKind   string
		wantStatus int
	}{
		{"background crash", -1, -1, jsemconv.VercelFailureKindBackground, 0},
		{"background 5xx", 500, -1, jsemconv.VercelFailureKindBackground, 500},
		{"background 503", 503, -1, jsemconv.VercelFailureKindBackground, 503},
		{"background 2xx is fine", 200, -1, "", 0},
		{"background 4xx is fine", 404, -1, "", 0},
		{"background without L is fine", 0, -1, "", 0},
		{"crash timed out", -1, 504, jsemconv.VercelFailureKindTimeout, 504},
		{"crash with the client's 500", -1, 500, jsemconv.VercelFailureKindCrash, 500},
		{"crash with the client's 502", -1, 502, jsemconv.VercelFailureKindCrash, 502},
		{"crash without a proxy", -1, noProxy, jsemconv.VercelFailureKindCrash, 0},
		{"crash with an invalid P", -1, 0, jsemconv.VercelFailureKindCrash, 0},
		{"crash with a 200 from the proxy", -1, 200, jsemconv.VercelFailureKindCrash, 200},
		{"504 with a valid L", 504, 504, jsemconv.VercelFailureKindTimeout, 504},
		{"504 from L alone", 504, noProxy, jsemconv.VercelFailureKindTimeout, 504},
		{"P wins over L", 200, 504, jsemconv.VercelFailureKindTimeout, 504},
		{"5xx", 500, 500, jsemconv.VercelFailureKindError, 500},
		{"5xx from L, invalid P", 502, 0, jsemconv.VercelFailureKindError, 502},
		{"5xx from L, no proxy", 503, noProxy, jsemconv.VercelFailureKindError, 503},
		{"proxy 200 hides L's 500", 500, 200, "", 0},
		{"4xx", 404, 404, "", 0},
		{"200", 200, 200, "", 0},
		{"invalid L and P", 0, 0, "", 0},
		{"invalid L, too high", 700, noProxy, "", 0},
		// The shapes measured in production (design 473, "Measured"): a request line has no
		// statusCode, only proxy.statusCode; a stderr line has neither.
		{"request line 500", 0, 500, jsemconv.VercelFailureKindError, 500},
		{"request line 502", 0, 502, jsemconv.VercelFailureKindError, 502},
		{"request line 504", 0, 504, jsemconv.VercelFailureKindTimeout, 504},
		{"request line 200", 0, 200, "", 0},
		{"request line 404", 0, 404, "", 0},
		{"stderr line without a status", 0, noProxy, "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := &drainLine{StatusCode: tt.l}
			if tt.p != noProxy {
				line.Proxy = &drainHTTP{StatusCode: tt.p}
			}

			kind, status, ok := classify(line)
			if ok != (tt.wantKind != "") || kind != tt.wantKind || status != tt.wantStatus {
				t.Errorf("classify(L=%d, P=%d) = %q, %d, %v; want %q, %d",
					tt.l, tt.p, kind, status, ok, tt.wantKind, tt.wantStatus)
			}
		})
	}
}

func TestStatusClass(t *testing.T) {
	for code, want := range map[int]string{
		-1: "-1", 0: "none", 99: "none", 100: "1xx", 204: "2xx", 302: "3xx", 404: "4xx", 599: "5xx", 600: "none",
	} {
		if got := statusClass(code); got != want {
			t.Errorf("statusClass(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestLogTypeAndSource(t *testing.T) {
	for in, want := range map[string]string{
		"stdout": "stdout", "edge-function-invocation": "edge-function-invocation", "fatal": "fatal",
		"": "_OTHER", "STDOUT": "_OTHER", "something-new": "_OTHER",
	} {
		if got := logType(in); got != want {
			t.Errorf("logType(%q) = %q, want %q", in, got, want)
		}
	}

	for in, want := range map[string]string{"lambda": "lambda", "build": "build", "redirect": "redirect", "x": "_OTHER"} {
		if got := knownSource(in); got != want {
			t.Errorf("knownSource(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFailureSetExpires(t *testing.T) {
	fs := newFailureSet(maxFailures)
	start := time.Unix(1_790_000_000, 0)

	steps := []struct {
		id    string
		at    time.Duration
		first bool
		size  int
	}{
		{"a", 0, true, 1},
		{"a", failureTTL - time.Second, false, 1}, // a repeat inside the window
		{"b", failureTTL - 30*time.Second, true, 2},
		// a's window is over, but the last sweep was under a minute ago: a counts again, and its
		// first entry stays queued.
		{"a", failureTTL + 10*time.Second, true, 2},
		// The sweep drops a's stale entry without forgetting its new count.
		{"c", failureTTL + 40*time.Second, true, 3},
		{"a", failureTTL + time.Minute, false, 3},
		// Long after: the sweep empties the set before c is added.
		{"c", 3 * failureTTL, true, 1},
	}

	for i, s := range steps {
		if got := fs.first(s.id, start.Add(s.at)); got != s.first {
			t.Errorf("step %d: first(%q) = %v, want %v", i, s.id, got, s.first)
		}

		if got := fs.size(); got != s.size {
			t.Errorf("step %d: size = %d, want %d", i, got, s.size)
		}
	}

	// The last sweep emptied the slice: the head starts over rather than pointing past its end.
	if fs.head != 0 || len(fs.order) != 1 {
		t.Errorf("after emptying: head = %d, len(order) = %d; want 0 and 1 (c)", fs.head, len(fs.order))
	}
}

func TestFailureSetSweepsStaleEntries(t *testing.T) {
	const recounted = 50

	fs := newFailureSet(maxFailures)
	start := time.Unix(1_790_000_000, 0)

	for i := range recounted {
		fs.first("id-"+strconv.Itoa(i), start)
	}

	// A sweep just before the IDs expire, so the next one is a minute away.
	fs.first("x", start.Add(failureTTL-30*time.Second))

	// Expired but not yet swept: each ID counts again, and its first entry stays queued.
	for i := range recounted {
		if !fs.first("id-"+strconv.Itoa(i), start.Add(failureTTL+time.Second)) {
			t.Fatalf("expired id-%d didn't count again", i)
		}
	}

	if got, want := len(fs.order)-fs.head, 2*recounted+1; got != want {
		t.Fatalf("queued entries = %d, want %d (stale, x, re-counted)", got, want)
	}

	// The next sweep drops the stale entries without forgetting the re-counts.
	fs.first("new", start.Add(failureTTL+31*time.Second))

	if got, want := fs.size(), recounted+2; got != want {
		t.Errorf("size = %d, want %d", got, want)
	}

	if got := len(fs.order) - fs.head; got != len(fs.seen) {
		t.Errorf("queued entries = %d, want one per remembered ID (%d)", got, len(fs.seen))
	}

	for i := range recounted {
		if fs.first("id-"+strconv.Itoa(i), start.Add(failureTTL+32*time.Second)) {
			t.Errorf("the sweep forgot re-counted id-%d", i)
		}
	}
}

func TestFailureSetCapDropsOldest(t *testing.T) {
	for _, limit := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) { checkCapDropsOldest(t, limit) })
	}
}

// checkCapDropsOldest fills a set capped at limit well past its cap, then checks it remembers
// only the newest IDs.
func checkCapDropsOldest(t *testing.T, limit int) {
	t.Helper()

	fs := newFailureSet(limit)
	now := time.Unix(1_790_000_000, 0)
	n := 4*limit + 3 // enough inserts past the cap to compact several times

	for i := range n {
		if !fs.first("id-"+strconv.Itoa(i), now) {
			t.Fatalf("id-%d not first", i)
		}
	}

	if got := fs.size(); got != limit {
		t.Errorf("size = %d, want the cap %d", got, limit)
	}

	// The newest limit IDs are still remembered; the one before them was dropped.
	for i := n - limit; i < n; i++ {
		if fs.first("id-"+strconv.Itoa(i), now) {
			t.Errorf("id-%d, one of the newest %d, was dropped", i, limit)
		}
	}

	if !fs.first("id-"+strconv.Itoa(n-limit-1), now) {
		t.Errorf("id-%d, the newest one past the cap, wasn't dropped", n-limit-1)
	}

	if !fs.first("id-0", now) {
		t.Error("the oldest ID wasn't dropped")
	}
}

func TestFailureSetReleasesDroppedIDs(t *testing.T) {
	fs := newFailureSet(4)
	now := time.Unix(1_790_000_000, 0)

	for i := range 50 {
		fs.first("id-"+strconv.Itoa(i), now)

		// Only the queued entries may reference a request ID: a dropped one, before the head or in
		// the spare capacity after a compaction, would keep its string alive.
		for j, e := range fs.order[:cap(fs.order)] {
			if (j < fs.head || j >= len(fs.order)) && e != (seenEntry{}) {
				t.Fatalf("after id-%d: slot %d outside [%d, %d) holds %q", i, j, fs.head, len(fs.order), e.id)
			}
		}
	}
}

// filledFailureSet is a set at maxFailures, and spare request IDs not in it.
func filledFailureSet(spare int) (*failureSet, []string, time.Time) {
	fs := newFailureSet(maxFailures)
	now := time.Unix(1_790_000_000, 0)

	for i := range maxFailures {
		fs.first("old-"+strconv.Itoa(i), now)
	}

	ids := make([]string, spare)
	for i := range ids {
		ids[i] = "new-" + strconv.Itoa(i)
	}

	return fs, ids, now
}

func TestFailureSetEvictsWithoutCopying(t *testing.T) {
	const runs = 1000

	fs, ids, now := filledFailureSet(runs + 1) // AllocsPerRun calls once more to warm up
	next := 0

	// The IDs are built beforehand, so only the set's own allocations count. Copying the queue
	// on every eviction is one per call; an occasional map or slice growth rounds down to 0.
	allocs := testing.AllocsPerRun(runs, func() {
		if !fs.first(ids[next], now) {
			t.Fatalf("%s not first", ids[next])
		}
		next++
	})
	if allocs >= 1 {
		t.Errorf("first at the cap allocates %v times per call, want 0", allocs)
	}

	if got := fs.size(); got != maxFailures {
		t.Errorf("size = %d, want the cap %d", got, maxFailures)
	}
}

// BenchmarkFailureSetFirstAtCap records a new failure in a full set (#917): the cost of each
// failing request during a storm. Run it with `task bench BENCH=FailureSet`.
func BenchmarkFailureSetFirstAtCap(b *testing.B) {
	// Twice the cap, so an ID has been dropped before it comes round again: every call is new.
	fs, ids, now := filledFailureSet(2 * maxFailures)
	next := 0

	b.ReportAllocs()
	for b.Loop() {
		fs.first(ids[next%len(ids)], now)
		next++
	}
}
