// Summarize-eval runs the bill summarizer on a fixed list of 50 bills with one or more
// models and writes results.jsonl plus a Markdown report per model (design 68, item 2). The model
// calls go through the same ai.Summarizer code as the pipeline's serve command.
//
// Through a local Gemini-API-compatible proxy, which needs no GCP credentials:
//
//	go run ./cmd/summarize-eval --models gemini-3.5-flash,gemini-3.8-flash --base-url http://127.0.0.1:8765
//
// With --crs each model runs twice, without and with the bills' CRS summaries in the prompt
// (reported as <model> and <model>+crs), and each report gives the share of 8-word sequences copied
// from the CRS text (design 197, item 4; the list is testdata/bills-crs.json):
//
//	go run ./cmd/summarize-eval --models gemini-3.8-flash --crs --bills cmd/summarize-eval/testdata/bills-crs.json \
//	  --base-url http://127.0.0.1:8765
//
// With --rule each model runs twice, without and with the <disapproved_rule> block (reported as
// <model> and <model>+rule), on CRA resolutions whose rule the list itself carries, and each report
// adds signals and a rubric for the rule (design 590; the list is testdata/bills-cra.json). --crs
// and --rule can't be combined:
//
//	go run ./cmd/summarize-eval --models gemini-3.8-flash --rule --bills cmd/summarize-eval/testdata/bills-cra.json \
//	  --base-url http://127.0.0.1:8765
//
// Without --base-url it calls Vertex AI with Application Default Credentials and GCP_PROJECT. A
// rerun skips bills that already have a result (other than an error), so a run stopped by a
// quota resumes the next day.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/genai"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

const (
	// serviceName is the pipeline's service.name, shared with serve and backfill.
	serviceName    = "justabill-pipeline"
	defaultModels  = "gemini-3.5-flash,gemini-3.8-flash"
	defaultBills   = "cmd/summarize-eval/testdata/bills.json"
	defaultWorkers = 4
	govinfoBaseURL = "https://api.govinfo.gov"
	// govinfoPause spaces the (at most 50) text downloads, to stay well under the key's rate limit.
	govinfoPause = 500 * time.Millisecond
	// congressPause spaces the CRS summary downloads (--crs), one per bill.
	congressPause = 500 * time.Millisecond
)

type options struct {
	models     []string
	crs        bool
	rule       bool
	baseURL    string
	billsPath  string
	outDir     string
	cacheDir   string
	workers    int
	limit      int
	reportOnly bool
}

func main() {
	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AddConfigPath("..")
	_ = viper.ReadInConfig()
	viper.AutomaticEnv()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	// obs.Main logs through the obs logger, logs a failure and flushes telemetry before it returns.
	code := obs.Main(ctx, obs.Config{Service: serviceName, Environment: viper.GetString("app_env")},
		func(ctx context.Context, logger *slog.Logger) error { return newCommand(logger).ExecuteContext(ctx) })
	stop()
	os.Exit(code)
}

// newCommand is the command line, logging to logger.
func newCommand(logger *slog.Logger) *cobra.Command {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		cacheRoot = os.TempDir()
	}
	cacheRoot = filepath.Join(cacheRoot, "justabill", "summarize-eval")

	var opts options
	var models string
	cmd := &cobra.Command{
		Use:   "summarize-eval",
		Short: "Summarize the committed 50-bill list with each model and report quality, cost and latency",
		// obs.Main logs the error.
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.models = splitModels(models)
			return run(cmd.Context(), logger, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&models, "models", defaultModels, "comma-separated model IDs")
	f.StringVar(&opts.baseURL, "base-url", "",
		"Gemini API-style endpoint, e.g. a local proxy http://127.0.0.1:8765 (empty: Vertex AI with ADC)")
	f.StringVar(&opts.billsPath, "bills", defaultBills, "the bill list")
	f.StringVar(&opts.outDir, "out", filepath.Join(cacheRoot, "out"), "where results.jsonl and the reports go")
	f.StringVar(&opts.cacheDir, "cache", filepath.Join(cacheRoot, "texts"), "where downloaded bill texts are kept")
	f.IntVar(&opts.workers, "workers", defaultWorkers, "concurrent model calls")
	f.IntVar(&opts.limit, "limit", 0, "only the first N bills of the list (0: all)")
	f.BoolVar(&opts.reportOnly, "report-only", false, "rewrite the reports from results.jsonl without calling a model")
	f.BoolVar(&opts.crs, "crs", false,
		"run each model without and with the bills' CRS summaries (<model> and <model>+crs) and report copying")
	f.BoolVar(&opts.rule, "rule", false,
		"run each model without and with the rules CRA resolutions disapprove (<model> and <model>+rule)")
	return cmd
}

func splitModels(s string) []string {
	var out []string
	for m := range strings.SplitSeq(s, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

func run(ctx context.Context, logger *slog.Logger, opts options) error {
	if len(opts.models) == 0 {
		return errors.New("--models: name at least one model")
	}
	if opts.crs && opts.rule {
		return errors.New("--crs and --rule: choose one")
	}
	bills, err := loadBills(opts.billsPath)
	if err != nil {
		return err
	}
	if opts.limit > 0 && opts.limit < len(bills) {
		bills = bills[:opts.limit]
	}
	if err = os.MkdirAll(opts.outDir, 0o750); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	res, err := openResults(filepath.Join(opts.outDir, "results.jsonl"))
	if err != nil {
		return err
	}

	var runErr error
	if !opts.reportOnly {
		runErr = summarizeAll(ctx, opts, bills, res, logger)
	}
	if err = writeReports(opts.outDir, opts.labels(), bills, res, time.Now()); err != nil {
		return errors.Join(runErr, err)
	}
	logger.InfoContext(ctx, "reports written", "dir", opts.outDir)
	return runErr
}

// labels names the runs: each model, and with --crs or --rule each model with that context too.
func (o options) labels() []string {
	var out []string
	for _, m := range o.models {
		for _, v := range o.variants() {
			out = append(out, v.label(m))
		}
	}
	return out
}

// variants lists one model's runs: without extra context, then with the CRS summaries (--crs) or
// the disapproved rules (--rule).
func (o options) variants() []variant {
	switch {
	case o.crs:
		return []variant{{}, {crs: true}}
	case o.rule:
		return []variant{{}, {rule: true}}
	default:
		return []variant{{}}
	}
}

// summarizeAll runs each model in turn. A model that can't go on (not served, budget spent)
// doesn't stop the others.
func summarizeAll(ctx context.Context, opts options, bills []evalBill, res *results, logger *slog.Logger) error {
	upCfg, err := upstream.ConfigFrom(viper.GetString)
	if err != nil {
		return err
	}
	up, err := upstream.NewPipeline(ctx, logger, upCfg)
	if err != nil {
		return err
	}
	defer up.CloseWithin(ctx, logger, time.Minute)
	cache := &textCache{
		dir:     opts.cacheDir,
		baseURL: govinfoBaseURL,
		fetch:   govinfo.NewClient(up.Client),
		pause:   govinfoPause,
	}
	texts, err := cache.load(ctx, bills)
	if err != nil {
		return err
	}
	var crs map[string]*ai.CRSContext
	if opts.crs {
		crsFiles := &crsCache{dir: opts.cacheDir, fetch: congress.NewClient(up.Client), pause: congressPause}
		if crs, err = crsFiles.load(ctx, bills); err != nil {
			return err
		}
	}
	cfg, err := ai.ConfigFrom(viper.GetString)
	if err != nil {
		return fmt.Errorf("ai config: %w", err)
	}
	r := &runner{
		bills: bills, texts: texts, crs: crs, results: res, check: newChecker(),
		workers: opts.workers, logger: logger, now: time.Now,
	}
	var stopped error
	for _, model := range opts.models {
		cfg.Model = model
		s, sErr := newSummarizer(ctx, opts.baseURL, cfg)
		if sErr != nil {
			return sErr
		}
		for _, v := range opts.variants() {
			r.variant = v
			err = r.run(ctx, s)
			switch {
			case errors.Is(err, errStopModel):
				logger.WarnContext(ctx, "model stopped early; rerun to resume", "model", model, "error", err)
				stopped = errors.Join(stopped, err)
			case err != nil:
				return err
			}
		}
	}
	return stopped
}

// newSummarizer builds the summarizer for cfg: through a Gemini API-style endpoint such as a
// local proxy when baseURL is set, otherwise on Vertex AI.
func newSummarizer(ctx context.Context, baseURL string, cfg ai.Config) (*ai.Summarizer, error) {
	if baseURL == "" {
		s, err := ai.NewSummarizer(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("summarizer: %w", err)
		}
		return s, nil
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend: genai.BackendGeminiAPI,
		// The proxy authenticates upstream itself; the SDK only needs a non-empty key.
		APIKey:      "unused",
		HTTPOptions: genai.HTTPOptions{BaseURL: baseURL, APIVersion: "v1"},
	})
	if err != nil {
		return nil, fmt.Errorf("gemini client: %w", err)
	}
	s, err := ai.NewSummarizerWithClient(client, cfg)
	if err != nil {
		return nil, fmt.Errorf("summarizer: %w", err)
	}
	return s, nil
}

// writeReports writes report-<label>.md for each run and compare.md across them.
func writeReports(dir string, labels []string, bills []evalBill, res *results, now time.Time) error {
	reports := make([]modelReport, 0, len(labels))
	for _, label := range labels {
		r := newModelReport(label, bills, res.latest(label))
		reports = append(reports, r)
		if err := writeFile(filepath.Join(dir, "report-"+label+".md"), func(f *os.File) error {
			return writeModelReport(f, r, now)
		}); err != nil {
			return err
		}
	}
	return writeFile(filepath.Join(dir, "compare.md"), func(f *os.File) error {
		return writeComparison(f, reports)
	})
}

func writeFile(path string, write func(*os.File) error) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err = write(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
