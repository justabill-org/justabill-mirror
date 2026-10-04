package sync

import (
	"fmt"
	"strconv"
	"strings"
)

// Defaults for [SummaryJobConfig] (docs/design/68-ai-summaries-gemini-3.md, "Job"). At 200 bills
// a run every 30 minutes, the 3,000 cap is what bounds a day, and the ~15,000-bill backlog clears
// in about 5 days.
const (
	DefaultSummaryBatch      = 200
	DefaultSummaryWorkers    = 8
	DefaultSummaryDailyCap   = 3000
	DefaultSummaryRecentDays = 30

	// DefaultLawChangeBatch and DefaultLawChangeDailyCap bound the law-change job
	// (docs/design/149-law-aware-assistant.md, "Cost"): at about $0.03 a bill, the cap holds a day
	// to about $15, and the ~9,000-bill backlog clears in under three weeks while about 60 new
	// texts a day keep up.
	DefaultLawChangeBatch    = 50
	DefaultLawChangeDailyCap = 500
)

// SummaryJobConfig holds the summary job's settings.
type SummaryJobConfig struct {
	// Batch is the most bills one run takes from the queue.
	Batch int
	// Workers is how many bills are summarized at once.
	Workers int
	// DailyCap is the most bills attempted per UTC day, counted from summary_attempts.
	DailyCap int
	// RecentDays is the width of the queue's second tier: bills whose status date is that recent.
	RecentDays int
	// ResummarizeOnPromptChange also queues bills whose summary is of the current text but came
	// from another prompt version, after every other due bill.
	ResummarizeOnPromptChange bool
	// DiffsEnabled turns on diff summaries. They're off by default (design open question 6).
	DiffsEnabled bool
	// LawBatch is the most bills one law-change run takes from its queue.
	LawBatch int
	// LawDailyCap is the most bills whose law changes are explained per UTC day, counted from
	// law_change_attempts, apart from DailyCap.
	LawDailyCap int
}

// DefaultSummaryJobConfig returns the job's default settings.
func DefaultSummaryJobConfig() SummaryJobConfig {
	return SummaryJobConfig{
		Batch:      DefaultSummaryBatch,
		Workers:    DefaultSummaryWorkers,
		DailyCap:   DefaultSummaryDailyCap,
		RecentDays: DefaultSummaryRecentDays,

		LawBatch:    DefaultLawChangeBatch,
		LawDailyCap: DefaultLawChangeDailyCap,
	}
}

// SummaryJobConfigFrom reads the job's settings with get, which takes an env name in lower case
// as viper.GetString does ("ai_summary_batch" for AI_SUMMARY_BATCH). Unset values get their
// defaults; a value that isn't a positive integer or a boolean is an error, so a bad deploy fails
// at startup.
func SummaryJobConfigFrom(get func(key string) string) (SummaryJobConfig, error) {
	cfg := DefaultSummaryJobConfig()
	ints := []struct {
		key string
		dst *int
	}{
		{"ai_summary_batch", &cfg.Batch},
		{"ai_workers", &cfg.Workers},
		{"ai_daily_request_cap", &cfg.DailyCap},
		{"ai_recent_days", &cfg.RecentDays},
		{"ai_law_batch", &cfg.LawBatch},
		{"ai_law_daily_cap", &cfg.LawDailyCap},
	}
	for _, f := range ints {
		v := strings.TrimSpace(get(f.key))
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return SummaryJobConfig{}, fmt.Errorf("%s %q: want a positive integer", strings.ToUpper(f.key), v)
		}
		*f.dst = n
	}
	bools := []struct {
		key string
		dst *bool
	}{
		{"ai_resummarize_on_prompt_change", &cfg.ResummarizeOnPromptChange},
		{"ai_diff_summaries_enabled", &cfg.DiffsEnabled},
	}
	for _, f := range bools {
		v := strings.TrimSpace(get(f.key))
		if v == "" {
			continue
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return SummaryJobConfig{}, fmt.Errorf("%s %q: want true or false", strings.ToUpper(f.key), v)
		}
		*f.dst = b
	}
	return cfg, nil
}
