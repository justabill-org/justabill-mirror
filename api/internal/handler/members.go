package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/district"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/scoring"
)

const (
	memberListCacheTTL = 10 * time.Minute

	// positionsCacheTTL: roll calls only change when the pipeline syncs votes.
	positionsCacheTTL = time.Hour
	// positionsCacheControl lets browsers keep positions for 5 minutes and the CDN for an
	// hour, serving a stale copy for up to a day while it revalidates. Positions are public
	// and the same for every visitor (docs/design/72-account-free-voting.md).
	positionsCacheControl = "public, max-age=300, s-maxage=3600, stale-while-revalidate=86400"
	// maxCongress bounds ?congress. The 200th Congress starts in 2387, so anything larger
	// is a typo or a probe, and it would only add cache keys.
	maxCongress = 200
)

// ListMembers returns a paginated list of members.
func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	params, err := parseListParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	cacheKey := listCacheKey("members:list", params)
	if cached, ok := h.cacheGet(ctx, cacheKey); ok {
		writeCachedJSON(w, cached)
		return
	}

	result, err := h.Members.List(ctx, params)
	if err != nil {
		h.serverError(w, r, "failed to list members", err)
		return
	}

	h.cacheJSON(ctx, cacheKey, result, memberListCacheTTL)

	writeJSON(w, http.StatusOK, result)
}

// GetMember returns a single member by bioguide ID.
func (h *Handler) GetMember(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validBioguideID(id) {
		writeError(w, http.StatusBadRequest, "invalid member id")
		return
	}

	member, err := h.Members.GetByID(r.Context(), id)
	if err != nil {
		h.serverError(w, r, "failed to get member", err)
		return
	}
	if member == nil {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}

	recentVotes, votesErr := h.Members.GetRecentVotes(r.Context(), id, defaultPageSize)
	if votesErr != nil {
		h.log.WarnContext(r.Context(), "failed to get member votes", "member_id", id, "error", votesErr)
	}
	if recentVotes != nil {
		member.RecentVotes = recentVotes
	}

	writeJSON(w, http.StatusOK, member)
}

// memberPositionsResponse is a member's position on each bill in one congress, and the
// name of the rule that picked them.
type memberPositionsResponse struct {
	MemberID  string                 `json:"member_id"`
	Congress  int                    `json:"congress"`
	Rule      string                 `json:"rule"`
	Positions []model.MemberPosition `json:"positions"`
}

// GetMemberPositions returns a member's position on every bill with a roll call in one
// congress (?congress, required, and in the congresses table), in either chamber. It's public and takes no user input beyond the path and
// congress, so the browser can score a voter's votes without sending them anywhere.
func (h *Handler) GetMemberPositions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validBioguideID(id) {
		writeError(w, http.StatusBadRequest, "invalid member id")
		return
	}
	congress, ok := parseCongress(r.URL.Query().Get("congress"))
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("congress must be a whole number from 1 to %d", maxCongress))
		return
	}

	cacheKey := fmt.Sprintf("positions:%s:%d", id, congress)
	if cached, hit := h.cacheGet(ctx, cacheKey); hit {
		w.Header().Set("Cache-Control", positionsCacheControl)
		writeCachedJSON(w, cached)
		return
	}

	// Checked after the cache: a cached answer's congress was loaded when it was cached.
	loaded, err := h.loadedCongresses(ctx)
	if err != nil {
		h.serverError(w, r, "failed to list congresses", err)
		return
	}
	if !loaded[congress] {
		writeUnknownCongress(w, congress)
		return
	}

	positions, err := h.Votes.MemberPositions(ctx, id, congress)
	if err != nil {
		h.serverError(w, r, "failed to get member positions", err)
		return
	}
	if len(positions) == 0 {
		// No positions is a normal answer for a member, but not for an ID nobody has.
		member, getErr := h.Members.GetByID(ctx, id)
		if getErr != nil {
			h.serverError(w, r, "failed to get member", getErr)
			return
		}
		if member == nil {
			writeError(w, http.StatusNotFound, "member not found")
			return
		}
	}

	resp := memberPositionsResponse{
		MemberID: id, Congress: congress, Rule: scoring.RuleName, Positions: positions,
	}
	h.cacheJSON(ctx, cacheKey, resp, positionsCacheTTL)
	w.Header().Set("Cache-Control", positionsCacheControl)
	writeJSON(w, http.StatusOK, resp)
}

// parseCongress reads a required congress number from 1 to maxCongress.
func parseCongress(raw string) (int, bool) {
	if !positiveInt(raw) {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n > maxCongress {
		return 0, false
	}
	return n, true
}

const (
	// maxAddressLen is the longest address, in characters, that POST /reps accepts.
	maxAddressLen = 200
	// maxLat and maxLon bound the latitude and longitude POST /reps accepts, in degrees.
	maxLat = 90
	maxLon = 180

	inputAddress     = "address"
	inputCoordinates = "coordinates"
)

// repsRequest is the body of POST /reps: an address, or a point as lat and lon.
type repsRequest struct {
	Address string   `json:"address"`
	Lat     *float64 `json:"lat"`
	Lon     *float64 `json:"lon"`
}

// repsPlace is where POST /reps looks: an address, or a point when point is set.
type repsPlace struct {
	address  string
	lat, lon float64
	point    bool
}

// parse checks a POST /reps body and returns the place it names, or the message for a 400. A
// blank address counts as none, so a form may send it empty alongside a point.
func (req repsRequest) parse() (repsPlace, string) {
	address := strings.TrimSpace(req.Address)
	hasPoint := req.Lat != nil || req.Lon != nil
	switch {
	case address != "" && hasPoint:
		return repsPlace{}, "send address or lat and lon, not both"
	case hasPoint && (req.Lat == nil || req.Lon == nil):
		return repsPlace{}, "lat and lon are both required"
	case hasPoint && (*req.Lat < -maxLat || *req.Lat > maxLat):
		return repsPlace{}, fmt.Sprintf("lat must be from -%d to %d", maxLat, maxLat)
	case hasPoint && (*req.Lon < -maxLon || *req.Lon > maxLon):
		return repsPlace{}, fmt.Sprintf("lon must be from -%d to %d", maxLon, maxLon)
	case hasPoint:
		return repsPlace{lat: *req.Lat, lon: *req.Lon, point: true}, ""
	case address == "":
		return repsPlace{}, "address or lat and lon is required"
	case utf8.RuneCountInString(address) > maxAddressLen:
		return repsPlace{}, fmt.Sprintf("address must be at most %d characters", maxAddressLen)
	default:
		return repsPlace{address: address}, ""
	}
}

// lookup resolves the place to its districts in a congress's map.
func (p repsPlace) lookup(ctx context.Context, d district.Lookup, congress int) ([]district.Result, error) {
	if p.point {
		return d.FromCoordinates(ctx, p.lat, p.lon, congress)
	}
	return d.FromAddress(ctx, p.address, congress)
}

// input names the kind of place for the log line, never the place itself.
func (p repsPlace) input() string {
	if p.point {
		return inputCoordinates
	}
	return inputAddress
}

// FindReps is POST /reps: it looks up representatives and senators by the address in the request
// body, {"address": "..."}, or by a point, {"lat": 38.8977, "lon": -77.0365} (degrees, such as a
// phone's location), in the current congress's district map. Send one or the other: both, half a
// point, a latitude outside -90..90 or a longitude outside -180..180 is a 400. While there's a map
// for the next congress, it also looks the place up there, in parallel and best effort, and adds
// the districts it votes in at the next general election as "election"
// (docs/design/237-election-district.md). A point in no district gets empty lists, as an
// unmatched address does; the single-seat fallback reads the address, so it doesn't apply.
//
// The address and the point are only ever in the body: they're never logged, put on spans or
// metrics, or echoed in the response (docs/design/71-api-hardening.md).
func (h *Handler) FindReps(w http.ResponseWriter, r *http.Request) {
	var req repsRequest
	if !decodeJSON(w, r.Body, &req) {
		return
	}
	place, msg := req.parse()
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	ctx := r.Context()

	congress, err := h.currentCongress(ctx)
	if err != nil {
		h.serverError(w, r, "failed to list congresses", err)
		return
	}
	if congress == 0 {
		h.log.ErrorContext(ctx, "no current congress", "route", routePattern(r))
		writeError(w, http.StatusServiceUnavailable, "no current congress")
		return
	}

	next, cancelNext := h.startElection(ctx, place, congress)
	defer cancelNext()

	districts, err := place.lookup(ctx, h.District, congress)
	if err != nil {
		// Never log the address or the point: errors from the lookup don't carry them either.
		h.log.ErrorContext(ctx, "district lookup failed", "congress", congress, keyError, err)
		writeError(w, http.StatusBadGateway, "failed to resolve address to district")
		return
	}

	election, electionOutcome := h.awaitElection(ctx, next, congress+1, districts)
	cancelNext()

	allReps, allSenators := h.membersFor(ctx, districts)

	h.log.InfoContext(ctx, "reps lookup", "input", place.input(), "congress", congress, "districts", len(districts),
		"district_source", districtSource(districts), "election_district", electionOutcome)

	resp := map[string]any{
		"reps":      allReps,
		"senators":  allSenators,
		"districts": toRepsDistricts(districts, congress),
	}
	if election != nil {
		resp["election"] = election
	}
	writeJSON(w, http.StatusOK, resp)
}

// membersFor returns the representatives of the districts and the senators of their states. A
// failed read is logged and skipped, so the answer may be partial but is never null.
func (h *Handler) membersFor(ctx context.Context, districts []district.Result) ([]model.Member, []model.Member) {
	allReps := []model.Member{}
	allSenators := []model.Member{}
	seenStates := make(map[string]bool)

	for _, d := range districts {
		reps, repErr := h.Members.GetByDistrict(ctx, d.State, d.District)
		if repErr != nil {
			h.log.WarnContext(ctx, "failed to get reps for district",
				"state", d.State, "district", d.District, keyError, repErr)
			continue
		}
		allReps = append(allReps, reps...)

		if !seenStates[d.State] {
			seenStates[d.State] = true
			senators, senErr := h.Members.GetSenators(ctx, d.State)
			if senErr != nil {
				h.log.WarnContext(ctx, "failed to get senators",
					"state", d.State, keyError, senErr)
				continue
			}
			allSenators = append(allSenators, senators...)
		}
	}
	return allReps, allSenators
}

// currentCongress returns the congress flagged current, or 0 when none is. Unlike
// resolveCongress it doesn't fall back to the newest congress: a district map for the wrong
// congress gives a wrong answer that looks right.
func (h *Handler) currentCongress(ctx context.Context) (int, error) {
	congresses, err := h.Congresses.List(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range congresses {
		if c.IsCurrent {
			return c.Number, nil
		}
	}
	return 0, nil
}

// districtSource summarizes where a lookup's districts came from for the log line: the first
// result's source, or "none" when the address matched nothing.
func districtSource(districts []district.Result) string {
	if len(districts) == 0 {
		return "none"
	}
	return districts[0].Source
}
