// Serve runs the pipeline as a long-lived service: it syncs bills, members, votes, texts,
// summaries, GovInfo changes, GAO reports, CRS summaries and the rules CRA resolutions disapprove
// into Spanner, and loads each new US Code release
// point, each on its own schedule, until it is stopped.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/aggregates"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/apicache"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/fedreg"
	"github.com/justabill-org/justabill/pipeline/internal/gcsblob"
	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
	"github.com/justabill-org/justabill/pipeline/internal/health"
	"github.com/justabill-org/justabill/pipeline/internal/leader"
	"github.com/justabill-org/justabill/pipeline/internal/revalidate"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
	"github.com/justabill-org/justabill/pipeline/internal/scheduler"
	psync "github.com/justabill-org/justabill/pipeline/internal/sync"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
	"github.com/justabill-org/justabill/pipeline/internal/uscode"
)

const (
	// serviceName is the pipeline's service.name, shared with backfill and seed.
	serviceName = "justabill-pipeline"
	// gaoBillsPerRun caps a sync-gao run's GovInfo searches (design Decision 4A).
	gaoBillsPerRun = 1000
	// craRulesPerRun caps a sync-cra-rules run's resolutions (docs/design/590-cra-disapproved-rules.md).
	craRulesPerRun = 500
	// craRulesJobName is the CRA rule lookup, which PIPELINE_CRA_RULES=false leaves out.
	craRulesJobName = "sync-cra-rules"
	// shutdownWait bounds how long SIGTERM waits for cancelled jobs before Spanner closes.
	shutdownWait = 30 * time.Second
	// healthShutdownWait bounds the health server's shutdown, after the jobs'.
	healthShutdownWait = 5 * time.Second
	// archiveFlushWait is how long shutdown waits for queued archive writes, after the jobs
	// and the health server, inside the pod's 45 s grace period.
	archiveFlushWait = 5 * time.Second
)

// The job table (docs/design/80-pipeline-operability.md, "Job table"). Every timeout is shorter
// than its interval, so a run always ends before the next one of the same job is due.
// PIPELINE_JOB_TIMEOUT_<JOB> overrides a timeout at startup (timeouts.go).
const (
	billSyncInterval   = 4 * time.Hour
	billSyncTimeout    = 3 * time.Hour
	memberSyncInterval = 24 * time.Hour
	memberSyncTimeout  = time.Hour
	voteSyncInterval   = 6 * time.Hour
	voteSyncTimeout    = 2 * time.Hour
	textSyncInterval   = 2 * time.Hour
	textSyncTimeout    = 90 * time.Minute
	// AI_SUMMARY_INTERVAL overrides the summary job's interval; its timeout follows (timeouts.go).
	summarySyncInterval = 30 * time.Minute
	summarySyncTimeout  = 25 * time.Minute
	// Law-change explanations take up to AI_LAW_BATCH bills an hour, within their own daily cap.
	lawChangeSyncInterval = time.Hour
	lawChangeSyncTimeout  = 50 * time.Minute
	govinfoSyncInterval   = 30 * time.Minute
	govinfoSyncTimeout    = 25 * time.Minute
	gaoSyncInterval       = 24 * time.Hour
	gaoSyncTimeout        = 2 * time.Hour
	// CRS summaries come from one list: a run is usually one request (docs/design/197-crs-summaries.md).
	crsSyncInterval = 6 * time.Hour
	crsSyncTimeout  = 30 * time.Minute
	// CRA rule lookups read only Spanner and the Federal Register (docs/design/590-cra-disapproved-rules.md).
	craRulesInterval = 6 * time.Hour
	craRulesTimeout  = 30 * time.Minute
	// The US Code loader downloads nothing unless the Law Revision Counsel has published a new
	// release point, which happens every 1 to 4 weeks (docs/design/149-law-aware-assistant.md), so
	// a daily run reads one page. A run that didn't succeed, as while uscode.house.gov is down for
	// maintenance, is retried hourly (#866).
	uscodeLoadInterval  = 24 * time.Hour
	uscodeRetryInterval = time.Hour
	uscodeLoadTimeout   = 2 * time.Hour
	// The aggregate-votes job reads only Spanner (docs/design/89-aggregate-analytics.md).
	aggregateJobName  = "aggregate-votes"
	aggregateInterval = time.Hour
	aggregateTimeout  = 50 * time.Minute
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "pipeline-serve",
		Short: "Just a Bill pipeline service (long-running scheduled sync)",
		RunE:  runServe,
	}

	rootCmd.Flags().String("spanner-project", "", "GCP project for Spanner")
	rootCmd.Flags().String("spanner-instance", "", "Spanner instance ID")
	rootCmd.Flags().String("spanner-database", "", "Spanner database name")
	rootCmd.Flags().String("congress-api-key", "", "Congress.gov API key")
	rootCmd.Flags().Int("congress", 0,
		"Congress number to sync (default: follow the calendar, and mark the congress in progress current)")
	rootCmd.Flags().String("health-addr", health.DefaultAddr, "Address for /healthz, /readyz and /status")

	_ = viper.BindPFlag("spanner_project", rootCmd.Flags().Lookup("spanner-project"))
	_ = viper.BindPFlag("spanner_instance", rootCmd.Flags().Lookup("spanner-instance"))
	_ = viper.BindPFlag("spanner_database", rootCmd.Flags().Lookup("spanner-database"))
	_ = viper.BindPFlag("congress_api_key", rootCmd.Flags().Lookup("congress-api-key"))
	_ = viper.BindPFlag("congress", rootCmd.Flags().Lookup("congress"))
	_ = viper.BindPFlag("pipeline_health_addr", rootCmd.Flags().Lookup("health-addr"))

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

func runServe(_ *cobra.Command, _ []string) error {
	// Telemetry starts first, so the clients below are traced (docs/design/53-observability.md).
	tel, err := startTelemetry()
	if err != nil {
		return err
	}
	// Deferred first, so it runs last: after Spanner closes and the jobs have returned.
	defer tel.ShutdownWithin(obs.ShutdownTimeout)
	logger := tel.Logger()

	upCfg, err := upstreamConfig()
	if err != nil {
		return err
	}

	pinned := viper.GetInt("congress")
	project := viper.GetString("spanner_project")
	instance := viper.GetString("spanner_instance")
	database := viper.GetString("spanner_database")

	// Clients live for the whole process and close last, so they don't get the signal context.
	ctx := context.Background()

	sc, err := spannerdb.NewClient(ctx, project, instance, database, spannerdb.WithBatchPriority())
	if err != nil {
		return err
	}
	defer sc.Close()

	up, err := startUpstream(ctx, logger, upCfg)
	if err != nil {
		return err
	}
	defer up.CloseWithin(ctx, logger, archiveFlushWait)
	store := spannerdb.NewPipelineStore(sc)
	svc := psync.New(store, congress.NewClient(up.Client), up.Client)

	if err = enableSummarizer(ctx, logger, svc); err != nil {
		return err
	}

	enableSources(ctx, logger, svc, up, upCfg.GovInfoAPIKey)
	if err = enableRevalidation(ctx, logger, svc, viper.GetString); err != nil {
		return err
	}

	holder, err := leader.HolderID()
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "starting pipeline service", "pinned_congress", pinned, "holder", holder)

	jobs, err := jobTable(svc, store, pinned, logger)
	if err != nil {
		return err
	}
	jobs, overridden, err := withTimeoutOverrides(jobs, os.Environ())
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "job timeouts", slog.Group("timeouts", timeoutAttrs(jobs)...), "overridden", overridden)

	s := scheduler.New(logger)
	for _, job := range jobs {
		s.Register(job)
	}
	el := leader.New(spannerdb.NewLeaseStore(sc), holder, logger)
	hs := health.New(logger, s, sc, health.WithLease(el))
	addr, err := hs.Listen(viper.GetString("pipeline_health_addr"))
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "health server listening", "addr", addr.String())
	serve(ctx, logger, s, el, hs)
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

// startUpstream builds the rate-limited upstream client (and its archive, when configured) and
// logs its request budgets.
func startUpstream(ctx context.Context, logger *slog.Logger, cfg upstream.Config) (*upstream.Pipeline, error) {
	up, err := upstream.NewPipeline(ctx, logger, cfg)
	if err != nil {
		return nil, err
	}
	up.LogBudgets(ctx, logger)
	return up, nil
}

// enableSummarizer gives svc the Vertex AI summarizer and the summary job's settings when a
// project is configured. A bad AI or job config is an error; a summarizer that fails to start is
// logged and the pipeline runs without it.
func enableSummarizer(ctx context.Context, logger *slog.Logger, svc *psync.Service) error {
	aiCfg, err := ai.ConfigFrom(viper.GetString)
	if err != nil {
		return err
	}
	jobCfg, err := psync.SummaryJobConfigFrom(viper.GetString)
	if err != nil {
		return err
	}
	batchCfg, err := psync.SummaryBatchConfigFrom(viper.GetString)
	if err != nil {
		return err
	}
	svc.SetSummaryJob(jobCfg)
	if aiCfg.Project == "" {
		return nil
	}
	summarizer, err := ai.NewSummarizer(ctx, aiCfg)
	if err != nil {
		logger.WarnContext(ctx, "failed to create AI summarizer", "error", err)
		return nil
	}
	svc.SetSummarizer(summarizer)
	logger.InfoContext(ctx, "AI summarizer enabled", "project", aiCfg.Project, "location", aiCfg.Location,
		"model", aiCfg.Model, "thinking_level", aiCfg.ThinkingLevel, "request_type", aiCfg.RequestType,
		"crs_context", aiCfg.CRSContext(), "rule_context", aiCfg.RuleContext(),
		"batch", jobCfg.Batch, "workers", jobCfg.Workers,
		"daily_cap", jobCfg.DailyCap, "recent_days", jobCfg.RecentDays,
		"resummarize_on_prompt_change", jobCfg.ResummarizeOnPromptChange,
		"diff_summaries", jobCfg.DiffsEnabled, "law_batch", jobCfg.LawBatch, "law_daily_cap", jobCfg.LawDailyCap)
	enableSummaryBatches(ctx, logger, svc, batchCfg, aiCfg)
	return nil
}

// enableSummaryBatches turns on polling of Vertex AI batch jobs in sync-summaries when
// AI_BATCH_BUCKET is set (docs/design/198-corpus-resummarization.md). A client that fails to start
// is logged, and serve runs without polling: the batches' holds run out on their own.
func enableSummaryBatches(
	ctx context.Context, logger *slog.Logger, svc *psync.Service, cfg psync.SummaryBatchConfig, aiCfg ai.Config,
) {
	if cfg.Bucket == "" {
		return
	}
	jobs, err := ai.NewBatchJobs(ctx, aiCfg.Project, cfg.Location)
	if err != nil {
		logger.WarnContext(ctx, "failed to create the batch job client", "error", err)
		return
	}
	files, err := gcsblob.New(ctx)
	if err != nil {
		logger.WarnContext(ctx, "failed to create the batch file store", "error", err)
		return
	}
	svc.SetSummaryBatches(cfg, aiCfg, jobs, files)
	logger.InfoContext(ctx, "summary batch polling enabled", "bucket", cfg.Bucket, "location", cfg.Location,
		"hold", cfg.Hold.String())
}

// enableSources gives svc the Federal Register client, which needs no key, and the GovInfo
// client when a GovInfo API key is configured.
func enableSources(
	ctx context.Context, logger *slog.Logger, svc *psync.Service, up *upstream.Pipeline, apiKey string,
) {
	svc.SetFederalRegister(fedreg.NewClient(up.Client))
	if apiKey != "" {
		svc.SetGovInfo(govinfo.NewClient(up.Client))
		logger.InfoContext(ctx, "GovInfo client enabled")
	}
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

// serve campaigns for the pipeline lease and runs the jobs while this process holds it
// (docs/design/80-pipeline-operability.md, Decision 1A), until SIGINT or SIGTERM. Then it follows
// the shutdown sequence ("Shutdown sequence"): mark the process not ready, cancel the running
// jobs, wait up to shutdownWait for them to return, release the lease with a fresh context, and
// stop the health server. The caller closes Spanner after it returns.
func serve(ctx context.Context, logger *slog.Logger, s *scheduler.Scheduler, el *leader.Elector, hs *health.Server) {
	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	elected := make(chan struct{})
	go func() {
		defer close(elected)
		el.Run(sigCtx, leadJobs(sigCtx, logger, s))
	}()
	<-sigCtx.Done()
	stop() // a second signal kills the process
	hs.Drain()
	logger.InfoContext(ctx, "shutting down pipeline service", "wait", shutdownWait.String())
	// Run returns once the jobs have (or shutdownWait has passed) and the lease is released.
	<-elected

	healthCtx, cancelHealth := context.WithTimeout(ctx, healthShutdownWait)
	defer cancelHealth()
	if err := hs.Shutdown(healthCtx); err != nil {
		logger.WarnContext(ctx, "health server shutdown", "error", err.Error())
	}
}

// leadJobs returns the elector's lead function: run the jobs while leaderCtx lasts, then wait for
// them. After a lost lease it waits for every job, however long, so the elector doesn't campaign
// again while one still runs and no job ever runs twice here; /healthz reports a job that ignores
// cancellation. On shutdown (sigCtx done), it waits at most shutdownWait and names stragglers.
func leadJobs(sigCtx context.Context, logger *slog.Logger, s *scheduler.Scheduler) func(context.Context) {
	return func(leaderCtx context.Context) {
		s.Start(leaderCtx)
		<-leaderCtx.Done()
		if sigCtx.Err() == nil {
			s.Wait(sigCtx)
			if sigCtx.Err() == nil {
				return
			}
		}
		waitCtx, cancel := context.WithTimeout(context.WithoutCancel(sigCtx), shutdownWait)
		defer cancel()
		if running := s.Wait(waitCtx); len(running) > 0 {
			logger.WarnContext(waitCtx, "jobs still running at shutdown", "jobs", running)
		}
	}
}

// jobTable returns serve's jobs: the sync jobs, with AI_SUMMARY_INTERVAL applied and without
// sync-cra-rules when PIPELINE_CRA_RULES is false, and the aggregate-votes job with its AGG_* rules.
func jobTable(
	svc *psync.Service,
	store *spannerdb.PipelineStoreImpl,
	pinned int,
	logger *slog.Logger,
) ([]scheduler.Job, error) {
	jobs, err := withSummaryInterval(
		syncJobs(svc, newCongressTracker(svc, pinned, logger), uscode.NewLoader(store, logger)),
		viper.GetString("ai_summary_interval"))
	if err != nil {
		return nil, err
	}
	if jobs, err = withCRARules(jobs, viper.GetString("pipeline_cra_rules")); err != nil {
		return nil, err
	}
	agg, err := aggregateJob(store, viper.GetString, logger)
	if err != nil {
		return nil, err
	}
	return append(jobs, agg), nil
}

// withCRARules returns jobs without sync-cra-rules when value (PIPELINE_CRA_RULES) is false. Empty
// means true; anything that isn't a boolean is an error.
func withCRARules(jobs []scheduler.Job, value string) ([]scheduler.Job, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return jobs, nil
	}
	on, err := strconv.ParseBool(value)
	if err != nil {
		return nil, fmt.Errorf("PIPELINE_CRA_RULES %q: want true or false", value)
	}
	if on {
		return jobs, nil
	}
	return slices.DeleteFunc(slices.Clone(jobs), func(j scheduler.Job) bool { return j.Name == craRulesJobName }), nil
}

// aggregateJob returns the hourly aggregate-votes job, with the rules read through get.
func aggregateJob(store aggregates.Store, get func(key string) string, logger *slog.Logger) (scheduler.Job, error) {
	cfg, err := aggregates.FromConfig(get)
	if err != nil {
		return scheduler.Job{}, err
	}
	job, err := aggregates.New(store, cfg, logger)
	if err != nil {
		return scheduler.Job{}, err
	}
	return scheduler.Job{
		Name:     aggregateJobName,
		Interval: aggregateInterval,
		Timeout:  aggregateTimeout,
		Run:      job.Run,
	}, nil
}

// syncJobs returns the sync jobs. Each sync run syncs the tracker's congresses in turn; the US
// Code load uses no Congress.gov or GovInfo quota.
func syncJobs(svc *psync.Service, tracker *congressTracker, usc *uscode.Loader) []scheduler.Job {
	return []scheduler.Job{
		{
			Name: "sync-bills", Interval: billSyncInterval, Timeout: billSyncTimeout,
			Run: func(ctx context.Context) error {
				return tracker.forEach(ctx, func(ctx context.Context, c int) error { return svc.SyncBills(ctx, c, 0) })
			},
		},
		{
			Name: "sync-members", Interval: memberSyncInterval, Timeout: memberSyncTimeout,
			Run: func(ctx context.Context) error {
				return tracker.forEach(ctx, func(ctx context.Context, c int) error {
					if err := svc.SyncMembers(ctx, c); err != nil {
						return err
					}
					return tracker.MembersSynced(ctx, c)
				})
			},
		},
		{
			Name: "sync-votes", Interval: voteSyncInterval, Timeout: voteSyncTimeout,
			Run: func(ctx context.Context) error {
				return tracker.forEach(ctx, func(ctx context.Context, c int) error {
					return svc.SyncVotes(ctx, c, rollcall.Sessions(c, time.Now()))
				})
			},
		},
		{
			Name: "sync-texts", Interval: textSyncInterval, Timeout: textSyncTimeout,
			Run: func(ctx context.Context) error {
				return tracker.forEach(
					ctx,
					func(ctx context.Context, c int) error { return svc.SyncBillTexts(ctx, c, 0) },
				)
			},
		},
		{
			Name: "sync-summaries", Interval: summarySyncInterval, Timeout: summarySyncTimeout,
			Run: func(ctx context.Context) error {
				return tracker.forEach(ctx, func(ctx context.Context, c int) error {
					return svc.SyncSummaries(ctx, c, 0)
				})
			},
		},
		{
			Name: "sync-law-changes", Interval: lawChangeSyncInterval, Timeout: lawChangeSyncTimeout,
			Run: func(ctx context.Context) error {
				return tracker.forEach(ctx, func(ctx context.Context, c int) error {
					return svc.SyncLawChanges(ctx, c, 0)
				})
			},
		},
		{
			Name: "sync-govinfo", Interval: govinfoSyncInterval, Timeout: govinfoSyncTimeout,
			Run: func(ctx context.Context) error {
				return tracker.forEach(
					ctx,
					func(ctx context.Context, c int) error { return svc.SyncGovInfoChanges(ctx, c) },
				)
			},
		},
		{
			Name: "load-uscode", Interval: uscodeLoadInterval, RetryInterval: uscodeRetryInterval,
			Timeout: uscodeLoadTimeout, Run: usc.Run,
		},
		{
			Name: "sync-gao", Interval: gaoSyncInterval, Timeout: gaoSyncTimeout,
			Run: func(ctx context.Context) error {
				return tracker.forEach(ctx, func(ctx context.Context, c int) error {
					return svc.SyncGAOReports(ctx, c, gaoBillsPerRun)
				})
			},
		},
		{
			Name: "sync-crs-summaries", Interval: crsSyncInterval, Timeout: crsSyncTimeout,
			Run: func(ctx context.Context) error {
				return tracker.forEach(ctx, svc.SyncCRSSummaries)
			},
		},
		{
			Name: craRulesJobName, Interval: craRulesInterval, Timeout: craRulesTimeout,
			Run: func(ctx context.Context) error {
				return tracker.forEach(ctx, func(ctx context.Context, c int) error {
					return svc.SyncCRARules(ctx, c, craRulesPerRun)
				})
			},
		},
	}
}
