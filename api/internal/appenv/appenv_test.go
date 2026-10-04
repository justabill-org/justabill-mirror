package appenv_test

import (
	"testing"

	"github.com/justabill-org/justabill/api/internal/appenv"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in      string
		want    appenv.Env
		wantErr bool
	}{
		{"", appenv.Development, false},
		{"development", appenv.Development, false},
		{"production", appenv.Production, false},
		{"prod", "", true},
		{"Production", "", true},
	}
	for _, tt := range tests {
		got, err := appenv.Parse(tt.in)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("Parse(%q) = %q, %v; want %q, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}
