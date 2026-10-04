package obs

import (
	"net/netip"
	"regexp"
)

// Redacted replaces a value that Redact removed.
const Redacted = "[redacted]"

var (
	// Secrets and addresses as query or form parameters (api_key=..., address=...).
	paramRe = regexp.MustCompile(`(?i)\b(api_?key|address)=[^&\s"']*`)
	// The same keys as JSON members ("address": "...").
	jsonRe = regexp.MustCompile(`(?i)"(api_?key|address)"\s*:\s*"(?:[^"\\]|\\.)*"`)
	// api.data.gov's key header, as a dumped header or request (X-Api-Key: ...).
	headerRe = regexp.MustCompile(`(?i)\b(x-api-key["']?\s*[:=]\s*["']?)[^\s&"',;}]+`)
	emailRe  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	// Candidates only: netip decides whether a match is an address, so "12:30:45" survives.
	ipv6Re = regexp.MustCompile(`(?:[0-9A-Fa-f]{0,4}:){2,7}(?:[0-9A-Fa-f]{1,4}|\d{1,3}(?:\.\d{1,3}){3})?`)
	ipv4Re = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
)

// Redact masks what must never reach telemetry from free text such as URLs, error messages and
// upstream log lines: api_key, X-Api-Key and address values, email addresses, and IPv4 and IPv6
// addresses. It's the source-side half of the design's privacy allowlist; the Collector's
// redaction processor is the backstop.
func Redact(s string) string {
	s = paramRe.ReplaceAllString(s, "${1}="+Redacted)
	s = jsonRe.ReplaceAllString(s, `"${1}":"`+Redacted+`"`)
	s = headerRe.ReplaceAllString(s, "${1}"+Redacted)
	s = emailRe.ReplaceAllString(s, Redacted)
	s = ipv6Re.ReplaceAllStringFunc(s, redactIP)

	return ipv4Re.ReplaceAllStringFunc(s, redactIP)
}

func redactIP(candidate string) string {
	if _, err := netip.ParseAddr(candidate); err != nil {
		return candidate
	}

	return Redacted
}
