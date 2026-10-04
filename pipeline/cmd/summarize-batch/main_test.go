package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	psync "github.com/justabill-org/justabill/pipeline/internal/sync"
)

// Bad flags are rejected before anything connects.
func TestSubmitRejectsBadFlags(t *testing.T) {
	for _, args := range [][]string{
		{"submit", "--congress", "-1"},
		{"submit", "--tiers", "other,old"},
		{"submit", "--max", "0"},
	} {
		cmd := newCommand(slog.New(slog.DiscardHandler))
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), args[1]) {
			t.Errorf("%v: err = %v, want %s rejected", args, err, args[1])
		}
	}
}

// Without AI_BATCH_BUCKET, submit and poll refuse before they connect.
func TestNeedsABucket(t *testing.T) {
	t.Setenv("AI_BATCH_BUCKET", "")
	for _, args := range [][]string{{"submit", "--congress", "119"}, {"poll"}} {
		cmd := newCommand(slog.New(slog.DiscardHandler))
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "AI_BATCH_BUCKET") {
			t.Errorf("%v: err = %v, want AI_BATCH_BUCKET named", args, err)
		}
	}
}

func TestPrintPlan(t *testing.T) {
	var out bytes.Buffer
	req := psync.SummaryBatchRequest{DryRun: true, OutputTokens: 400, InputPrice: 0.375, OutputPrice: 1.875}
	printPlan(&out, req, &psync.SummaryBatchPlan{
		Due: map[int]int{3: 5, 2: 10}, Bills: 15, InputTokens: 30000, OutputTokens: 6000, Cost: 0.02,
	})
	want := "due other   10\ndue prompt  5\nbills     15 (skipped, no text: 0)\n" +
		"tokens    ~30000 in, ~6000 out (characters / 4; 400 out per bill)\ncost      ~$0.02\n"
	if out.String() != want {
		t.Errorf("plan =\n%s\nwant\n%s", out.String(), want)
	}
}
