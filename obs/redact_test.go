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
		{"ipv6 after a label", "ip:2001:db8::1", "ip:[redacted]"},
		{"ipv6 after a long label", "client_ip:2001:db8:85a3::8a2e:370:7334", "client_ip:[redacted]"},
		{"ipv6 after a spaced label", "remote addr:2001:db8::1 port 5", "remote addr:[redacted] port 5"},
		{"ipv6 after a hex label", "id:2001:db8::1", "id:[redacted]"},
		{"ipv4-mapped", "ip=::ffff:10.1.2.3", "ip=[redacted]"},
		{"ipv4-mapped json", `{"ip":"::ffff:10.1.2.3"}`, `{"ip":"[redacted]"}`},
		{"ipv6 with node port", "connect ECONNREFUSED 2001:db8::1:443", "connect ECONNREFUSED [redacted]"},
		{"loopback with node port", "connect ECONNREFUSED ::1:3000", "connect ECONNREFUSED [redacted]"},
		{
			"ipv6 with go port",
			"dial tcp [2001:db8::1]:443: connect: connection refused",
			"dial tcp [[redacted]]:443: connect: connection refused",
		},
		{"ipv6 ends a clause", "address 2001:db8::1: not reachable", "address [redacted]: not reachable"},
		{"ipv6 after a leading colon", ":2001:db8::1 refused", ":[redacted] refused"},
		{"ipv6 at the start", "2001:db8::1 refused", "[redacted] refused"},
		{"identifiers with ::", "std::string foo::bar", "std::string foo::bar"},
		{"times", "12:30:45 ts=12:30:45.123", "12:30:45 ts=12:30:45.123"},
		{"timestamp", "at 2026-10-12T12:30:45Z", "at 2026-10-12T12:30:45Z"},
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
