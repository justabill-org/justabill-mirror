package model_test

import (
	"testing"

	"github.com/justabill-org/justabill/db/model"
)

func TestHouseSeatsCoversTheApportionment(t *testing.T) {
	codes := []string{
		"AL", "AK", "AZ", "AR", "CA", "CO", "CT", "DE", "FL", "GA", "HI", "ID", "IL", "IN", "IA", "KS", "KY",
		"LA", "ME", "MD", "MA", "MI", "MN", "MS", "MO", "MT", "NE", "NV", "NH", "NJ", "NM", "NY", "NC", "ND",
		"OH", "OK", "OR", "PA", "RI", "SC", "SD", "TN", "TX", "UT", "VT", "VA", "WA", "WV", "WI", "WY",
	}
	voting := 0
	for _, code := range codes {
		n, ok := model.HouseSeats(code)
		if !ok || n < 1 {
			t.Errorf("HouseSeats(%q) = %d, %v; want a state", code, n, ok)
		}
		voting += n
	}
	if voting != 435 {
		t.Errorf("the 50 states have %d seats, want 435", voting)
	}
	for _, code := range []string{"DC", "PR", "GU", "VI", "AS", "MP"} {
		if n, ok := model.HouseSeats(code); !ok || n != 1 {
			t.Errorf("HouseSeats(%q) = %d, %v; want one non-voting seat", code, n, ok)
		}
	}
	for _, code := range []string{"", "ca", "XX", "CAL", "UM", "FM", "PW", "MH"} {
		if n, ok := model.HouseSeats(code); ok {
			t.Errorf("HouseSeats(%q) = %d, true; want false", code, n)
		}
	}
}

func TestValidDistrict(t *testing.T) {
	tests := []struct {
		state    string
		district int
		want     bool
	}{
		{"CA", 1, true},
		{"CA", 52, true},
		{"CA", 53, false},
		{"CA", 0, false},
		{"CA", -1, false},
		{"TX", 38, true},
		{"AK", 0, true},
		{"AK", 1, false},
		{"WY", 0, true},
		{"DC", 0, true},
		{"PR", 0, true},
		{"MP", 1, false},
		{"MT", 2, true},
		{"MT", 0, false},
		{"XX", 0, false},
		{"ca", 12, false},
	}
	for _, tt := range tests {
		if got := model.ValidDistrict(tt.state, tt.district); got != tt.want {
			t.Errorf("ValidDistrict(%q, %d) = %v, want %v", tt.state, tt.district, got, tt.want)
		}
	}
}
