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
}

func TestFailureSetCapDropsOldest(t *testing.T) {
	const limit = 3

	fs := newFailureSet(limit)
	now := time.Unix(1_790_000_000, 0)

	for i := range limit + 2 {
		if !fs.first("id-"+strconv.Itoa(i), now) {
			t.Fatalf("id-%d not first", i)
		}
	}

	if got := fs.size(); got != limit {
		t.Errorf("size = %d, want the cap %d", got, limit)
	}

	// The two oldest were dropped, so they count again; the newest are still remembered.
	if fs.first("id-4", now) {
		t.Error("the newest ID was dropped")
	}

	if !fs.first("id-0", now) {
		t.Error("the oldest ID wasn't dropped")
	}
}
