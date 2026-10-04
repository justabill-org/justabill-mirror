package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/api/internal/auth"
)

func TestSettingsValidate(t *testing.T) {
	tests := []struct {
		name    string
		s       auth.Settings
		wantErr string
	}{
		{"production with the emulator", auth.Settings{
			Production: true, Enabled: true, ProjectID: "p", EmulatorHost: "localhost:9099",
		}, "FIREBASE_AUTH_EMULATOR_HOST"},
		{"production with the emulator and accounts off", auth.Settings{
			Production: true, EmulatorHost: "localhost:9099",
		}, "FIREBASE_AUTH_EMULATOR_HOST"},
		{"production without a project", auth.Settings{Production: true, Enabled: true}, "AUTH_PROJECT_ID"},
		{"development without a project", auth.Settings{Enabled: true}, "demo-justabill"},
		{"production, accounts off", auth.Settings{Production: true}, ""},
		{"production, accounts on", auth.Settings{Production: true, Enabled: true, ProjectID: "p"}, ""},
		{"development with the emulator", auth.Settings{
			Enabled: true, ProjectID: "demo-justabill", EmulatorHost: "localhost:9099",
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.s.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("Validate() = %v, want an error mentioning %s", err, tt.wantErr)
			}
		})
	}
}

func TestSignedInWithin(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		authTime time.Time
		want     bool
	}{
		{now.Add(-4 * time.Minute), true},
		{now.Add(-5 * time.Minute), true},
		{now.Add(-6 * time.Minute), false},
		{time.Time{}, false},
	}
	for _, tt := range tests {
		p := auth.Principal{UID: "u", AuthTime: tt.authTime}
		if got := p.SignedInWithin(5*time.Minute, now); got != tt.want {
			t.Errorf("SignedInWithin(5m) with auth_time %v = %v, want %v", tt.authTime, got, tt.want)
		}
	}
}
