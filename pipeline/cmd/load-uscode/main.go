// Load-uscode loads the current US Code release point from the Law Revision Counsel into
// Spanner once and exits. It does nothing if that release point is already loaded, unless
// --force is set, and then writes only the sections whose content changed. The pipeline service
// runs the same load weekly; this command is for a first load or a manual refresh.
//
//	go run ./cmd/load-uscode
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/uscode"
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
		Use:   "pipeline-load-uscode",
		Short: "Load the current US Code release point (uscode.house.gov USLM) into Spanner",
		// obs.Main logs the error.
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLoad(cmd, logger)
		},
	}

	rootCmd.Flags().String("spanner-project", "", "GCP project for Spanner")
	rootCmd.Flags().String("spanner-instance", "", "Spanner instance ID")
	rootCmd.Flags().String("spanner-database", "", "Spanner database name")
	rootCmd.Flags().Bool("force", false,
		"Download and parse the release point even if it's loaded already (still writes only changed sections)")
	rootCmd.Flags().String("page-url", uscode.DefaultPageURL, "Download page that links the current release point")

	_ = viper.BindPFlag("spanner_project", rootCmd.Flags().Lookup("spanner-project"))
	_ = viper.BindPFlag("spanner_instance", rootCmd.Flags().Lookup("spanner-instance"))
	_ = viper.BindPFlag("spanner_database", rootCmd.Flags().Lookup("spanner-database"))
	return rootCmd
}

func runLoad(cmd *cobra.Command, logger *slog.Logger) error {
	force, err := cmd.Flags().GetBool("force")
	if err != nil {
		return err
	}
	pageURL, err := cmd.Flags().GetString("page-url")
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

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	loader := uscode.NewLoader(spannerdb.NewPipelineStore(sc), logger, uscode.WithPageURL(pageURL))
	_, err = loader.Load(sigCtx, force)
	return err
}
