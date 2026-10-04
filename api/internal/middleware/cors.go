package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	// allowedHeaders are the request headers browsers may send cross-origin.
	// traceparent and tracestate carry the browser's trace into the API
	// (docs/design/53-observability.md).
	allowedHeaders = "Content-Type, Authorization, X-Firebase-AppCheck, traceparent, tracestate"
	// allowedMethods are the methods browsers may use cross-origin.
	allowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	// preflightMaxAge is how long, in seconds, a browser may reuse a preflight answer.
	preflightMaxAge = 600
	// DevCORSOrigin is the allowlist when CORS_ORIGIN is unset outside
	// production: the Next.js dev server.
	DevCORSOrigin = "http://localhost:3000"
)

// CORS answers cross-origin requests from an explicit allowlist of origins.
// It never allows "*" and never sends Access-Control-Allow-Credentials: the
// API authenticates with the Authorization header, not cookies, so browsers
// have no credentials to include.
type CORS struct {
	exact    map[string]bool
	patterns []originPattern
}

// originPattern is an allowlist entry with one "*" in its first host label,
// such as https://justabill-*-team.vercel.app for Vercel preview deployments.
type originPattern struct {
	prefix, suffix string
}

// NewCORS parses spec, CORS_ORIGIN's comma-separated list of origins
// (scheme://host[:port]). An entry may put one "*" in the first label of its
// host, where it matches one or more letters, digits or hyphens, never a dot.
// An empty spec means DevCORSOrigin outside production and is an error in
// production, which also accepts only https origins.
func NewCORS(spec string, production bool) (*CORS, error) {
	c := &CORS{exact: map[string]bool{}}
	for entry := range strings.SplitSeq(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if err := c.add(entry, production); err != nil {
			return nil, fmt.Errorf("CORS_ORIGIN entry %q: %w", entry, err)
		}
	}
	if len(c.exact) == 0 && len(c.patterns) == 0 {
		if production {
			return nil, errors.New("CORS_ORIGIN is required in production: the web app's origins, comma-separated")
		}
		c.exact[DevCORSOrigin] = true
	}
	return c, nil
}

func (c *CORS) add(entry string, production bool) error {
	if entry == "*" || entry == "null" {
		return errors.New("not allowed: list each origin explicitly")
	}
	u, err := url.Parse(strings.TrimSuffix(entry, "/"))
	if err != nil {
		return err
	}
	switch {
	case u.Scheme != "https" && u.Scheme != "http":
		return errors.New("want scheme://host[:port] with scheme http or https")
	case production && u.Scheme != "https":
		return errors.New("production allows only https origins")
	case u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "":
		return errors.New("want scheme://host[:port] with no path, query or credentials")
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Host)
	if !strings.Contains(origin, "*") {
		c.exact[origin] = true
		return nil
	}
	p, err := parsePattern(origin, len(u.Scheme)+len("://"))
	if err != nil {
		return err
	}
	c.patterns = append(c.patterns, p)
	return nil
}

// parsePattern splits origin around its "*", which must be the only one and
// sit in the first host label (the host starts at hostStart), next to other
// characters of that label so a pattern can't match every subdomain.
func parsePattern(origin string, hostStart int) (originPattern, error) {
	prefix, suffix, _ := strings.Cut(origin, "*")
	label, _, _ := strings.Cut(origin[hostStart:], ".")
	switch {
	case strings.Contains(suffix, "*"):
		return originPattern{}, errors.New("at most one * per origin")
	case !strings.Contains(label, "*"):
		return originPattern{}, errors.New("* is allowed only in the first label of the host")
	case label == "*":
		return originPattern{}, errors.New("* must share its label with fixed text, e.g. https://app-*-team.vercel.app")
	case !strings.Contains(suffix, "."):
		return originPattern{}, errors.New("a pattern needs a fixed domain after the first label")
	}
	return originPattern{prefix: prefix, suffix: suffix}, nil
}

// Allowed reports whether a request from origin may read the API's responses.
func (c *CORS) Allowed(origin string) bool {
	if origin == "" {
		return false
	}
	if c.exact[origin] {
		return true
	}
	for _, p := range c.patterns {
		if p.matches(origin) {
			return true
		}
	}
	return false
}

func (p originPattern) matches(origin string) bool {
	if len(origin) <= len(p.prefix)+len(p.suffix) ||
		!strings.HasPrefix(origin, p.prefix) || !strings.HasSuffix(origin, p.suffix) {
		return false
	}
	for _, r := range origin[len(p.prefix) : len(origin)-len(p.suffix)] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// Handler adds the CORS headers for allowed origins and answers preflight
// requests itself. Every response carries Vary: Origin, because whether it
// allows the caller depends on the Origin header, so a shared cache must not
// hand one origin's answer to another.
func (c *CORS) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); c.Allowed(origin) {
			h.Set("Access-Control-Allow-Origin", origin)
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", allowedMethods)
				h.Set("Access-Control-Allow-Headers", allowedHeaders)
				h.Set("Access-Control-Max-Age", strconv.Itoa(preflightMaxAge))
			}
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
