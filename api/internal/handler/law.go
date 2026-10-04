package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/db/cachekey"
	"github.com/justabill-org/justabill/db/model"
)

// Current law and what bills change in it (docs/design/149-law-aware-assistant.md, plan item 5).
const (
	// alsoChangedByLimit is how many other bills of the congress each law-changes entry lists.
	alsoChangedByLimit = 5
	// lawCacheTTL: the pipeline deletes a bill's law-changes key when a sync stores its text or
	// its explanations, and both responses are keyed on the release point, so the TTL only
	// bounds what's missed.
	lawCacheTTL = time.Hour
	// releasePointCacheTTL is how long a new US Code release point can take to reach the keys.
	releasePointCacheTTL  = 5 * time.Minute
	releasePointCacheKey  = "law:release-point"
	lawSectionCachePrefix = "law:section:"

	// maxUSCTitle bounds the title number; the US Code has 54 titles.
	maxUSCTitle = 99
	// maxSectionNumberLen bounds the section number ("1395w-4", "300gg-91").
	maxSectionNumberLen = 40
)

// sectionNumberPattern is a US Code section number: letters and digits, in parts joined by
// hyphens.
var sectionNumberPattern = regexp.MustCompile(`^[0-9A-Za-z]+(-[0-9A-Za-z]+)*$`)

// lawChangesResponse is the GET /bills/{id}/law-changes body. AIGenerated is true when any entry
// has an explanation, which is AI text; Explained says which model wrote them.
// CurrentReleasePoint is the US Code release point loaded now, or null before the first load.
type lawChangesResponse struct {
	*model.BillLawChanges

	AIGenerated         bool                   `json:"ai_generated"`
	CurrentReleasePoint *model.USCReleasePoint `json:"current_release_point"`
}

// lawSectionResponse is the GET /law/{title}/{section} body: the section as stored, whose
// release_point is the one its text last changed at, and the release point loaded now.
type lawSectionResponse struct {
	*model.USCSection

	CurrentReleasePoint *model.USCReleasePoint `json:"current_release_point"`
}

// GetBillLawChanges returns what a bill's latest text changes in current law: one entry per
// section it amends, repeals or adds, with its explanation when there is one.
func (h *Handler) GetBillLawChanges(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validBillID(id) {
		writeError(w, http.StatusBadRequest, "invalid bill id")
		return
	}
	rp, err := h.currentReleasePoint(ctx)
	if err != nil {
		h.serverError(w, r, "failed to get law changes", err)
		return
	}
	rpName := releasePointName(rp)

	// The cached value starts with the release point it was built at, so a new one misses.
	cacheKey := cachekey.BillLawChanges(id)
	if cached, ok := h.cacheGet(ctx, cacheKey); ok {
		if at, body, found := strings.Cut(cached, "\n"); found && at == rpName {
			writeCachedJSON(w, body)
			return
		}
	}

	qctx, cancel := context.WithTimeout(ctx, graphQueryTimeout)
	defer cancel()
	changes, err := h.Law.BillLawChangeEntries(qctx, id, alsoChangedByLimit)
	if err != nil {
		h.writeGraphError(w, r, "law changes", err, qctx.Err())
		return
	}
	if changes == nil {
		writeError(w, http.StatusNotFound, msgBillNotFound)
		return
	}

	resp := lawChangesResponse{BillLawChanges: changes, AIGenerated: changes.Explained != nil, CurrentReleasePoint: rp}
	if encoded, encErr := json.Marshal(resp); encErr == nil {
		h.cacheSet(ctx, cacheKey, rpName+"\n"+string(encoded), lawCacheTTL)
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetLawSection returns a US Code section's current text, heading and status:
// GET /law/42/1395w-4 is the section "/us/usc/t42/s1395w-4".
func (h *Handler) GetLawSection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	title, section, ok := parseLawSectionPath(chi.URLParam(r, "title"), chi.URLParam(r, "section"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid US Code title or section")
		return
	}
	sectionID := model.USCSectionID(title, section)

	rp, err := h.currentReleasePoint(ctx)
	if err != nil {
		h.serverError(w, r, "failed to get law section", err)
		return
	}
	// Keyed on the release point: a load rewrites sections before it records the point.
	cacheKey := ""
	if rp != nil {
		cacheKey = lawSectionCachePrefix + rp.ReleasePoint + ":" + sectionID
		if cached, hit := h.cacheGet(ctx, cacheKey); hit {
			writeCachedJSON(w, cached)
			return
		}
	}

	qctx, cancel := context.WithTimeout(ctx, graphQueryTimeout)
	defer cancel()
	sec, err := h.Law.Section(qctx, sectionID)
	if err != nil {
		h.writeGraphError(w, r, "law section", err, qctx.Err())
		return
	}
	if sec == nil {
		writeError(w, http.StatusNotFound, "section not found")
		return
	}

	resp := lawSectionResponse{USCSection: sec, CurrentReleasePoint: rp}
	if cacheKey != "" {
		h.cacheJSON(ctx, cacheKey, resp, lawCacheTTL)
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseLawSectionPath validates the title and section of GET /law/{title}/{section}. The section
// may be written with the en dash the Law Revision Counsel prints ("1395w–4").
func parseLawSectionPath(rawTitle, rawSection string) (int, string, bool) {
	if !positiveInt(rawTitle) {
		return 0, "", false
	}
	title, err := strconv.Atoi(rawTitle)
	if err != nil || title > maxUSCTitle {
		return 0, "", false
	}
	section := strings.NewReplacer("–", "-", "—", "-").Replace(rawSection)
	if len(section) > maxSectionNumberLen || !sectionNumberPattern.MatchString(section) {
		return 0, "", false
	}
	return title, section, true
}

// currentReleasePoint returns the loaded US Code release point, cached for
// releasePointCacheTTL, or nil before the first load (not cached, so the first load shows at
// once).
func (h *Handler) currentReleasePoint(ctx context.Context) (*model.USCReleasePoint, error) {
	if cached, ok := h.cacheGet(ctx, releasePointCacheKey); ok {
		var rp model.USCReleasePoint
		if err := json.Unmarshal([]byte(cached), &rp); err == nil {
			return &rp, nil
		}
	}
	rp, err := h.Law.CurrentReleasePoint(ctx)
	if err != nil || rp == nil {
		return nil, err
	}
	h.cacheJSON(ctx, releasePointCacheKey, rp, releasePointCacheTTL)
	return rp, nil
}

// releasePointName is rp's name, or "" before the first load.
func releasePointName(rp *model.USCReleasePoint) string {
	if rp == nil {
		return ""
	}
	return rp.ReleasePoint
}
