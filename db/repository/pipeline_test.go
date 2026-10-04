package repository_test

import (
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

func TestNextRetryAt(t *testing.T) {
	failed := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		attempts  int
		permanent bool
		wantDelay time.Duration
		wantOK    bool
	}{
		{name: "first failure", attempts: 1, wantDelay: 30 * time.Minute, wantOK: true},
		{name: "second doubles", attempts: 2, wantDelay: time.Hour, wantOK: true},
		{name: "sixth", attempts: 6, wantDelay: 16 * time.Hour, wantOK: true},
		{name: "seventh is capped", attempts: 7, wantDelay: 24 * time.Hour, wantOK: true},
		{name: "ninth is capped", attempts: 9, wantDelay: 24 * time.Hour, wantOK: true},
		{name: "tenth gives up", attempts: repository.MaxRetryAttempts},
		{name: "past the cap gives up", attempts: repository.MaxRetryAttempts + 3},
		{name: "permanent gives up at once", attempts: 1, permanent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := repository.NextRetryAt(tt.attempts, failed, tt.permanent)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				if !got.IsZero() {
					t.Errorf("given up but next = %v, want zero", got)
				}
				return
			}
			if want := failed.Add(tt.wantDelay); !got.Equal(want) {
				t.Errorf("next = %v, want %v (+%v)", got, want, tt.wantDelay)
			}
		})
	}
}
