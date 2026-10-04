package legislators

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// filter118 selects the 118th Congress (2023-01-03 to 2025-01-03).
func filter118() Filter {
	return Filter{
		Start: day(2023, time.January, 3),
		End:   day(2025, time.January, 3),
		ValidState: func(code string) bool {
			return slices.Contains([]string{"AL", "CA", "DC", "NE", "OH", "PA", "TX", "WV"}, code)
		},
	}
}

// fixtureClient serves testdata/ (legislators-current.json and legislators-historical.json).
func fixtureClient(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.FileServer(http.Dir("testdata")))
	t.Cleanup(srv.Close)
	return New(srv.URL + "/")
}

func TestTerms118(t *testing.T) {
	people, err := fixtureClient(t).Fetch(t.Context())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(people) != 11 {
		t.Fatalf("Fetch returned %d people, want 11 (4 current + 7 historical)", len(people))
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	terms, skipped := filter118().Terms(t.Context(), logger, people)

	want := []Term{
		// Manchin: one Senate term, party as of the end of the congress (Independent).
		{Bioguide: "M001183", LIS: "S338", Chamber: ChamberSenate, State: "WV", Party: "Independent",
			Start: day(2023, time.January, 3), End: day(2025, time.January, 3)},
		// Moore: AL-2 in the 118th, not his 119th district (AL-1).
		{Bioguide: "M001212", Chamber: ChamberHouse, State: "AL", District: new(2), Party: "Republican",
			Start: day(2023, time.January, 3), End: day(2025, time.January, 3)},
		// Norton: a delegate, district 0.
		{Bioguide: "N000147", Chamber: ChamberHouse, State: "DC", District: new(0), Party: "Democrat",
			Start: day(2023, time.January, 3), End: day(2025, time.January, 3)},
		// Ricketts: an appointment and a special election collapse into one Senate term.
		{Bioguide: "R000618", LIS: "S423", Chamber: ChamberSenate, State: "NE", Party: "Republican",
			Start: day(2023, time.January, 23), End: day(2025, time.January, 3)},
		// Schiff: House CA-30 until 2024-12-08, then the Senate. Both carry his LIS ID.
		{Bioguide: "S001150", LIS: "S427", Chamber: ChamberHouse, State: "CA", District: new(30),
			Party: "Democrat", Start: day(2023, time.January, 3), End: day(2024, time.December, 8)},
		{Bioguide: "S001150", LIS: "S427", Chamber: ChamberSenate, State: "CA", Party: "Democrat",
			Start: day(2024, time.December, 9), End: day(2025, time.January, 3)},
	}
	if !reflect.DeepEqual(terms, want) {
		t.Errorf("Terms =\n%+v\nwant\n%+v", terms, want)
	}
	// Toomey's term ended the day the 118th began: excluded, not skipped.
	if skipped != 5 {
		t.Errorf("skipped = %d, want 5 (bad state, bioguide, LIS, district, dates)", skipped)
	}
	for _, reason := range []string{"unknown state", "bad bioguide ID", "bad LIS ID", "bad district", "bad dates"} {
		if !strings.Contains(logs.String(), reason) {
			t.Errorf("logs lack %q:\n%s", reason, logs.String())
		}
	}
	if strings.Contains(logs.String(), "T000461") {
		t.Errorf("an out-of-range term was logged as skipped:\n%s", logs.String())
	}
}

func TestTermRejects(t *testing.T) {
	f := filter118()
	id := ID{Bioguide: "A000001"}
	base := datedTerm{
		Type: termTypeRep, State: "OH", District: new(1),
		start: day(2023, time.January, 3), end: day(2025, time.January, 3),
	}
	cases := map[string]func(*datedTerm){
		"bad district":      func(d *datedTerm) { d.District = new(-1) },
		"unknown term type": func(d *datedTerm) { d.Type = "gov" },
		"unknown state":     func(d *datedTerm) { d.State = "Ohio" },
	}
	for want, mutate := range cases {
		raw := base
		mutate(&raw)
		if _, reason := f.term(id, raw); reason != want {
			t.Errorf("term(%+v) reason = %q, want %q", raw.RawTerm, reason, want)
		}
	}
	noDistrict := base
	noDistrict.District = nil
	if _, reason := f.term(id, noDistrict); reason != "bad district" {
		t.Errorf("a House term without a district: reason = %q, want bad district", reason)
	}
	if _, reason := (Filter{}).term(id, base); reason != "unknown state" {
		t.Errorf("a filter without ValidState: reason = %q, want unknown state", reason)
	}
}

func TestFetchSizeCap(t *testing.T) {
	body := `[{"id":{"bioguide":"A000001"},"terms":[]}]`
	for name, handler := range map[string]http.HandlerFunc{
		"content-length": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) },
		"chunked": func(w http.ResponseWriter, _ *http.Request) {
			w.(http.Flusher).Flush() // no Content-Length: only the read limit catches it
			_, _ = w.Write([]byte(body))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			c := New(srv.URL)
			c.maxBytes = int64(len(body) - 1)
			people, err := c.Fetch(t.Context())
			if !errors.Is(err, ErrTooLarge) {
				t.Fatalf("Fetch error = %v, want ErrTooLarge", err)
			}
			if people != nil {
				t.Errorf("Fetch returned %d people with an error", len(people))
			}
		})
	}
}

func TestFetchTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c := New(srv.URL)
	c.httpClient.Timeout = 50 * time.Millisecond
	if _, err := c.Fetch(t.Context()); err == nil {
		t.Fatal("Fetch from a server that never answers returned no error")
	}
}

func TestFetchErrors(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"not found": http.NotFound,
		"bad JSON":  func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"not":"a list"}`)) },
		// The current file is fine but the historical one fails: nothing is returned.
		"second file fails": func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, currentFile) {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			people, err := New(srv.URL).Fetch(t.Context())
			if err == nil || people != nil {
				t.Errorf("Fetch = %d people, %v; want an error and nothing", len(people), err)
			}
		})
	}
	if _, err := New("http://[::1]:namedport").Fetch(t.Context()); err == nil {
		t.Error("Fetch with a malformed base URL returned no error")
	}
}

func TestNewBaseURL(t *testing.T) {
	if got := New("").baseURL; got != DefaultBaseURL {
		t.Errorf(`New("").baseURL = %q, want %q`, got, DefaultBaseURL)
	}
	pinned := "https://raw.githubusercontent.com/unitedstates/congress-legislators/577ca04"
	if got := New(pinned + "/").baseURL; got != pinned {
		t.Errorf("New(pinned/).baseURL = %q, want %q", got, pinned)
	}
}
