package handler

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/db/model"
)

// Published aggregates (docs/design/89-aggregate-analytics.md, plan item 7). The routes read only
// vote_aggregates and rep_alignment through [repository.AggregateReader], never user_votes.
const (
	// aggregatesCacheTTL: the aggregation job republishes at most hourly, so five minutes of Redis
	// only bounds how late a new snapshot shows.
	aggregatesCacheTTL = 5 * time.Minute
	// aggregatesCacheControl is the design's header: the numbers are public and the same for
	// every visitor.
	aggregatesCacheControl = "public, max-age=300"

	codeAggregatesOff = "aggregates_off"
	msgAggregatesOff  = "aggregates aren't public"
	msgInvalidBillID  = "invalid bill id"
)

// scopeKeyPattern is a state ("CA") or district ("CA-12", at-large "AK-0") scope key.
var scopeKeyPattern = regexp.MustCompile(`^[A-Z]{2}(-[0-9]{1,2})?$`)

// aggregateCell is a served cell. It lists its fields one by one, so nothing the job keeps for
// itself (basis_yea, basis_nay, hold_reason) can reach a response through model.VoteAggregate.
type aggregateCell struct {
	Scope       string     `json:"scope"`
	ScopeKey    string     `json:"scope_key"`
	Status      string     `json:"status"`
	YeaPct      *int       `json:"yea_pct"`
	NayPct      *int       `json:"nay_pct"`
	VotersFloor *int       `json:"voters_floor"`
	PublishedAt *time.Time `json:"published_at"`
}

// billAggregatesResponse is the GET /bills/{id}/aggregates body. AsOf is when the newest served
// cell was computed, or null when no cell is served.
type billAggregatesResponse struct {
	BillID    string          `json:"bill_id"`
	AsOf      *time.Time      `json:"as_of"`
	National  *aggregateCell  `json:"national"`
	States    []aggregateCell `json:"states"`
	Districts []aggregateCell `json:"districts"`
}

// scopeAggregateResponse is the GET /bills/{id}/aggregates/{scope_key} body: one constituency's
// cell (null when it isn't served: too few votes yet, or none) and the votes on the bill of the
// members who held its seat.
type scopeAggregateResponse struct {
	BillID   string                       `json:"bill_id"`
	Scope    string                       `json:"scope"`
	ScopeKey string                       `json:"scope_key"`
	AsOf     *time.Time                   `json:"as_of"`
	Cell     *aggregateCell               `json:"cell"`
	Members  []model.ConstituencyPosition `json:"members"`
}

// memberAlignmentResponse is the GET /members/{id}/alignment body.
type memberAlignmentResponse struct {
	MemberID  string               `json:"member_id"`
	Alignment []model.RepAlignment `json:"alignment"`
}

// servedCell converts a published or held cell, and reports false for any other status, so a
// suppressed cell is left out even if a reader returned one.
func servedCell(c *model.VoteAggregate) (aggregateCell, bool) {
	if c.Status != model.AggregateStatusPublished && c.Status != model.AggregateStatusHeld {
		return aggregateCell{}, false
	}
	return aggregateCell{
		Scope: c.Scope, ScopeKey: c.ScopeKey, Status: c.Status,
		YeaPct: c.YeaPct, NayPct: c.NayPct, VotersFloor: c.VotersFloor, PublishedAt: c.PublishedAt,
	}, true
}

// aggregatesOn writes a 404 and reports false while AGGREGATES_PUBLIC is off, so the web panel
// renders nothing (docs/design/89-aggregate-analytics.md, "Rollout").
func (h *Handler) aggregatesOn(w http.ResponseWriter) bool {
	if h.Aggregates == nil {
		writeCodedError(w, http.StatusNotFound, codeAggregatesOff, msgAggregatesOff)
		return false
	}
	return true
}

// billExists reports whether the bill exists; on false it has written the response (404 or 500).
func (h *Handler) billExists(w http.ResponseWriter, r *http.Request, id string) bool {
	bill, err := h.Bills.GetByID(r.Context(), id)
	if err != nil {
		h.serverError(w, r, "failed to get bill", err)
		return false
	}
	if bill == nil {
		writeError(w, http.StatusNotFound, msgBillNotFound)
		return false
	}
	return true
}

// GetBillAggregates is GET /bills/{id}/aggregates: the bill's published and held cells,
// national, then states and districts by key. Suppressed cells are left out, not sent as zeros.
func (h *Handler) GetBillAggregates(w http.ResponseWriter, r *http.Request) {
	if !h.aggregatesOn(w) {
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validBillID(id) {
		writeError(w, http.StatusBadRequest, msgInvalidBillID)
		return
	}
	cacheKey := "aggregates:bill:" + id
	if cached, hit := h.cacheGet(ctx, cacheKey); hit {
		w.Header().Set("Cache-Control", aggregatesCacheControl)
		writeCachedJSON(w, cached)
		return
	}

	cells, err := h.Aggregates.BillAggregates(ctx, id)
	if err != nil {
		h.serverError(w, r, "failed to get aggregates", err)
		return
	}
	resp := billAggregatesResponse{BillID: id, States: []aggregateCell{}, Districts: []aggregateCell{}}
	for i := range cells {
		cell, ok := servedCell(&cells[i])
		if !ok {
			continue
		}
		resp.AsOf = latest(resp.AsOf, cells[i].ComputedAt)
		switch cell.Scope {
		case model.AggregateScopeNational:
			resp.National = &cell
		case model.AggregateScopeState:
			resp.States = append(resp.States, cell)
		case model.AggregateScopeDistrict:
			resp.Districts = append(resp.Districts, cell)
		}
	}
	// No cell is a normal answer for a bill, but not for an ID nobody has.
	if resp.AsOf == nil && !h.billExists(w, r, id) {
		return
	}

	h.cacheJSON(ctx, cacheKey, resp, aggregatesCacheTTL)
	w.Header().Set("Cache-Control", aggregatesCacheControl)
	writeJSON(w, http.StatusOK, resp)
}

// GetScopeAggregate is GET /bills/{id}/aggregates/{scope_key}: one state's or district's cell,
// with the votes on the bill of the members who held that seat (the district-vs-rep card). A
// constituency without a served cell gets a null cell, so the card can still show its members.
func (h *Handler) GetScopeAggregate(w http.ResponseWriter, r *http.Request) {
	if !h.aggregatesOn(w) {
		return
	}
	ctx := r.Context()
	id, scopeKey := chi.URLParam(r, "id"), chi.URLParam(r, "scope_key")
	if !validBillID(id) {
		writeError(w, http.StatusBadRequest, msgInvalidBillID)
		return
	}
	if !scopeKeyPattern.MatchString(scopeKey) {
		writeError(w, http.StatusBadRequest, "scope_key must be a state (CA) or a district (CA-12)")
		return
	}
	cacheKey := "aggregates:scope:" + id + ":" + scopeKey
	if cached, hit := h.cacheGet(ctx, cacheKey); hit {
		w.Header().Set("Cache-Control", aggregatesCacheControl)
		writeCachedJSON(w, cached)
		return
	}

	stored, err := h.Aggregates.BillAggregate(ctx, id, scopeKey)
	if err != nil {
		h.serverError(w, r, "failed to get aggregate", err)
		return
	}
	members, err := h.Aggregates.ConstituencyPositions(ctx, id, scopeKey)
	if err != nil {
		h.serverError(w, r, "failed to get member votes", err)
		return
	}
	resp := scopeAggregateResponse{
		BillID: id, Scope: scopeOf(scopeKey), ScopeKey: scopeKey, Members: members,
	}
	if resp.Members == nil {
		resp.Members = []model.ConstituencyPosition{}
	}
	if stored != nil {
		if cell, ok := servedCell(stored); ok {
			resp.Cell = &cell
			resp.AsOf = &stored.ComputedAt
		}
	}
	if resp.Cell == nil && len(resp.Members) == 0 && !h.billExists(w, r, id) {
		return
	}

	h.cacheJSON(ctx, cacheKey, resp, aggregatesCacheTTL)
	w.Header().Set("Cache-Control", aggregatesCacheControl)
	writeJSON(w, http.StatusOK, resp)
}

// GetMemberAlignment is GET /members/{id}/alignment: how often the published majority of the
// member's constituency agreed with their Yea/Nay votes, per congress, newest first.
func (h *Handler) GetMemberAlignment(w http.ResponseWriter, r *http.Request) {
	if !h.aggregatesOn(w) {
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validBioguideID(id) {
		writeError(w, http.StatusBadRequest, "invalid member id")
		return
	}
	cacheKey := "aggregates:alignment:" + id
	if cached, hit := h.cacheGet(ctx, cacheKey); hit {
		w.Header().Set("Cache-Control", aggregatesCacheControl)
		writeCachedJSON(w, cached)
		return
	}

	rows, err := h.Aggregates.MemberAlignment(ctx, id)
	if err != nil {
		h.serverError(w, r, "failed to get alignment", err)
		return
	}
	if len(rows) == 0 {
		member, getErr := h.Members.GetByID(ctx, id)
		if getErr != nil {
			h.serverError(w, r, "failed to get member", getErr)
			return
		}
		if member == nil {
			writeError(w, http.StatusNotFound, "member not found")
			return
		}
		rows = []model.RepAlignment{}
	}

	resp := memberAlignmentResponse{MemberID: id, Alignment: rows}
	h.cacheJSON(ctx, cacheKey, resp, aggregatesCacheTTL)
	w.Header().Set("Cache-Control", aggregatesCacheControl)
	writeJSON(w, http.StatusOK, resp)
}

// scopeOf names the scope of a valid scope key: a district has a number after the state.
func scopeOf(scopeKey string) string {
	if strings.Contains(scopeKey, "-") {
		return model.AggregateScopeDistrict
	}
	return model.AggregateScopeState
}

// latest returns the later of cur and t.
func latest(cur *time.Time, t time.Time) *time.Time {
	if cur == nil || t.After(*cur) {
		return &t
	}
	return cur
}
