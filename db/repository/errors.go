package repository

import (
	"errors"
	"fmt"
	"time"
)

// ErrNotFound reports that a write refers to a row that doesn't exist, such
// as a vote or favorite for a bill ID with no bills row. Check it with
// [errors.Is]; implementations wrap it.
var ErrNotFound = errors.New("not found")

// ErrInvalidSearch means the database rejected a list's search text as an invalid argument: a
// client error, not a failure. Check it with [errors.Is]; implementations wrap it.
var ErrInvalidSearch = errors.New("invalid search")

// ErrDailyVoteCap means the vote would exceed the user's daily vote cap.
var ErrDailyVoteCap = errors.New("daily vote cap reached")

// ErrInvalidSeat means a user update names a state that isn't a state, DC or territory, or a
// district that isn't one of its state's House seats (see model.ValidDistrict). Check it with
// [errors.Is]; implementations wrap it.
var ErrInvalidSeat = errors.New("not a House seat")

// ErrStaleBatchAttempt means a summary batch's result is for a bill the batch no longer holds for
// that text: an attempt for newer text, or by the synchronous job or another batch, replaced the
// hold. The import skips the line and writes nothing. Check it with [errors.Is].
var ErrStaleBatchAttempt = errors.New("summary batch result is stale")

// DistrictChangeError means the user changed their state or district too
// recently to change it again.
type DistrictChangeError struct {
	// NextAllowed is the earliest time the next change is accepted.
	NextAllowed time.Time
}

// Error says when the next change is allowed, in RFC 3339 UTC.
func (e *DistrictChangeError) Error() string {
	return fmt.Sprintf("district changed too recently; next change allowed at %s",
		e.NextAllowed.UTC().Format(time.RFC3339))
}
