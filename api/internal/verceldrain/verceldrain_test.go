package verceldrain_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/justabill-org/justabill/api/internal/verceldrain"
	"github.com/justabill-org/justabill/obs/obstest"
)

const (
	drainSecret   = "drain-secret-for-tests"
	webhookSecret = "webhook-secret-for-tests"
)

// setup records telemetry and returns the receiver's handler, built after obstest.New so it
// takes the recording LoggerProvider.
func setup(t *testing.T) (http.Handler, *obstest.Telemetry) {
	t.Helper()

	tel := obstest.New(t)

	rc, err := verceldrain.New(verceldrain.Config{DrainSecret: drainSecret, WebhookSecret: webhookSecret})
	if err != nil {
		t.Fatal(err)
	}

	return rc.Handler(), tel
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)

	return hex.EncodeToString(mac.Sum(nil))
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// post sends body to path signed with secret; an empty secret sends no signature.
func post(t *testing.T, h http.Handler, path, secret string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(body))
	if secret != "" {
		req.Header.Set(verceldrain.SignatureHeader, sign(secret, body))
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func attrs(r *sdklog.Record) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		m[string(kv.Key)] = kv.Value

		return true
	})

	return m
}

// everything is a record's body and attributes as one string, for checking what must not leak.
func everything(r *sdklog.Record) string {
	var b strings.Builder

	b.WriteString(r.Body().String())
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		b.WriteString(" " + string(kv.Key) + "=" + kv.Value.String())

		return true
	})

	return b.String()
}

// assertAttrs checks each wanted attribute and that there are no others.
func assertAttrs(t *testing.T, what string, got map[string]attribute.Value, want map[string]string, extra int) {
	t.Helper()

	for k, v := range want {
		if g := got[k].String(); g != v {
			t.Errorf("%s: %s = %q, want %q", what, k, g, v)
		}
	}

	if len(got) != len(want)+extra {
		t.Errorf("%s: attributes = %v, want only the allowlist", what, got)
	}
}

// assertNoLeaks fails if any record's body or attributes contain one of the values.
func assertNoLeaks(t *testing.T, logs []sdklog.Record, values ...string) {
	t.Helper()

	for i := range logs {
		all := everything(&logs[i])
		for _, v := range values {
			if strings.Contains(all, v) {
				t.Errorf("record %d leaks %q: %s", i, v, all)
			}
		}
	}
}

func TestNewRequiresBothSecrets(t *testing.T) {
	for _, cfg := range []verceldrain.Config{
		{},
		{DrainSecret: drainSecret},
		{WebhookSecret: webhookSecret},
	} {
		if _, err := verceldrain.New(cfg); err == nil {
			t.Errorf("New(%+v) = nil error, want ErrNoSecret", cfg)
		}
	}
}

func TestSignatures(t *testing.T) {
	drain := fixture(t, "drain.ndjson")
	alert := fixture(t, "alert.json")

	tests := []struct {
		name   string
		path   string
		body   []byte
		header func(body []byte) string
		want   int
	}{
		{"drain signed", "/v1/drain", drain, func(b []byte) string { return sign(drainSecret, b) }, http.StatusOK},
		{
			"webhook signed",
			"/v1/webhook",
			alert,
			func(b []byte) string { return sign(webhookSecret, b) },
			http.StatusOK,
		},
		{"drain missing", "/v1/drain", drain, func([]byte) string { return "" }, http.StatusForbidden},
		{"webhook missing", "/v1/webhook", alert, func([]byte) string { return "" }, http.StatusForbidden},
		{"drain with the webhook secret", "/v1/drain", drain,
			func(b []byte) string { return sign(webhookSecret, b) }, http.StatusForbidden},
		{"webhook with the drain secret", "/v1/webhook", alert,
			func(b []byte) string { return sign(drainSecret, b) }, http.StatusForbidden},
		{"not hex", "/v1/drain", drain, func([]byte) string { return "not-a-signature" }, http.StatusForbidden},
		{
			"too short",
			"/v1/drain",
			drain,
			func(b []byte) string { return sign(drainSecret, b)[:20] },
			http.StatusForbidden,
		},
		{"body changed after signing", "/v1/drain", drain,
			func(b []byte) string { return sign(drainSecret, append([]byte("x"), b...)) }, http.StatusForbidden},
		// The signature is checked before parsing: garbage with a bad signature is 403, not 400.
		{
			"garbage unsigned",
			"/v1/drain",
			[]byte("{not json"),
			func([]byte) string { return "00" },
			http.StatusForbidden,
		},
		{"garbage signed", "/v1/drain", []byte("{not json"),
			func(b []byte) string { return sign(drainSecret, b) }, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, tel := setup(t)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tt.path, bytes.NewReader(tt.body))
			if sig := tt.header(tt.body); sig != "" {
				req.Header.Set(verceldrain.SignatureHeader, sig)
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}

			if tt.want != http.StatusOK && len(tel.Logs()) != 0 {
				t.Errorf("rejected request emitted %d records", len(tel.Logs()))
			}
		})
	}
}

func TestBodyTooLarge(t *testing.T) {
	h, tel := setup(t)
	body := bytes.Repeat([]byte("a"), verceldrain.MaxBodyBytes+1)

	for _, path := range []string{"/v1/drain", "/v1/webhook"} {
		if rec := post(t, h, path, drainSecret, body); rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: status = %d, want 413", path, rec.Code)
		}
	}

	if len(tel.Logs()) != 0 {
		t.Errorf("oversized bodies emitted %d records", len(tel.Logs()))
	}
}

func TestDrainForwardsScrubbedLines(t *testing.T) {
	h, tel := setup(t)

	if rec := post(t, h, "/v1/drain", drainSecret, fixture(t, "drain.ndjson")); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	logs := tel.Logs()
	// Five lines: two are our own obs JSON (dropped as duplicates), three are forwarded.
	if len(logs) != 3 {
		t.Fatalf("got %d records, want 3", len(logs))
	}

	timeout, fetch, build := &logs[0], &logs[1], &logs[2]

	if got := timeout.Body().AsString(); got != "Task timed out after 30.00 seconds" {
		t.Errorf("body = %q", got)
	}

	if timeout.Severity() != log.SeverityError || timeout.SeverityText() != "error" {
		t.Errorf("severity = %v %q, want ERROR", timeout.Severity(), timeout.SeverityText())
	}

	if want := time.UnixMilli(1790000000123); !timeout.Timestamp().Equal(want) {
		t.Errorf("timestamp = %v, want %v", timeout.Timestamp(), want)
	}

	if got := timeout.TraceID().String(); got != "1b02cd14bb8642fd092bc23f54c7ffcd" {
		t.Errorf("trace ID = %q", got)
	}

	if got := timeout.SpanID().String(); got != "f24e8631bd11faa7" {
		t.Errorf("span ID = %q", got)
	}

	a := attrs(timeout)
	assertAttrs(t, "timeout line", a, map[string]string{
		"justabill.vercel.source":     "lambda",
		"justabill.vercel.request.id": "643af4e3-975a-4cc7-9e7a-1eda11539d90",
		"deployment.id":               "dpl_233NRGRjVZX1caZrXWtz5g1TAksD",
		"deployment.environment.name": "production",
		"server.address":              "justabill.vercel.app",
		"http.request.method":         "GET",
		"url.path":                    "/bills/hr1-119",
		"justabill.vercel.log.type":   "stderr",
	}, 1)

	// statusCode -1 (crashed) falls back to the proxy's 504.
	if got := a["http.response.status_code"].AsInt64(); got != http.StatusGatewayTimeout {
		t.Errorf("http.response.status_code = %d, want 504", got)
	}

	if fetch.Severity() != log.SeverityWarn {
		t.Errorf("warning line severity = %v", fetch.Severity())
	}

	if fetch.TraceID().IsValid() {
		t.Error("a line without a trace ID got one")
	}

	if got := attrs(build)["justabill.vercel.source"].AsString(); got != "build" {
		t.Errorf("build line source = %q", got)
	}

	assertNoLeaks(t, logs,
		"203.0.113.7", "2001:db8::1", "alice@example.com", "sk-test-123", "1600", "Pennsylvania",
		"q=tax", "Mozilla", "news.example.org", "769c83e5b", "t13d1516h2", "gdufoJxB6b9b1fEqr1jUtFkyavUU")
}

func TestDrainJSONArray(t *testing.T) {
	h, tel := setup(t)

	if rec := post(t, h, "/v1/drain", drainSecret, fixture(t, "drain.json")); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	logs := tel.Logs()
	if len(logs) != 2 {
		t.Fatalf("got %d records, want 2", len(logs))
	}

	if logs[0].Severity() != log.SeverityFatal {
		t.Errorf("fatal line severity = %v", logs[0].Severity())
	}

	a := attrs(&logs[0])
	if got := a["http.response.status_code"].AsInt64(); got != http.StatusInternalServerError {
		t.Errorf("status = %d, want the proxy's 500", got)
	}

	if got := a["url.path"].AsString(); got != "/api/otel/v1/traces" {
		t.Errorf("url.path = %q", got)
	}

	if _, ok := attrs(&logs[1])["http.response.status_code"]; ok {
		t.Error("a line without a status code got one")
	}

	if strings.Contains(everything(&logs[0]), "198.51.100.4") {
		t.Error("client IP leaked")
	}
}

func TestDrainBadLines(t *testing.T) {
	good := `{"source":"lambda","timestamp":1790000000000,"level":"info","message":"ok"}`

	tests := []struct {
		name    string
		body    string
		status  int
		records int
	}{
		{"one bad line among good ones", good + "\n{broken\n\n" + good + "\n", http.StatusOK, 2},
		{"only bad lines", "{broken\nalso broken\n", http.StatusBadRequest, 0},
		{"bad array", `[{"source":`, http.StatusBadRequest, 0},
		{"one bad element among good ones", "[" + good + `,{"timestamp":"soon"},"text",` + good + "]",
			http.StatusOK, 2},
		{"only bad elements", `[{"statusCode":"500"},7]`, http.StatusBadRequest, 0},
		{"empty batch", "", http.StatusOK, 0},
		{"stray console output is forwarded", `{"source":"lambda","level":"info","message":"{\"not\":\"ours\"}"}`,
			http.StatusOK, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, tel := setup(t)

			if rec := post(t, h, "/v1/drain", drainSecret, []byte(tt.body)); rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}

			if got := len(tel.Logs()); got != tt.records {
				t.Errorf("records = %d, want %d", got, tt.records)
			}
		})
	}
}

func TestDrainTruncatesLongMessages(t *testing.T) {
	h, tel := setup(t)
	body := `{"source":"lambda","level":"error","message":"` + strings.Repeat("é", 20000) + `"}`

	post(t, h, "/v1/drain", drainSecret, []byte(body))

	logs := tel.Logs()
	if len(logs) != 1 {
		t.Fatalf("records = %d", len(logs))
	}

	got := logs[0].Body().AsString()
	if len(got) > 16<<10 || !strings.HasPrefix(got, "éé") || strings.ContainsRune(got, '�') {
		t.Errorf("body not truncated cleanly: %d bytes", len(got))
	}
}

// wantEvent is an event record a webhook should produce.
type wantEvent struct {
	name     string
	severity log.Severity
	attrs    map[string]string
	body     []string
}

func checkEvent(t *testing.T, r *sdklog.Record, w wantEvent) {
	t.Helper()

	if r.EventName() != w.name || r.Severity() != w.severity {
		t.Errorf("event = %q %v, want %q %v", r.EventName(), r.Severity(), w.name, w.severity)
	}

	assertAttrs(t, r.EventName(), attrs(r), w.attrs, 0)

	for _, s := range w.body {
		if !strings.Contains(r.Body().AsString(), s) {
			t.Errorf("event body %q lacks %q", r.Body().AsString(), s)
		}
	}
}

func TestWebhookEvents(t *testing.T) {
	tests := []struct {
		fixture string
		want    []wantEvent
	}{
		{
			"alert.json",
			[]wantEvent{
				{
					"justabill.vercel.alert",
					log.SeverityError,
					map[string]string{"justabill.vercel.alert.type": "error_anomaly"},
					[]string{
						"Spike in 5xx responses",
						"edge_requests",
						"https://vercel.com/justabill/justabill/observability",
					},
				},
				{
					"justabill.vercel.alert",
					log.SeverityError,
					map[string]string{"justabill.vercel.alert.type": "usage_anomaly"},
					[]string{"Usage spike"},
				},
			},
		},
		{"deployment-error.json", []wantEvent{
			{"justabill.vercel.deployment", log.SeverityError, map[string]string{
				"justabill.vercel.deployment.state": "ERROR",
				"deployment.id":                     "dpl_9aBc",
				"deployment.environment.name":       "production",
			}, []string{"ERROR", "justabill", "https://vercel.com/justabill/justabill/dpl_9aBc"}},
		}},
		{"deployment-rollback.json", []wantEvent{
			{"justabill.vercel.deployment", log.SeverityInfo, map[string]string{
				"justabill.vercel.deployment.state": "ROLLBACK",
				"deployment.id":                     "dpl_old",
				"deployment.environment.name":       "preview",
			}, []string{"ROLLBACK"}},
		}},
		{"project-created.json", nil},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			h, tel := setup(t)

			if rec := post(t, h, "/v1/webhook", webhookSecret, fixture(t, tt.fixture)); rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}

			logs := tel.Logs()
			if len(logs) != len(tt.want) {
				t.Fatalf("records = %d, want %d", len(logs), len(tt.want))
			}

			for i, w := range tt.want {
				checkEvent(t, &logs[i], w)
			}

			// The deployment payload's meta names the commit author; it's not on the allowlist.
			assertNoLeaks(t, logs, "someone", "feat: something")
		})
	}
}

func TestWebhookTimestamps(t *testing.T) {
	tests := []struct {
		fixture string
		want    time.Time
	}{
		{"deployment-error.json", time.UnixMilli(1790000004000)},
		{"deployment-rollback.json", time.Date(2026, 9, 21, 19, 40, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		h, tel := setup(t)
		post(t, h, "/v1/webhook", webhookSecret, fixture(t, tt.fixture))

		if logs := tel.Logs(); len(logs) != 1 || !logs[0].Timestamp().Equal(tt.want) {
			t.Errorf("%s: timestamp wrong: %v", tt.fixture, logs)
		}
	}

	h, tel := setup(t)
	before := time.Now()
	post(t, h, "/v1/webhook", webhookSecret,
		[]byte(`{"type":"deployment.canceled","createdAt":null,"payload":{"deployment":{"id":"dpl_x"}}}`))

	logs := tel.Logs()
	if len(logs) != 1 || logs[0].Timestamp().Before(before) || logs[0].Severity() != log.SeverityWarn {
		t.Errorf("canceled deployment without createdAt: %v", logs)
	}
}

func TestWebhookIgnoredAndBad(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"deployment check event", `{"type":"deployment.check-rerequested","payload":{"deployment":{"id":"d"}}}`,
			http.StatusOK},
		{"alert without entries", `{"type":"alerts.triggered","payload":{"severity":"low"}}`, http.StatusOK},
		{"not json", `{"type":`, http.StatusBadRequest},
		{"bad alert payload", `{"type":"alerts.triggered","payload":"nope"}`, http.StatusBadRequest},
		{"bad deployment payload", `{"type":"deployment.error","payload":[1]}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, tel := setup(t)

			if rec := post(t, h, "/v1/webhook", webhookSecret, []byte(tt.body)); rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}

			if tt.name == "alert without entries" {
				logs := tel.Logs()
				if len(logs) != 1 || attrs(&logs[0])["justabill.vercel.alert.type"].AsString() != "unknown" ||
					logs[0].Severity() != log.SeverityWarn {
					t.Errorf("alert without entries: %v", logs)
				}
			}
		})
	}
}

func TestRoutes(t *testing.T) {
	h, _ := setup(t)

	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/v1/drain", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/other", http.StatusNotFound},
	}

	for _, tt := range tests {
		req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path, rec.Code, tt.want)
		}
	}
}
