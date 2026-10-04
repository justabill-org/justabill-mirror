package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
	psync "github.com/justabill-org/justabill/pipeline/internal/sync"
)

func TestVoteSessions(t *testing.T) {
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		session int
		want    []int
	}{
		{0, []int{1, 2}},
		{1, []int{1}},
		{2, []int{2}},
	}
	for _, tt := range tests {
		got, err := voteSessions(119, tt.session, now)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("voteSessions(119, %d) = %v, %v; want %v", tt.session, got, err, tt.want)
		}
	}

	if got, err := voteSessions(119, 0, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)); err != nil ||
		!slices.Equal(got, []int{1}) {
		t.Errorf("voteSessions(119, 0) in 2025 = %v, %v; want [1]", got, err)
	}

	for _, session := range []int{-1, 3} {
		if got, err := voteSessions(119, session, now); err == nil {
			t.Errorf("voteSessions(119, %d) = %v, want an error", session, got)
		}
	}
}

func TestParseSteps(t *testing.T) {
	tests := []struct {
		value string
		want  []string
	}{
		{"members,votes,texts", []string{"members", "votes", "texts"}},
		{"texts, members", []string{"texts", "members"}},
		{"bills,", []string{"bills"}},
		{"members,votes,voted-bills,texts", []string{"members", "votes", "voted-bills", "texts"}},
		{"members,bills,votes,texts,summaries,govinfo,gao,crs-summaries",
			[]string{"members", "bills", "votes", "texts", "summaries", "govinfo", "gao", "crs-summaries"}},
	}
	for _, tt := range tests {
		got, err := parseSteps(tt.value)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("parseSteps(%q) = %v, %v; want %v", tt.value, got, err, tt.want)
		}
	}
}

func TestParseStepsRejects(t *testing.T) {
	tests := []struct {
		value     string
		wantInErr string
	}{
		{"members,votez", `unknown step "votez"; allowed steps: members, member-terms, bills, votes, voted-bills`},
		{"members,votes,members", `step "members" is listed twice`},
		{"", "--steps is empty"},
		{" , ", "--steps is empty"},
	}
	for _, tt := range tests {
		got, err := parseSteps(tt.value)
		if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
			t.Errorf("parseSteps(%q) = %v, %v; want an error containing %q", tt.value, got, err, tt.wantInErr)
		}
	}
}

func TestParseResumeSince(t *testing.T) {
	got, err := parseResumeSince("2026-10-01T00:00:00Z")
	if want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); err != nil || !got.Equal(want) {
		t.Errorf("parseResumeSince = %v, %v; want %v", got, err, want)
	}
	if got, err = parseResumeSince(""); err != nil || !got.IsZero() {
		t.Errorf("parseResumeSince(\"\") = %v, %v; want the zero time", got, err)
	}
	if _, err = parseResumeSince("2026-10-01"); err == nil {
		t.Error("parseResumeSince accepted a date without a time")
	}
}

func TestVoteSessions_PastCongress(t *testing.T) {
	// The 118th has ended, so session 0 means both of its sessions, in 2023 and 2024.
	got, err := voteSessions(118, 0, time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC))
	if err != nil || !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("voteSessions(118, 0) = %v, %v; want [1 2]", got, err)
	}
	for session, year := range map[int]int{1: 2023, 2: 2024} {
		if y := rollcall.Year(118, session); y != year {
			t.Errorf("rollcall.Year(118, %d) = %d, want %d", session, y, year)
		}
	}
}

func TestResolveSteps(t *testing.T) {
	tests := []struct {
		value string
		past  bool
		want  []string
	}{
		{"", false, []string{"members", "bills"}},
		{"", true, []string{"members", "member-terms", "votes", "voted-bills"}},
		{"  ", true, []string{"members", "member-terms", "votes", "voted-bills"}},
		{"voted-bills", true, []string{"voted-bills"}},
		{"member-terms,summaries", true, []string{"member-terms", "summaries"}},
		{"crs-summaries", true, []string{"crs-summaries"}},
		{"crs-summaries,passed-summaries", false, []string{"crs-summaries", "passed-summaries"}},
		{"members,votes", false, []string{"members", "votes"}},
	}
	for _, tt := range tests {
		got, err := resolveSteps(tt.value, tt.past)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("resolveSteps(%q, %v) = %v, %v; want %v", tt.value, tt.past, got, err, tt.want)
		}
	}
}

func TestResolveStepsRejects(t *testing.T) {
	tests := []struct {
		value     string
		past      bool
		wantInErr string
	}{
		{"members,bills", true, "--past-congress doesn't run bills"},
		{"members,member-terms", false, "member-terms loads a past congress's terms"},
		{"member-terms,bogus", true, `unknown step "bogus"`},
		{",", true, "--steps is empty"},
	}
	for _, tt := range tests {
		got, err := resolveSteps(tt.value, tt.past)
		if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
			t.Errorf("resolveSteps(%q, %v) = %v, %v; want an error containing %q",
				tt.value, tt.past, got, err, tt.wantInErr)
		}
	}
}

func TestCheckPastCongress(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	congresses := []model.Congress{
		{Number: 118},
		{Number: 119, IsCurrent: true},
		{Number: 120},
	}
	if err := checkPastCongress(congresses, 118, now); err != nil {
		t.Errorf("checkPastCongress(118) = %v, want nil", err)
	}

	tests := []struct {
		congress  int
		now       time.Time
		wantInErr string
	}{
		{119, now, "congress 119 is the current congress"},
		// Not marked current (is_current hasn't moved yet), but still running by the calendar.
		{120, now, "congress 120 runs until 2029-01-03"},
		{118, time.Date(2025, 1, 2, 23, 59, 0, 0, time.UTC), "congress 118 runs until 2025-01-03"},
		{117, now, "congress 117 has no congresses row"},
	}
	for _, tt := range tests {
		err := checkPastCongress(congresses, tt.congress, tt.now)
		if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
			t.Errorf("checkPastCongress(%d, %s) = %v, want an error containing %q",
				tt.congress, tt.now.Format(time.DateOnly), err, tt.wantInErr)
		}
	}
}

func TestParseLegislatorsURL(t *testing.T) {
	pinned := "https://raw.githubusercontent.com/unitedstates/congress-legislators/577ca04"
	for _, value := range []string{"", pinned} {
		if got, err := parseLegislatorsURL(value); err != nil || got != value {
			t.Errorf("parseLegislatorsURL(%q) = %q, %v; want it unchanged", value, got, err)
		}
	}
	for _, value := range []string{"http://unitedstates.github.io/congress-legislators", "raw.githubusercontent.com",
		"https://", "::"} {
		if got, err := parseLegislatorsURL(value); err == nil {
			t.Errorf("parseLegislatorsURL(%q) = %q, want an error", value, got)
		}
	}
}

func TestLogSummary(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logSummary(t.Context(), logger, summary{
		congress: 118,
		steps:    []string{"members", "votes"},
		err:      errors.New("step votes: boom"),
		requests: 3,
		elapsed:  90 * time.Second,
		tally:    psync.NewTally(),
	})

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("summary isn't one JSON line: %v\n%s", err, buf.String())
	}
	want := map[string]any{
		"msg": "backfill summary", "congress": 118.0, "steps": "members,votes", "status": "failed",
		"congress_gov_requests": 3.0, "elapsed": "1m30s", "bills_fetched": 0.0, "unmatched_senators": 0.0,
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("summary %s = %v, want %v", k, line[k], v)
		}
	}
}

func TestResolveCongress(t *testing.T) {
	lastDayOf119th := time.Date(2027, 1, 2, 23, 59, 0, 0, time.UTC)
	firstDayOf120th := time.Date(2027, 1, 3, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		flag int
		past bool
		now  time.Time
		want int
	}{
		{"default before January 3, 2027", 0, false, lastDayOf119th, 119},
		{"default from January 3, 2027", 0, false, firstDayOf120th, 120},
		{"flag wins", 118, false, firstDayOf120th, 118},
		{"flag with --past-congress", 118, true, firstDayOf120th, 118},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCongress(tt.flag, tt.past, tt.now)
			if err != nil || got != tt.want {
				t.Fatalf("resolveCongress(%d, %t, %s) = %d, %v; want %d", tt.flag, tt.past, tt.now, got, err, tt.want)
			}
		})
	}
}

func TestResolveCongressRejects(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	if _, err := resolveCongress(0, true, now); err == nil || !strings.Contains(err.Error(), "needs --congress") {
		t.Errorf("--past-congress without --congress: err = %v, want 'needs --congress'", err)
	}
	if _, err := resolveCongress(-1, false, now); err == nil {
		t.Error("--congress -1: want an error")
	}
}
