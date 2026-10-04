package aggregates_test

import (
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/aggregates"
)

func getter(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func TestFromConfigDefaults(t *testing.T) {
	cfg, err := aggregates.FromConfig(getter(nil))
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if cfg != aggregates.DefaultConfig() {
		t.Errorf("config = %+v, want the defaults %+v", cfg, aggregates.DefaultConfig())
	}
	want := aggregates.Config{
		MinAccountAge: 48 * time.Hour, RequireAppCheck: true, MinCellVotes: 50, MinNationalVotes: 100,
		RepublishMinChanges: 10, RepublishMinInterval: time.Hour, BurstFactor: 5, BurstMinVotes: 50,
		YoungAccountAge: 7 * 24 * time.Hour, YoungShareMaxPct: 40, SwingPoints: 15,
	}
	if cfg != want {
		t.Errorf("defaults = %+v, want the design's %+v", cfg, want)
	}
}

func TestFromConfigOverrides(t *testing.T) {
	cfg, err := aggregates.FromConfig(getter(map[string]string{
		"agg_min_account_age_hours": "72", "agg_require_app_check": "false", "agg_min_cell_votes": " 80 ",
		"agg_min_national_votes": "200", "agg_republish_min_changes": "20", "agg_republish_min_minutes": "180",
		"agg_burst_factor": "3", "agg_burst_min_votes": "30", "agg_young_account_days": "14",
		"agg_young_share_max_pct": "25", "agg_swing_points": "10",
	}))
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	want := aggregates.Config{
		MinAccountAge: 72 * time.Hour, RequireAppCheck: false, MinCellVotes: 80, MinNationalVotes: 200,
		RepublishMinChanges: 20, RepublishMinInterval: 3 * time.Hour, BurstFactor: 3, BurstMinVotes: 30,
		YoungAccountAge: 14 * 24 * time.Hour, YoungShareMaxPct: 25, SwingPoints: 10,
	}
	if cfg != want {
		t.Errorf("config = %+v, want %+v", cfg, want)
	}
}

func TestFromConfigRejectsBadValues(t *testing.T) {
	for _, tt := range []struct{ key, value, want string }{
		{"agg_min_cell_votes", "fifty", "AGG_MIN_CELL_VOTES"},
		{"agg_swing_points", "-1", "AGG_SWING_POINTS"},
		{"agg_require_app_check", "maybe", "AGG_REQUIRE_APP_CHECK"},
		{"agg_min_cell_votes", "0", "want 1 or more"},
		{"agg_min_national_votes", "0", "want 1 or more"},
	} {
		_, err := aggregates.FromConfig(getter(map[string]string{tt.key: tt.value}))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s=%q: error %v, want one naming %s", tt.key, tt.value, err, tt.want)
		}
	}
}
