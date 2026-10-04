package handler

import (
	"net/http"
	"time"
)

const (
	// policyAreasCacheKey holds GET /policy-areas. A new policy area appears only when the pipeline
	// syncs the first bill on it, which is rare (Congress.gov has used about 33 for decades).
	policyAreasCacheKey = "policy-areas"
	policyAreasCacheTTL = time.Hour
	// policyAreasCacheControl lets the web's server and Vercel reuse the list for an hour, as
	// GET /bill-index does.
	policyAreasCacheControl = "public, max-age=3600, s-maxage=3600, stale-while-revalidate=86400"
)

// policyAreasResponse is every policy area name, A to Z (#708).
type policyAreasResponse struct {
	PolicyAreas []string `json:"policy_areas"`
}

// ListPolicyAreas returns the name of every policy area Congress.gov has given a bill we hold, A to
// Z: the values GET /bills's policy_area filter takes. /vote's filter lists them all, so a policy
// area with no recently updated bill can still be picked (#708).
func (h *Handler) ListPolicyAreas(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if cached, hit := h.cacheGet(ctx, policyAreasCacheKey); hit {
		w.Header().Set("Cache-Control", policyAreasCacheControl)
		writeCachedJSON(w, cached)
		return
	}

	names, err := h.Bills.PolicyAreas(ctx)
	if err != nil {
		h.serverError(w, r, "failed to list policy areas", err)
		return
	}
	resp := policyAreasResponse{PolicyAreas: names}
	h.cacheJSON(ctx, policyAreasCacheKey, resp, policyAreasCacheTTL)
	w.Header().Set("Cache-Control", policyAreasCacheControl)
	writeJSON(w, http.StatusOK, resp)
}
