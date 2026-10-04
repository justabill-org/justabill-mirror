package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/testdb"
)

// during119th returns a day in the 119th Congress's second session.
func during119th() time.Time {
	return time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
}

func TestParseArgs(t *testing.T) {
	during120th := time.Date(2027, time.January, 5, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		args []string
		now  time.Time
		want options
	}{
		{"default is the current 119th", nil, during119th(), options{congress: 119, current: true}},
		{"past congress is not current", []string{"--congress", "118"}, during119th(), options{congress: 118}},
		{"--current marks a past congress", []string{"--congress", "118", "--current"}, during119th(),
			options{congress: 118, current: true}},
		{"--current=false on the 119th", []string{"--current=false"}, during119th(), options{congress: 119}},
		{"first January congress", []string{"--congress=74"}, during119th(), options{congress: 74}},
		{"next congress isn't current yet", []string{"--congress=120"}, during119th(), options{congress: 120}},
		{"default is the 120th once it starts", nil, during120th, options{congress: 120, current: true}},
		{"the 119th is past once the 120th starts", []string{"--congress", "119"}, during120th,
			options{congress: 119}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args, io.Discard, tt.now)
			if err != nil {
				t.Fatalf("parseArgs(%v): %v", tt.args, err)
			}
			if got != tt.want {
				t.Errorf("parseArgs(%v) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestParseArgsRejects(t *testing.T) {
	for _, args := range [][]string{
		{"--congress", "0"},
		{"--congress", "-118"},
		{"--congress", "73"},
		{"--congress", "abc"},
		{"--year", "2023"},
		{"118"},
	} {
		if got, err := parseArgs(args, io.Discard, during119th()); err == nil {
			t.Errorf("parseArgs(%v) = %+v, want an error", args, got)
		}
	}
}

// Bad flags fail before run connects to Spanner, so this needs no emulator.
func TestRunRejectsBadFlagsBeforeConnecting(t *testing.T) {
	t.Setenv("SPANNER_EMULATOR_HOST", "")
	t.Setenv("SPANNER_PROJECT", "no-such-project")
	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"--congress", "0"}, &stdout, io.Discard); err == nil {
		t.Fatal("run with --congress 0: want an error")
	}
	if err := run(t.Context(), []string{"-h"}, &stdout, io.Discard); err != nil {
		t.Errorf("run -h = %v, want nil", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
	if _, err := parseArgs([]string{"-h"}, io.Discard, during119th()); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("parseArgs(-h) error = %v, want flag.ErrHelp", err)
	}
}

func TestHelpNamesTheCongressInProgress(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseArgs([]string{"-h"}, &stderr, time.Date(2027, time.March, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(
		err, flag.ErrHelp) {
		t.Fatalf("parseArgs(-h) error = %v, want flag.ErrHelp", err)
	}
	for _, want := range []string{"(default 120)", "default true for 120"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("help = %q, want it to contain %q", stderr.String(), want)
		}
	}
}

func TestCongressAt(t *testing.T) {
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load America/New_York: %v", err)
	}
	tests := []struct {
		now  time.Time
		want int
	}{
		{time.Date(1935, time.January, 3, 0, 0, 0, 0, time.UTC), 74},
		{time.Date(2025, time.January, 2, 23, 59, 59, 0, time.UTC), 118},
		{time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC), 119},
		{during119th(), 119},
		{time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC), 119},
		{time.Date(2027, time.January, 2, 23, 59, 59, 0, time.UTC), 119},
		{time.Date(2027, time.January, 3, 0, 0, 0, 0, time.UTC), 120},
		// 20:00 Eastern on January 2 is already January 3 in UTC.
		{time.Date(2027, time.January, 2, 20, 0, 0, 0, eastern), 120},
		{time.Date(2028, time.December, 31, 0, 0, 0, 0, time.UTC), 120},
		{time.Date(2029, time.January, 3, 0, 0, 0, 0, time.UTC), 121},
	}
	for _, tt := range tests {
		if got := congressAt(tt.now); got != tt.want {
			t.Errorf("congressAt(%s) = %d, want %d", tt.now, got, tt.want)
		}
	}
}

func TestCongressDates(t *testing.T) {
	tests := []struct {
		congress   int
		start, end string
	}{
		{74, "1935-01-03", "1937-01-03"},
		{118, "2023-01-03", "2025-01-03"},
		{119, "2025-01-03", "2027-01-03"},
		{120, "2027-01-03", "2029-01-03"},
	}
	for _, tt := range tests {
		start, end := congressDates(tt.congress)
		if start.String() != tt.start || end.String() != tt.end {
			t.Errorf("congressDates(%d) = %s, %s; want %s, %s", tt.congress, start, end, tt.start, tt.end)
		}
	}
}

type congressRow struct {
	Number    int64      `spanner:"number"`
	StartDate civil.Date `spanner:"start_date"`
	EndDate   civil.Date `spanner:"end_date"`
	IsCurrent bool       `spanner:"is_current"`
}

func readCongress(t *testing.T, client *spanner.Client, number int64) congressRow {
	t.Helper()
	row, err := client.Single().ReadRow(t.Context(), "congresses", spanner.Key{number},
		[]string{"number", "start_date", "end_date", "is_current"})
	if err != nil {
		t.Fatalf("read congress %d: %v", number, err)
	}
	var c congressRow
	if decodeErr := row.ToStruct(&c); decodeErr != nil {
		t.Fatalf("decode congress %d: %v", number, decodeErr)
	}
	return c
}

func TestSeedPastCongressLeavesCurrentAlone(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	var out bytes.Buffer

	if err := seed(ctx, client, options{congress: 119, current: true}, &out); err != nil {
		t.Fatalf("seed 119: %v", err)
	}
	if err := seed(ctx, client, options{congress: 118}, &out); err != nil {
		t.Fatalf("seed 118: %v", err)
	}

	want119 := congressRow{119, civil.Date{Year: 2025, Month: time.January, Day: 3},
		civil.Date{Year: 2027, Month: time.January, Day: 3}, true}
	want118 := congressRow{118, civil.Date{Year: 2023, Month: time.January, Day: 3},
		civil.Date{Year: 2025, Month: time.January, Day: 3}, false}
	if got := readCongress(t, client, 119); got != want119 {
		t.Errorf("congress 119 = %+v, want %+v", got, want119)
	}
	if got := readCongress(t, client, 118); got != want118 {
		t.Errorf("congress 118 = %+v, want %+v", got, want118)
	}
	if strings.Contains(out.String(), "Warning") {
		t.Errorf("output = %q, want no warning", out.String())
	}

	// Seeding again is idempotent.
	if err := seed(ctx, client, options{congress: 118}, &out); err != nil {
		t.Fatalf("reseed 118: %v", err)
	}
	if got := readCongress(t, client, 118); got != want118 {
		t.Errorf("reseeded congress 118 = %+v, want %+v", got, want118)
	}
}

func TestSeedSecondCurrentWarnsButClearsNothing(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()

	if err := seed(ctx, client, options{congress: 119, current: true}, io.Discard); err != nil {
		t.Fatalf("seed 119: %v", err)
	}
	var out bytes.Buffer
	if err := seed(ctx, client, options{congress: 118, current: true}, &out); err != nil {
		t.Fatalf("seed 118 as current: %v", err)
	}
	if !strings.Contains(out.String(), "Warning: congresses [119] are also marked current") {
		t.Errorf("output = %q, want a warning naming 119", out.String())
	}
	if !readCongress(t, client, 119).IsCurrent || !readCongress(t, client, 118).IsCurrent {
		t.Error("both rows should be current: seed-congress never clears is_current elsewhere")
	}
}
