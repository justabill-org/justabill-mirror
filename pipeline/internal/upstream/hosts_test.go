package upstream_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestConfigFromDefaults(t *testing.T) {
	cfg, err := upstream.ConfigFrom(env(map[string]string{"congress_api_key": " k1 ", "govinfo_api_key": "k2"}))
	if err != nil {
		t.Fatal(err)
	}
	want := upstream.Config{
		CongressAPIKey: "k1", GovInfoAPIKey: "k2",
		CongressRPS: 1.2, CongressBurst: 5, GovInfoRPS: 1.0, FedRegRPS: 1.0, ReservePct: 5,
	}
	if cfg != want {
		t.Errorf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestConfigFromKeyFiles(t *testing.T) {
	dir := t.TempDir()
	congress, govinfo := filepath.Join(dir, "congress"), filepath.Join(dir, "govinfo")
	for path, key := range map[string]string{congress: "k1\n", govinfo: "k2\n"} {
		if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := upstream.ConfigFrom(env(map[string]string{
		"congress_api_key_file": congress, "govinfo_api_key_file": govinfo,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CongressAPIKey != "k1" || cfg.GovInfoAPIKey != "k2" {
		t.Errorf("keys = %q, %q; want k1, k2 from the files", cfg.CongressAPIKey, cfg.GovInfoAPIKey)
	}

	for _, key := range []string{"congress_api_key", "govinfo_api_key"} {
		_, err = upstream.ConfigFrom(env(map[string]string{key: "k", key + "_file": congress}))
		if err == nil || !strings.Contains(err.Error(), "not both") {
			t.Errorf("%s and its _FILE: err = %v, want one saying not both", key, err)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	cfg, err := upstream.ConfigFrom(env(map[string]string{
		"pipeline_congress_rps":      "3",
		"pipeline_congress_burst":    "10",
		"pipeline_govinfo_rps":       "0.5",
		"pipeline_fedreg_rps":        "2",
		"pipeline_quota_reserve_pct": "0",
		"pipeline_shared_key_budget": "true",
		"upstream_archive_bucket":    " example-upstream-archive ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := upstream.Config{
		CongressRPS: 3, CongressBurst: 10, GovInfoRPS: 0.5, FedRegRPS: 2, ReservePct: 0, SharedKeyBudget: true,
		ArchiveBucket: "example-upstream-archive",
	}
	if cfg != want {
		t.Errorf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestConfigFromRejectsBadValues(t *testing.T) {
	for key, value := range map[string]string{
		"pipeline_congress_rps":      "0",
		"pipeline_govinfo_rps":       "fast",
		"pipeline_fedreg_rps":        "-1",
		"pipeline_congress_burst":    "0",
		"pipeline_quota_reserve_pct": "-1",
		"pipeline_shared_key_budget": "maybe",
	} {
		_, err := upstream.ConfigFrom(env(map[string]string{key: value}))
		if err == nil || !strings.Contains(err.Error(), strings.ToUpper(key)) {
			t.Errorf("%s=%q: err = %v, want one naming %s", key, value, err, strings.ToUpper(key))
		}
	}
}

func budgetNames(p *upstream.Pipeline) []string {
	names := make([]string, len(p.Budgets))
	for i, b := range p.Budgets {
		names[i] = b.Name()
	}
	return names
}

func TestNewPipelineBudgets(t *testing.T) {
	log, _ := newLogger()
	cfg := upstream.Config{CongressRPS: 1.2, CongressBurst: 5, GovInfoRPS: 1.0, ReservePct: 5}
	p, err := upstream.NewPipeline(t.Context(), log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"congress", "govinfo", "congress-www", "house-clerk", "senate", "federalregister"}
	if got := budgetNames(p); !slices.Equal(got, want) {
		t.Errorf("budgets = %v, want %v", got, want)
	}
	if b := p.Budgets[0]; b.Rate() != 1.2 || b.Burst() != 5 {
		t.Errorf("congress budget = %v/s burst %d, want 1.2/s burst 5", b.Rate(), b.Burst())
	}
	if b := p.Budgets[1]; b.Rate() != 1.0 {
		t.Errorf("govinfo budget = %v/s, want 1", b.Rate())
	}
	if b := p.Budgets[5]; b.Rate() != 1.0 || b.Burst() != 5 {
		t.Errorf("federalregister budget = %v/s burst %d, want the default 1/s burst 5", b.Rate(), b.Burst())
	}

	cfg.SharedKeyBudget = true
	if p, err = upstream.NewPipeline(t.Context(), log, cfg); err != nil {
		t.Fatal(err)
	}
	if got := budgetNames(p); slices.Contains(got, "govinfo") {
		t.Errorf("shared key budget: budgets = %v, want no separate govinfo budget", got)
	}

	cfg.ReservePct = 100
	if _, err = upstream.NewPipeline(t.Context(), log, cfg); err == nil {
		t.Error("NewPipeline accepted a 100% reserve")
	}
}

// The client knows the pipeline's hosts and nothing else. These requests all fail before
// anything is sent, so the test needs no network.
func TestNewPipelineHosts(t *testing.T) {
	log, _ := newLogger()
	p, err := upstream.NewPipeline(t.Context(), log, upstream.Config{
		CongressAPIKey: "k", GovInfoAPIKey: "k", CongressRPS: 1, CongressBurst: 1, GovInfoRPS: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]error{
		"https://example.com/":                   upstream.ErrUnknownHost,
		"https://congress.gov/":                  upstream.ErrUnknownHost,
		"http://api.congress.gov/v3/bill/119":    upstream.ErrInsecureKey, // keyed
		"http://api.govinfo.gov/collections/X/Y": upstream.ErrInsecureKey, // keyed
	} {
		_, err = get(t.Context(), p.Client, target, nil)
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", target, err, want)
		}
	}
}

// Without UPSTREAM_ARCHIVE_BUCKET (local dev and tests) there's no archive, so nothing is
// written anywhere.
func TestNewPipelineWithoutArchiveBucket(t *testing.T) {
	log, lb := newLogger()
	p, err := upstream.NewPipeline(t.Context(), log, upstream.Config{CongressRPS: 1, CongressBurst: 1, GovInfoRPS: 1})
	if err != nil {
		t.Fatal(err)
	}
	if p.Archive != nil {
		t.Error("archive enabled without a bucket")
	}
	if err = p.Close(t.Context()); err != nil {
		t.Errorf("Close = %v", err)
	}
	if strings.Contains(lb.String(), "upstream_archive") {
		t.Errorf("archive logs without a bucket: %s", lb.String())
	}
}

// GovInfo gets its own budget unless the key is shared, so it needs its own positive rate.
func TestNewPipelineRejectsGovInfoRate(t *testing.T) {
	log, _ := newLogger()
	_, err := upstream.NewPipeline(t.Context(), log, upstream.Config{CongressRPS: 1, CongressBurst: 1})
	if err == nil || !strings.Contains(err.Error(), "govinfo") {
		t.Errorf("err = %v, want the govinfo budget's error", err)
	}
}

func TestLogBudgets(t *testing.T) {
	log, lb := newLogger()
	p, err := upstream.NewPipeline(t.Context(), log, upstream.Config{CongressRPS: 1.2, CongressBurst: 5, GovInfoRPS: 1})
	if err != nil {
		t.Fatal(err)
	}
	p.LogBudgets(t.Context(), log)
	if n := lb.count("upstream_budget"); n != len(p.Budgets) {
		t.Errorf("logged %d upstream_budget lines, want %d", n, len(p.Budgets))
	}
	if !strings.Contains(lb.String(), `"provider":"congress","rps":1.2,"burst":5`) {
		t.Errorf("no congress budget line in %s", lb.String())
	}
}

// A configured rate the key's hourly limit can't sustain is logged once, on the first
// response that reports the limit.
func TestQuotaRateHighIsLoggedOnce(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("X-Ratelimit-Limit", "3600") // 1 request a second
		w.Header().Set("X-Ratelimit-Remaining", "3500")
	})
	for _, tc := range []struct {
		rps  float64
		want int
	}{{rps: 50, want: 1}, {rps: 1, want: 0}} {
		log, lb := newLogger()
		c := newClient(t, log, map[string]upstream.Host{srv.host(): host(newBudget(t, tc.rps, 5, 0))}, upstream.Hooks{})
		for range 3 {
			if _, err := get(t.Context(), c, srv.URL, nil); err != nil {
				t.Fatal(err)
			}
		}
		if n := lb.count("quota_rate_high"); n != tc.want {
			t.Errorf("rps %v: logged quota_rate_high %d times, want %d", tc.rps, n, tc.want)
		}
	}
}
