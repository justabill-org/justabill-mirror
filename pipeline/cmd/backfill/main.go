// Backfill syncs one congress from Congress.gov, GovInfo and the House and Senate vote feeds
// into Spanner once and exits: members and bills by default, and votes, texts, summaries,
// GovInfo changes, GAO reports, CRS summaries and CRA rules when their flags ask for them. Without
// --congress it loads the congress in progress by the calendar (the 119th until January 2, 2027,
// the 120th from January 3).
//
//	go run ./cmd/backfill --limit 100 --steps members,bills,votes
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/apicache"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/fedreg"
	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
	"github.com/justabill-org/justabill/pipeline/internal/leader"
	"github.com/justabill-org/justabill/pipeline/internal/legislators"
	"github.com/justabill-org/justabill/pipeline/internal/revalidate"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
	psync "github.com/justabill-org/justabill/pipeline/internal/sync"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

const defaultSummaryLimit = 50

// archiveFlushWait is how long a finished backfill waits for its queued archive writes.
const archiveFlushWait = 2 * time.Minute

// serviceName is the pipeline's service.name, shared with serve and seed.
const serviceName = "justabill-pipeline"

// jobPrefix names each step's job: backfill-bills, backfill-votes. They stay apart from serve's
// sync-* jobs, so a limited backfill doesn't count as a fresh sync.
const jobPrefix = "backfill-"

// congressUsage is the --congress help text of the root command and links: 0, the default, is
// the congress in progress.
const congressUsage = "Congress number to backfill (default: the congress in progress, by the calendar)"

// The backfill steps, in the order the old flags ran them. --steps runs any of them in the
// order given.
const (
	stepMembers     = "members"
	stepMemberTerms = "member-terms"
	stepBills       = "bills"
	stepVotes       = "votes"
	stepVotedBills  = "voted-bills"
	stepTexts       = "texts"
	stepSummaries   = "summaries"
	stepPassed      = "passed-summaries"
	stepGovInfo     = "govinfo"
	stepGAO         = "gao"
	stepCRS         = "crs-summaries"
	// stepCRARules matches the congress's CRA resolutions to Federal Register documents. It reads
	// only Spanner and the Federal Register: run it before summaries, so they get the rule.
	stepCRARules = "cra-rules"
)

func allSteps() []string {
	return []string{
		stepMembers, stepMemberTerms, stepBills, stepVotes, stepVotedBills,
		stepTexts, stepCRARules, stepSummaries, stepGovInfo, stepGAO, stepCRS, stepPassed,
	}
}

// defaultSteps is what --steps runs when it's empty: members and bills for the congress in
// progress, and with --past-congress the past-congress load (docs/design/78-118th-backfill.md):
// the roster, terms from congress-legislators, both sessions' votes, then the bills they name.
func defaultSteps(past bool) string {
	if past {
		return strings.Join([]string{stepMembers, stepMemberTerms, stepVotes, stepVotedBills}, ",")
	}
	return stepMembers + "," + stepBills
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "pipeline-backfill",
		Short: "Just a Bill pipeline backfill (one-shot sync)",
		RunE:  runBackfill,
	}

	rootCmd.PersistentFlags().String("spanner-project", "", "GCP project for Spanner")
	rootCmd.PersistentFlags().String("spanner-instance", "", "Spanner instance ID")
	rootCmd.PersistentFlags().String("spanner-database", "", "Spanner database name")
	rootCmd.Flags().String("congress-api-key", "", "Congress.gov API key")
	rootCmd.Flags().Int("congress", 0, congressUsage)
	rootCmd.Flags().Int("limit", 0,
		"Limit number of bills (0 = all) in bills and voted-bills; also caps summaries and gao (0 = 50) "+
			"and passed-summaries and cra-rules (0 = all)")
	rootCmd.Flags().String("steps", "",
		"Comma-separated steps to run, in the order given: "+strings.Join(allSteps(), ", ")+
			" (default "+defaultSteps(false)+", or "+defaultSteps(true)+" with --past-congress)")
	rootCmd.Flags().Bool("past-congress", false,
		"Load a congress that has ended: members stores the roster only, member-terms takes terms and "+
			"LIS IDs from congress-legislators, and bills isn't allowed (voted-bills loads the bills its "+
			"votes name). Refuses the current congress")
	rootCmd.Flags().String("legislators-base-url", "",
		"HTTPS base URL of the congress-legislators JSON files for member-terms, e.g. "+
			"https://raw.githubusercontent.com/unitedstates/congress-legislators/<sha> to pin a commit "+
			"(default "+legislators.DefaultBaseURL+")")
	rootCmd.Flags().String("resume-since", "",
		"RFC 3339 time: the bills step skips bills fully synced at or after it (resume an interrupted load)")
	rootCmd.Flags().Bool("force", false, "Force full re-sync, skip no data")
	rootCmd.Flags().Int("session", 0, "Vote session: 1 or 2, or 0 for every session that has started")
	rootCmd.Flags().Bool("wait-for-lease", false,
		"Wait for the pipeline lease when another process (a running serve) holds it, instead of exiting")

	rootCmd.AddCommand(newLinksCmd(), newReparseTextsCmd(), newDiffsCmd(), newStatusHistoryCmd())

	_ = viper.BindPFlag("spanner_project", rootCmd.PersistentFlags().Lookup("spanner-project"))
	_ = viper.BindPFlag("spanner_instance", rootCmd.PersistentFlags().Lookup("spanner-instance"))
	_ = viper.BindPFlag("spanner_database", rootCmd.PersistentFlags().Lookup("spanner-database"))
	_ = viper.BindPFlag("congress_api_key", rootCmd.Flags().Lookup("congress-api-key"))
	_ = viper.BindPFlag("congress", rootCmd.Flags().Lookup("congress"))
	_ = viper.BindPFlag("limit", rootCmd.Flags().Lookup("limit"))
	_ = viper.BindPFlag("steps", rootCmd.Flags().Lookup("steps"))
	_ = viper.BindPFlag("resume_since", rootCmd.Flags().Lookup("resume-since"))
	_ = viper.BindPFlag("force", rootCmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("session", rootCmd.Flags().Lookup("session"))
	_ = viper.BindPFlag("past_congress", rootCmd.Flags().Lookup("past-congress"))
	_ = viper.BindPFlag("legislators_base_url", rootCmd.Flags().Lookup("legislators-base-url"))
	_ = viper.BindPFlag("wait_for_lease", rootCmd.Flags().Lookup("wait-for-lease"))

	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AddConfigPath("..")
	_ = viper.ReadInConfig()

	viper.AutomaticEnv()

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runBackfill(_ *cobra.Command, _ []string) error {
	tel, err := startTelemetry()
	if err != nil {
		return err
	}
	// Deferred first, so it runs last and flushes what the steps recorded before the process exits.
	defer tel.ShutdownWithin(obs.ShutdownTimeout)
	logger := tel.Logger()

	upCfg, err := upstreamConfig()
	if err != nil {
		return err
	}

	start := time.Now().UTC().Truncate(time.Second)
	opts, err := readOptions(start)
	if err != nil {
		return err
	}
	ctx := context.Background()
	sc, err := spannerdb.NewClient(ctx, viper.GetString("spanner_project"), viper.GetString("spanner_instance"),
		viper.GetString("spanner_database"), spannerdb.WithBatchPriority())
	if err != nil {
		return err
	}
	defer sc.Close()

	if opts.past {
		// Before anything is written: a past-congress load must never touch the live congress.
		congresses, listErr := spannerdb.NewCongressRepo(sc).List(ctx)
		if listErr != nil {
			return fmt.Errorf("read congresses: %w", listErr)
		}
		if err = checkPastCongress(congresses, opts.congress, start); err != nil {
			return err
		}
	}

	svc, api, up, err := newService(ctx, logger, sc, upCfg)
	if err != nil {
		return err
	}
	defer up.CloseWithin(ctx, logger, archiveFlushWait)
	svc.SetForceSync(viper.GetBool("force"))
	svc.SetResumeSince(opts.resumeSince)
	tally := psync.NewTally()
	svc.SetTally(tally)
	if opts.legislatorsURL != "" {
		svc.SetLegislators(legislators.New(opts.legislatorsURL))
	}

	// The steps run under the pipeline lease, so they never overlap a serve replica's jobs
	// (docs/design/80-pipeline-operability.md, Decision 1A). A signal cancels them, and the
	// lease is released before Spanner closes.
	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	leaseCtx, release, err := holdLease(sigCtx, logger, spannerdb.NewLeaseStore(sc), viper.GetBool("wait_for_lease"))
	if err != nil {
		return err
	}
	defer release()

	logger.InfoContext(ctx, "starting backfill",
		"congress", opts.congress,
		"limit", opts.limit,
		"steps", strings.Join(opts.steps, ","),
		"past_congress", opts.past,
		"resume_since", viper.GetString("resume_since"),
	)
	// If this run is interrupted, rerunning it with this flag skips the bills it finished.
	logger.InfoContext(ctx, "resume with --resume-since="+start.Format(time.RFC3339))

	r := stepRunner{svc: svc, congress: opts.congress, limit: opts.limit, sessions: opts.sessions, past: opts.past}
	err = r.runAll(leaseCtx, logger, opts.steps)
	logSummary(ctx, logger, summary{
		congress: opts.congress, steps: opts.steps, err: err,
		requests: api.Requests(), elapsed: time.Since(start), tally: tally,
	})
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "backfill complete")
	return nil
}

// startTelemetry sets up OpenTelemetry with obs.Start and makes its logger slog's default, which
// psync.New logs to.
func startTelemetry() (*obs.Telemetry, error) {
	tel, err := obs.Start(context.Background(),
		obs.Config{Service: serviceName, Environment: viper.GetString("app_env")})
	if err != nil {
		return nil, fmt.Errorf("setting up telemetry: %w", err)
	}
	slog.SetDefault(tel.Logger())
	return tel, nil
}

// upstreamConfig reads the upstream client's settings, which must include a Congress.gov key.
func upstreamConfig() (upstream.Config, error) {
	cfg, err := upstream.ConfigFrom(viper.GetString)
	if err != nil {
		return upstream.Config{}, err
	}
	if cfg.CongressAPIKey == "" {
		return upstream.Config{}, errors.New("congress-api-key, CONGRESS_API_KEY or CONGRESS_API_KEY_FILE is required")
	}
	return cfg, nil
}

// options are the root command's settings, parsed and checked.
type options struct {
	congress       int
	limit          int
	steps          []string
	resumeSince    time.Time
	sessions       []int
	past           bool
	legislatorsURL string
}

// readOptions reads and checks the flags (or their environment variables) at time now.
func readOptions(now time.Time) (options, error) {
	o := options{
		limit: viper.GetInt("limit"),
		past:  viper.GetBool("past_congress"),
	}
	var err error
	if o.congress, err = resolveCongress(viper.GetInt("congress"), o.past, now); err != nil {
		return options{}, err
	}
	if o.steps, err = resolveSteps(viper.GetString("steps"), o.past); err != nil {
		return options{}, err
	}
	if o.resumeSince, err = parseResumeSince(viper.GetString("resume_since")); err != nil {
		return options{}, err
	}
	if o.sessions, err = voteSessions(o.congress, viper.GetInt("session"), now); err != nil {
		return options{}, err
	}
	if o.legislatorsURL, err = parseLegislatorsURL(viper.GetString("legislators_base_url")); err != nil {
		return options{}, err
	}
	return o, nil
}

// resolveCongress returns the congress to load: the --congress flag, or when it's 0 the congress
// in progress at now (rollcall.Current). A past-congress load has no default, since the congress
// in progress is never a past one.
func resolveCongress(flag int, past bool, now time.Time) (int, error) {
	switch {
	case flag < 0:
		return 0, fmt.Errorf("--congress %d: must be a congress number, or 0 for the congress in progress", flag)
	case flag > 0:
		return flag, nil
	case past:
		return 0, errors.New("--past-congress needs --congress, the congress that has ended (e.g. --congress 118)")
	}
	current, _ := rollcall.Current(now)
	return current, nil
}

// checkPastCongress refuses a past-congress load of a congress that is marked current, hasn't
// ended by the calendar (a new congress starts on January 3 of an odd year), or has no
// congresses row to hang its terms and votes on.
func checkPastCongress(congresses []model.Congress, congressNum int, now time.Time) error {
	i := slices.IndexFunc(congresses, func(c model.Congress) bool { return c.Number == congressNum })
	if i < 0 {
		return fmt.Errorf("congress %d has no congresses row; seed it first (task seed:past CONGRESS=%d)",
			congressNum, congressNum)
	}
	if congresses[i].IsCurrent {
		return fmt.Errorf("congress %d is the current congress; --past-congress only loads one that has ended",
			congressNum)
	}
	if end := congressEnd(congressNum); now.Before(end) {
		return fmt.Errorf("congress %d runs until %s; --past-congress only loads one that has ended",
			congressNum, end.Format(time.DateOnly))
	}
	return nil
}

// congressEnd is the day the next congress begins: January 3, two years after the congress's
// first session began.
func congressEnd(congressNum int) time.Time {
	const years, day = 2, 3
	return time.Date(rollcall.Year(congressNum, 1)+years, time.January, day, 0, 0, 0, 0, time.UTC)
}

// summary is what a finished (or failed) run did, for its one summary line.
type summary struct {
	congress int
	steps    []string
	err      error
	requests int64
	elapsed  time.Duration
	tally    *psync.Tally
}

// logSummary logs the run's summary line: votes stored per chamber and session, voted bills
// fetched, failed and unresolved, unmatched senators, and Congress.gov requests used.
func logSummary(ctx context.Context, logger *slog.Logger, s summary) {
	status := "ok"
	if s.err != nil {
		status = "failed"
	}
	attrs := []any{
		"congress", s.congress,
		"steps", strings.Join(s.steps, ","),
		"status", status,
		"congress_gov_requests", s.requests,
		"elapsed", s.elapsed.Round(time.Second).String(),
	}
	logger.InfoContext(ctx, "backfill summary", append(attrs, s.tally.Attrs()...)...)
}

// holdLease takes the pipeline lease for the backfill. When a serve replica (or another backfill)
// holds it, it fails with an error naming the holder, or with wait, waits for it. The returned
// context is cancelled if the lease is lost.
func holdLease(
	ctx context.Context, logger *slog.Logger, store repository.LeaseStore, wait bool, opts ...leader.Option,
) (context.Context, func(), error) {
	holder, err := leader.HolderID()
	if err != nil {
		return nil, nil, err
	}
	el := leader.New(store, holder, logger, opts...)
	if wait {
		logger.InfoContext(ctx, "waiting for the pipeline lease", "holder", holder)
	}
	leaseCtx, release, err := el.Hold(ctx, wait)
	if errors.Is(err, leader.ErrHeld) {
		return nil, nil, fmt.Errorf("%w; stop pipeline-serve (scale it to zero) or pass --wait-for-lease", err)
	}
	if err != nil {
		return nil, nil, err
	}
	return leaseCtx, release, nil
}

// newService builds the sync service and its Congress.gov client on the pipeline's upstream
// client, with the optional AI summarizer and GovInfo client when their settings are present.
// The caller closes the upstream client, which flushes the archive.
func newService(
	ctx context.Context, logger *slog.Logger, sc *spannerdb.Client, upCfg upstream.Config,
) (*psync.Service, *congress.Client, *upstream.Pipeline, error) {
	up, err := upstream.NewPipeline(ctx, logger, upCfg)
	if err != nil {
		return nil, nil, nil, err
	}
	up.LogBudgets(ctx, logger)
	api := congress.NewClient(up.Client)
	svc := psync.New(spannerdb.NewPipelineStore(sc), api, up.Client)
	if err = configureOptionalClients(ctx, logger, svc, upCfg.GovInfoAPIKey, up.Client); err != nil {
		up.CloseWithin(ctx, logger, archiveFlushWait)
		return nil, nil, nil, err
	}
	return svc, api, up, nil
}

// configureOptionalClients gives the service the Federal Register client, and an AI summarizer, a
// GovInfo client and the web revalidation hook when their settings are present. httpClient is the pipeline's upstream client.
func configureOptionalClients(
	ctx context.Context, logger *slog.Logger, svc *psync.Service, govInfoAPIKey string, httpClient *http.Client,
) error {
	aiCfg, err := ai.ConfigFrom(viper.GetString)
	if err != nil {
		return err
	}
	if aiCfg.Project != "" {
		summarizer, aiErr := ai.NewSummarizer(ctx, aiCfg)
		if aiErr != nil {
			logger.WarnContext(ctx, "failed to create AI summarizer", "error", aiErr)
		} else {
			svc.SetSummarizer(summarizer)
			logger.InfoContext(ctx, "AI summarizer enabled", "project", aiCfg.Project, "location", aiCfg.Location,
				"model", aiCfg.Model, "thinking_level", aiCfg.ThinkingLevel, "request_type", aiCfg.RequestType)
		}
	}

	svc.SetFederalRegister(fedreg.NewClient(httpClient))
	if govInfoAPIKey != "" {
		svc.SetGovInfo(govinfo.NewClient(httpClient))
		logger.InfoContext(ctx, "GovInfo client enabled")
	}
	return enableRevalidation(ctx, logger, svc, viper.GetString)
}

// enableRevalidation turns on the web revalidation hook when WEB_REVALIDATE_URL and its secret
// are set (docs/design/297-revalidate-after-sync.md), and the clearing of the API's cached bill
// responses when REDIS_URL is set (#400). get reads the configuration (viper.GetString).
func enableRevalidation(
	ctx context.Context, logger *slog.Logger, svc *psync.Service, get func(key string) string,
) error {
	rv, err := revalidate.FromConfig(get, revalidate.WithLogger(logger))
	if err != nil {
		return err
	}
	svc.SetRevalidator(rv)
	if rv != nil {
		logger.InfoContext(ctx, "web revalidation enabled", "url", get("web_revalidate_url"))
	}
	ac, err := apicache.FromConfig(get, apicache.WithLogger(logger))
	if err != nil {
		return err
	}
	svc.SetAPICache(ac)
	if ac != nil {
		logger.InfoContext(ctx, "api cache clearing enabled")
	}
	return nil
}

// parseSteps splits a comma-separated --steps value and checks each step is known and
// appears once. The order is kept.
func parseSteps(value string) ([]string, error) {
	allowed := allSteps()
	usage := "allowed steps: " + strings.Join(allowed, ", ")
	var steps []string
	for part := range strings.SplitSeq(value, ",") {
		step := strings.TrimSpace(part)
		switch {
		case step == "":
			continue
		case !slices.Contains(allowed, step):
			return nil, fmt.Errorf("unknown step %q; %s", step, usage)
		case slices.Contains(steps, step):
			return nil, fmt.Errorf("step %q is listed twice", step)
		}
		steps = append(steps, step)
	}
	if len(steps) == 0 {
		return nil, errors.New("--steps is empty; " + usage)
	}
	return steps, nil
}

// resolveSteps parses --steps, or the default steps when it's empty, and checks them against
// the mode: --past-congress takes member-terms but not bills, and only it takes member-terms.
func resolveSteps(value string, past bool) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		value = defaultSteps(past)
	}
	steps, err := parseSteps(value)
	if err != nil {
		return nil, err
	}
	switch {
	case past && slices.Contains(steps, stepBills):
		return nil, errors.New("--past-congress doesn't run bills, which lists every bill of the congress; " +
			"voted-bills loads the bills its votes name")
	case !past && slices.Contains(steps, stepMemberTerms):
		return nil, errors.New("member-terms loads a past congress's terms from congress-legislators; " +
			"it needs --past-congress")
	}
	return steps, nil
}

// parseLegislatorsURL checks --legislators-base-url. Empty means the dataset's default.
func parseLegislatorsURL(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("--legislators-base-url must be an https URL, got %q", value)
	}
	return value, nil
}

// parseResumeSince parses --resume-since. Empty means no resume: the zero time.
func parseResumeSince(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("--resume-since must be an RFC 3339 time like 2026-10-01T00:00:00Z: %w", err)
	}
	return t, nil
}

// voteSessions returns the sessions the votes step syncs: every session of the congress that has
// started when session is 0, otherwise just that one.
func voteSessions(congressNum, session int, now time.Time) ([]int, error) {
	switch session {
	case 0:
		return rollcall.Sessions(congressNum, now), nil
	case 1, 2: //nolint:mnd // the two sessions of a congress
		return []int{session}, nil
	default:
		return nil, fmt.Errorf("--session must be 0 (every session that has started), 1 or 2, got %d", session)
	}
}

// stepRunner runs one backfill step at a time against a sync service.
type stepRunner struct {
	svc      *psync.Service
	congress int
	limit    int
	sessions []int
	// past is --past-congress: members stores the roster only.
	past bool
}

// runAll runs steps in order under leaseCtx and stops at the first that fails. Each step is a
// pipeline job (obs.Job) named backfill-<step>: its own trace, metrics and finished record. A step
// cut short because the lease was lost fails with leader.ErrLost.
func (r stepRunner) runAll(leaseCtx context.Context, logger *slog.Logger, steps []string) error {
	for _, step := range steps {
		logger.InfoContext(leaseCtx, "running step", "step", step)
		_, err := obs.Job(leaseCtx, logger, jobPrefix+step, func(ctx context.Context) error {
			return r.run(ctx, step)
		})
		if err != nil {
			if cause := context.Cause(leaseCtx); errors.Is(cause, leader.ErrLost) {
				err = cause
			}
			return fmt.Errorf("step %s: %w", step, err)
		}
	}
	return nil
}

func (r stepRunner) run(ctx context.Context, step string) error {
	switch step {
	case stepMembers:
		if r.past {
			return r.svc.SyncMemberRoster(ctx, r.congress)
		}
		return r.svc.SyncMembers(ctx, r.congress)
	case stepMemberTerms:
		return r.svc.SyncPastMemberTerms(ctx, r.congress)
	case stepBills:
		return r.svc.SyncBills(ctx, r.congress, r.limit)
	case stepVotes:
		return r.svc.SyncVotes(ctx, r.congress, r.sessions)
	case stepVotedBills:
		return r.svc.SyncVotedBills(ctx, r.congress, r.limit)
	case stepTexts:
		return r.svc.SyncBillTexts(ctx, r.congress, 0)
	case stepSummaries:
		return r.svc.SyncSummaries(ctx, r.congress, r.limitOrDefault())
	case stepGovInfo:
		return r.svc.SyncGovInfoChanges(ctx, r.congress)
	case stepGAO:
		return r.svc.SyncGAOReports(ctx, r.congress, r.limitOrDefault())
	case stepCRS:
		return r.svc.SyncCRSSummaries(ctx, r.congress)
	case stepCRARules:
		return r.svc.SyncCRARules(ctx, r.congress, r.limit)
	case stepPassed:
		return r.svc.SyncPassedSummaries(ctx, r.congress, r.limit)
	default:
		return fmt.Errorf("step %q has no runner", step)
	}
}

// limitOrDefault is the item cap for summaries and GAO reports: --limit, or 50 when it's 0.
func (r stepRunner) limitOrDefault() int {
	if r.limit == 0 {
		return defaultSummaryLimit
	}
	return r.limit
}

func newLinksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "links",
		Short: "Rebuild the ontology link tables from the stored bill JSON (no Congress.gov calls)",
		Long: "Rebuilds bill_sponsorships, bill_committees, bill_subjects and bill_relations for one congress\n" +
			"from the JSON columns on bills. Safe to rerun; an interrupted run resumes from its sync_state\n" +
			"checkpoint (step \"links\").",
		RunE: runBackfillLinks,
	}
	cmd.Flags().Int("congress", 0, congressUsage)
	cmd.Flags().Int("page-size", psync.DefaultLinksPageSize, "Bills per page and per checkpoint")
	return cmd
}

func runBackfillLinks(cmd *cobra.Command, _ []string) error {
	return runStoredDataStep(
		cmd,
		noLease,
		func(ctx context.Context, svc *psync.Service, congressNum, pageSize int) error {
			return svc.BackfillLinks(ctx, congressNum, pageSize)
		},
	)
}

// leaseMode says whether a stored-data subcommand runs under the pipeline lease.
type leaseMode int

const (
	noLease      leaseMode = iota // reads and rewrites only what no sync writes
	takeLease                     // exits at once if serve or a load Job holds the lease
	waitForLease                  // waits for the lease
)

// runStoredDataStep runs a subcommand that rebuilds data from what's stored for one congress,
// with no upstream calls: it reads --congress and --page-size and connects to Spanner.
func runStoredDataStep(
	cmd *cobra.Command, mode leaseMode,
	run func(ctx context.Context, svc *psync.Service, congressNum, pageSize int) error,
) error {
	flagCongress, err := cmd.Flags().GetInt("congress")
	if err != nil {
		return err
	}
	congressNum, err := resolveCongress(flagCongress, false, time.Now())
	if err != nil {
		return err
	}
	pageSize, err := cmd.Flags().GetInt("page-size")
	if err != nil {
		return err
	}
	return runStored(cmd, mode, func(ctx context.Context, svc *psync.Service) error {
		return run(ctx, svc, congressNum, pageSize)
	})
}

// runStored starts telemetry, connects to Spanner and runs a subcommand that works only on stored
// data, with a sync service that makes no upstream calls, under the pipeline lease unless mode is
// noLease.
func runStored(cmd *cobra.Command, mode leaseMode, run func(ctx context.Context, svc *psync.Service) error) error {
	tel, err := startTelemetry()
	if err != nil {
		return err
	}
	defer tel.ShutdownWithin(obs.ShutdownTimeout)

	ctx := cmd.Context()
	sc, err := spannerdb.NewClient(ctx, viper.GetString("spanner_project"),
		viper.GetString("spanner_instance"), viper.GetString("spanner_database"), spannerdb.WithBatchPriority())
	if err != nil {
		return err
	}
	defer sc.Close()

	if mode != noLease {
		sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		leaseCtx, release, leaseErr := holdLease(
			sigCtx,
			tel.Logger(),
			spannerdb.NewLeaseStore(sc),
			mode == waitForLease,
		)
		if leaseErr != nil {
			return leaseErr
		}
		defer release()
		ctx = leaseCtx
	}
	return run(ctx, psync.New(spannerdb.NewPipelineStore(sc), nil, nil))
}

func newStatusHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status-history",
		Short: "Derive every stored bill's status and status history again from its stored actions (no downloads)",
		Long: "Classifies the actions already stored for one congress's bills again (#659) and replaces each\n" +
			"bill's current status and status history, so a classifier fix reaches bills the sync won't\n" +
			"fetch again. Logs every signed or enacted bill still missing a House or Senate passage. Runs\n" +
			"under the pipeline lease, so never alongside serve or a load Job. Safe to rerun; an interrupted\n" +
			"run resumes from its sync_state checkpoint (step \"status_history\").",
		RunE: runStatusHistory,
	}
	cmd.Flags().Int("congress", 0, congressUsage)
	cmd.Flags().Int("page-size", psync.DefaultStatusHistoryPageSize, "Bills per page and per checkpoint")
	cmd.Flags().Bool("wait-for-lease", false,
		"Wait for the pipeline lease when another process (serve, a load Job) holds it, instead of exiting")
	return cmd
}

// runStatusHistory derives the stored bills' statuses again. Unlike the other stored-data
// commands it rewrites what the bills sync writes, so it runs under the pipeline lease.
func runStatusHistory(cmd *cobra.Command, _ []string) error {
	wait, err := cmd.Flags().GetBool("wait-for-lease")
	if err != nil {
		return err
	}
	mode := takeLease
	if wait {
		mode = waitForLease
	}
	return runStoredDataStep(cmd, mode, func(ctx context.Context, svc *psync.Service, congressNum, pageSize int) error {
		return svc.RederiveStatusHistory(ctx, congressNum, pageSize)
	})
}

func newReparseTextsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reparse-texts",
		Short: "Rebuild bill_texts.sections from the stored bill texts (no downloads)",
		Long: "Parses every bill text already stored for one congress again and replaces its sections, so a\n" +
			"parser fix reaches old texts without downloading them. Stored diffs still compare the old\n" +
			"sections. Safe to rerun; an interrupted run resumes from its sync_state checkpoint\n" +
			"(step \"reparse_texts\").",
		RunE: runReparseTexts,
	}
	cmd.Flags().Int("congress", 0, congressUsage)
	cmd.Flags().Int("page-size", psync.DefaultReparsePageSize, "Texts per page and per checkpoint")
	return cmd
}

func runReparseTexts(cmd *cobra.Command, _ []string) error {
	return runStoredDataStep(
		cmd,
		noLease,
		func(ctx context.Context, svc *psync.Service, congressNum, pageSize int) error {
			return svc.ReparseTexts(ctx, congressNum, pageSize)
		},
	)
}

func newDiffsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diffs",
		Short: "Sweep the stored text diffs, and with --recompute compute them all again (no downloads)",
		Long: "Runs sync-texts' diff sweep on its own, for every congress: deletes the diffs that don't compare\n" +
			"consecutive fetched versions and computes the missing ones from the stored sections. With\n" +
			"--recompute it first computes every stored diff again and replaces the ones that changed (their\n" +
			"AI summaries are deleted for sync-summaries to write again), flagging the ones now empty so no\n" +
			"reader shows them; a change to the diff or the section parser so reaches stored diffs. Safe to rerun.",
		RunE: runDiffs,
	}
	cmd.Flags().Bool("recompute", false, "Compute every stored diff again and replace those that changed")
	return cmd
}

func runDiffs(cmd *cobra.Command, _ []string) error {
	recompute, err := cmd.Flags().GetBool("recompute")
	if err != nil {
		return err
	}
	return runStored(cmd, noLease, func(ctx context.Context, svc *psync.Service) error {
		return svc.SweepDiffs(ctx, recompute)
	})
}
