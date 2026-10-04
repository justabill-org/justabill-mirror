// Seed-congress upserts a congress row, which backfill needs before it runs.
//
// With no flags it seeds the congress in progress as the current congress: the
// 119th until January 2, 2027, the 120th from January 3, 2027. To load a past
// congress, seed its row first:
//
//	seed-congress --congress 118
//
// A past congress isn't current unless --current says so, and seeding one never
// clears is_current on another row.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/obs"
)

const (
	// Since the 20th Amendment, the Nth congress starts on January 3 of 1787 + 2N and
	// lasts two years. The 74th (1935) is the first that started on January 3.
	firstJanuaryCongress = 74
	congressYearOffset   = 1787
	congressYears        = 2
	termStartDay         = 3
)

type options struct {
	congress int
	current  bool
}

func main() {
	os.Exit(obs.Main(context.Background(), obs.Config{Service: "justabill-db", Environment: os.Getenv("APP_ENV")},
		func(ctx context.Context, _ *slog.Logger) error { return run(ctx, os.Args[1:], os.Stdout, os.Stderr) }))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseArgs(args, stderr, time.Now())
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}

	project := envOr("SPANNER_PROJECT", "justabill-local")
	instance := envOr("SPANNER_INSTANCE", "test-instance")
	database := envOr("SPANNER_DATABASE", "justabill")
	db := fmt.Sprintf("projects/%s/instances/%s/databases/%s", project, instance, database)

	client, err := spanner.NewClient(ctx, db)
	if err != nil {
		return err
	}
	defer client.Close()

	return seed(ctx, client, opts, stdout)
}

// parseArgs reads the flags. --congress defaults to the congress in progress at now, and
// --current defaults to true for that congress only.
func parseArgs(args []string, stderr io.Writer, now time.Time) (options, error) {
	inProgress := congressAt(now)
	fs := flag.NewFlagSet("seed-congress", flag.ContinueOnError)
	fs.SetOutput(stderr)
	congress := fs.Int("congress", inProgress, "congress number to seed; the default is the congress in progress")
	current := fs.Bool("current", false, fmt.Sprintf("mark the congress as current (default true for %d)",
		inProgress))
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *congress < firstJanuaryCongress {
		return options{}, fmt.Errorf("--congress %d: congresses before the %dth didn't start on January 3",
			*congress, firstJanuaryCongress)
	}

	opts := options{congress: *congress, current: *congress == inProgress}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "current" {
			opts.current = *current
		}
	})
	return opts, nil
}

// congressAt returns the congress in progress at now. A congress starts on January 3 of an odd
// year, so January 1 and 2 still belong to the previous one. Dates are compared in UTC, like
// rollcall.Current in the pipeline.
func congressAt(now time.Time) int {
	now = now.UTC()
	year := now.Year()
	if now.Before(time.Date(year, time.January, termStartDay, 0, 0, 0, 0, time.UTC)) {
		year--
	}
	return (year - congressYearOffset) / congressYears
}

// congressDates returns the first day of the congress and the first day of the next one.
func congressDates(congress int) (civil.Date, civil.Date) {
	year := congressYearOffset + congressYears*congress
	return civil.Date{Year: year, Month: time.January, Day: termStartDay},
		civil.Date{Year: year + congressYears, Month: time.January, Day: termStartDay}
}

// seed upserts the congress row. It warns, but changes nothing else, when another row is
// also marked current.
func seed(ctx context.Context, client *spanner.Client, opts options, stdout io.Writer) error {
	start, end := congressDates(opts.congress)
	_, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.InsertOrUpdate("congresses",
			[]string{"number", "start_date", "end_date", "is_current"},
			[]any{int64(opts.congress), start, end, opts.current}),
	})
	if err != nil {
		return fmt.Errorf("seed congress %d: %w", opts.congress, err)
	}
	fmt.Fprintf(stdout, "Seeded congress %d (%s to %s, current=%t)\n", opts.congress, start, end, opts.current)

	if !opts.current {
		return nil
	}
	others, err := otherCurrent(ctx, client, opts.congress)
	if err != nil {
		return err
	}
	if len(others) > 0 {
		fmt.Fprintf(stdout, "Warning: congresses %v are also marked current; seed-congress leaves them as they are\n",
			others)
	}
	return nil
}

func otherCurrent(ctx context.Context, client *spanner.Client, congress int) ([]int64, error) {
	iter := client.Single().Query(ctx, spanner.Statement{
		SQL:    "SELECT number FROM congresses WHERE is_current AND number != @congress ORDER BY number",
		Params: map[string]any{"congress": int64(congress)},
	})
	defer iter.Stop()
	var out []int64
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("check current congresses: %w", err)
		}
		var n int64
		if colErr := row.Columns(&n); colErr != nil {
			return nil, fmt.Errorf("check current congresses: %w", colErr)
		}
		out = append(out, n)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
