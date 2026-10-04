package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// decodeJSON decodes a JSON request body into dst. On failure it writes the
// error response and returns false: 413 when the body is over its
// [http.MaxBytesReader] cap (the router's MaxBytes middleware, or a route's
// own), 400 for anything else.
func decodeJSON(w http.ResponseWriter, body io.Reader, dst any) bool {
	err := json.NewDecoder(body).Decode(dst)
	if err == nil {
		return true
	}
	if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	}
	writeError(w, http.StatusBadRequest, "invalid request body")
	return false
}
