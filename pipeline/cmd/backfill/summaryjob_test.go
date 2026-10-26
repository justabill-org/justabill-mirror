package main

import (
	"log/slog"
	"net/http"
	"testing"

	psync "github.com/justabill-org/justabill/pipeline/internal/sync"
)

// TestConfigureOptionalClientsSummaryJob checks that the backfill applies the AI_* summary-job
// settings as pipeline-serve does (#1021), and refuses a bad one at start-up.
func TestConfigureOptionalClientsSummaryJob(t *testing.T) {
	tests := []struct {
		name    string
		cfg     map[string]string
		want    psync.SummaryJobConfig
		wantErr string
	}{
		{name: "defaults without settings", cfg: map[string]string{}, want: psync.SummaryJobConfig{
			Batch: 200, Workers: 8, DailyCap: 3000, RecentDays: 30, LawBatch: 50, LawDailyCap: 500,
		}},
		{name: "every setting applied", cfg: map[string]string{
			"ai_summary_batch": "5", "ai_workers": "2", "ai_daily_request_cap": "10", "ai_recent_days": "7",
			"ai_resummarize_on_prompt_change": "true", "ai_diff_summaries_enabled": "true",
			"ai_law_batch": "3", "ai_law_daily_cap": "4",
		}, want: psync.SummaryJobConfig{
			Batch: 5, Workers: 2, DailyCap: 10, RecentDays: 7, ResummarizeOnPromptChange: true,
			DiffsEnabled: true, LawBatch: 3, LawDailyCap: 4,
		}},
		{
			name: "a bad daily cap fails start-up", cfg: map[string]string{"ai_daily_request_cap": "abc"},
			wantErr: `AI_DAILY_REQUEST_CAP "abc": want a positive integer`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := psync.New(nil, nil, nil)
			get := func(key string) string { return tt.cfg[key] }

			err := configureOptionalClients(t.Context(), slog.New(slog.DiscardHandler), svc, "", &http.Client{}, get)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("configureOptionalClients = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("configureOptionalClients: %v", err)
			}
			if got := svc.SummaryJob(); got != tt.want {
				t.Errorf("summary job = %+v, want %+v", got, tt.want)
			}
		})
	}
}
