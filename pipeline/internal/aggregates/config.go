package aggregates

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Defaults for [Config], from "The rules, in one place" in docs/design/89-aggregate-analytics.md.
// The repo is public, so these are too; production can tighten them with the AGG_* variables.
const (
	defaultMinAccountAgeHours  = 48
	defaultMinCellVotes        = 50
	defaultMinNationalVotes    = 100
	defaultRepublishMinChanges = 10
	defaultRepublishMinMinutes = 60
	defaultBurstFactor         = 5
	defaultBurstMinVotes       = 50
	defaultYoungAccountDays    = 7
	defaultYoungShareMaxPct    = 40
	defaultSwingPoints         = 15

	day = 24 * time.Hour
	// burstWindow is the recent window of the burst rule, and baselineWindow the trailing
	// window before it whose hourly mean the burst is compared with.
	burstWindow    = time.Hour
	baselineWindow = 7 * day
)

// Config holds the eligibility, publication and hold rules. Every field is an AGG_* variable
// (named in [FromConfig]).
type Config struct {
	// MinAccountAge is how old an account must be for its votes to count.
	MinAccountAge time.Duration
	// RequireAppCheck counts only votes that passed App Check when they were cast.
	RequireAppCheck bool
	// MinCellVotes is the fewest eligible votes a state or district cell needs to be shown, and
	// MinNationalVotes the fewest for the national cell.
	MinCellVotes     int
	MinNationalVotes int
	// A shown cell is republished only after RepublishMinChanges changed votes and
	// RepublishMinInterval since its last publish.
	RepublishMinChanges  int
	RepublishMinInterval time.Duration
	// The burst rule holds a cell whose votes in the last hour exceed both BurstFactor times the
	// trailing 7-day hourly mean and BurstMinVotes.
	BurstFactor   int
	BurstMinVotes int
	// The young-account rule holds a cell when more than YoungShareMaxPct percent of its new
	// votes since the last publish come from accounts younger than YoungAccountAge.
	YoungAccountAge  time.Duration
	YoungShareMaxPct int
	// The swing rule holds a cell whose Yea share would move by SwingPoints or more.
	SwingPoints int
}

// DefaultConfig returns the design's defaults.
func DefaultConfig() Config {
	return Config{
		MinAccountAge:        defaultMinAccountAgeHours * time.Hour,
		RequireAppCheck:      true,
		MinCellVotes:         defaultMinCellVotes,
		MinNationalVotes:     defaultMinNationalVotes,
		RepublishMinChanges:  defaultRepublishMinChanges,
		RepublishMinInterval: defaultRepublishMinMinutes * time.Minute,
		BurstFactor:          defaultBurstFactor,
		BurstMinVotes:        defaultBurstMinVotes,
		YoungAccountAge:      defaultYoungAccountDays * day,
		YoungShareMaxPct:     defaultYoungShareMaxPct,
		SwingPoints:          defaultSwingPoints,
	}
}

// intSetting is one whole-number AGG_* variable and the field it sets.
type intSetting struct {
	key string
	set func(c *Config, n int)
}

func intSettings() []intSetting {
	return []intSetting{
		{"agg_min_account_age_hours", func(c *Config, n int) { c.MinAccountAge = time.Duration(n) * time.Hour }},
		{"agg_min_cell_votes", func(c *Config, n int) { c.MinCellVotes = n }},
		{"agg_min_national_votes", func(c *Config, n int) { c.MinNationalVotes = n }},
		{"agg_republish_min_changes", func(c *Config, n int) { c.RepublishMinChanges = n }},
		{"agg_republish_min_minutes", func(c *Config, n int) {
			c.RepublishMinInterval = time.Duration(n) * time.Minute
		}},
		{"agg_burst_factor", func(c *Config, n int) { c.BurstFactor = n }},
		{"agg_burst_min_votes", func(c *Config, n int) { c.BurstMinVotes = n }},
		{"agg_young_account_days", func(c *Config, n int) { c.YoungAccountAge = time.Duration(n) * day }},
		{"agg_young_share_max_pct", func(c *Config, n int) { c.YoungShareMaxPct = n }},
		{"agg_swing_points", func(c *Config, n int) { c.SwingPoints = n }},
	}
}

// FromConfig reads the rules through get (viper.GetString in serve, so keys are lowercase):
// AGG_MIN_ACCOUNT_AGE_HOURS, AGG_REQUIRE_APP_CHECK, AGG_MIN_CELL_VOTES, AGG_MIN_NATIONAL_VOTES,
// AGG_REPUBLISH_MIN_CHANGES, AGG_REPUBLISH_MIN_MINUTES, AGG_BURST_FACTOR, AGG_BURST_MIN_VOTES,
// AGG_YOUNG_ACCOUNT_DAYS, AGG_YOUNG_SHARE_MAX_PCT and AGG_SWING_POINTS. An unset variable keeps
// its default; a value that isn't a whole number (or a bool, for AGG_REQUIRE_APP_CHECK) is an
// error, as are minimums below 1, since a cell of one vote would show that person's vote.
func FromConfig(get func(key string) string) (Config, error) {
	cfg := DefaultConfig()
	for _, s := range intSettings() {
		raw := strings.TrimSpace(get(s.key))
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("%s=%q: want a whole number, 0 or more", strings.ToUpper(s.key), raw)
		}
		s.set(&cfg, n)
	}
	if raw := strings.TrimSpace(get("agg_require_app_check")); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("AGG_REQUIRE_APP_CHECK=%q: want true or false", raw)
		}
		cfg.RequireAppCheck = b
	}
	if cfg.MinCellVotes < 1 || cfg.MinNationalVotes < 1 {
		return Config{}, fmt.Errorf("AGG_MIN_CELL_VOTES=%d, AGG_MIN_NATIONAL_VOTES=%d: want 1 or more",
			cfg.MinCellVotes, cfg.MinNationalVotes)
	}
	return cfg, nil
}

// minVotes is the fewest eligible votes a cell of the scope needs to be shown.
func (c Config) minVotes(scope string) int {
	if scope == scopeNational {
		return c.MinNationalVotes
	}
	return c.MinCellVotes
}
