package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	psync "github.com/justabill-org/justabill/pipeline/internal/sync"
)

func TestEnableRevalidation(t *testing.T) {
	tests := []struct {
		name      string
		cfg       map[string]string
		wantLog   bool
		wantCache bool
		wantErr   bool
	}{
		{name: "off without settings", cfg: map[string]string{}},
		{name: "on with URL and secret", wantLog: true, cfg: map[string]string{
			"web_revalidate_url": "https://justabill.test/api/revalidate", "web_revalidate_secret": "s",
		}},
		{name: "a bad URL fails start-up", wantErr: true, cfg: map[string]string{
			"web_revalidate_url": "justabill.test", "web_revalidate_secret": "s",
		}},
		{name: "API cache clearing with REDIS_URL", wantCache: true, cfg: map[string]string{
			"redis_url": "redis://:s@localhost:6379",
		}},
		{name: "a bad REDIS_URL fails start-up", wantErr: true, cfg: map[string]string{
			"redis_url": "redis://:s@localhost:notaport",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&log, nil))
			get := func(key string) string { return tt.cfg[key] }

			err := enableRevalidation(t.Context(), logger, psync.New(nil, nil, nil), get)
			if (err != nil) != tt.wantErr {
				t.Fatalf("enableRevalidation = %v, want error: %v", err, tt.wantErr)
			}
			if got := strings.Contains(log.String(), "web revalidation enabled"); got != tt.wantLog {
				t.Errorf("log:\n%s\nwant the enabled line: %v", log.String(), tt.wantLog)
			}
			if got := strings.Contains(log.String(), "api cache clearing enabled"); got != tt.wantCache {
				t.Errorf("log:\n%s\nwant the api cache line: %v", log.String(), tt.wantCache)
			}
			if err != nil && strings.Contains(err.Error(), ":s@") {
				t.Errorf("the error holds the Redis password: %v", err)
			}
			if strings.Contains(log.String(), `web_revalidate_secret`) || strings.Contains(log.String(), "=s ") {
				t.Errorf("the secret was logged:\n%s", log.String())
			}
		})
	}
}
