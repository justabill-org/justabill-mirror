package main

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"

	"cloud.google.com/go/spanner"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/db/testdb"
)

// setDatabaseRoleEnv sets SPANNER_DATABASE_ROLE for one test, or unsets it
// when value is empty, and points viper at the environment.
func setDatabaseRoleEnv(t *testing.T, value string) {
	t.Helper()
	t.Setenv("SPANNER_DATABASE_ROLE", value)
	if value == "" {
		_ = os.Unsetenv("SPANNER_DATABASE_ROLE")
	}
	viper.Reset()
	viper.AutomaticEnv()
	t.Cleanup(viper.Reset)
}

// SPANNER_DATABASE_ROLE reaches the Spanner client's DatabaseRole, and the
// startup log names it; unset, the client sets no role.
func TestSpannerOptions(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		wantRole string
		wantLog  string
	}{
		{name: "unset sets no role", wantLog: `"msg":"spanner database role: none`},
		{name: "api", value: "api", wantRole: "api", wantLog: `"role":"api"`},
		{name: "spaces trimmed", value: " api ", wantRole: "api", wantLog: `"role":"api"`},
		{name: "digits and underscores", value: "api_v2", wantRole: "api_v2", wantLog: `"role":"api_v2"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setDatabaseRoleEnv(t, tt.value)
			var logs bytes.Buffer
			opts, err := spannerOptions(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatalf("spannerOptions(): %v", err)
			}
			var cfg spanner.ClientConfig
			for _, o := range opts {
				o(&cfg)
			}
			if cfg.DatabaseRole != tt.wantRole {
				t.Errorf("DatabaseRole = %q, want %q", cfg.DatabaseRole, tt.wantRole)
			}
			if !strings.Contains(logs.String(), tt.wantLog) {
				t.Errorf("startup log %s doesn't contain %s", logs.String(), tt.wantLog)
			}
		})
	}
}

// A name Spanner wouldn't accept as a role fails at startup, naming the setting.
func TestSpannerOptionsRejectsBadRole(t *testing.T) {
	for _, value := range []string{"2api", "api-server", "api role", strings.Repeat("a", maxDatabaseRoleLength+1)} {
		t.Run(value, func(t *testing.T) {
			setDatabaseRoleEnv(t, value)
			_, err := spannerOptions(t.Context(), slog.New(slog.DiscardHandler))
			if err == nil || !strings.Contains(err.Error(), "SPANNER_DATABASE_ROLE") {
				t.Errorf("spannerOptions() error = %v, want one naming SPANNER_DATABASE_ROLE", err)
			}
		})
	}
}

// openSpanner refuses a bad role before it connects.
func TestOpenSpannerRejectsBadRole(t *testing.T) {
	setDatabaseRoleEnv(t, "api-server")
	sc, err := openSpanner(t.Context(), "p", "i", "d", slog.New(slog.DiscardHandler))
	if err == nil {
		sc.Close()
		t.Fatal("openSpanner() with SPANNER_DATABASE_ROLE=api-server: want an error")
	}
}

// With SPANNER_DATABASE_ROLE=api, openSpanner connects and reads on the
// emulator, which accepts the role without enforcing it.
func TestOpenSpannerWithRole(t *testing.T) {
	_, db := testdb.NewEmpty(t)
	setDatabaseRoleEnv(t, "api")
	sc, err := openSpanner(t.Context(), db.Project, db.Instance, db.ID, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("openSpanner(): %v", err)
	}
	defer sc.Close()
	if err = sc.Ping(t.Context()); err != nil {
		t.Errorf("Ping with the api role: %v", err)
	}
}
