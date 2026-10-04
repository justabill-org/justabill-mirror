// Backfill-law-refs parses the US Code references of every bill text already stored for one
// congress and writes them to bill_law_refs. sync-texts does the same for each text it stores
// from now on. It makes no Congress.gov calls; with a GovInfo key it makes one GovInfo request
// per text version, for the MODS citations the XML didn't tag, paced by the pipeline's GovInfo
// budget (PIPELINE_GOVINFO_RPS and the other upstream settings). It's safe to rerun, and an
// interrupted run resumes from its sync_state checkpoint (step "law_refs").
//
//	go run ./cmd/backfill-law-refs --congress 119
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
	psync "github.com/justabill-org/justabill/pipeline/internal/sync"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

// serviceName is the pipeline's service.name, shared with serve and backfill.
const serviceName = "justabill-pipeline"

func main() {
	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AddConfigPath("..")
	_ = viper.ReadInConfig()

	viper.AutomaticEnv()

	// obs.Main logs through the obs logger, logs a failure and flushes telemetry before the
	// process exits.
	os.Exit(obs.Main(context.Background(), obs.Config{Service: serviceName, Environment: viper.GetString("app_env")},
		func(ctx context.Context, logger *slog.Logger) error { return newCommand(logger).ExecuteContext(ctx) }))
}

// newCommand is the command line, logging to logger.
func newCommand(logger *slog.Logger) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "pipeline-backfill-law-refs",
		Short: "Write bill_law_refs for the bill texts already stored (no Congress.gov calls)",
		// obs.Main logs the error.
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBackfill(cmd, logger)
		},
	}

	rootCmd.Flags().String("spanner-project", "", "GCP project for Spanner")
	rootCmd.Flags().String("spanner-instance", "", "Spanner instance ID")
	rootCmd.Flags().String("spanner-database", "", "Spanner database name")
	rootCmd.Flags().String("govinfo-api-key", "",
		"GovInfo API key for the MODS citations (default GOVINFO_API_KEY; without one, XML references only)")
	rootCmd.Flags().Int("congress", 0, "Congress whose bill texts to parse (0 = the congress in progress)")
	rootCmd.Flags().Int("page-size", psync.DefaultLawRefsPageSize, "Text versions per page and per checkpoint")

	_ = viper.BindPFlag("spanner_project", rootCmd.Flags().Lookup("spanner-project"))
	_ = viper.BindPFlag("spanner_instance", rootCmd.Flags().Lookup("spanner-instance"))
	_ = viper.BindPFlag("spanner_database", rootCmd.Flags().Lookup("spanner-database"))
	_ = viper.BindPFlag("govinfo_api_key", rootCmd.Flags().Lookup("govinfo-api-key"))
	return rootCmd
}

func runBackfill(cmd *cobra.Command, logger *slog.Logger) error {
	// psync.New logs to slog's default logger.
	slog.SetDefault(logger)

	congressNum, err := cmd.Flags().GetInt("congress")
	if err != nil {
		return err
	}
	if congressNum == 0 {
		congressNum, _ = rollcall.Current(time.Now())
	}
	if congressNum < 0 {
		return fmt.Errorf("--congress %d: must be positive", congressNum)
	}
	pageSize, err := cmd.Flags().GetInt("page-size")
	if err != nil {
		return err
	}
	upCfg, err := upstream.ConfigFrom(viper.GetString)
	if err != nil {
		return err
	}

	// The Spanner client closes last, so it doesn't get the signal context.
	ctx := cmd.Context()
	sc, err := spannerdb.NewClient(ctx, viper.GetString("spanner_project"),
		viper.GetString("spanner_instance"), viper.GetString("spanner_database"), spannerdb.WithBatchPriority())
	if err != nil {
		return err
	}
	defer sc.Close()

	svc := psync.New(spannerdb.NewPipelineStore(sc), nil, nil)
	if upCfg.GovInfoAPIKey != "" {
		up, upErr := upstream.NewPipeline(ctx, logger, upCfg)
		if upErr != nil {
			return upErr
		}
		defer up.CloseWithin(ctx, logger, time.Minute)
		up.LogBudgets(ctx, logger)
		svc.SetGovInfo(govinfo.NewClient(up.Client))
	} else {
		logger.WarnContext(ctx, "no GovInfo API key: references come from the bill XML only, without MODS")
	}

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return svc.BackfillLawRefs(sigCtx, congressNum, pageSize)
}
