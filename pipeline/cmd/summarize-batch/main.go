// Summarize-batch sends a whole-corpus re-summarization through Vertex AI batch inference, at
// half the synchronous price (docs/design/198-corpus-resummarization.md). submit exports the due
// bills of the bulk tiers to a batch job and holds them out of the synchronous queue; poll moves
// open batches on (store the job's state, import its results, or release its bills), as every
// sync-summaries run in serve also does. Both need AI_BATCH_BUCKET, except submit --dry-run,
// which only counts and prices the due bills.
//
//	go run ./cmd/summarize-batch submit --congress 119 --tiers other,prompt --max 50000 --dry-run \
//		--input-price 0.375 --output-price 1.875
//	go run ./cmd/summarize-batch poll
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/gcsblob"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
	psync "github.com/justabill-org/justabill/pipeline/internal/sync"
)

// serviceName is the pipeline's service.name, shared with serve and backfill.
const serviceName = "justabill-pipeline"

// defaultMax is submit's default --max: the largest re-run the design prices (~50,000 bills), well
// under a batch input's 200,000-request limit.
const defaultMax = 50_000

func main() {
	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AddConfigPath("..")
	_ = viper.ReadInConfig()

	viper.AutomaticEnv()

	os.Exit(obs.Main(context.Background(), obs.Config{Service: serviceName, Environment: viper.GetString("app_env")},
		func(ctx context.Context, logger *slog.Logger) error { return newCommand(logger).ExecuteContext(ctx) }))
}

// newCommand is the command line, logging to logger.
func newCommand(logger *slog.Logger) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "pipeline-summarize-batch",
		Short:         "Re-summarize many bills through Vertex AI batch inference",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	rootCmd.PersistentFlags().String("spanner-project", "", "GCP project for Spanner")
	rootCmd.PersistentFlags().String("spanner-instance", "", "Spanner instance ID")
	rootCmd.PersistentFlags().String("spanner-database", "", "Spanner database name")
	_ = viper.BindPFlag("spanner_project", rootCmd.PersistentFlags().Lookup("spanner-project"))
	_ = viper.BindPFlag("spanner_instance", rootCmd.PersistentFlags().Lookup("spanner-instance"))
	_ = viper.BindPFlag("spanner_database", rootCmd.PersistentFlags().Lookup("spanner-database"))

	rootCmd.AddCommand(newSubmitCommand(logger), newPollCommand(logger))
	return rootCmd
}

func newSubmitCommand(logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Export the due bills of the given tiers to a Vertex AI batch job",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSubmit(cmd, logger)
		},
	}
	cmd.Flags().Int("congress", 0, "Congress whose bills to export (0 = the congress in progress)")
	cmd.Flags().String("tiers", "other,prompt", "Queue tiers to take due bills from: voted, recent, other, prompt")
	cmd.Flags().Int("max", defaultMax, "Most bills to export")
	cmd.Flags().Bool("dry-run", false, "Print the due bills per tier and an estimated cost, and change nothing")
	cmd.Flags().Float64("input-price", 0, "Batch price of 1M input tokens in USD, for the estimate")
	cmd.Flags().Float64("output-price", 0, "Batch price of 1M output tokens in USD, for the estimate")
	cmd.Flags().Int("output-tokens", psync.DefaultBatchOutputTokens, "Output tokens assumed per bill, for the estimate")
	return cmd
}

func newPollCommand(logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Store open batch jobs' states, import finished ones and release failed ones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPoll(cmd, logger)
		},
	}
	cmd.Flags().Int("congress", 0, "Only poll this congress's batches (0 = every congress)")
	return cmd
}

// submitFlags reads submit's flags into a request.
func submitFlags(cmd *cobra.Command) (psync.SummaryBatchRequest, error) {
	f := cmd.Flags()
	var req psync.SummaryBatchRequest
	var err error
	if req.Congress, err = f.GetInt("congress"); err != nil {
		return req, err
	}
	if req.Congress == 0 {
		req.Congress, _ = rollcall.Current(time.Now())
	}
	if req.Congress < 0 {
		return req, fmt.Errorf("--congress %d: must be positive", req.Congress)
	}
	tiers, err := f.GetString("tiers")
	if err != nil {
		return req, err
	}
	if req.Tiers, err = psync.ParseSummaryTiers(tiers); err != nil {
		return req, fmt.Errorf("--tiers: %w", err)
	}
	if req.Max, err = f.GetInt("max"); err != nil {
		return req, err
	}
	if req.Max <= 0 {
		return req, fmt.Errorf("--max %d: must be positive", req.Max)
	}
	if req.DryRun, err = f.GetBool("dry-run"); err != nil {
		return req, err
	}
	if req.InputPrice, err = f.GetFloat64("input-price"); err != nil {
		return req, err
	}
	if req.OutputPrice, err = f.GetFloat64("output-price"); err != nil {
		return req, err
	}
	req.OutputTokens, err = f.GetInt("output-tokens")
	return req, err
}

func runSubmit(cmd *cobra.Command, logger *slog.Logger) error {
	req, err := submitFlags(cmd)
	if err != nil {
		return err
	}
	svc, closeAll, err := newService(cmd.Context(), logger, !req.DryRun)
	if err != nil {
		return err
	}
	defer closeAll()

	sigCtx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	plan, err := svc.SubmitSummaryBatch(sigCtx, req)
	if plan != nil {
		printPlan(cmd.OutOrStdout(), req, plan)
	}
	return err
}

func runPoll(cmd *cobra.Command, logger *slog.Logger) error {
	congressNum, err := cmd.Flags().GetInt("congress")
	if err != nil {
		return err
	}
	svc, closeAll, err := newService(cmd.Context(), logger, true)
	if err != nil {
		return err
	}
	defer closeAll()

	sigCtx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return svc.PollSummaryBatches(sigCtx, congressNum)
}

// newService connects to Spanner and, when needBatch, to Vertex AI and Cloud Storage for the
// batch path, which needs AI_BATCH_BUCKET and GCP_PROJECT. The returned func closes what it opened.
func newService(ctx context.Context, logger *slog.Logger, needBatch bool) (*psync.Service, func(), error) {
	aiCfg, err := ai.ConfigFrom(viper.GetString)
	if err != nil {
		return nil, nil, err
	}
	jobCfg, err := psync.SummaryJobConfigFrom(viper.GetString)
	if err != nil {
		return nil, nil, err
	}
	batchCfg, err := psync.SummaryBatchConfigFrom(viper.GetString)
	if err != nil {
		return nil, nil, err
	}
	if needBatch && batchCfg.Bucket == "" {
		return nil, nil, psync.ErrBatchNotConfigured
	}

	sc, err := spannerdb.NewClient(ctx, viper.GetString("spanner_project"),
		viper.GetString("spanner_instance"), viper.GetString("spanner_database"), spannerdb.WithBatchPriority())
	if err != nil {
		return nil, nil, err
	}
	// psync.New logs to slog's default logger.
	slog.SetDefault(logger)
	svc := psync.New(spannerdb.NewPipelineStore(sc), nil, nil)
	svc.SetSummaryJob(jobCfg)
	closeAll := func() { sc.Close() }
	if !needBatch {
		svc.SetSummaryBatches(psync.SummaryBatchConfig{}, aiCfg, nil, nil)
		return svc, closeAll, nil
	}

	jobs, err := ai.NewBatchJobs(ctx, aiCfg.Project, batchCfg.Location)
	if err != nil {
		sc.Close()
		return nil, nil, err
	}
	files, err := gcsblob.New(ctx)
	if err != nil {
		sc.Close()
		return nil, nil, err
	}
	svc.SetSummaryBatches(batchCfg, aiCfg, jobs, files)
	logger.InfoContext(ctx, "summary batches enabled", "bucket", batchCfg.Bucket, "location", batchCfg.Location,
		"model", aiCfg.Model, "hold", batchCfg.Hold.String(), "min_bills", batchCfg.MinBills)
	return svc, func() { _ = files.Close(); sc.Close() }, nil
}

// printPlan writes what a submit exported, or on a dry run would export, and its estimate.
func printPlan(w io.Writer, req psync.SummaryBatchRequest, plan *psync.SummaryBatchPlan) {
	names := map[int]string{
		repository.SummaryTierVoted: "voted", repository.SummaryTierRecent: "recent",
		repository.SummaryTierOther: "other", repository.SummaryTierPromptChange: "prompt",
	}
	tiers := slices.Sorted(maps.Keys(plan.Due))
	for _, t := range tiers {
		_, _ = fmt.Fprintf(w, "due %-7s %d\n", names[t], plan.Due[t])
	}
	_, _ = fmt.Fprintf(w, "bills     %d (skipped, no text: %d)\n", plan.Bills, plan.Skipped)
	if req.DryRun {
		_, _ = fmt.Fprintf(w, "tokens    ~%d in, ~%d out (characters / 4; %d out per bill)\n",
			plan.InputTokens, plan.OutputTokens, req.OutputTokens)
		if req.InputPrice == 0 && req.OutputPrice == 0 {
			_, _ = fmt.Fprintln(w, "cost      pass --input-price and --output-price (USD per 1M tokens, batch price)")
		} else {
			_, _ = fmt.Fprintf(w, "cost      ~$%.2f\n", plan.Cost)
		}
		return
	}
	if plan.BatchID != "" {
		_, _ = fmt.Fprintf(w, "batch     %s\njob       %s\n", plan.BatchID, plan.JobName)
	}
}
