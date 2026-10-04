package upstream

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/secretfile"
)

// The hosts the pipeline calls. A request to any other host fails with ErrUnknownHost.
const (
	CongressAPIHost = "api.congress.gov"
	GovInfoAPIHost  = "api.govinfo.gov"
	CongressWWWHost = "www.congress.gov" // bill text downloads
	HouseClerkHost  = "clerk.house.gov"  // House roll-call XML
	SenateHost      = "www.senate.gov"   // Senate roll-call XML
	// FederalRegisterHost is the Federal Register API, for the rules CRA resolutions disapprove
	// (docs/design/590-cra-disapproved-rules.md). It needs no key.
	FederalRegisterHost = "www.federalregister.gov"
)

// DownloadTimeout is the attempt timeout for large text downloads: bill text from
// www.congress.gov, and GovInfo's /packages/*/xml and /htm (set per request with
// WithAttemptTimeout).
const DownloadTimeout = 120 * time.Second

// Defaults from docs/design/67-upstream-quota-retries.md. 1.2 req/s is 4,320 requests an
// hour, 86% of Congress.gov's documented 5,000.
const (
	defaultCongressRPS   = 1.2
	defaultCongressBurst = 5
	defaultGovInfoRPS    = 1.0
	defaultReservePct    = 5
	// The Federal Register sends no rate-limit headers; 1 req/s is well under what a browser
	// session does (docs/design/590-cra-disapproved-rules.md).
	defaultFedRegRPS = 1.0

	apiBurst       = 5
	congressWWWRPS = 4
	voteFeedRPS    = 5 // the vote workers' rate before this client existed

	apiTimeout      = 30 * time.Second
	voteFeedTimeout = 60 * time.Second

	mib          = 1 << 20
	smallBodyCap = 16 * mib
	largeBodyCap = 64 * mib
)

// Config is the pipeline's upstream configuration. ConfigFrom reads it from the
// environment; the zero value of each rate field means its default.
type Config struct {
	CongressAPIKey string
	GovInfoAPIKey  string
	CongressRPS    float64
	CongressBurst  int
	GovInfoRPS     float64
	// FedRegRPS paces the Federal Register API (PIPELINE_FEDREG_RPS).
	FedRegRPS float64
	// ReservePct pauses a budget when a response reports less than this percent of the
	// hourly quota left.
	ReservePct int
	// SharedKeyBudget puts Congress.gov and GovInfo on one budget, at CongressRPS, for a key
	// that api.data.gov counts across both APIs.
	SharedKeyBudget bool
	// ArchiveBucket is the GCS bucket that keeps every 2xx response body (production's
	// load). Empty means nothing is archived: local dev and tests.
	ArchiveBucket string
}

// ConfigFrom reads the configuration with get, which takes lowercase keys the way
// viper.GetString does ("pipeline_congress_rps" for PIPELINE_CONGRESS_RPS). Unset values get
// their defaults; malformed ones are an error. Each key comes from its variable or from the file
// its _FILE variable names (CONGRESS_API_KEY_FILE, GOVINFO_API_KEY_FILE), not both.
func ConfigFrom(get func(key string) string) (Config, error) {
	cfg := Config{
		CongressRPS:   defaultCongressRPS,
		CongressBurst: defaultCongressBurst,
		GovInfoRPS:    defaultGovInfoRPS,
		FedRegRPS:     defaultFedRegRPS,
		ReservePct:    defaultReservePct,
		ArchiveBucket: strings.TrimSpace(get("upstream_archive_bucket")),
	}
	var err error
	if cfg.CongressAPIKey, err = secretfile.Resolve(get, "congress_api_key"); err != nil {
		return Config{}, err
	}
	if cfg.GovInfoAPIKey, err = secretfile.Resolve(get, "govinfo_api_key"); err != nil {
		return Config{}, err
	}
	if cfg.CongressRPS, err = parseRate(get, "pipeline_congress_rps", cfg.CongressRPS); err != nil {
		return Config{}, err
	}
	if cfg.GovInfoRPS, err = parseRate(get, "pipeline_govinfo_rps", cfg.GovInfoRPS); err != nil {
		return Config{}, err
	}
	if cfg.FedRegRPS, err = parseRate(get, "pipeline_fedreg_rps", cfg.FedRegRPS); err != nil {
		return Config{}, err
	}
	if cfg.CongressBurst, err = parseInt(get, "pipeline_congress_burst", cfg.CongressBurst, 1); err != nil {
		return Config{}, err
	}
	if cfg.ReservePct, err = parseInt(get, "pipeline_quota_reserve_pct", cfg.ReservePct, 0); err != nil {
		return Config{}, err
	}
	if v := strings.TrimSpace(get("pipeline_shared_key_budget")); v != "" {
		if cfg.SharedKeyBudget, err = strconv.ParseBool(v); err != nil {
			return Config{}, fmt.Errorf("PIPELINE_SHARED_KEY_BUDGET %q: want true or false", v)
		}
	}
	return cfg, nil
}

func parseRate(get func(string) string, key string, def float64) (float64, error) {
	v := strings.TrimSpace(get(key))
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("%s %q: want a positive number of requests per second", strings.ToUpper(key), v)
	}
	return f, nil
}

func parseInt(get func(string) string, key string, def, minimum int) (int, error) {
	v := strings.TrimSpace(get(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minimum {
		return 0, fmt.Errorf("%s %q: want an integer of at least %d", strings.ToUpper(key), v, minimum)
	}
	return n, nil
}

// Pipeline is the pipeline's one upstream client, the budgets that pace it and, when
// configured, the archive it writes responses to. Close it before the process exits.
type Pipeline struct {
	Client  *http.Client
	Budgets []*Budget
	// Archive is nil unless Config.ArchiveBucket is set.
	Archive *Archive

	store *GCSStore
}

// NewPipeline builds the client for every host the pipeline calls, with the budgets,
// timeouts and body caps from the design, and the archive when cfg.ArchiveBucket is set.
func NewPipeline(ctx context.Context, log *slog.Logger, cfg Config) (*Pipeline, error) {
	p := &Pipeline{}
	budget := func(name string, rps float64, burst, reservePct int) (*Budget, error) {
		b, err := NewBudget(name, rps, burst, reservePct)
		if err == nil {
			p.Budgets = append(p.Budgets, b)
		}
		return b, err
	}
	congress, err := budget("congress", cfg.CongressRPS, cfg.CongressBurst, cfg.ReservePct)
	if err != nil {
		return nil, err
	}
	govinfo := congress
	if !cfg.SharedKeyBudget {
		if govinfo, err = budget("govinfo", cfg.GovInfoRPS, apiBurst, cfg.ReservePct); err != nil {
			return nil, err
		}
	}
	www, err := budget("congress-www", congressWWWRPS, apiBurst, 0)
	if err != nil {
		return nil, err
	}
	house, err := budget("house-clerk", voteFeedRPS, voteFeedRPS, 0)
	if err != nil {
		return nil, err
	}
	senate, err := budget("senate", voteFeedRPS, voteFeedRPS, 0)
	if err != nil {
		return nil, err
	}
	fedreg, err := budget("federalregister", cmp.Or(cfg.FedRegRPS, defaultFedRegRPS), apiBurst, 0)
	if err != nil {
		return nil, err
	}
	var opts []Option
	if cfg.ArchiveBucket != "" {
		if p.store, err = NewGCSStore(ctx, cfg.ArchiveBucket); err != nil {
			return nil, err
		}
		p.Archive = NewArchive(log, p.store)
		opts = append(opts, WithArchive(p.Archive))
		log.InfoContext(ctx, "upstream_archive_enabled", "bucket", cfg.ArchiveBucket)
	}
	p.Client, err = NewClient(log, map[string]Host{
		CongressAPIHost: {Budget: congress, APIKey: cfg.CongressAPIKey, AttemptTimeout: apiTimeout,
			MaxBodyBytes: smallBodyCap},
		GovInfoAPIHost: {Budget: govinfo, APIKey: cfg.GovInfoAPIKey, AttemptTimeout: apiTimeout,
			MaxBodyBytes: largeBodyCap},
		CongressWWWHost:     {Budget: www, AttemptTimeout: DownloadTimeout, MaxBodyBytes: largeBodyCap},
		HouseClerkHost:      {Budget: house, AttemptTimeout: voteFeedTimeout, MaxBodyBytes: smallBodyCap},
		SenateHost:          {Budget: senate, AttemptTimeout: voteFeedTimeout, MaxBodyBytes: smallBodyCap},
		FederalRegisterHost: {Budget: fedreg, AttemptTimeout: apiTimeout, MaxBodyBytes: smallBodyCap},
	}, opts...)
	if err != nil {
		return nil, errors.Join(err, p.Close(ctx))
	}
	return p, nil
}

// CloseWithin is Close with at most d for the archive's queued writes. It logs a failure,
// for callers that are exiting anyway.
func (p *Pipeline) CloseWithin(ctx context.Context, log *slog.Logger, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		log.WarnContext(ctx, "upstream_close", "error", err.Error())
	}
}

// Close flushes the archive, waiting for queued writes until ctx is done, and closes its
// storage client. It does nothing when there's no archive.
func (p *Pipeline) Close(ctx context.Context) error {
	var err error
	if p.Archive != nil {
		err = p.Archive.Close(ctx)
	}
	if p.store != nil {
		err = errors.Join(err, p.store.Close())
	}
	return err
}

// LogBudgets logs one upstream_budget line per budget, at startup.
func (p *Pipeline) LogBudgets(ctx context.Context, log *slog.Logger) {
	for _, b := range p.Budgets {
		log.InfoContext(ctx, "upstream_budget", "provider", b.Name(), "rps", b.Rate(), "burst", b.Burst(),
			"reserve_pct", b.reservePct)
	}
}
