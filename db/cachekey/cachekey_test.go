package cachekey_test

import (
	"testing"

	"github.com/justabill-org/justabill/db/cachekey"
)

func TestBillDetail(t *testing.T) {
	if got, want := cachekey.BillDetail("hr-119-1"), "bills:detail:hr-119-1"; got != want {
		t.Errorf("BillDetail = %q, want %q", got, want)
	}
}

func TestBillLawChanges(t *testing.T) {
	if got, want := cachekey.BillLawChanges("hr-119-1"), "bills:law-changes:hr-119-1"; got != want {
		t.Errorf("BillLawChanges = %q, want %q", got, want)
	}
}
