package rollcall_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
)

func TestYear(t *testing.T) {
	tests := []struct{ congress, session, want int }{
		{119, 1, 2025},
		{119, 2, 2026},
		{118, 1, 2023},
		{118, 2, 2024},
		{104, 2, 1996},
	}
	for _, tt := range tests {
		if got := rollcall.Year(tt.congress, tt.session); got != tt.want {
			t.Errorf("Year(%d, %d) = %d, want %d", tt.congress, tt.session, got, tt.want)
		}
	}
}

func TestCurrent(t *testing.T) {
	est := time.FixedZone("EST", -5*60*60)
	tests := []struct {
		now               time.Time
		congress, session int
	}{
		{time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC), 119, 2},
		{time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC), 119, 1},
		{time.Date(2025, time.January, 2, 23, 59, 59, 0, time.UTC), 118, 2},
		{time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC), 119, 1},
		{time.Date(2026, time.January, 3, 0, 0, 0, 0, time.UTC), 119, 2},
		{time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC), 119, 2},
		{time.Date(2027, time.January, 3, 0, 0, 0, 0, time.UTC), 120, 1},
		// 2027-01-02 21:00 EST is already January 3 in UTC.
		{time.Date(2027, time.January, 2, 21, 0, 0, 0, est), 120, 1},
		{time.Date(2028, time.December, 31, 0, 0, 0, 0, time.UTC), 120, 2},
		{time.Date(1996, time.June, 1, 0, 0, 0, 0, time.UTC), 104, 2},
	}
	for _, tt := range tests {
		congress, session := rollcall.Current(tt.now)
		if congress != tt.congress || session != tt.session {
			t.Errorf("Current(%s) = (%d, %d), want (%d, %d)", tt.now.Format(time.RFC3339),
				congress, session, tt.congress, tt.session)
		}
	}
}

// Current agrees with Sessions and Year every day: its session is the latest one Sessions lists
// for its congress, and that session's year is the current (January-3-based) year.
func TestCurrentMatchesSessions(t *testing.T) {
	for day := time.Date(2022, time.December, 25, 0, 0, 0, 0, time.UTC); day.Year() < 2031; day = day.AddDate(0, 0, 1) {
		congress, session := rollcall.Current(day)
		sessions := rollcall.Sessions(congress, day)
		if len(sessions) == 0 || sessions[len(sessions)-1] != session {
			t.Fatalf("%s: Current = (%d, %d) but Sessions = %v", day.Format(time.DateOnly), congress, session, sessions)
		}
		if next := rollcall.Sessions(congress+1, day); len(next) != 0 {
			t.Fatalf("%s: Current = %d but congress %d has sessions %v", day.Format(time.DateOnly),
				congress, congress+1, next)
		}
	}
}

func TestStart(t *testing.T) {
	want := time.Date(2027, time.January, 3, 0, 0, 0, 0, time.UTC)
	if got := rollcall.Start(120); !got.Equal(want) {
		t.Errorf("Start(120) = %v, want %v", got, want)
	}
}

func TestSessions(t *testing.T) {
	tests := []struct {
		name     string
		congress int
		now      time.Time
		want     []int
	}{
		{"before the congress", 119, time.Date(2025, 1, 2, 23, 59, 0, 0, time.UTC), []int{}},
		{"first day", 119, time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), []int{1}},
		{"last day of session 1", 119, time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC), []int{1}},
		{"first day of session 2", 119, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), []int{1, 2}},
		{"today", 119, time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC), []int{1, 2}},
		{"after the congress", 119, time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC), []int{1, 2}},
		{"next congress not started", 120, time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC), []int{}},
		{
			"compared in UTC", 119,
			time.Date(2026, 1, 2, 20, 0, 0, 0, time.FixedZone("EST", -5*60*60)), // 2026-01-03 01:00 UTC
			[]int{1, 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rollcall.Sessions(tt.congress, tt.now); !slices.Equal(got, tt.want) {
				t.Errorf("Sessions(%d, %s) = %v, want %v", tt.congress, tt.now, got, tt.want)
			}
		})
	}
}

func TestIDs(t *testing.T) {
	tests := []struct{ got, want string }{
		{rollcall.HouseID(119, 2, 17), "house-119-s2-roll017"},
		{rollcall.HouseID(119, 1, 1), "house-119-s1-roll001"},
		{rollcall.HouseID(104, 1, 1000), "house-104-s1-roll1000"},
		{rollcall.SenateID(119, 2, 17), "senate-119-s2-vote00017"},
		{rollcall.SenateID(119, 1, 659), "senate-119-s1-vote00659"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
}

func TestParseOrdinalSession(t *testing.T) {
	valid := []struct {
		in   string
		want int
	}{{"1st", 1}, {"2nd", 2}, {"3rd", 3}, {" 2ND ", 2}}
	for _, tt := range valid {
		got, err := rollcall.ParseOrdinalSession(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseOrdinalSession(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{"", "2", "second", "nd", "0th", "-1st", "2nd session"} {
		if got, err := rollcall.ParseOrdinalSession(in); err == nil {
			t.Errorf("ParseOrdinalSession(%q) = %d, want an error", in, got)
		}
	}
}

func TestHighestRoll(t *testing.T) {
	for _, tc := range []struct {
		name    string
		highest int // rolls 1..highest exist
		limit   int
		want    int
	}{
		{"no rolls yet", 0, 2000, 0},
		{"one roll", 1, 2000, 1},
		{"two rolls", 2, 2000, 2},
		{"three rolls", 3, 2000, 3},
		{"a power of two", 8, 2000, 8},
		{"one past a power of two", 9, 2000, 9},
		{"a year's worth", 431, 2000, 431},
		{"over a thousand", 1000, 2000, 1000},
		{"at the limit", 2000, 2000, 2000},
		{"past the limit stops there", 5000, 2000, 2000},
		{"no limit", 5, 0, 0},
	} {
		calls := 0
		got, err := rollcall.HighestRoll(func(roll int) (bool, error) {
			calls++
			return roll <= tc.highest, nil
		}, tc.limit)
		if err != nil || got != tc.want {
			t.Errorf("%s: HighestRoll = %d, %v; want %d", tc.name, got, err, tc.want)
		}
		if calls > 25 {
			t.Errorf("%s: %d calls, want about 2·log2(n)", tc.name, calls)
		}
	}
}

func TestHighestRollStopsOnAnError(t *testing.T) {
	boom := errors.New("boom")
	got, err := rollcall.HighestRoll(func(roll int) (bool, error) {
		if roll > 16 {
			return false, boom
		}
		return true, nil
	}, 2000)
	if !errors.Is(err, boom) || got != 0 {
		t.Fatalf("HighestRoll = %d, %v; want 0, boom", got, err)
	}
}

func TestIsHouseRoll(t *testing.T) {
	for body, want := range map[string]bool{
		`<?xml version="1.0" encoding="utf-8"?><rollcall-vote><vote-metadata>`: true,
		`<xml>Error sanitizing file "roll500.xml". Please try again.</xml>`:    false,
		``: false,
	} {
		if got := rollcall.IsHouseRoll([]byte(body)); got != want {
			t.Errorf("IsHouseRoll(%q) = %v, want %v", body, got, want)
		}
	}
}
