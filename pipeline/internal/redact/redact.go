// Package redact removes API keys from error text before it is stored or sent elsewhere
// (sync_state.last_error, Gemini tool results). The clients already keep keys out of
// URLs; this is the second layer at the boundaries where error text leaves the process.
package redact

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxLen is the longest string String returns, in bytes, not counting the truncation suffix.
const MaxLen = 1024

const (
	placeholder     = "[REDACTED]"
	truncatedSuffix = "…(truncated)"
)

// keyPattern matches `api_key=<value>` (query form) and `X-Api-Key: <value>` (header form,
// also `X-Api-Key=` and quoted JSON keys) in any case. Group 1 is the prefix kept in the
// output; group 2 is the secret.
func keyPattern() *regexp.Regexp {
	return regexp.MustCompile(`(?i)(api_key=|x-api-key["']?\s*[:=]\s*["']?)([^\s&"',;}]+)`)
}

// String returns s with API keys replaced, cut to at most MaxLen bytes (plus a suffix).
func String(s string) string {
	s = keyPattern().ReplaceAllString(s, "${1}"+placeholder)
	if len(s) <= MaxLen {
		return s
	}
	cut := MaxLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncatedSuffix
}

// Error returns err's text passed through String. A nil error returns "".
func Error(err error) string {
	if err == nil {
		return ""
	}
	return String(err.Error())
}

// URLError trims the URL of the *[url.Error] in err's chain to scheme, host and path, so
// query strings (keys, offsets, dates) stay out of the message. It returns err. Call it on
// the error from [http.NewRequest] or [http.Client.Do] before wrapping it: [fmt.Errorf] copies
// the message when it wraps.
func URLError(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		ue.URL = StripQuery(ue.URL)
	}
	return err
}

// StripQuery returns rawURL without its query string and fragment.
func StripQuery(rawURL string) string {
	if i := strings.IndexAny(rawURL, "?#"); i >= 0 {
		return rawURL[:i]
	}
	return rawURL
}
