package handler

import (
	"context"
	"fmt"
	"net/http"
	"slices"
)

// ListCongresses returns all congresses.
func (h *Handler) ListCongresses(w http.ResponseWriter, r *http.Request) {
	congresses, err := h.Congresses.List(r.Context())
	if err != nil {
		h.serverError(w, r, "failed to list congresses", err)
		return
	}

	writeJSON(w, http.StatusOK, congresses)
}

// loadedCongresses returns the numbers in the congresses table: the congresses a filter may name.
func (h *Handler) loadedCongresses(ctx context.Context) (map[int]bool, error) {
	congresses, err := h.Congresses.List(ctx)
	if err != nil {
		return nil, err
	}
	loaded := make(map[int]bool, len(congresses))
	for _, c := range congresses {
		loaded[c.Number] = true
	}
	return loaded, nil
}

// congressFilter reads the scorecard routes' repeatable ?congress= filter
// (?congress=118&congress=119): loaded congress numbers, sorted and without repeats. No filter
// is nil, which means every loaded congress. For a value that isn't a whole number or isn't a
// loaded congress it writes a 400 (a 500 if the congresses can't be read) and returns false.
func (h *Handler) congressFilter(w http.ResponseWriter, r *http.Request) ([]int, bool) {
	raw := r.URL.Query()["congress"]
	if len(raw) == 0 {
		return nil, true
	}
	congresses := make([]int, 0, len(raw))
	for _, v := range raw {
		n, ok := parseCongress(v)
		if !ok {
			writeError(
				w,
				http.StatusBadRequest,
				fmt.Sprintf("congress must be a whole number from 1 to %d", maxCongress),
			)
			return nil, false
		}
		congresses = append(congresses, n)
	}
	slices.Sort(congresses)
	congresses = slices.Compact(congresses)

	loaded, err := h.loadedCongresses(r.Context())
	if err != nil {
		h.serverError(w, r, "failed to list congresses", err)
		return nil, false
	}
	for _, n := range congresses {
		if !loaded[n] {
			writeUnknownCongress(w, n)
			return nil, false
		}
	}
	return congresses, true
}

// writeUnknownCongress answers 400 for a congress that isn't in the congresses table.
func writeUnknownCongress(w http.ResponseWriter, n int) {
	writeError(w, http.StatusBadRequest, fmt.Sprintf("congress %d isn't loaded", n))
}
