package obs

import (
	"net/netip"
	"regexp"
	"strings"
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
	// Candidates only: netip decides whether a match is an address, so "12:30:45" survives. Group 1
	// is the character before a candidate, kept in the output, so a match can't start inside a word
	// ("ip:2001:db8::1", "std::string"); group 2 is the candidate. The dotted-quad tail comes first,
	// so an IPv4-mapped address ("::ffff:10.1.2.3") is one match.
	ipv6Re = regexp.MustCompile(`(^|[^0-9A-Za-z])` +
		`((?:[0-9A-Fa-f]{0,4}:){2,7}(?:\d{1,3}(?:\.\d{1,3}){3}|[0-9A-Fa-f]{1,4})?)`)
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
	s = redactIPv6(s)

	return ipv4Re.ReplaceAllStringFunc(s, redactIP)
}

// redactIPv6 masks the IPv6 addresses among ipv6Re's candidates. It takes the groups from matches
// over the whole text: rerunning the pattern on one match would let ^ take the guard character.
func redactIPv6(s string) string {
	matches := ipv6Re.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}

	var b strings.Builder

	b.Grow(len(s))

	last := 0
	for _, m := range matches {
		start, end := m[4], m[5]
		b.WriteString(s[last:start])
		b.WriteString(maskIPv6(s[start:end]))
		last = end
	}

	b.WriteString(s[last:])

	return b.String()
}

// maskIPv6 masks a candidate that is an IPv6 address. The candidate pattern is greedy, so it can
// take the colons that end a clause ("2001:db8::1: not reachable") or, at the start of the text,
// the colon before an address (":2001:db8::1"): it retries without them and keeps them around
// the mask.
func maskIPv6(candidate string) string {
	if isAddr(candidate) {
		return Redacted
	}

	addr := strings.TrimRight(candidate, ":")
	head, tail := "", candidate[len(addr):]

	if !isAddr(addr) {
		rest, ok := strings.CutPrefix(addr, ":")
		if !ok || !isAddr(rest) {
			return candidate
		}

		head = ":"
	}

	return head + Redacted + tail
}

func redactIP(candidate string) string {
	if !isAddr(candidate) {
		return candidate
	}

	return Redacted
}

func isAddr(s string) bool {
	_, err := netip.ParseAddr(s)

	return err == nil
}
