// Package rollcall works out the sessions of a congress and builds roll-call vote IDs.
//
// Roll numbers restart at 1 in each session, so an ID carries congress, session and roll number:
// house-119-s2-roll017 and senate-119-s2-vote00017. Every congress since the 77th (1941) has had
// exactly two regular sessions, each starting on January 3, so the session calendar is plain
// arithmetic on the congress number.
package rollcall

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// yearBase makes Year(c, s) = yearBase + 2c + s; the 119th Congress's first session is 2025.
	yearBase            = 1786
	yearsPerCongress    = 2
	sessionsPerCongress = 2
	sessionStartDay     = 3
)

// Year returns the calendar year of a congress's session, which is also the House Clerk's
// folder for its roll calls: Year(119, 2) is 2026.
func Year(congress, session int) int {
	return yearBase + yearsPerCongress*congress + session
}

// Current returns the congress and session in progress at now. A congress starts on January 3
// of an odd year, so January 1 and 2 still belong to the previous congress's second session:
// Current(2027-01-02) is (119, 2) and Current(2027-01-03) is (120, 1). Dates are compared in
// UTC, like Sessions.
func Current(now time.Time) (int, int) {
	now = now.UTC()
	year := now.Year()
	if now.Before(time.Date(year, time.January, sessionStartDay, 0, 0, 0, 0, time.UTC)) {
		year--
	}
	// Year(c, s) = yearBase + 2c + s with s in {1, 2}, so year - yearBase - 1 = 2c + (s - 1).
	offset := year - yearBase - 1
	return offset / yearsPerCongress, offset%yearsPerCongress + 1
}

// Start returns the day a congress begins, January 3 of its first session's year.
func Start(congress int) time.Time {
	return time.Date(Year(congress, 1), time.January, sessionStartDay, 0, 0, 0, 0, time.UTC)
}

// Sessions returns the sessions of a congress that have started by now, in order: none before
// January 3 of its first year, [1] from then, and [1, 2] from January 3 of its second year on,
// including after the congress has ended. Dates are compared in UTC.
func Sessions(congress int, now time.Time) []int {
	now = now.UTC()
	sessions := make([]int, 0, sessionsPerCongress)
	for session := 1; session <= sessionsPerCongress; session++ {
		start := time.Date(Year(congress, session), time.January, sessionStartDay, 0, 0, 0, 0, time.UTC)
		if now.Before(start) {
			break
		}
		sessions = append(sessions, session)
	}
	return sessions
}

// HouseID returns the ID of a House roll call, e.g. house-119-s2-roll017. Rolls from 1000 on
// get four digits.
func HouseID(congress, session, roll int) string {
	return fmt.Sprintf("house-%d-s%d-roll%03d", congress, session, roll)
}

// SenateID returns the ID of a Senate roll call, e.g. senate-119-s2-vote00017.
func SenateID(congress, session, number int) string {
	return fmt.Sprintf("senate-%d-s%d-vote%05d", congress, session, number)
}

// ParseOrdinalSession parses the House Clerk's session element ("1st", "2nd", "3rd") into a
// session number.
func ParseOrdinalSession(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, suffix := range []string{"st", "nd", "rd", "th"} {
		digits, ok := strings.CutSuffix(s, suffix)
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(digits); err == nil && n >= 1 {
			return n, nil
		}
		break
	}
	return 0, fmt.Errorf("invalid ordinal session %q", s)
}

// HighestRoll finds the highest roll number of a session whose rolls run 1..n with no gaps, given
// exists(roll), in about 2·log2(n) calls: it doubles until a roll is missing, then halves the gap.
// It returns 0 when roll 1 doesn't exist, and never looks past limit. The House Clerk took down
// its per-year index pages (clerk.house.gov/evs/<year>/index.asp was a 404 for 2024 and 2025 on
// 2026-10-01), so the roll files themselves are the only listing left.
func HighestRoll(exists func(roll int) (bool, error), limit int) (int, error) {
	const factor = 2 // the guess doubles, then the gap halves
	if limit < 1 {
		return 0, nil
	}
	ok, err := exists(1)
	if err != nil || !ok {
		return 0, err
	}
	lo, hi := 1, factor // lo exists; hi is the next guess
	for hi <= limit {
		ok, err = exists(hi)
		if err != nil {
			return 0, err
		}
		if !ok {
			break
		}
		lo, hi = hi, hi*factor
	}
	hi = min(hi, limit+1) // hi doesn't exist, or is past the limit
	for hi-lo > 1 {
		mid := lo + (hi-lo)/factor
		ok, err = exists(mid)
		if err != nil {
			return 0, err
		}
		if ok {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo, nil
}

// IsHouseRoll reports whether a body from the Clerk's roll-file URL is a roll call. For a roll
// that doesn't exist the Clerk answers 200 with `<xml>Error sanitizing file "roll500.xml". Please
// try again.</xml>` (2026-10-01), so the status alone says nothing.
func IsHouseRoll(body []byte) bool {
	return bytes.Contains(body, []byte("<rollcall-vote"))
}
