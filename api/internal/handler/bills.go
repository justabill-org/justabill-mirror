package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/cachekey"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

const (
	billListCacheTTL = 5 * time.Minute
	includeSummary   = "summary"
	includeCard      = "card"
	// includeCRSSummary asks for each bill's latest CRS summary lead (#714).
	includeCRSSummary = "crs_summary"
	// billDetailCacheTTL matches the list TTL. The pipeline syncs hourly, so a
	// bill page up to 5 minutes stale doesn't matter.
	billDetailCacheTTL = 5 * time.Minute
)

// billListItem is a bill in a list requested with include: the bill's fields plus the extras
// asked for. The web /vote deck reads the AI summary (#74) and the card facts (#705) with it, and
// /bills the CRS summary's lead (#714).
type billListItem struct {
	model.Bill

	// Summary is the bill's AI summary (include=summary), null when it has none.
	Summary included[model.BillSummary] `json:"summary,omitzero"`
	// Card is the bill's /vote card facts (include=card), with every key even when the bill has
	// no fact.
	Card included[model.BillCardFacts] `json:"card,omitzero"`
	// CRSSummary is the lead of the bill's latest CRS summary (include=crs_summary), the object
	// a card's crs is, null when the bill has none.
	CRSSummary included[model.CardCRS] `json:"crs_summary,omitzero"`
}

// included is an extra of a list item: absent from the JSON unless the include asked for it, and
// then the value, or null when the bill has none.
type included[T any] struct {
	asked bool
	value *T
}

// IsZero reports whether the extra wasn't asked for, so omitzero leaves it out.
func (i included[T]) IsZero() bool { return !i.asked }

// MarshalJSON writes the value, or null.
func (i included[T]) MarshalJSON() ([]byte, error) { return json.Marshal(i.value) }

// includedOf is the extra a batch read found for the bill, or null.
func includedOf[T any](found map[string]T, id string) included[T] {
	v, ok := found[id]
	if !ok {
		return included[T]{asked: true}
	}
	return included[T]{asked: true, value: &v}
}

// billInclude is the set of extras a bill list carries, from its include parameter.
type billInclude struct {
	summary    bool
	card       bool
	crsSummary bool
}

// cachePrefix is the list's cache prefix: each include set is a different body, so each gets its
// own ("bills:list", "bills:list+summary", "bills:list+card", "bills:list+summary+card", and
// each of those with "+crs_summary").
func (in billInclude) cachePrefix() string {
	prefix := "bills:list"
	if in.summary {
		prefix += "+" + includeSummary
	}
	if in.card {
		prefix += "+" + includeCard
	}
	if in.crsSummary {
		prefix += "+" + includeCRSSummary
	}
	return prefix
}

// any reports whether the list carries any extra.
func (in billInclude) any() bool { return in.summary || in.card || in.crsSummary }

// ListBills returns a paginated list of bills.
//
// unvoted=true leaves out the bills the signed-in user has voted on. The user
// comes only from a verified ID token (the auth middleware puts it in the
// context); without one the parameter is ignored. That list is personal, so
// it never goes through the shared cache, and the auth middleware marks the
// response private.
func (h *Handler) ListBills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	params, err := parseListParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	include, err := parseBillInclude(r.URL.Query()["include"])
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if r.URL.Query().Get("unvoted") == "true" {
		if userID := mw.UserIDFromContext(ctx); userID != "" {
			params.UnvotedBy = &userID
		}
	}

	// listCacheKey leaves out UnvotedBy, so only the shared list is cached.
	cacheKey := listCacheKey(include.cachePrefix(), params)
	shared := params.UnvotedBy == nil
	if shared {
		if cached, ok := h.cacheGet(ctx, cacheKey); ok {
			writeCachedJSON(w, cached)
			return
		}
	}

	result, err := h.Bills.List(ctx, params)
	if errors.Is(err, repository.ErrInvalidSearch) {
		writeError(w, http.StatusBadRequest, "search text could not be read; try plain words")
		return
	}
	if err != nil {
		h.serverError(w, r, "failed to list bills", err)
		return
	}

	var body any = result
	if include.any() {
		var ok bool
		if body, ok = h.withIncludes(w, r, result, include); !ok {
			return
		}
	}

	if shared {
		h.cacheJSON(ctx, cacheKey, body, billListCacheTTL)
	}

	writeJSON(w, http.StatusOK, body)
}

// CountBills is GET /bills/counts (#713): how many bills the list's filters (congress, type,
// chamber, policy_area, q) match, by current status, from one query, so a page can size its status
// views without one list call per view. The list's other parameters (offset, limit, sort, status,
// status_mode, unvoted, include) are ignored: the counts are the same whichever page or view asks.
// It is cached like the list, under the filters alone, for [billListCacheTTL].
func (h *Handler) CountBills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	params, err := parseCountParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cacheKey := listCacheKey("bills:counts", params)
	if cached, ok := h.cacheGet(ctx, cacheKey); ok {
		writeCachedJSON(w, cached)
		return
	}

	counts, err := h.Bills.CountByStatus(ctx, params)
	if errors.Is(err, repository.ErrInvalidSearch) {
		writeError(w, http.StatusBadRequest, "search text could not be read; try plain words")
		return
	}
	if err != nil {
		h.serverError(w, r, "failed to count bills", err)
		return
	}
	h.cacheJSON(ctx, cacheKey, counts, billListCacheTTL)
	writeJSON(w, http.StatusOK, counts)
}

// parseCountParams reads GET /bills/counts' filters: the ones [parseListParams] reads for a bill
// list, minus paging, sort and status, with the same congress, policy area and search checks. A
// filter the list gains must be read here too, or the counts stop matching the list.
func parseCountParams(r *http.Request) (model.ListParams, error) {
	q := r.URL.Query()
	congress, err := queryCongress(q)
	if err != nil {
		return model.ListParams{}, err
	}
	params := model.ListParams{
		Congress:   congress,
		BillType:   queryStr(q, "type", "bill_type"),
		Chamber:    queryStr(q, "chamber"),
		PolicyArea: queryStr(q, "policy_area"),
		Search:     queryStr(q, "q", "search"),
	}
	if err = checkPolicyArea(params.PolicyArea); err != nil {
		return model.ListParams{}, err
	}
	if params.Search != nil {
		if err = checkSearch(*params.Search, 0); err != nil {
			return model.ListParams{}, err
		}
	}
	return params, nil
}

// parseBillInclude reads the list's include parameter (repeated or comma-separated): any of
// "summary", "card" and "crs_summary". Anything else is a client error rather than silently
// ignored.
func parseBillInclude(values []string) (billInclude, error) {
	var in billInclude
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			switch strings.TrimSpace(part) {
			case includeSummary:
				in.summary = true
			case includeCard:
				in.card = true
			case includeCRSSummary:
				in.crsSummary = true
			case "":
			default:
				return billInclude{}, errors.New(`include must be one or more of "summary", "card" and "crs_summary"`)
			}
		}
	}
	return in, nil
}

// withIncludes attaches the included extras to the page, each with one batch read, the reads
// running concurrently. When a read fails it writes the 500 and returns false.
func (h *Handler) withIncludes(
	w http.ResponseWriter, r *http.Request, page *model.ListResult[model.Bill], in billInclude,
) (any, bool) {
	ctx := r.Context()
	ids := make([]string, len(page.Items))
	for i, b := range page.Items {
		ids[i] = b.ID
	}
	var (
		summaries                   map[string]model.BillSummary
		cards                       map[string]model.BillCardFacts
		crs                         map[string]model.CardCRS
		summaryErr, cardErr, crsErr error
		wg                          sync.WaitGroup
	)
	if in.summary {
		wg.Go(func() { summaries, summaryErr = h.Bills.GetSummaries(ctx, ids) })
	}
	if in.card {
		wg.Go(func() { cards, cardErr = h.Bills.GetCardFacts(ctx, ids) })
	}
	if in.crsSummary {
		wg.Go(func() { crs, crsErr = h.Bills.GetCRSLeads(ctx, ids) })
	}
	wg.Wait()
	for _, read := range []struct {
		err error
		msg string
	}{
		{summaryErr, "failed to get bill summaries"},
		{cardErr, "failed to get bill card facts"},
		{crsErr, "failed to get bill CRS summaries"},
	} {
		if read.err != nil {
			h.serverError(w, r, read.msg, read.err)
			return nil, false
		}
	}

	return mapBills(page, func(b model.Bill) billListItem {
		item := billListItem{Bill: b}
		if in.summary {
			item.Summary = includedOf(summaries, b.ID)
		}
		if in.card {
			card := cardOf(cards, b.ID)
			item.Card = included[model.BillCardFacts]{asked: true, value: &card}
		}
		if in.crsSummary {
			item.CRSSummary = includedOf(crs, b.ID)
		}
		return item
	}), true
}

// cardOf is a bill's card facts, or the empty facts (an empty passage list, not null) when the
// batch read found none.
func cardOf(cards map[string]model.BillCardFacts, id string) model.BillCardFacts {
	c := cards[id]
	if c.Passage == nil {
		c.Passage = []model.PassageEntry{}
	}
	return c
}

// mapBills turns a page of bills into a page of list items, keeping its paging.
func mapBills[T any](page *model.ListResult[model.Bill], item func(model.Bill) T) *model.ListResult[T] {
	items := make([]T, len(page.Items))
	for i, b := range page.Items {
		items[i] = item(b)
	}
	return &model.ListResult[T]{Items: items, Total: page.Total, Offset: page.Offset, Limit: page.Limit}
}

// GetBill returns a single bill by ID with related data.
//
// The bill is read first, so a missing one is a 404; its sections are then
// read concurrently. A section that fails is null in the response and logged,
// and a response with a failed section isn't cached, so a transient error
// isn't pinned for the cache TTL. A complete response is cached under
// [cachekey.BillDetail] for [billDetailCacheTTL]. The pipeline deletes that key
// when a sync changes the bill (#400).
func (h *Handler) GetBill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	cacheKey := cachekey.BillDetail(id)
	if cached, ok := h.cacheGet(ctx, cacheKey); ok {
		h.log.DebugContext(ctx, "get bill", "bill_id", id, "cache", "hit")
		writeCachedJSON(w, cached)
		return
	}
	h.log.DebugContext(ctx, "get bill", "bill_id", id, "cache", "miss")

	bill, err := h.Bills.GetByID(ctx, id)
	if err != nil {
		h.serverError(w, r, "failed to get bill", err)
		return
	}
	if bill == nil {
		writeError(w, http.StatusNotFound, msgBillNotFound)
		return
	}

	detail, complete := h.readBillDetail(ctx, id, bill)
	if complete {
		h.cacheJSON(ctx, cacheKey, detail, billDetailCacheTTL)
	}
	writeJSON(w, http.StatusOK, detail)
}

// billDetail is the GET /bills/{id} response. A section that failed to load is
// null.
type billDetail struct {
	Bill          *model.Bill               `json:"bill"`
	Actions       []model.BillAction        `json:"actions"`
	Summary       *model.BillSummary        `json:"summary"`
	TextVersions  []model.BillTextVersion   `json:"text_versions"`
	Diffs         []model.BillTextDiff      `json:"diffs"`
	Amendments    []model.Amendment         `json:"amendments"`
	Votes         []model.CongressionalVote `json:"votes"`
	StatusHistory []model.BillStatusEntry   `json:"status_history"`
	GAOReports    []model.GAOReport         `json:"gao_reports"`
	Sponsorships  []model.BillSponsorship   `json:"sponsorships"`
	// CRSSummary is the latest Congressional Research Service summary, as plain text
	// (docs/design/197-crs-summaries.md); null when the bill has none.
	CRSSummary *model.CRSSummary `json:"crs_summary"`
	// DisapprovedRule is the rule a CRA resolution disapproves, matched or not
	// (docs/design/590-cra-disapproved-rules.md); null for any other bill, or a resolution not
	// checked yet.
	DisapprovedRule *model.CRARule `json:"disapproved_rule"`
}

// readBillDetail reads the sections of bill id concurrently and reports
// whether every read succeeded. One failed read doesn't cancel the others.
func (h *Handler) readBillDetail(ctx context.Context, id string, bill *model.Bill) (*billDetail, bool) {
	d := &billDetail{Bill: bill}
	s := &sectionReads{ctx: ctx, log: h.log, billID: id}
	readSection(s, "actions", &d.Actions, h.Bills.GetActions)
	readSection(s, "summary", &d.Summary, h.Bills.GetSummary)
	readSection(s, "text versions", &d.TextVersions, h.Bills.GetTextVersions)
	readSection(s, "diffs", &d.Diffs, h.Bills.GetDiffs)
	readSection(s, "amendments", &d.Amendments, h.Bills.GetAmendments)
	readSection(s, "votes", &d.Votes, h.Votes.GetCongressionalVotes)
	readSection(s, "status history", &d.StatusHistory, h.Bills.GetStatusHistory)
	readSection(s, "GAO reports", &d.GAOReports, h.Bills.ListGAOReports)
	readSection(s, "sponsorships", &d.Sponsorships, h.Bills.GetSponsorships)
	readSection(s, "CRS summary", &d.CRSSummary, h.Bills.GetCRSSummary)
	readSection(s, "disapproved rule", &d.DisapprovedRule, h.disapprovedRule)
	s.wg.Wait()
	return d, !s.failed.Load()
}

// sectionReads tracks the concurrent section reads of one bill.
type sectionReads struct {
	ctx    context.Context
	log    *slog.Logger
	billID string
	wg     sync.WaitGroup
	failed atomic.Bool
}

// readSection starts read in its own goroutine and stores the result in dst,
// which only that goroutine writes. On an error or a panic dst stays nil,
// the failure is logged and the reads are marked failed. The panic is
// recovered here because the router's recoverer only covers the request's
// own goroutine.
func readSection[T any](s *sectionReads, section string, dst *T, read func(context.Context, string) (T, error)) {
	s.wg.Go(func() {
		defer func() {
			if p := recover(); p != nil {
				s.failed.Store(true)
				s.log.ErrorContext(s.ctx, "bill section read panicked",
					"bill_id", s.billID, "section", section, "panic", p)
			}
		}()
		v, err := read(s.ctx, s.billID)
		if err != nil {
			s.failed.Store(true)
			s.log.ErrorContext(s.ctx, "failed to get bill section",
				"bill_id", s.billID, "section", section, keyError, err)
			return
		}
		*dst = v
	})
}

// GetBillActions returns actions for a bill.
func (h *Handler) GetBillActions(w http.ResponseWriter, r *http.Request) {
	writeBillList(h, w, r, "actions", h.Bills.GetActions)
}

// writeBillList answers one of a bill's lists, read by read: 400 for an ID that isn't a bill ID,
// 500 (as "failed to get <what>") when a read fails, and 404 for a bill that doesn't exist. Only
// an empty list costs the read that tells a bill without rows, answered [], from an unknown one.
func writeBillList[T any](
	h *Handler, w http.ResponseWriter, r *http.Request, what string, read func(context.Context, string) ([]T, error),
) {
	id := chi.URLParam(r, "id")
	if !validBillID(id) {
		writeError(w, http.StatusBadRequest, msgInvalidBillID)
		return
	}
	items, err := read(r.Context(), id)
	if err != nil {
		h.serverError(w, r, "failed to get "+what, err)
		return
	}
	if len(items) == 0 {
		if !h.billExists(w, r, id) {
			return
		}
		items = []T{}
	}
	writeJSON(w, http.StatusOK, items)
}

// GetBillText returns the text content for a specific text version. When the
// text's sections parsed, the raw XML is left out (it roughly doubles a big
// bill's response) unless the request asks for it with ?raw=1.
func (h *Handler) GetBillText(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vid := chi.URLParam(r, "vid")

	text, err := h.Bills.GetTextContent(r.Context(), id, vid)
	if err != nil {
		h.serverError(w, r, "failed to get text content", err)
		return
	}
	if text == nil {
		writeError(w, http.StatusNotFound, "text content not found")
		return
	}
	if hasSections(text.Sections) && r.URL.Query().Get("raw") != "1" {
		sectionsOnly := *text
		sectionsOnly.Content = ""
		text = &sectionsOnly
	}

	writeJSON(w, http.StatusOK, text)
}

// hasSections reports whether sections, a JSON array, has at least one
// element. A column that is NULL, "null" or "[]" has none. It looks only at
// the brackets: a big bill's sections run to megabytes.
func hasSections(sections []byte) bool {
	rest, ok := bytes.CutPrefix(bytes.TrimSpace(sections), []byte("["))
	return ok && !bytes.HasPrefix(bytes.TrimSpace(rest), []byte("]"))
}

// GetBillDiff returns a specific diff with its AI change summary.
func (h *Handler) GetBillDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	did := chi.URLParam(r, "did")

	diff, err := h.Bills.GetDiffByID(ctx, id, did)
	if err != nil {
		h.serverError(w, r, "failed to get diff", err)
		return
	}
	if diff == nil {
		writeError(w, http.StatusNotFound, "diff not found")
		return
	}

	summary, summaryErr := h.Bills.GetDiffSummary(ctx, did)
	if summaryErr != nil {
		h.log.WarnContext(ctx, "failed to get diff summary", "diff_id", did, "error", summaryErr)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"diff":    diff,
		"summary": summary,
	})
}

// GetBillVotes returns congressional votes for a bill.
func (h *Handler) GetBillVotes(w http.ResponseWriter, r *http.Request) {
	writeBillList(h, w, r, "votes", h.Votes.GetCongressionalVotes)
}

// ListTextVersions returns all text versions for a bill.
func (h *Handler) ListTextVersions(w http.ResponseWriter, r *http.Request) {
	writeBillList(h, w, r, "text versions", h.Bills.GetTextVersions)
}

// ListDiffs returns all text diffs for a bill.
func (h *Handler) ListDiffs(w http.ResponseWriter, r *http.Request) {
	writeBillList(h, w, r, "diffs", h.Bills.GetDiffs)
}

// ListGAOReports returns GAO reports linked to a bill.
func (h *Handler) ListGAOReports(w http.ResponseWriter, r *http.Request) {
	writeBillList(h, w, r, "GAO reports", h.Bills.ListGAOReports)
}

// ListAmendments returns all amendments for a bill.
func (h *Handler) ListAmendments(w http.ResponseWriter, r *http.Request) {
	writeBillList(h, w, r, "amendments", h.Bills.GetAmendments)
}
