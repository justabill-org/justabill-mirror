package spannerdb

import (
	"testing"
	"time"
)

// TestSummaryRetryDelay pins the retry schedule of docs/design/68-ai-summaries-gemini-3.md
// ("Queue") in literal durations, so renaming its constants can't change it.
func TestSummaryRetryDelay(t *testing.T) {
	tests := []struct {
		failures int64
		want     time.Duration
	}{
		{failures: 1, want: time.Hour},
		{failures: 2, want: 4 * time.Hour},
		{failures: 3, want: 24 * time.Hour},
		{failures: 10, want: 24 * time.Hour},
	}
	for _, tt := range tests {
		if got := summaryRetryDelay(tt.failures); got != tt.want {
			t.Errorf("summaryRetryDelay(%d) = %v, want %v", tt.failures, got, tt.want)
		}
	}
}
