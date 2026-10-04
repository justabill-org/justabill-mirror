// Command pipeline-aggregates is the operator's tool for the user-vote aggregates
// (docs/design/89-aggregate-analytics.md, item 6; runbook docs/runbooks/aggregates.md):
//
//	pipeline-aggregates report [--all]            # dry run of the aggregate-votes job: nothing written
//	pipeline-aggregates hold --bill hr-119-1 [--cell CA-12]
//	pipeline-aggregates release --bill hr-119-1 [--cell CA-12]
//	pipeline-aggregates exclude --provider password --created-from T --created-to T [--dry-run]
//
// report reads the same AGG_* rules as serve and prints what the next run would publish, suppress
// and hold. hold and release change stored cells in one read-write transaction, so they're safe
// while serve runs the job. exclude marks a cohort of accounts (a sign-in provider and a creation
// time range) so their votes stop counting at the next run. Output names bills, cells and counts,
// never a user.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/aggregates"
)

// serviceName is the pipeline's service.name, shared with serve and backfill.
const serviceName = "justabill-pipeline"

// store is what the commands read and write.
type store interface {
	aggregates.Store
	CountAggregateCohort(ctx context.Context, c repository.AggregateCohort) (int64, error)
	ExcludeAggregateCohort(ctx context.Context, c repository.AggregateCohort, at time.Time) (int64, error)
}

// openStore connects to the store; the caller calls close when done.
type openStore func(ctx context.Context) (s store, closeFn func(), err error)

func main() {
	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AddConfigPath("..")
	_ = viper.ReadInConfig()

	viper.AutomaticEnv()

	os.Exit(obs.Main(context.Background(), obs.Config{Service: serviceName, Environment: viper.GetString("app_env")},
		func(ctx context.Context, logger *slog.Logger) error {
			return newCommand(logger, openSpanner, time.Now).ExecuteContext(ctx)
		}))
}

// openSpanner opens the pipeline store on the database the SPANNER_* settings or flags name.
func openSpanner(ctx context.Context) (store, func(), error) {
	sc, err := spannerdb.NewClient(ctx, viper.GetString("spanner_project"),
		viper.GetString("spanner_instance"), viper.GetString("spanner_database"), spannerdb.WithBatchPriority())
	if err != nil {
		return nil, nil, err
	}
	return spannerdb.NewPipelineStore(sc), sc.Close, nil
}

// newCommand is the command line, logging to logger.
func newCommand(logger *slog.Logger, open openStore, now func() time.Time) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "pipeline-aggregates",
		Short: "Report on, hold, release and exclude votes from the user-vote aggregates",
		// obs.Main logs the error.
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	rootCmd.PersistentFlags().String("spanner-project", "", "GCP project for Spanner")
	rootCmd.PersistentFlags().String("spanner-instance", "", "Spanner instance ID")
	rootCmd.PersistentFlags().String("spanner-database", "", "Spanner database name")
	_ = viper.BindPFlag("spanner_project", rootCmd.PersistentFlags().Lookup("spanner-project"))
	_ = viper.BindPFlag("spanner_instance", rootCmd.PersistentFlags().Lookup("spanner-instance"))
	_ = viper.BindPFlag("spanner_database", rootCmd.PersistentFlags().Lookup("spanner-database"))

	app := &app{log: logger, open: open, now: now}
	rootCmd.AddCommand(app.reportCommand(), app.holdCommand(true), app.holdCommand(false), app.excludeCommand())
	return rootCmd
}

// app runs the subcommands.
type app struct {
	log  *slog.Logger
	open openStore
	now  func() time.Time
}

// withStore opens the store, runs fn and closes it.
func (a *app) withStore(ctx context.Context, fn func(s store) error) error {
	s, closeFn, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	return fn(s)
}

func (a *app) reportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Print what the next aggregate-votes run would publish, suppress and hold (writes nothing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, err := cmd.Flags().GetBool("all")
			if err != nil {
				return err
			}
			cfg, err := aggregates.FromConfig(viper.GetString)
			if err != nil {
				return err
			}
			return a.withStore(cmd.Context(), func(s store) error {
				job, jobErr := aggregates.New(s, cfg, a.log, aggregates.WithClock(a.now))
				if jobErr != nil {
					return jobErr
				}
				res, runErr := job.Report(cmd.Context())
				if runErr != nil {
					return runErr
				}
				return writeReport(cmd.OutOrStdout(), res, a.now().UTC(), all)
			})
		},
	}
	cmd.Flags().Bool("all", false, "List every cell, not only those on hold or newly held")
	return cmd
}

// holdCommand is hold, or release when hold is false.
func (a *app) holdCommand(hold bool) *cobra.Command {
	use, short, apply := "hold", "Put a bill's cells (or one cell) on a manual hold", aggregates.Hold
	if !hold {
		use, short, apply = "release", "Release a bill's cells (or one cell) from their hold", aggregates.Release
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			billID, cell, err := cellFlags(cmd)
			if err != nil {
				return err
			}
			return a.withStore(cmd.Context(), func(s store) error {
				changes, applyErr := apply(cmd.Context(), s, billID, cell)
				if applyErr != nil {
					return applyErr
				}
				changed := writeChanges(cmd.OutOrStdout(), changes)
				a.log.InfoContext(cmd.Context(), "aggregate cells "+use, "bill_id", billID,
					"cells", len(changes), "changed", changed)
				return nil
			})
		},
	}
	cmd.Flags().String("bill", "", "Bill ID, e.g. hr-119-1 (required)")
	cmd.Flags().String("cell", "", "Only this cell: national, a state (CA) or a district (CA-12)")
	return cmd
}

// cellFlags reads --bill and --cell.
func cellFlags(cmd *cobra.Command) (string, *aggregates.CellKey, error) {
	billID, err := cmd.Flags().GetString("bill")
	if err != nil {
		return "", nil, err
	}
	if billID == "" {
		return "", nil, errors.New("--bill is required")
	}
	name, err := cmd.Flags().GetString("cell")
	if err != nil || name == "" {
		return billID, nil, err
	}
	cell, err := aggregates.ParseCell(billID, name)
	if err != nil {
		return "", nil, fmt.Errorf("--cell: %w", err)
	}
	return billID, &cell, nil
}

func (a *app) excludeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exclude",
		Short: "Stop counting the votes of a cohort: one sign-in provider, created in [--created-from, --created-to)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cohort, err := cohortFlags(cmd)
			if err != nil {
				return err
			}
			dryRun, err := cmd.Flags().GetBool("dry-run")
			if err != nil {
				return err
			}
			return a.withStore(cmd.Context(), func(s store) error {
				return a.exclude(cmd, s, cohort, dryRun)
			})
		},
	}
	cmd.Flags().
		String("provider", "", "Sign-in provider, as recorded from the token (password, google.com, apple.com, ...)")
	cmd.Flags().
		String("created-from", "", "Accounts created at or after this time (RFC 3339, e.g. 2026-10-05T14:00:00Z)")
	cmd.Flags().String("created-to", "", "Accounts created before this time (RFC 3339)")
	cmd.Flags().Bool("dry-run", false, "Only count the accounts that would be excluded")
	return cmd
}

func (a *app) exclude(cmd *cobra.Command, s store, cohort repository.AggregateCohort, dryRun bool) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	desc := fmt.Sprintf("sign-in provider %s, created from %s to %s", cohort.Provider,
		cohort.CreatedFrom.Format(time.RFC3339), cohort.CreatedTo.Format(time.RFC3339))
	if dryRun {
		n, err := s.CountAggregateCohort(ctx, cohort)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Would exclude %d accounts (%s). Nothing written.\n", n, desc)
		return err
	}
	at := a.now().UTC()
	n, err := s.ExcludeAggregateCohort(ctx, cohort, at)
	if err != nil {
		return err
	}
	a.log.InfoContext(ctx, "aggregate cohort excluded", "provider", cohort.Provider,
		"created_from", cohort.CreatedFrom, "created_to", cohort.CreatedTo, "excluded_at", at, "accounts", n)
	_, err = fmt.Fprintf(out, "Excluded %d accounts (%s) at %s. Their votes leave the aggregates at the next "+
		"aggregate-votes run; run report to see the effect.\n", n, desc, at.Format(time.RFC3339Nano))
	return err
}

// cohortFlags reads --provider, --created-from and --created-to.
func cohortFlags(cmd *cobra.Command) (repository.AggregateCohort, error) {
	var c repository.AggregateCohort
	var err error
	if c.Provider, err = cmd.Flags().GetString("provider"); err != nil {
		return c, err
	}
	if c.Provider == "" {
		return c, errors.New("--provider is required")
	}
	for _, f := range []struct {
		name string
		dst  *time.Time
	}{{"created-from", &c.CreatedFrom}, {"created-to", &c.CreatedTo}} {
		raw, flagErr := cmd.Flags().GetString(f.name)
		if flagErr != nil {
			return c, flagErr
		}
		if *f.dst, err = time.Parse(time.RFC3339, raw); err != nil {
			return c, fmt.Errorf("--%s %q: want an RFC 3339 time, e.g. 2026-10-05T14:00:00Z", f.name, raw)
		}
	}
	if !c.CreatedFrom.Before(c.CreatedTo) {
		return c, errors.New("--created-from must be before --created-to")
	}
	return c, nil
}
