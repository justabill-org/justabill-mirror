package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/db/model"
)

// Limits for the civic_graph endpoints (docs/design/31-bill-ontology.md). The handler clamps
// ?limit itself so each bill or member has a small, fixed set of cache keys.
const (
	defaultRelatedLimit      = 10
	maxRelatedLimit          = 50
	defaultCollaboratorLimit = 20
	maxCollaboratorLimit     = 100
	// companionVoteRows is how many member votes the companion-votes endpoint reads: four full
	// House roll calls or twenty Senate ones. It is fixed, so each bill has one cache key.
	companionVoteRows = 2000

	// graphQueryTimeout bounds each graph query, so a well-connected bill can't hold a Spanner
	// session for the whole request timeout.
	graphQueryTimeout = 2 * time.Second
	// graphCacheTTL: link tables only change when the pipeline syncs a bill, a few times a day.
	graphCacheTTL = time.Hour

	bioguideIDLen = 7
	// currentCongressKey stands in for the congress in collaborator cache keys when the request
	// leaves it to the server, so cache hits don't need to look up the current congress.
	currentCongressKey = "current"
)

// isBillType reports whether t is a Congress.gov bill type, lower-cased as they appear in bill IDs.
func isBillType(t string) bool {
	switch t {
	case "hr", "s", "hjres", "sjres", "hconres", "sconres", "hres", "sres":
		return true
	}
	return false
}

// validBillID reports whether id has the <type>-<congress>-<number> form the pipeline writes.
func validBillID(id string) bool {
	parts := strings.Split(id, "-")
	if len(parts) != 3 || !isBillType(parts[0]) { // type-congress-number
		return false
	}
	return positiveInt(parts[1]) && positiveInt(parts[2])
}

// validBioguideID reports whether id looks like a bioguide ID: one capital letter, six digits.
func validBioguideID(id string) bool {
	if len(id) != bioguideIDLen || id[0] < 'A' || id[0] > 'Z' {
		return false
	}
	return allDigits(id[1:])
}

func allDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// positiveInt reports whether s is a plain decimal number greater than zero (no sign or spaces).
func positiveInt(s string) bool {
	if !allDigits(s) {
		return false
	}
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

// clampQueryLimit reads ?limit, falling back to def when it is missing or not positive.
func clampQueryLimit(r *http.Request, def, maxLimit int) int {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		return def
	}
	return min(limit, maxLimit)
}

// GetRelatedBills returns bills explicitly related to a bill or sharing at least two subjects
// with it, explicit relations first.
func (h *Handler) GetRelatedBills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validBillID(id) {
		writeError(w, http.StatusBadRequest, "invalid bill id")
		return
	}
	limit := clampQueryLimit(r, defaultRelatedLimit, maxRelatedLimit)

	cacheKey := fmt.Sprintf("graph:related:%s:%d", id, limit)
	if cached, ok := h.cacheGet(ctx, cacheKey); ok {
		writeCachedJSON(w, cached)
		return
	}

	qctx, cancel := context.WithTimeout(ctx, graphQueryTimeout)
	defer cancel()
	related, err := h.Graph.RelatedBills(qctx, id, limit)
	if err != nil {
		h.writeGraphError(w, r, "related bills", err, qctx.Err())
		return
	}

	h.cacheGraphResult(ctx, cacheKey, len(related), related)
	writeJSON(w, http.StatusOK, related)
}

// collaboratorsResponse names the congress the collaborators were counted in, since the
// server picks the current one when the request doesn't say.
type collaboratorsResponse struct {
	Congress      int                  `json:"congress"`
	Collaborators []model.Collaborator `json:"collaborators"`
}

// GetCollaborators returns the members who sponsored or cosponsored the most bills together
// with a member in one congress (?congress, default the current congress).
func (h *Handler) GetCollaborators(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validBioguideID(id) {
		writeError(w, http.StatusBadRequest, "invalid member id")
		return
	}
	congressKey := currentCongressKey
	if raw := r.URL.Query().Get("congress"); raw != "" {
		if !positiveInt(raw) {
			writeError(w, http.StatusBadRequest, "congress must be a positive integer")
			return
		}
		congressKey = raw
	}
	limit := clampQueryLimit(r, defaultCollaboratorLimit, maxCollaboratorLimit)

	cacheKey := fmt.Sprintf("graph:collaborators:%s:%s:%d", id, congressKey, limit)
	if cached, ok := h.cacheGet(ctx, cacheKey); ok {
		writeCachedJSON(w, cached)
		return
	}

	congress, err := h.resolveCongress(ctx, congressKey)
	if err != nil {
		h.serverError(w, r, "failed to get collaborators", err)
		return
	}
	if congress == 0 {
		writeError(w, http.StatusBadRequest, "congress query parameter is required")
		return
	}

	qctx, cancel := context.WithTimeout(ctx, graphQueryTimeout)
	defer cancel()
	collaborators, err := h.Graph.Collaborators(qctx, id, congress, limit)
	if err != nil {
		h.writeGraphError(w, r, "collaborators", err, qctx.Err())
		return
	}

	resp := collaboratorsResponse{Congress: congress, Collaborators: collaborators}
	h.cacheGraphResult(ctx, cacheKey, len(collaborators), resp)
	writeJSON(w, http.StatusOK, resp)
}

// companionRollCall is one roll call on a bill's companion with every member's vote on it.
type companionRollCall struct {
	CompanionBillID string                `json:"companion_bill_id"`
	VoteID          string                `json:"vote_id"`
	Chamber         string                `json:"chamber"`
	VoteDate        time.Time             `json:"vote_date"`
	Question        *string               `json:"question,omitempty"`
	Result          *string               `json:"result,omitempty"`
	Votes           []companionMemberVote `json:"votes"`
}

// companionMemberVote is one member's vote on a companion roll call. Party comes from the
// member's term in the roll call's chamber and is missing when that term isn't synced.
type companionMemberVote struct {
	MemberID  string  `json:"member_id"`
	FirstName string  `json:"first_name"`
	LastName  string  `json:"last_name"`
	Party     *string `json:"party,omitempty"`
	Vote      string  `json:"vote"`
}

// GetCompanionVotes returns the recorded roll calls on a bill's companion (an identical bill
// in the other chamber) with each member's vote, newest roll call first.
func (h *Handler) GetCompanionVotes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validBillID(id) {
		writeError(w, http.StatusBadRequest, "invalid bill id")
		return
	}

	cacheKey := "graph:companion-votes:" + id
	if cached, ok := h.cacheGet(ctx, cacheKey); ok {
		writeCachedJSON(w, cached)
		return
	}

	qctx, cancel := context.WithTimeout(ctx, graphQueryTimeout)
	defer cancel()
	votes, err := h.Graph.CompanionVotes(qctx, id, companionVoteRows)
	if err != nil {
		h.writeGraphError(w, r, "companion votes", err, qctx.Err())
		return
	}

	rollCalls := groupCompanionVotes(votes, len(votes) >= companionVoteRows)
	h.cacheGraphResult(ctx, cacheKey, len(rollCalls), rollCalls)
	writeJSON(w, http.StatusOK, rollCalls)
}

// groupCompanionVotes folds member votes, ordered by roll call, into one entry per roll call.
// When the query hit its row limit (truncated), the last roll call may be missing members, so
// it is dropped rather than shown with a wrong tally, unless it is the only one.
func groupCompanionVotes(votes []model.CompanionVote, truncated bool) []companionRollCall {
	out := []companionRollCall{}
	for _, v := range votes {
		if len(out) == 0 || out[len(out)-1].VoteID != v.VoteID {
			out = append(out, companionRollCall{
				CompanionBillID: v.CompanionBillID,
				VoteID:          v.VoteID,
				Chamber:         v.Chamber,
				VoteDate:        v.VoteDate,
				Question:        v.Question,
				Result:          v.Result,
				Votes:           []companionMemberVote{},
			})
		}
		last := &out[len(out)-1]
		last.Votes = append(last.Votes, companionMemberVote{
			MemberID:  v.MemberID,
			FirstName: v.FirstName,
			LastName:  v.LastName,
			Party:     v.Party,
			Vote:      v.Vote,
		})
	}
	if truncated && len(out) > 1 {
		out = out[:len(out)-1]
	}
	return out
}

// resolveCongress turns a congress cache-key part into a number: the current congress (or the
// newest one if none is flagged current) for currentCongressKey. It returns 0 when there are
// no congresses at all.
func (h *Handler) resolveCongress(ctx context.Context, key string) (int, error) {
	if key != currentCongressKey {
		return strconv.Atoi(key)
	}
	congresses, err := h.Congresses.List(ctx)
	if err != nil {
		return 0, err
	}
	newest := 0
	for _, c := range congresses {
		if c.IsCurrent {
			return c.Number, nil
		}
		newest = max(newest, c.Number)
	}
	return newest, nil
}

// cacheGraphResult caches a non-empty graph result. Empty results aren't cached: they are cheap
// to recompute, and caching them would let requests for made-up IDs fill the cache.
func (h *Handler) cacheGraphResult(ctx context.Context, key string, n int, v any) {
	if n == 0 {
		return
	}
	h.cacheJSON(ctx, key, v, graphCacheTTL)
}

// writeGraphError answers 504 when the graph query hit its timeout (qerr is the query context's
// error) and 500 otherwise.
func (h *Handler) writeGraphError(w http.ResponseWriter, r *http.Request, what string, err, qerr error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(qerr, context.DeadlineExceeded) {
		h.log.ErrorContext(r.Context(), "graph query timed out", "route", routePattern(r), "query", what, "error", err)
		writeError(w, http.StatusGatewayTimeout, what+" query timed out")
		return
	}
	h.serverError(w, r, "failed to get "+what, err)
}
