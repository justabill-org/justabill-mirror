package repository_test

import (
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

func TestDistrictChangeErrorNamesTheNextChange(t *testing.T) {
	next := time.Date(2026, time.October, 27, 5, 45, 0, 0, time.FixedZone("EDT", -4*3600))
	err := &repository.DistrictChangeError{NextAllowed: next}
	if got, want := err.Error(), "district changed too recently; next change allowed at 2026-10-27T09:45:00Z"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
