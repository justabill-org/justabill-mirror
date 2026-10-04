package verceldrain_test

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/justabill-org/justabill/api/internal/verceldrain"
	"github.com/justabill-org/justabill/obs/obstest"
	jsemconv "github.com/justabill-org/justabill/obs/semconv"
)

// fakeClock is a settable clock, safe for concurrent reads.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// The fixtures' first line is at 1790000000000 ms; the clock starts 2 minutes after it.
func setupClock(t *testing.T) (http.Handler, *obstest.Telemetry, *fakeClock) {
	t.Helper()

	tel := obstest.New(t)
	clock := &fakeClock{now: time.UnixMilli(1790000000000).Add(2 * time.Minute)}

	rc, err := verceldrain.New(verceldrain.Config{
		DrainSecret: drainSecret, WebhookSecret: webhookSecret, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	return rc.Handler(), tel, clock
}

func drainOK(t *testing.T, h http.Handler, body []byte) {
	t.Helper()

	if rec := post(t, h, "/v1/drain", drainSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// sums returns an int counter's points as a map from their sorted attributes to their values.
func sums(t *testing.T, tel *obstest.Telemetry, name string) map[string]int64 {
	t.Helper()

	m, ok := tel.Metric(t, name)
	if !ok {
		return map[string]int64{}
	}

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s is %T, want an int64 sum", name, m.Data)
	}

	got := map[string]int64{}
	for _, dp := range sum.DataPoints {
		got[key(dp.Attributes)] += dp.Value
	}

	return got
}

// key renders an attribute set as "k=v,k=v", sorted by key.
func key(set attribute.Set) string {
	parts := make([]string, 0, set.Len())
	for _, kv := range set.ToSlice() {
		parts = append(parts, string(kv.Key)+"="+kv.Value.String())
	}

	return strings.Join(parts, ",")
}

func failureKey(kind string, status int) string {
	k := "http.response.status_code=" + strconv.Itoa(status) + ","
	if status == 0 {
		k = ""
	}

	return k + string(jsemconv.VercelFailureKindKey) + "=" + kind
}

func assertCounts(t *testing.T, what string, got, want map[string]int64) {
	t.Helper()

	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", what, got, want)

		return
	}

	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s[%s] = %d, want %d (all: %v)", what, k, got[k], v, got)
		}
	}
}

func TestFailuresCountOncePerRequest(t *testing.T) {
	h, tel, clock := setupClock(t)

	// Three failing lines of one timed-out request in one batch, then a fourth in a later batch.
	drainOK(t, h, fixture(t, "failure-timeout.ndjson"))
	clock.Advance(5 * time.Minute)
	drainOK(t, h, fixture(t, "failure-timeout-late.ndjson"))
	drainOK(t, h, fixture(t, "failure-crash.ndjson"))
	drainOK(t, h, fixture(t, "failure-background.ndjson"))

	assertCounts(t, "failures", sums(t, tel, jsemconv.VercelFunctionFailuresName), map[string]int64{
		failureKey(jsemconv.VercelFailureKindTimeout, 504):    1,
		failureKey(jsemconv.VercelFailureKindCrash, 500):      1,
		failureKey(jsemconv.VercelFailureKindBackground, 500): 1,
	})

	// After the 15-minute window the same request ID counts again.
	clock.Advance(15 * time.Minute)
	drainOK(t, h, fixture(t, "failure-timeout-late.ndjson"))

	if got := sums(t, tel, jsemconv.VercelFunctionFailuresName)[failureKey(jsemconv.VercelFailureKindTimeout, 504)]; got != 2 {
		t.Errorf("timeouts after the window = %d, want 2", got)
	}
}

func TestFailuresOnlyOursLinesCountButAreNotForwarded(t *testing.T) {
	h, tel, _ := setupClock(t)

	drainOK(t, h, fixture(t, "failure-ours.ndjson"))

	assertCounts(t, "failures", sums(t, tel, jsemconv.VercelFunctionFailuresName), map[string]int64{
		failureKey(jsemconv.VercelFailureKindError, 500): 1,
	})

	if logs := tel.Logs(); len(logs) != 0 {
		t.Errorf("forwarded %d of our own lines, want none", len(logs))
	}
}

func TestDrainCensus(t *testing.T) {
	h, tel, _ := setupClock(t)

	drainOK(t, h, fixture(t, "failure-timeout.ndjson"))
	drainOK(t, h, fixture(t, "failure-background.ndjson"))
	drainOK(t, h, fixture(t, "drain.ndjson"))
	drainOK(t, h, fixture(t, "drain.json"))

	census := func(source, typ, class, outcome string) string {
		return strings.Join([]string{
			string(jsemconv.VercelLineOutcomeKey) + "=" + outcome,
			string(jsemconv.VercelLogTypeKey) + "=" + typ,
			string(jsemconv.VercelSourceKey) + "=" + source,
			string(jsemconv.VercelStatusClassKey) + "=" + class,
		}, ",")
	}

	assertCounts(t, "drain.lines", sums(t, tel, jsemconv.VercelDrainLinesName), map[string]int64{
		// failure-timeout: three -1 lines of one request (one is ours), counted once.
		census("lambda", "stdout", "-1", "failure"): 1,
		census("lambda", "stderr", "-1", "repeat"):  1,
		census("lambda", "report", "-1", "repeat"):  1,
		// failure-background: a failed and a fine revalidation.
		census("lambda", "stderr", "5xx", "failure"): 1,
		// …and in drain.ndjson, a warning line.
		census("lambda", "stdout", "2xx", "forwarded"): 2,
		// drain.ndjson: a timeout line, two of our lines without request IDs and a build line.
		census("lambda", "stderr", "-1", "failure"):     1,
		census("lambda", "stdout", "none", "ours"):      2,
		census("build", "command", "none", "forwarded"): 1,
		// drain.json: an edge line and a lambda line without a type or request ID.
		census("edge", "_OTHER", "none", "forwarded"):       1,
		census("lambda", "_OTHER", "none", "no_request_id"): 1,
	})
}

func TestDrainLag(t *testing.T) {
	h, tel, _ := setupClock(t)

	// The clock is 2 minutes after the first line: lags of 120, 90 and 89.999 s, by lambda.
	drainOK(t, h, fixture(t, "failure-timeout.ndjson"))

	m, ok := tel.Metric(t, jsemconv.VercelDrainLagName)
	if !ok {
		t.Fatal("no lag histogram")
	}

	hist, ok := m.Data.(metricdata.Histogram[float64])
	if !ok || len(hist.DataPoints) != 1 {
		t.Fatalf("lag = %#v, want one histogram point", m.Data)
	}

	dp := hist.DataPoints[0]
	if got := key(dp.Attributes); got != string(jsemconv.VercelSourceKey)+"=lambda" {
		t.Errorf("lag attributes = %s", got)
	}

	if dp.Count != 3 {
		t.Errorf("lag count = %d, want 3", dp.Count)
	}

	want := []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800}
	if fmt.Sprint(dp.Bounds) != fmt.Sprint(want) {
		t.Errorf("bounds = %v, want %v", dp.Bounds, want)
	}

	// 90 s lands in (60, 120] twice, and 120 s too (upper bounds are inclusive).
	if got := dp.BucketCounts[5]; got != 3 {
		t.Errorf("(60, 120] bucket = %d, want 3 (all: %v)", got, dp.BucketCounts)
	}
}

func TestDrainForwardsLogType(t *testing.T) {
	h, tel, _ := setupClock(t)

	drainOK(t, h, fixture(t, "failure-crash.ndjson"))

	logs := tel.Logs()
	if len(logs) != 2 {
		t.Fatalf("got %d records, want 2", len(logs))
	}

	for i, want := range []string{"fatal", "report"} {
		if got := attrs(&logs[i])[string(jsemconv.VercelLogTypeKey)].AsString(); got != want {
			t.Errorf("record %d log type = %q, want %q", i, got, want)
		}
	}
}

func TestDrainMetricsHoldNoRequestData(t *testing.T) {
	h, tel, _ := setupClock(t)

	for _, f := range []string{
		"failure-timeout.ndjson", "failure-crash.ndjson", "failure-background.ndjson",
		"failure-ours.ndjson", "drain.ndjson", "drain.json",
	} {
		drainOK(t, h, fixture(t, f))
	}

	var all strings.Builder

	for _, sm := range tel.Metrics(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			for _, set := range attributeSets(m.Data) {
				all.WriteString(key(set) + "\n")
			}
		}
	}

	allowed := map[attribute.Key]bool{
		jsemconv.VercelFailureKindKey: true, "http.response.status_code": true,
		jsemconv.VercelSourceKey: true, jsemconv.VercelLogTypeKey: true,
		jsemconv.VercelStatusClassKey: true, jsemconv.VercelLineOutcomeKey: true,
	}

	for line := range strings.SplitSeq(strings.TrimSpace(all.String()), "\n") {
		for kv := range strings.SplitSeq(line, ",") {
			k, _, _ := strings.Cut(kv, "=")
			if !allowed[attribute.Key(k)] {
				t.Errorf("metric attribute %q isn't on the allowlist", k)
			}
		}
	}

	for _, leak := range []string{"req-", "643af4e3", "/bills", "justabill.vercel.app", "203.0.113", "timed out", "dpl_"} {
		if strings.Contains(all.String(), leak) {
			t.Errorf("metrics leak %q:\n%s", leak, all.String())
		}
	}
}

// attributeSets returns the attribute sets of a metric's points.
func attributeSets(data metricdata.Aggregation) []attribute.Set {
	var sets []attribute.Set

	switch d := data.(type) {
	case metricdata.Sum[int64]:
		for _, dp := range d.DataPoints {
			sets = append(sets, dp.Attributes)
		}
	case metricdata.Histogram[float64]:
		for _, dp := range d.DataPoints {
			sets = append(sets, dp.Attributes)
		}
	}

	return sets
}

// TestFailuresConcurrentBatches posts the same failures from many goroutines, as two batches of
// one request can reach a replica at once: each request still counts once (run with -race).
func TestFailuresConcurrentBatches(t *testing.T) {
	h, tel, _ := setupClock(t)

	const requests, posts = 20, 8

	var batch bytes.Buffer
	for i := range requests {
		fmt.Fprintf(&batch, `{"source":"lambda","timestamp":1790000000000,"type":"stderr",`+
			`"requestId":"req-%d","statusCode":-1,"message":"boom","proxy":{"statusCode":502}}`+"\n", i)
	}

	var wg sync.WaitGroup
	for range posts {
		wg.Go(func() {
			if rec := post(t, h, "/v1/drain", drainSecret, batch.Bytes()); rec.Code != http.StatusOK {
				t.Errorf("status = %d", rec.Code)
			}
		})
	}

	wg.Wait()

	assertCounts(t, "failures", sums(t, tel, jsemconv.VercelFunctionFailuresName), map[string]int64{
		failureKey(jsemconv.VercelFailureKindCrash, 502): requests,
	})

	lines := sums(t, tel, jsemconv.VercelDrainLinesName)

	var total int64
	for _, v := range lines {
		total += v
	}

	if total != requests*posts {
		t.Errorf("census lines = %d, want %d", total, requests*posts)
	}
}
