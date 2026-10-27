package upstream

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrUnknownHost means the request's host isn't declared in the client's host map. Every
// upstream host needs its own budget and timeout, so undeclared hosts (including redirect
// targets) are refused before anything is sent.
var ErrUnknownHost = errors.New("upstream: host not declared")

// ErrInsecureKey means a host with an API key was requested over plain HTTP. Keys are only
// sent over HTTPS, or to a loopback host (tests and local mocks).
var ErrInsecureKey = errors.New("upstream: refusing to send an API key over plain http")

// ErrBodyTooLarge means a response body was larger than the host's MaxBodyBytes.
var ErrBodyTooLarge = errors.New("upstream: response body exceeds the size cap")

// StatusError is a request that finally failed with an HTTP error status. Its text names
// the host and path only, never the query string.
type StatusError struct {
	Host     string
	Path     string
	Status   int
	Attempts int
}

// Error implements error.
func (e *StatusError) Error() string {
	return fmt.Sprintf("upstream %s%s: HTTP %d after %d attempt(s)", e.Host, e.Path, e.Status, e.Attempts)
}

// TransportError is a request whose host never answered, after the retries: the last attempt's
// connection, TLS or read error, or its deadline. Like [StatusError], its text names the host
// and path only.
type TransportError struct {
	Host     string
	Path     string
	Attempts int
	Err      error
}

// Error implements error.
func (e *TransportError) Error() string {
	return fmt.Sprintf("upstream %s%s after %d attempt(s): %v", e.Host, e.Path, e.Attempts, e.Err)
}

// Unwrap returns the last attempt's error.
func (e *TransportError) Unwrap() error { return e.Err }

// Outage is a request that failed because its host was down: Status is the HTTP 5xx it
// answered, or 0 when it never answered ([TransportError]).
type Outage struct {
	Host   string
	Status int
}

// Outages returns the outage behind every cause in err's tree, and whether every cause is one:
// a [StatusError] with a 5xx status or a [TransportError]. It walks a join's parts one by one, so
// a 5xx joined with any other error (a 4xx, a parse or store error) isn't an outage, where
// [errors.As] on the join would match the 5xx alone. A sentinel wrapped beside a 5xx
// (fmt.Errorf("%w: %w", errX, err)) is such another cause.
func Outages(err error) ([]Outage, bool) {
	switch e := err.(type) { //nolint:errorlint // walks the tree by hand to see every cause
	case nil:
		return nil, false
	case *StatusError:
		return []Outage{{Host: e.Host, Status: e.Status}}, e.Status >= http.StatusInternalServerError
	case *TransportError:
		return []Outage{{Host: e.Host}}, true
	case interface{ Unwrap() []error }:
		causes := e.Unwrap()
		var out []Outage
		for _, c := range causes {
			o, ok := Outages(c)
			if !ok {
				return nil, false
			}
			out = append(out, o...)
		}
		return out, len(causes) > 0
	case interface{ Unwrap() error }:
		return Outages(e.Unwrap())
	default:
		return nil, false
	}
}

// IsPermanent reports whether err will fail the same way if retried later: HTTP 400, 401,
// 403, 404 or 410, an oversized body, an undeclared host, or a key refused over plain HTTP.
func IsPermanent(err error) bool {
	if errors.Is(err, ErrBodyTooLarge) || errors.Is(err, ErrUnknownHost) || errors.Is(err, ErrInsecureKey) {
		return true
	}
	se, ok := errors.AsType[*StatusError](err)
	if !ok {
		return false
	}
	switch se.Status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusGone:
		return true
	}
	return false
}
