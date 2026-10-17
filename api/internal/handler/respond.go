package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/db/model"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	// maxOffset caps list paging. Spanner reads and discards every skipped
	// row, so an unbounded offset would let any client make each request scan
	// the whole filtered set. 10,000 is 500 pages of 20.
	maxOffset = 10_000
	// maxSearchOffset caps paging through a search, whose every page runs two full-text searches
	// and a count (#619): 25 pages of 20, deep enough for anyone reading results.
	maxSearchOffset = 500
	// maxSearchRunes and maxSearchTerms bound a search's text, so its cost doesn't grow with
	// whatever a visitor sends (#619). Terms are runs of non-space characters.
	maxSearchRunes = 100
	maxSearchTerms = 8
	// maxPolicyAreaRunes bounds the policy_area filter, whose values are names such as "Armed
	// Forces and National Security" (#708), so a request can't mint huge cache keys.
	maxPolicyAreaRunes = 100
	// maxDistrict bounds the district filter: districts are numbered 0 (at-large) to two digits, as
	// the Census's GEOIDs number them, and no state has ever had more than 53.
	maxDistrict = 99
	// stateCodeLen is the length of a state filter: a USPS postal code, as member_terms.state holds.
	stateCodeLen = 2

	keyError = "error"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// An encoding error here is almost always a client that hung up; the
	// status is already sent, so there's nothing useful to do with it.
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{keyError: msg})
}

// writeCodedError adds a machine-readable code that clients branch on, in the
// same shape as the auth middleware's errors.
func writeCodedError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{keyError: msg, "code": code})
}

// serverError logs err with the route and request ID and writes a 500 with
// msg, or a 504 when the request's deadline (chi's Timeout in cmd/server) has
// passed, since then the failure is the time limit's. Every 5xx from a handler
// goes through it, so a failing query is never silent; 4xx responses aren't
// logged, since the access log has the status.
func (h *Handler) serverError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	ctx := r.Context()
	attrs := []any{"route", routePattern(r), "op", msg, keyError, err}
	if id := middleware.GetReqID(ctx); id != "" {
		attrs = append(attrs, "request_id", id)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		h.log.ErrorContext(ctx, "request timed out", attrs...)
		writeError(w, http.StatusGatewayTimeout, "request timed out")
		return
	}
	h.log.ErrorContext(ctx, "request failed", attrs...)
	writeError(w, http.StatusInternalServerError, msg)
}

// routePattern returns the matched chi route pattern (e.g.
// "/api/v1/bills/{id}/vote"), so logs name the route without raw IDs from the
// path. It's empty when the request wasn't routed through chi.
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		return rctx.RoutePattern()
	}
	return ""
}

// parseListParams reads the paging and filter query params. A malformed or
// negative offset, an offset above maxOffset, or a malformed limit is an
// error whose message is fit for a 400, as is a search that [checkSearch] refuses, a
// policy_area that isn't valid UTF-8 or is over maxPolicyAreaRunes characters, a congress that
// isn't a whole number from 1 to maxCongress, a district that isn't one from 0 to maxDistrict
// (#881), and a type, chamber or state that [queryBillType], [queryChamber] or [queryState] refuses
// (#915).
// limit is clamped to 1–100. A sort that isn't one of the bill list's ([model.IsBillSort]) reads as
// none and a status_mode other than past as the default, so neither mints a cache key of its own
// for the order or filter the default gives (#943).
func parseListParams(r *http.Request) (model.ListParams, error) {
	q := r.URL.Query()
	offset, limit := 0, defaultPageSize

	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return model.ListParams{}, errors.New("offset must be a non-negative integer")
		}
		if n > maxOffset {
			return model.ListParams{}, errors.New("offset too large; narrow the list with filters")
		}
		offset = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return model.ListParams{}, errors.New("limit must be an integer")
		}
		if n > 0 {
			limit = min(n, maxPageSize)
		}
	}

	congress, err := queryCongress(q)
	if err != nil {
		return model.ListParams{}, err
	}
	district, err := queryWhole(q, "district", 0, maxDistrict)
	if err != nil {
		return model.ListParams{}, err
	}
	params := model.ListParams{
		Offset:     offset,
		Limit:      limit,
		Congress:   congress,
		PolicyArea: queryStr(q, "policy_area"),
		Search:     queryStr(q, "q", "search"),
		District:   district,
		Sort:       querySort(q),
	}
	if err = readBillFilters(q, &params); err != nil {
		return model.ListParams{}, err
	}
	if params.State, err = queryState(q); err != nil {
		return model.ListParams{}, err
	}
	statuses, mode, err := parseStatuses(q)
	if err != nil {
		return model.ListParams{}, err
	}
	params.Statuses, params.StatusMode = statuses, mode
	if err = checkPolicyArea(params.PolicyArea); err != nil {
		return model.ListParams{}, err
	}
	if params.Search != nil {
		if err = checkSearch(*params.Search, offset); err != nil {
			return model.ListParams{}, err
		}
	}
	return params, nil
}

// checkPolicyArea refuses a policy_area filter that isn't valid UTF-8 or is over
// maxPolicyAreaRunes characters, with a message fit for a 400. A nil filter passes.
func checkPolicyArea(a *string) error {
	if a != nil && (!utf8.ValidString(*a) || utf8.RuneCountInString(*a) > maxPolicyAreaRunes) {
		return errors.New("policy_area must be a policy area name, as GET /policy-areas lists them")
	}
	return nil
}

// parseStatuses reads the list's status filter: status, comma-separated or repeated, and
// status_mode (#712). The statuses come back without duplicates and sorted, so the same set always
// builds the same cache key. An unknown status, or status_mode=past with several statuses (each
// one more join of the audit trail, and no page needs it), is an error fit for a 400. Any
// status_mode but past is the default, the bill's current status, and comes back empty (#943).
func parseStatuses(q url.Values) ([]string, string, error) {
	var statuses []string
	for _, v := range q["status"] {
		for part := range strings.SplitSeq(v, ",") {
			s := strings.TrimSpace(part)
			switch {
			case s == "":
			case !model.IsBillStatus(s):
				return nil, "", errors.New("unknown status; use the values of a bill's current_status")
			case !slices.Contains(statuses, s):
				statuses = append(statuses, s)
			}
		}
	}
	if len(statuses) == 0 {
		return nil, "", nil
	}
	slices.Sort(statuses)
	if q.Get("status_mode") != model.StatusModePast {
		return statuses, "", nil
	}
	if len(statuses) > 1 {
		return nil, "", errors.New("status_mode=past takes one status")
	}
	return statuses, model.StatusModePast, nil
}

// checkSearch refuses a search Spanner can't run or that would cost more than a visitor's search
// should: text that isn't valid UTF-8, more than maxSearchRunes characters or maxSearchTerms
// terms, or a page past maxSearchOffset (#619). Its messages are static, fit for a 400.
func checkSearch(search string, offset int) error {
	// Spanner can't encode invalid UTF-8 and fails the query as Internal (#452).
	if !utf8.ValidString(search) {
		return errors.New("search must be valid UTF-8 text")
	}
	if utf8.RuneCountInString(search) > maxSearchRunes || len(strings.Fields(search)) > maxSearchTerms {
		return errors.New("search too long; use at most 8 words and 100 characters")
	}
	if offset > maxSearchOffset {
		return errors.New("offset too large for a search; narrow the search")
	}
	return nil
}

// queryStr returns a pointer to the first non-empty query value for the given keys.
func queryStr(q interface{ Get(string) string }, keys ...string) *string {
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			return &v
		}
	}
	return nil
}

// querySort reads the optional sort: one of the bill list's sorts ([model.IsBillSort]), or nil, the
// default order, for anything else, so a sort the list doesn't know adds no cache key (#943).
func querySort(q url.Values) *string {
	if sort := q.Get("sort"); model.IsBillSort(sort) {
		return &sort
	}
	return nil
}

// readBillFilters reads the type and chamber filters into p, as [queryBillType] and [queryChamber]
// check them. The bill list and its counts both read them here, so they refuse the same input.
func readBillFilters(q url.Values, p *model.ListParams) error {
	var err error
	if p.BillType, err = queryBillType(q); err != nil {
		return err
	}
	p.Chamber, err = queryChamber(q)
	return err
}

// queryBillType reads the optional type (or bill_type) filter: nil when absent, else a bill type
// in any case, lower-cased as bill IDs spell it (and bills.bill_type holds it), so each type has one
// cache key. Anything else, invalid UTF-8 among it (which Spanner fails as Internal), is an error
// fit for a 400 (#915).
func queryBillType(q url.Values) (*string, error) {
	billType := queryStr(q, "type", "bill_type")
	if billType == nil {
		return nil, nil //nolint:nilnil // an absent filter is no filter, not an error
	}
	*billType = strings.ToLower(*billType)
	if !isBillType(*billType) {
		return nil, errors.New("type must be a bill type: hr, s, hjres, sjres, hconres, sconres, hres, sres")
	}
	return billType, nil
}

// queryChamber reads the optional chamber filter: nil when absent, else house or senate in any
// case, lower-cased, or an error fit for a 400 (#915). The repositories map it to Congress.gov's
// spelling.
func queryChamber(q url.Values) (*string, error) {
	chamber := queryStr(q, "chamber")
	if chamber == nil {
		return nil, nil //nolint:nilnil // an absent filter is no filter, not an error
	}
	*chamber = strings.ToLower(*chamber)
	if *chamber != "house" && *chamber != "senate" {
		return nil, errors.New("chamber must be house or senate")
	}
	return chamber, nil
}

// queryState reads the optional state filter: nil when absent, else two ASCII letters, upper-cased
// as member_terms.state holds postal codes, or an error fit for a 400 (#915). A pair that's no
// state matches no member, as before.
func queryState(q url.Values) (*string, error) {
	state := queryStr(q, "state")
	if state == nil {
		return nil, nil //nolint:nilnil // an absent filter is no filter, not an error
	}
	if len(*state) != stateCodeLen || !isASCIILetter((*state)[0]) || !isASCIILetter((*state)[1]) {
		return nil, errors.New("state must be a two-letter postal code")
	}
	*state = strings.ToUpper(*state)
	return state, nil
}

func isASCIILetter(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// queryCongress reads the optional congress filter: nil when it's absent, and an error fit for a
// 400 when it isn't a whole number from 1 to maxCongress. Silently dropping it would list every
// congress (#881).
func queryCongress(q url.Values) (*int, error) {
	return queryWhole(q, "congress", 1, maxCongress)
}

// queryWhole reads an optional query value written in decimal digits alone (leading zeros allowed,
// no sign) from lo to hi: nil when it's absent or empty, and an error naming key and the range,
// fit for a 400, when it's anything else.
func queryWhole(q url.Values, key string, lo, hi int) (*int, error) {
	v := q.Get(key)
	if v == "" {
		return nil, nil //nolint:nilnil // an absent filter is no filter, not an error
	}
	n, err := strconv.Atoi(v)
	if !allDigits(v) || err != nil || n < lo || n > hi {
		return nil, fmt.Errorf("%s must be a whole number from %d to %d", key, lo, hi)
	}
	return &n, nil
}

// billListParams is p with only the fields the bill list reads (BillRepo.List): a
// state or district, which it ignores, neither reaches the query nor mints a cache key (#943).
func billListParams(p model.ListParams) model.ListParams {
	return model.ListParams{
		Offset: p.Offset, Limit: p.Limit, Congress: p.Congress, BillType: p.BillType,
		Statuses: p.Statuses, StatusMode: p.StatusMode, Chamber: p.Chamber, PolicyArea: p.PolicyArea,
		Search: p.Search, UnvotedBy: p.UnvotedBy, Sort: p.Sort,
	}
}

// memberListParams is p with only the fields the member list reads (MemberRepo.List):
// paging, congress, chamber, state and district. A search, type, policy area, sort or status,
// which it ignores, neither reaches the query nor mints a cache key (#943).
func memberListParams(p model.ListParams) model.ListParams {
	return model.ListParams{
		Offset: p.Offset, Limit: p.Limit, Congress: p.Congress, Chamber: p.Chamber,
		State: p.State, District: p.District,
	}
}

// listCacheKey is the cache key of a list request: prefix, then the parsed
// params in a fixed order. Equivalent queries (?a=1&b=2 and ?b=2&a=1) share a
// key, and query params the API doesn't recognize can't mint new ones. A route
// passes only the params it reads ([billListParams], [memberListParams]), so
// one it ignores can't either (#943).
// UnvotedBy is left out on purpose: a personal list must never be cached.
func listCacheKey(prefix string, p model.ListParams) string {
	v := url.Values{}
	v.Set("offset", strconv.Itoa(p.Offset))
	v.Set("limit", strconv.Itoa(p.Limit))
	for key, n := range map[string]*int{"congress": p.Congress, "district": p.District} {
		if n != nil {
			v.Set(key, strconv.Itoa(*n))
		}
	}
	strs := map[string]*string{
		"type": p.BillType, "chamber": p.Chamber,
		"q": p.Search, "state": p.State, "sort": p.Sort, "policy_area": p.PolicyArea,
	}
	for key, s := range strs {
		if s != nil {
			v.Set(key, *s)
		}
	}
	if len(p.Statuses) > 0 {
		v.Set("status", strings.Join(p.Statuses, ","))
	}
	if p.StatusMode != "" {
		v.Set("status_mode", p.StatusMode)
	}
	// Encode sorts by key and escapes the values, so the order is fixed and a
	// value can't forge another param.
	return prefix + ":" + v.Encode()
}

// cacheKeyPrefix is what a log may say about a cache key: everything before
// its last colon, such as "bills:list" or "positions:<member id>". A list
// key's params, a visitor's search text among them, come after that colon and
// hold none themselves (Encode escapes ":"), so they never reach the logs (#759).
func cacheKeyPrefix(key string) string {
	if prefix, _, found := strings.CutLast(key, ":"); found {
		return prefix
	}
	return key
}

// cacheGet retrieves a cached value by key. Returns the value and true on hit,
// or empty string and false on miss or if caching is disabled.
func (h *Handler) cacheGet(ctx context.Context, key string) (string, bool) {
	if h.cache == nil {
		return "", false
	}
	val, err := h.cache.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, cache.ErrUnavailable) {
			h.log.WarnContext(ctx, "cache get error", "key_prefix", cacheKeyPrefix(key), "error", err)
		}
		return "", false
	}
	if val == "" {
		return "", false
	}
	return val, true
}

// cacheJSON encodes v and caches it under key. An encoding failure is logged
// and the response is simply not cached.
func (h *Handler) cacheJSON(ctx context.Context, key string, v any, ttl time.Duration) {
	encoded, err := json.Marshal(v)
	if err != nil {
		h.log.WarnContext(ctx, "cache encode error", "key_prefix", cacheKeyPrefix(key), "error", err)
		return
	}
	h.cacheSet(ctx, key, string(encoded), ttl)
}

// cacheSet stores a value in cache with the given TTL. It is a no-op if
// caching is disabled.
//
// No handler deletes cache keys, so cached data must be fine to serve until
// its TTL runs out: data only the pipeline changes. A response that changes with a
// user's own write must either stay out of the cache or be invalidated by the
// handler that writes it (docs/design/81-api-correctness.md, item 6).
func (h *Handler) cacheSet(ctx context.Context, key, value string, ttl time.Duration) {
	if h.cache == nil {
		return
	}
	if err := h.cache.Set(ctx, key, value, ttl); err != nil && !errors.Is(err, cache.ErrUnavailable) {
		h.log.WarnContext(ctx, "cache set error", "key_prefix", cacheKeyPrefix(key), "error", err)
	}
}

// writeCachedJSON writes a pre-encoded JSON string as a 200 response body.
func writeCachedJSON(w http.ResponseWriter, raw string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(raw)) //nolint:gosec // raw is JSON this API encoded and cached, served as application/json
}
