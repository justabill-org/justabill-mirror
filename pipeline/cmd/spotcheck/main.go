// Command pipeline-spotcheck checks a loaded database against the official record before launch
// (design 77, item 3). It only reads: every database read goes through one read-only
// transaction, so in production it needs no more than roles/spanner.databaseReader.
//
// --rolls N samples N roll calls spread over House and Senate, both sessions, downloads each
// one's XML from clerk.house.gov or senate.gov, parses it with its own structs, and compares the
// question, result, date, totals, linked bill and every member's position with what's stored.
//
// --coverage counts what's loaded: bills per type against Congress.gov (eight requests), roll
// calls against the official lists, members, senators without an LIS ID, voted bills that are
// missing or have no summary, and text versions without text.
//
// It checks the congress in progress by the calendar unless --congress names another.
//
//	pipeline-spotcheck --rolls 20 --coverage
//
// It exits 0 when every check passes, 1 when one fails, and 2 when it can't run. The report goes
// to stdout; logs and telemetry go through obs (service justabill-pipeline), like serve's.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
	"github.com/justabill-org/justabill/pipeline/internal/secretfile"
)

const (
	// serviceName is the pipeline's service.name, shared with serve and backfill.
	serviceName = "justabill-pipeline"
	exitFailed  = 1
	exitError   = 2
)

// errChecksFailed means the checks ran and at least one failed.
var errChecksFailed = errors.New("spot-check failed")

type options struct {
	congress int
	rolls    int
	coverage bool
	cutLine  bool
	seed     uint64
}

func main() {
	var opts options
	v := viper.New()
	cmd := &cobra.Command{
		Use:           "pipeline-spotcheck",
		Short:         "Compare the loaded roll calls and counts with the official record (read-only)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), v, opts, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.IntVar(&opts.congress, "congress", 0, "congress to check (default: the congress in progress, by the calendar)")
	f.IntVar(&opts.rolls, "rolls", 0, "roll calls to sample and compare member by member (0: none)")
	f.BoolVar(&opts.coverage, "coverage", false, "check counts against Congress.gov and the official roll-call lists")
	f.BoolVar(&opts.cutLine, "cut-line", false,
		"with --coverage, report bills under 99% of Congress.gov as a warning instead of a failure")
	f.Uint64Var(&opts.seed, "seed", 0, "sampling seed, to repeat an earlier run's sample (0: a new one)")
	f.String("spanner-project", "", "GCP project for Spanner (SPANNER_PROJECT)")
	f.String("spanner-instance", "", "Spanner instance ID (SPANNER_INSTANCE)")
	f.String("spanner-database", "", "Spanner database name (SPANNER_DATABASE)")
	f.String("congress-api-key", "", "Congress.gov API key, for --coverage (CONGRESS_API_KEY)")
	// Each flag's value can also come from its environment variable or the .env file, whose keys
	// viper lowercases: spanner-project, SPANNER_PROJECT or spanner_project in .env.
	for _, name := range []string{"spanner-project", "spanner-instance", "spanner-database", "congress-api-key"} {
		key := configKey(name)
		_ = v.BindPFlag(key, f.Lookup(name))
		_ = v.BindEnv(key, strings.ToUpper(key))
	}
	_ = v.BindEnv("app_env", "APP_ENV")
	_ = v.BindEnv("congress_api_key_file", "CONGRESS_API_KEY_FILE")

	v.SetConfigName(".env")
	v.SetConfigType("env")
	v.AddConfigPath(".")
	v.AddConfigPath("..")
	_ = v.ReadInConfig()

	os.Exit(execute(cmd, obs.Config{Service: serviceName, Environment: v.GetString("app_env")}))
}

// execute runs cmd with telemetry set up from cfg and returns the exit code. It logs through obs
// and flushes telemetry before it returns.
func execute(cmd *cobra.Command, cfg obs.Config) int {
	tel, err := obs.Start(context.Background(), cfg)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "pipeline-spotcheck: setting up telemetry:", err)
		return exitError
	}
	defer tel.ShutdownWithin(obs.ShutdownTimeout)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return exitCode(ctx, tel.Logger(), cmd.ExecuteContext(ctx))
}

// exitCode is the exit code for the command's error, which it logs on logger: 0 when every check
// passed, exitFailed when one failed (the report on stdout says which), exitError otherwise.
func exitCode(ctx context.Context, logger *slog.Logger, err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errChecksFailed):
		logger.WarnContext(ctx, "spot-check failed; the report on stdout has the differences")
		return exitFailed
	default:
		logger.ErrorContext(ctx, "pipeline-spotcheck failed", slog.String("error", obs.Redact(err.Error())))
		return exitError
	}
}

// configKey is a flag's viper key: spanner-project is spanner_project.
func configKey(flag string) string { return strings.ReplaceAll(flag, "-", "_") }

func run(ctx context.Context, v *viper.Viper, opts options, out io.Writer) error {
	if opts.rolls <= 0 && !opts.coverage {
		return errors.New("nothing to check: pass --rolls N, --coverage or both")
	}
	key, err := secretfile.Resolve(v.GetString, configKey("congress-api-key"))
	if err != nil {
		return err
	}
	if opts.coverage && key == "" {
		return errors.New("--coverage needs --congress-api-key, CONGRESS_API_KEY or CONGRESS_API_KEY_FILE")
	}
	if opts.seed == 0 {
		opts.seed = rand.Uint64() //nolint:gosec // a sampling seed, not a secret
	}
	now := time.Now()
	if opts.congress, err = resolveCongress(opts.congress, now); err != nil {
		return err
	}

	sc, err := spannerdb.NewClient(
		ctx,
		v.GetString(
			configKey("spanner-project"),
		),
		v.GetString(configKey("spanner-instance")),
		v.GetString(configKey("spanner-database")),
		spannerdb.WithBatchPriority(),
	)
	if err != nil {
		return err
	}
	defer sc.Close()
	reader := spannerdb.NewSpotcheckReader(sc)
	defer reader.Close()

	c := &checker{
		opts: opts, reader: reader, src: defaultSources(), counter: newCongressCounter(key),
		cmp: newComparer(), now: now,
	}
	r, err := c.run(ctx, out)
	if err != nil {
		return err
	}
	return verdict(r)
}

// resolveCongress returns the congress to check: --congress, or when it's 0 the congress in
// progress at now (rollcall.Current).
func resolveCongress(congress int, now time.Time) (int, error) {
	switch {
	case congress < 0:
		return 0, fmt.Errorf("--congress %d: must be a congress number, or 0 for the congress in progress", congress)
	case congress > 0:
		return congress, nil
	}
	current, _ := rollcall.Current(now)
	return current, nil
}

// verdict is errChecksFailed when a check failed, so the command exits 1.
func verdict(r *report) error {
	if !r.passed() {
		return errChecksFailed
	}
	return nil
}

// checker runs the checks opts asks for.
type checker struct {
	opts    options
	reader  repository.SpotcheckReader
	src     sources
	counter billCounter
	cmp     comparer
	now     time.Time
}

// run prints each check's result to out. An error means a check couldn't run (the database or
// an official list couldn't be read); a failed check is in the report.
func (c *checker) run(ctx context.Context, out io.Writer) (*report, error) {
	r := &report{w: out}
	_, _ = fmt.Fprintf(out, "pipeline-spotcheck: congress %d, %s\n", c.opts.congress, c.now.UTC().Format(time.RFC3339))

	strata, err := c.src.strata(ctx, c.opts.congress, c.now)
	if err != nil {
		return nil, fmt.Errorf("official roll-call lists: %w", err)
	}
	if c.opts.rolls > 0 {
		if err = c.checkRolls(ctx, strata, r); err != nil {
			return nil, err
		}
	}
	if c.opts.coverage {
		if err = c.checkCoverage(ctx, strata, r); err != nil {
			return nil, err
		}
	}
	r.summary()
	return r, nil
}

func (c *checker) checkRolls(ctx context.Context, strata []stratum, r *report) error {
	picks := sample(strata, c.opts.rolls, rand.New(rand.NewPCG(c.opts.seed, c.opts.seed))) //nolint:gosec // sampling
	r.sectionf("Roll calls: %d requested, seed %d (repeat this sample with --seed %d)",
		c.opts.rolls, c.opts.seed, c.opts.seed)
	for i, st := range strata {
		if len(st.Numbers) == 0 {
			r.infof("%s: no roll calls yet", st)
			continue
		}
		for _, n := range picks[i] {
			if err := c.checkRoll(ctx, st, n, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *checker) checkRoll(ctx context.Context, st stratum, n int, r *report) error {
	id := st.voteID(n)
	off, err := c.src.fetchRoll(ctx, st, n)
	if err != nil {
		r.failf("%s: can't read the official XML: %v", id, err)
		return nil
	}
	stored, err := c.reader.StoredRollCall(ctx, id)
	if err != nil {
		return fmt.Errorf("read %s: %w", id, err)
	}
	diffs := c.cmp.compare(off, stored)
	if len(diffs) == 0 {
		r.okf("%s: matches, %d members", id, len(off.Positions))
		return nil
	}
	r.failf("%s: %d differences", id, len(diffs))
	for _, d := range diffs {
		r.detail(d)
	}
	return nil
}

func (c *checker) checkCoverage(ctx context.Context, strata []stratum, r *report) error {
	upstream := make(map[string]int, len(billTypes()))
	for _, t := range billTypes() {
		n, err := c.counter.billCount(ctx, c.opts.congress, t)
		if err != nil {
			return err
		}
		upstream[t] = n
	}
	stored, err := c.reader.Coverage(ctx, c.opts.congress)
	if err != nil {
		return err
	}
	coverage{
		congress: c.opts.congress, upstream: upstream, strata: strata, stored: stored, cutLine: c.opts.cutLine,
	}.check(r)
	return nil
}
