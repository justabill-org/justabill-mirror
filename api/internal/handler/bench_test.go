package handler_test

// Benchmarks for the API's hot paths (#873). Run them with `task bench`; they report, they don't
// enforce: the numbers are a baseline to compare before and after a change on the same machine.
//
// A "miss" serves without a cache, as when Redis is down or not configured: the repository reads
// (fakes here, so Spanner's time isn't in it) and the JSON encoding. A real miss adds one Redis
// GET and one SET to that. A "hit" reads the encoded response from the Redis at REDIS_URL and is
// skipped without one.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/cachekey"
	"github.com/justabill-org/justabill/db/model"
)

// The size of the fixtures: a full page of the bill list, and a bill with as many actions,
// versions, votes and cosponsors as a bill that reached the floor of both chambers.
const (
	benchPageSize     = 20
	benchActions      = 40
	benchVersions     = 6
	benchVotes        = 4
	benchSponsors     = 60
	benchTextSections = 2000
)

// benchBillRepo is mockBillRepo without the call records that would grow with every iteration.
type benchBillRepo struct {
	*mockBillRepo
}

func (r benchBillRepo) List(_ context.Context, _ model.ListParams) (*model.ListResult[model.Bill], error) {
	return r.listResult, nil
}

func (r benchBillRepo) GetByID(_ context.Context, _ string) (*model.Bill, error) {
	return r.bill, nil
}

func benchBill(i int) model.Bill {
	introduced := time.Date(2025, time.March, 1+i%28, 0, 0, 0, 0, time.UTC)
	status, chamber, area := "passed_house", "House", "Health"
	return model.Bill{
		ID:             fmt.Sprintf("hr-119-%d", 1000+i),
		Congress:       119,
		BillType:       "hr",
		Number:         1000 + i,
		Title:          "To amend the Public Health Service Act to reauthorize programs, and for other purposes",
		IntroducedDate: &introduced,
		OriginChamber:  &chamber,
		LatestAction:   json.RawMessage(`{"actionDate":"2025-06-11","text":"Received in the Senate and Read twice."}`),
		CurrentStatus:  &status,
		StatusDate:     &introduced,
		PolicyArea:     &area,
		Sponsors: json.RawMessage(
			`[{"bioguideId":"A000370","firstName":"Alma","lastName":"Adams","party":"D","state":"NC"}]`),
		UpdatedAt: &introduced,
	}
}

// benchBillRepoFull is a bill repository with a full list page and a bill with every section.
func benchBillRepoFull() benchBillRepo {
	page := make([]model.Bill, benchPageSize)
	for i := range page {
		page[i] = benchBill(i)
	}
	bill := benchBill(0)
	short, long := "A plain-language summary.", strings.Repeat("A plain-language summary. ", 40)
	at := time.Date(2025, time.June, 11, 0, 0, 0, 0, time.UTC)
	actions := make([]model.BillAction, benchActions)
	for i := range actions {
		actions[i] = model.BillAction{
			ID: fmt.Sprintf("a%d", i), BillID: bill.ID, ActionDate: at.AddDate(0, 0, -i),
			ActionText: "Referred to the Subcommittee on Health.", SortOrder: i,
		}
	}
	versions := make([]model.BillTextVersion, benchVersions)
	for i := range versions {
		versions[i] = model.BillTextVersion{
			ID: fmt.Sprintf("v%d", i), BillID: bill.ID, VersionType: "Introduced in House", VersionCode: "ih",
			Date: &at, Formats: json.RawMessage(`[{"type":"Formatted XML","url":"https://www.congress.gov/x.xml"}]`),
		}
	}
	votes := make([]model.CongressionalVote, benchVotes)
	for i := range votes {
		votes[i] = model.CongressionalVote{ID: fmt.Sprintf("house-119-1-%d", i), BillID: &bill.ID,
			Congress: 119, Chamber: "House", VoteDate: at}
	}
	sponsors := make([]model.BillSponsorship, benchSponsors)
	for i := range sponsors {
		sponsors[i] = model.BillSponsorship{BioguideID: fmt.Sprintf("B%06d", i), FirstName: "Pat",
			LastName: "Member", Role: "cosponsor", SponsoredDate: &at, IsOriginal: i%2 == 0}
	}
	return benchBillRepo{&mockBillRepo{
		listResult: &model.ListResult[model.Bill]{Items: page, Total: 12345, Limit: benchPageSize},
		bill:       &bill,
		actions:    actions,
		summary:    &model.BillSummary{BillID: bill.ID, ShortSummary: &short, LongSummary: &long},
		versions:   versions,
		sponsors:   sponsors,
		texts:      map[string]*model.BillText{billKey(bill.ID, "v0"): benchText()},
	}}
}

// benchText is a big bill's parsed text: benchTextSections sections, about 1.5 MB of JSON.
func benchText() *model.BillText {
	var b strings.Builder
	b.WriteString("[")
	for i := range benchTextSections {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"s%d","kind":"section","enum":"Sec. %d.","header":"Amendments to section %d",`+
			`"content":%q}`, i, i+1, i, strings.Repeat("Section 1886(d) of the Social Security Act is amended. ", 12))
	}
	b.WriteString("]")
	return &model.BillText{ID: "t0", TextVersionID: "v0", Format: "xml", Content: "<bill/>",
		Sections: json.RawMessage(b.String())}
}

func newBenchHandler(b *testing.B, c *cache.Cache) *handler.Handler {
	b.Helper()
	repo := benchBillRepoFull()
	h := newTestHandler(repo)
	h.Votes = &mockVoteRepo{votes: []model.CongressionalVote{{ID: "house-119-1-1", Chamber: "House"}}}
	if c != nil {
		h.SetCache(c)
	}
	return h
}

// benchCache is the Redis at REDIS_URL, or skips the benchmark when there's none or it doesn't
// answer: a hit benchmark against a down Redis would time the uncached path.
func benchCache(b *testing.B) *cache.Cache {
	b.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		b.Skip("REDIS_URL not set; skipping the cache-hit benchmark")
	}
	c, err := cache.New(url)
	if err != nil {
		b.Fatalf("cache.New: %v", err)
	}
	b.Cleanup(func() { _ = c.Close() })
	if err = c.Ping(b.Context()); err != nil {
		b.Skipf("Redis at REDIS_URL doesn't answer (%v); skipping the cache-hit benchmark", err)
	}
	return c
}

// serveBench serves req to route on a chi router (so URL params are set) b.N times, failing on
// any status but 200, and reports the response size.
func serveBench(b *testing.B, pattern string, route http.HandlerFunc, target string) {
	b.Helper()
	r := chi.NewRouter()
	r.Get(pattern, route)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	if w.Code != http.StatusOK {
		b.Fatalf("GET %s: status %d: %s", target, w.Code, w.Body.String())
	}
	b.SetBytes(int64(w.Body.Len()))
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			b.Fatalf("GET %s: status %d", target, rec.Code)
		}
	}
}

func BenchmarkListBills(b *testing.B) {
	b.Run("miss", func(b *testing.B) {
		h := newBenchHandler(b, nil)
		serveBench(b, "/api/v1/bills", h.ListBills, "/api/v1/bills?congress=119&status=passed_house")
	})
	b.Run("hit", func(b *testing.B) {
		c := benchCache(b)
		h := newBenchHandler(b, c)
		// A search no visitor sends, so the key is this run's alone.
		target := fmt.Sprintf("/api/v1/bills?congress=119&q=bench%d", time.Now().UnixNano())
		p, err := handler.ParseListParams(httptest.NewRequest(http.MethodGet, target, nil))
		if err != nil {
			b.Fatalf("parse %s: %v", target, err)
		}
		b.Cleanup(func() { _ = c.Delete(context.Background(), handler.ListCacheKey("bills:list", p)) })
		serveBench(b, "/api/v1/bills", h.ListBills, target)
	})
}

func BenchmarkGetBill(b *testing.B) {
	b.Run("miss", func(b *testing.B) {
		h := newBenchHandler(b, nil)
		serveBench(b, "/api/v1/bills/{id}", h.GetBill, "/api/v1/bills/hr-119-1000")
	})
	b.Run("hit", func(b *testing.B) {
		c := benchCache(b)
		h := newBenchHandler(b, c)
		id := fmt.Sprintf("hr-119-%d", time.Now().UnixNano())
		b.Cleanup(func() { _ = c.Delete(context.Background(), cachekey.BillDetail(id)) })
		serveBench(b, "/api/v1/bills/{id}", h.GetBill, "/api/v1/bills/"+id)
	})
}

// BenchmarkGetBillText serves a big bill's parsed sections (GET /bills/{id}/text/{vid}), which
// the API passes through as stored JSON without a cache.
func BenchmarkGetBillText(b *testing.B) {
	h := newBenchHandler(b, nil)
	serveBench(b, "/api/v1/bills/{id}/text/{vid}", h.GetBillText, "/api/v1/bills/hr-119-1000/text/v0")
}

func BenchmarkListCacheKey(b *testing.B) {
	congress, area, search := 119, "Health", "medicare drug prices"
	cases := map[string]model.ListParams{
		"defaults": {Limit: benchPageSize},
		"filtered": {
			Offset: 40, Limit: benchPageSize, Congress: &congress, PolicyArea: &area, Search: &search,
			Statuses: []string{"passed_house", "passed_senate"}, StatusMode: "current",
		},
	}
	for name, p := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = handler.ListCacheKey("bills:list", p)
			}
		})
	}
}
