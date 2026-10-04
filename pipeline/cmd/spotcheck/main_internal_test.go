package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

func TestResolveCongress(t *testing.T) {
	tests := []struct {
		name     string
		congress int
		now      time.Time
		want     int
	}{
		{"default in 2026", 0, time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), 119},
		{"default on January 2, 2027", 0, time.Date(2027, 1, 2, 23, 59, 0, 0, time.UTC), 119},
		{"default from January 3, 2027", 0, time.Date(2027, 1, 3, 0, 0, 0, 0, time.UTC), 120},
		{"flag wins", 118, time.Date(2027, 1, 3, 0, 0, 0, 0, time.UTC), 118},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCongress(tt.congress, tt.now)
			if err != nil || got != tt.want {
				t.Fatalf("resolveCongress(%d, %s) = %d, %v; want %d", tt.congress, tt.now, got, err, tt.want)
			}
		})
	}
	if _, err := resolveCongress(-1, time.Now()); err == nil {
		t.Error("--congress -1: want an error")
	}
}

func TestExitCodeLogsThroughTheLogger(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		want      int
		wantLevel string
	}{
		{"passed", nil, 0, ""},
		{"a check failed", fmt.Errorf("rolls: %w", errChecksFailed), exitFailed, "WARN"},
		{"couldn't run", errors.New("read clerk list: 503"), exitError, "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			got := exitCode(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)), tt.err)
			if got != tt.want {
				t.Errorf("exitCode = %d, want %d", got, tt.want)
			}
			if tt.wantLevel == "" {
				if logs.Len() != 0 {
					t.Errorf("logged %s for a pass", logs.String())
				}
				return
			}
			var line struct {
				Level string `json:"level"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
				t.Fatalf("log %q: %v", logs.String(), err)
			}
			if line.Level != tt.wantLevel || (tt.want == exitError && line.Error != tt.err.Error()) {
				t.Errorf("log = %+v, want %s with the error", line, tt.wantLevel)
			}
		})
	}
}
