package model_test

import (
	"testing"

	"github.com/justabill-org/justabill/db/model"
)

func TestIsBillStatus(t *testing.T) {
	for _, s := range []string{
		"introduced", "in_committee", "reported", "passed_house", "passed_senate",
		"resolving_differences", "to_president", "signed", "vetoed", "became_law",
	} {
		if !model.IsBillStatus(s) {
			t.Errorf("IsBillStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "passed", "Became_Law", "became_law ", "none"} {
		if model.IsBillStatus(s) {
			t.Errorf("IsBillStatus(%q) = true, want false", s)
		}
	}
}
