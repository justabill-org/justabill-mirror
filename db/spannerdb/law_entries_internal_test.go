package spannerdb

import (
	"testing"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"
)

func TestCompareNullDatesDesc(t *testing.T) {
	older := spanner.NullDate{Date: civil.Date{Year: 2025, Month: 1, Day: 3}, Valid: true}
	newer := spanner.NullDate{Date: civil.Date{Year: 2026, Month: 2, Day: 1}, Valid: true}
	null := spanner.NullDate{}
	tests := []struct {
		a, b spanner.NullDate
		want int
	}{
		{newer, older, -1},
		{older, newer, 1},
		{older, older, 0},
		{older, null, -1},
		{null, older, 1},
		{null, null, 0},
	}
	for _, tt := range tests {
		if got := compareNullDatesDesc(tt.a, tt.b); got != tt.want {
			t.Errorf("compareNullDatesDesc(%v, %v) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
