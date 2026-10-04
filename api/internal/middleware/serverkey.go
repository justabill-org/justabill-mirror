package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// ServerKeyHeader is the request header the web app's server sends its key
// in (API_SERVER_KEY on the web side).
const ServerKeyHeader = "X-Server-Key"

// ServerKeys returns a [CallerClass] that puts a request whose
// [ServerKeyHeader] matches one of keys in [ClassWebServer], and every other
// request, with a wrong, empty or missing key, in [ClassIP]. With no keys,
// every request is [ClassIP]: the feature is off. Accepting more than one key
// lets a key be rotated without downtime.
//
// It compares SHA-256 digests in constant time and checks every key, so
// neither a key nor its length leaks through timing. The key grants a higher
// rate limit and nothing else, and it's never logged.
func ServerKeys(keys []string) CallerClass {
	digests := make([][sha256.Size]byte, 0, len(keys))
	for _, k := range keys {
		if k != "" {
			digests = append(digests, sha256.Sum256([]byte(k)))
		}
	}
	return func(r *http.Request) string {
		got := r.Header.Get(ServerKeyHeader)
		if got == "" || len(digests) == 0 {
			return ClassIP
		}
		sum := sha256.Sum256([]byte(got))
		match := 0
		for i := range digests {
			match |= subtle.ConstantTimeCompare(sum[:], digests[i][:])
		}
		if match == 1 {
			return ClassWebServer
		}
		return ClassIP
	}
}
