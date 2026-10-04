package handler

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const (
	// billIndexCacheTTL: bills are added and updated when the pipeline syncs, a few times a day.
	billIndexCacheTTL = time.Hour
	// billIndexCacheControl matches the web sitemap, which regenerates at most once an hour.
	billIndexCacheControl = "public, max-age=3600, s-maxage=3600, stale-while-revalidate=86400"
)

// congressBillsResponse is a list of bills in one congress: every bill for the web sitemap
// (#86), or the bills past committee with their status for My votes (#853).
type congressBillsResponse[T any] struct {
	Congress int `json:"congress"`
	Bills    []T `json:"bills"`
}

// GetBillIndex returns the ID and updated_at of every bill in ?congress, in one response. The
// web sitemap lists them all; paging through /bills would take hundreds of requests.
func (h *Handler) GetBillIndex(w http.ResponseWriter, r *http.Request) {
	serveCongressBills(h, w, r, "bills:index", "failed to list the bill index", h.Bills.Index)
}

// GetBillStatuses returns the ID and current_status of every bill in ?congress that has passed a
// chamber or gone further, in one response. My votes joins the visitor's own votes to it in the
// browser, so no request names the bills they voted on (design 72's promise for signed-out
// votes); the answer is the same for everyone and reads no identity.
func (h *Handler) GetBillStatuses(w http.ResponseWriter, r *http.Request) {
	serveCongressBills(h, w, r, "bills:statuses", "failed to list bill statuses", h.Bills.StatusIndex)
}

// serveCongressBills answers a public, per-congress list of bills read by read: a 400 for a bad
// ?congress, then Redis under <keyPrefix>:<congress> for billIndexCacheTTL, then read, with the
// CDN's billIndexCacheControl on every 200. A bill's row changes only when the pipeline syncs it,
// a few times a day.
func serveCongressBills[T any](
	h *Handler, w http.ResponseWriter, r *http.Request, keyPrefix, failure string,
	read func(ctx context.Context, congress int) ([]T, error),
) {
	ctx := r.Context()
	congress, ok := parseCongress(r.URL.Query().Get("congress"))
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("congress must be a whole number from 1 to %d", maxCongress))
		return
	}

	cacheKey := fmt.Sprintf("%s:%d", keyPrefix, congress)
	if cached, hit := h.cacheGet(ctx, cacheKey); hit {
		w.Header().Set("Cache-Control", billIndexCacheControl)
		writeCachedJSON(w, cached)
		return
	}

	bills, err := read(ctx, congress)
	if err != nil {
		h.serverError(w, r, failure, err)
		return
	}
	resp := congressBillsResponse[T]{Congress: congress, Bills: bills}
	h.cacheJSON(ctx, cacheKey, resp, billIndexCacheTTL)
	w.Header().Set("Cache-Control", billIndexCacheControl)
	writeJSON(w, http.StatusOK, resp)
}
