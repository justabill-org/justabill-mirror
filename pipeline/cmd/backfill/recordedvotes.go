package main

import (
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/obs"
	psync "github.com/justabill-org/justabill/pipeline/internal/sync"
)

func newRecordedVotesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recorded-votes",
		Short: "Fetch the actions of every bill with a stored roll call again, so they link it (#1046)",
		Long: "Fetches only /bill/{congress}/{type}/{number}/actions from Congress.gov for each stored bill one\n" +
			"congress's recorded roll calls name, and replaces its actions, unrecorded passages and status\n" +
			"history as the bills sync does, so actions stored before the sync kept their roll calls link them\n" +
			"(bill_actions.recorded_vote). About one request per bill. Needs CONGRESS_API_KEY (or its _FILE).\n" +
			"A failed bill doesn't stop the others; the run then exits non-zero. Runs under the pipeline\n" +
			"lease, so never alongside serve or a load Job. Safe to rerun; an interrupted run resumes from its\n" +
			"sync_state checkpoint (step \"recorded_votes\").",
		RunE: runRecordedVotes,
	}
	cmd.Flags().Int("congress", 0, congressUsage)
	cmd.Flags().Int("page-size", psync.DefaultRecordedVotesPageSize, "Bills per page and per checkpoint")
	cmd.Flags().Bool("wait-for-lease", false,
		"Wait for the pipeline lease when another process (serve, a load Job) holds it, instead of exiting")
	return cmd
}

// recordedVotesOptions are the recorded-votes subcommand's flags, parsed and checked.
type recordedVotesOptions struct {
	congress int
	pageSize int
	wait     bool
}

// readRecordedVotesFlags reads the subcommand's flags at time now: --congress 0 is the congress in
// progress.
func readRecordedVotesFlags(cmd *cobra.Command, now time.Time) (recordedVotesOptions, error) {
	flagCongress, err := cmd.Flags().GetInt("congress")
	if err != nil {
		return recordedVotesOptions{}, err
	}
	var o recordedVotesOptions
	if o.congress, err = resolveCongress(flagCongress, false, now); err != nil {
		return recordedVotesOptions{}, err
	}
	if o.pageSize, err = cmd.Flags().GetInt("page-size"); err != nil {
		return recordedVotesOptions{}, err
	}
	if o.pageSize <= 0 {
		return recordedVotesOptions{}, fmt.Errorf("--page-size %d: must be at least 1", o.pageSize)
	}
	if o.wait, err = cmd.Flags().GetBool("wait-for-lease"); err != nil {
		return recordedVotesOptions{}, err
	}
	return o, nil
}

// runRecordedVotes fetches the voted bills' actions again. Unlike the stored-data subcommands it
// calls Congress.gov, so it builds the full sync service (with the API cache and web revalidation
// hooks, so stored bills refresh) and, since it rewrites what the bills sync writes, holds the lease.
func runRecordedVotes(cmd *cobra.Command, _ []string) error {
	opts, err := readRecordedVotesFlags(cmd, time.Now())
	if err != nil {
		return err
	}
	tel, err := startTelemetry()
	if err != nil {
		return err
	}
	defer tel.ShutdownWithin(obs.ShutdownTimeout)
	logger := tel.Logger()

	upCfg, err := upstreamConfig()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	sc, err := spannerdb.NewClient(ctx, viper.GetString("spanner_project"),
		viper.GetString("spanner_instance"), viper.GetString("spanner_database"), spannerdb.WithBatchPriority())
	if err != nil {
		return err
	}
	defer sc.Close()

	svc, api, up, err := newService(ctx, logger, sc, upCfg)
	if err != nil {
		return err
	}
	defer up.CloseWithin(ctx, logger, archiveFlushWait)

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	leaseCtx, release, err := holdLease(sigCtx, logger, spannerdb.NewLeaseStore(sc), opts.wait)
	if err != nil {
		return err
	}
	defer release()

	start := time.Now()
	err = svc.BackfillRecordedVotes(leaseCtx, opts.congress, opts.pageSize)
	status := "ok"
	if err != nil {
		status = "failed"
	}
	logger.InfoContext(ctx, "recorded votes summary", "congress", opts.congress, "status", status,
		"congress_gov_requests", api.Requests(), "elapsed", time.Since(start).Round(time.Second).String())
	return err
}
