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
