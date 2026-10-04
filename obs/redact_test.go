package obs_test

import (
	"testing"

	"github.com/justabill-org/justabill/obs"
)

func TestRedact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in, want string
	}{
		{"api key param", "GET /v3/bill?api_key=abc123&format=json", "GET /v3/bill?api_key=[redacted]&format=json"},
		{"apikey param", "url=https://x/y?apiKey=abc", "url=https://x/y?apiKey=[redacted]"},
		{"address param", "GET /api/v1/reps?address=1600+Pennsylvania+Ave", "GET /api/v1/reps?address=[redacted]"},
		{
			"address json",
			`{"address": "1 Main St, \"Apt\" 2", "zip":"20500"}`,
			`{"address":"[redacted]", "zip":"20500"}`,
		},
		{
			"api key header",
			"sent X-Api-Key: abc123 to api.congress.gov",
			"sent X-Api-Key: [redacted] to api.congress.gov",
		},
		{"api key header json", `{"x-api-key":"abc123"}`, `{"x-api-key":"[redacted]"}`},
		{"email", "user jane.doe+x@example.co.uk signed in", "user [redacted] signed in"},
		{"ipv4", "dial tcp 10.1.2.3:443: refused", "dial tcp [redacted]:443: refused"},
		{"ipv6", "from 2001:db8::1 and fe80::1", "from [redacted] and [redacted]"},
		{"not an ip", "999.1.1.1 at 12:30:45 v1.2.3", "999.1.1.1 at 12:30:45 v1.2.3"},
		{"clean", "bill hr-119-1 synced", "bill hr-119-1 synced"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := obs.Redact(tt.in); got != tt.want {
				t.Errorf("Redact(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}
