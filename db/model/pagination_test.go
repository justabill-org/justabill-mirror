package model_test

import (
	"testing"

	"github.com/justabill-org/justabill/db/model"
)

// TestIsBillSort: the API keeps only these sorts and reads anything else as the default order, so
// a value that spannerdb's billListOrder doesn't know must not pass (#943).
func TestIsBillSort(t *testing.T) {
	for _, s := range []string{"updated_at", "number", "latest_action"} {
		if !model.IsBillSort(s) {
			t.Errorf("IsBillSort(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "introduced_date", "Latest_Action", "number ", "title"} {
		if model.IsBillSort(s) {
			t.Errorf("IsBillSort(%q) = true, want false", s)
		}
	}
}
