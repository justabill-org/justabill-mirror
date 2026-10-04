package district_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/justabill-org/justabill/api/internal/district"
	"github.com/justabill-org/justabill/obs/obstest"
)

const layer119 = "119th Congressional Districts"

// geocoder is an httptest Census geocoder that answers with a canned body and records the
// query string of every request.
type geocoder struct {
	srv     *httptest.Server
	body    string
	status  int
	queries []url.Values
}

func newGeocoder(t *testing.T, body string) *geocoder {
	t.Helper()
	g := &geocoder{body: body, status: http.StatusOK}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.queries = append(g.queries, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(g.status)
		_, _ = w.Write([]byte(g.body))
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *geocoder) lookup() *district.CensusLookup {
	return district.NewCensusLookupWithURL(g.srv.Client(), g.srv.URL)
}

// fixture reads a geocoder response captured from the live service on 2026-09-27.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return string(b)
}

// oneRecord builds a response with one address match holding one record of the given layer.
func oneRecord(layer, geoid, session string) string {
	return `{"result":{"addressMatches":[{"geographies":{"` + layer + `":[` +
		`{"GEOID":"` + geoid + `","CDSESSN":"` + session + `","STATE":"` + geoid[:2] + `"}]}}]}}`
}

func TestFromAddress_Fixtures(t *testing.T) {
	tests := []struct {
		fixture string
		address string
		want    []district.Result
	}{
		{"austin_tx", "1100 Congress Ave, Austin, TX 78701",
			[]district.Result{{State: "TX", District: 37, Source: district.SourceGeocoder}}},
		{"cheyenne_wy", "200 W 24th St, Cheyenne, WY 82001",
			[]district.Result{{State: "WY", District: 0, AtLarge: true, Source: district.SourceGeocoder}}},
		{"bismarck_nd", "600 E Boulevard Ave, Bismarck, ND 58505",
			[]district.Result{{State: "ND", District: 0, AtLarge: true, Source: district.SourceGeocoder}}},
		{"white_house_dc", "1600 Pennsylvania Ave NW, Washington, DC 20500",
			[]district.Result{{State: "DC", District: 0, AtLarge: true, Source: district.SourceGeocoder}}},
		{"san_juan_pr", "1 Calle Fortaleza, San Juan, PR 00901",
			[]district.Result{{State: "PR", District: 0, AtLarge: true, Source: district.SourceGeocoder}}},
		// Two address matches, both in NY-10: deduplicated.
		{"new_york_ny", "100 Broadway, New York, NY 10005",
			[]district.Result{{State: "NY", District: 10, Source: district.SourceGeocoder}}},
		// The address-range benchmark doesn't cover the Island Areas: no match, so the
		// single-seat fallback answers from the address.
		{"guam", "1 Marine Corps Dr, Hagatna, GU 96910",
			[]district.Result{{State: "GU", District: 0, AtLarge: true, Source: district.SourceState}}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			g := newGeocoder(t, fixture(t, tt.fixture))

			got, err := g.lookup().FromAddress(context.Background(), tt.address, 119)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("result %d: got %+v, want %+v", i, got[i], tt.want[i])
				}
			}
			if q := g.queries[0]; q.Get("address") != tt.address {
				t.Errorf("address = %q, want %q", q.Get("address"), tt.address)
			}
		})
	}
}

func TestFromAddress_RequestsOneLayerOfTheCongressMap(t *testing.T) {
	tests := []struct {
		congress int
		vintage  string
		layer    string
		body     string
	}{
		{119, "ACS2025_Current", layer119, oneRecord(layer119, "4837", "119")},
		{120, "ACS2026_Current", "120th Congressional Districts",
			oneRecord("120th Congressional Districts", "4810", "120")},
	}
	for _, tt := range tests {
		g := newGeocoder(t, tt.body)

		if _, err := g.lookup().FromAddress(context.Background(), "an address", tt.congress); err != nil {
			t.Fatalf("congress %d: unexpected error: %v", tt.congress, err)
		}
		q := g.queries[0]
		if q.Get("vintage") != tt.vintage || q.Get("benchmark") != "Public_AR_Current" {
			t.Errorf("congress %d: vintage %q benchmark %q", tt.congress, q.Get("vintage"), q.Get("benchmark"))
		}
		if layers := q["layers"]; len(layers) != 1 || layers[0] != tt.layer {
			t.Errorf("congress %d: layers = %q, want exactly %q", tt.congress, layers, tt.layer)
		}
	}
}

func TestFromAddress_UnexpectedLayer(t *testing.T) {
	tests := map[string]string{
		// What the geocoder sends for the default vintage: its default layers, including
		// the 120th districts, and no 119th layer at all.
		"only the 120th layer": fixture(t, "current_current_austin_tx"),
		"wrong CDSESSN":        oneRecord(layer119, "4810", "120"),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			g := newGeocoder(t, body)

			got, err := g.lookup().FromAddress(context.Background(), "1100 Congress Ave, Austin, TX", 119)
			if !errors.Is(err, district.ErrUnexpectedLayer) {
				t.Fatalf("got %+v, %v; want ErrUnexpectedLayer", got, err)
			}
		})
	}
}

func TestFromAddress_UnsupportedCongress(t *testing.T) {
	g := newGeocoder(t, oneRecord(layer119, "4837", "119"))

	_, err := g.lookup().FromAddress(context.Background(), "an address", 118)
	if !errors.Is(err, district.ErrUnsupportedCongress) {
		t.Fatalf("got %v, want ErrUnsupportedCongress", err)
	}
	if len(g.queries) != 0 {
		t.Errorf("made %d requests for an unsupported congress, want 0", len(g.queries))
	}
}

func TestFromAddress_DistrictCodes(t *testing.T) {
	tests := []struct {
		geoid string
		want  []district.Result
	}{
		{"0612", []district.Result{{State: "CA", District: 12, Source: district.SourceGeocoder}}},
		{"0200", []district.Result{{State: "AK", District: 0, AtLarge: true, Source: district.SourceGeocoder}}},
		{"6698", []district.Result{{State: "GU", District: 0, AtLarge: true, Source: district.SourceGeocoder}}},
		{"36ZZ", nil}, // water with no district
		{"4899", nil}, // not a district number
		{"9901", nil}, // unknown state FIPS
		{"481", nil},  // malformed
	}
	for _, tt := range tests {
		t.Run(tt.geoid, func(t *testing.T) {
			body := `{"result":{"addressMatches":[{"geographies":{"` + layer119 + `":[` +
				`{"GEOID":"` + tt.geoid + `","CDSESSN":"119"}]}}]}}`
			g := newGeocoder(t, body)

			got, err := g.lookup().FromAddress(context.Background(), "an address", 119)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) || (len(got) == 1 && got[0] != tt.want[0]) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFromAddress_HTTPErrors(t *testing.T) {
	g := newGeocoder(t, `{}`)
	g.status = http.StatusInternalServerError

	if _, err := g.lookup().FromAddress(context.Background(), "an address", 119); err == nil {
		t.Error("expected an error for a 500 response")
	}

	g.status = http.StatusOK
	g.body = `not json`
	if _, err := g.lookup().FromAddress(context.Background(), "an address", 119); err == nil {
		t.Error("expected an error for an invalid body")
	}
}

func TestFromAddress_ErrorOmitsAddress(t *testing.T) {
	g := newGeocoder(t, `{}`)
	g.srv.Close() // connection refused: the client returns a *url.Error holding the URL

	const address = "1600 Pennsylvania Ave NW, Washington, DC 20500"
	_, err := g.lookup().FromAddress(context.Background(), address, 119)
	if err == nil {
		t.Fatal("expected an error from a closed server")
	}
	for _, part := range []string{"Pennsylvania", url.QueryEscape(address)} {
		if strings.Contains(err.Error(), part) {
			t.Errorf("error %q contains the address", err)
		}
	}
}

// TestFromAddress_Live checks the real Census geocoder. Run it with CENSUS_LIVE=1 whenever a
// congress is added to the layer table.
func TestFromAddress_Live(t *testing.T) {
	if os.Getenv("CENSUS_LIVE") != "1" {
		t.Skip("set CENSUS_LIVE=1 to query the live Census geocoder")
	}
	tests := []struct {
		address  string
		congress int
		want     district.Result
	}{
		{"1100 Congress Ave, Austin, TX 78701", 119, district.Result{State: "TX", District: 37}},
		{"1100 Congress Ave, Austin, TX 78701", 120, district.Result{State: "TX", District: 10}},
		{"200 W 24th St, Cheyenne, WY 82001", 119, district.Result{State: "WY", District: 0, AtLarge: true}},
		{"1600 Pennsylvania Ave NW, Washington, DC 20500", 119,
			district.Result{State: "DC", District: 0, AtLarge: true}},
	}
	lookup := district.NewCensusLookup()
	for _, tt := range tests {
		tt.want.Source = district.SourceGeocoder
		got, err := lookup.FromAddress(context.Background(), tt.address, tt.congress)
		if err != nil {
			t.Errorf("%s (%d): %v", tt.address, tt.congress, err)
			continue
		}
		if len(got) != 1 || got[0] != tt.want {
			t.Errorf("%s (%d): got %+v, want %+v", tt.address, tt.congress, got, tt.want)
		}
	}
}

// NewCensusLookupAt sends requests to its base URL, with the same vintage and layer pinning.
func TestNewCensusLookupAt(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"result":{"addressMatches":[]}}`))
	}))
	t.Cleanup(srv.Close)

	if _, err := district.NewCensusLookupAt(srv.URL).FromAddress(context.Background(), "1 Main St", 119); err != nil {
		t.Fatalf("FromAddress: %v", err)
	}
	if got.Get("vintage") != "ACS2025_Current" || got.Get("layers") != layer119 {
		t.Errorf("query = %v, want the 119th's vintage and layer", got)
	}
}

// Geocoder calls are CLIENT spans under the caller's span, and the Census
// Bureau gets neither our trace context nor, in the span, the address.
func TestNewCensusLookupAtTraces(t *testing.T) {
	tel := obstest.New(t)
	var traceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("Traceparent")
		_, _ = w.Write([]byte(`{"result":{"addressMatches":[]}}`))
	}))
	t.Cleanup(srv.Close)

	ctx, parent := otel.Tracer("test").Start(t.Context(), "request")
	if _, err := district.NewCensusLookupAt(srv.URL).FromAddress(ctx, "1 Main St", 119); err != nil {
		t.Fatalf("FromAddress: %v", err)
	}
	parent.End()

	if traceparent != "" {
		t.Errorf("sent traceparent %q to the geocoder", traceparent)
	}
	spans := tel.Ended()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want the geocoder call and its parent", len(spans))
	}
	call := spans[0]
	if call.SpanKind() != trace.SpanKindClient || call.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Errorf("span %q (%v), want a client span under the request", call.Name(), call.SpanKind())
	}
	for _, kv := range call.Attributes() {
		if strings.Contains(kv.Value.String(), "Main") {
			t.Errorf("%s = %q holds the address", kv.Key, kv.Value.String())
		}
	}
}

// The 120th's map, recorded from ACS2026_Current on 2026-09-28, puts the Texas Capitol in
// TX-10 (TX-37 in the 119th).
func TestFromAddress_NextCongressFixture(t *testing.T) {
	g := newGeocoder(t, fixture(t, "acs2026_austin_tx"))

	got, err := g.lookup().FromAddress(context.Background(), "1100 Congress Ave, Austin, TX 78701", 120)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := district.Result{State: "TX", District: 10, Source: district.SourceGeocoder}
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A recorded coordinates response, captured from the live service on 2026-10-03, and the
// longitude and latitude the lookup sent as x and y.
func TestFromCoordinates_Fixtures(t *testing.T) {
	dc := district.Result{State: "DC", District: 0, AtLarge: true, Source: district.SourceGeocoder}
	tests := []struct {
		fixture  string
		lat, lon float64
		congress int
		want     []district.Result
	}{
		{"coordinates_white_house_dc", 38.8977, -77.0365, 119, []district.Result{dc}},
		{"coordinates_acs2026_white_house_dc", 38.8977, -77.0365, 120, []district.Result{dc}},
		{"coordinates_austin_tx", 30.2672, -97.7431, 119,
			[]district.Result{{State: "TX", District: 37, Source: district.SourceGeocoder}}},
		// Open ocean: the geocoder answers with no layers at all, which is no match, not an error.
		{"coordinates_atlantic", 30, -40, 119, nil},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			g := newGeocoder(t, fixture(t, tt.fixture))

			got, err := g.lookup().FromCoordinates(context.Background(), tt.lat, tt.lon, tt.congress)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) || (len(got) == 1 && got[0] != tt.want[0]) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
			q := g.queries[0]
			wantX := strconv.FormatFloat(tt.lon, 'f', -1, 64)
			wantY := strconv.FormatFloat(tt.lat, 'f', -1, 64)
			if q.Get("x") != wantX || q.Get("y") != wantY || q.Has("address") {
				t.Errorf("query = %v, want x=%s (longitude) and y=%s (latitude), no address", q, wantX, wantY)
			}
		})
	}
}

// FromCoordinates pins the same benchmark, vintage and single layer as FromAddress, on the
// coordinates endpoint next to the configured one-line-address endpoint.
func TestFromCoordinates_RequestsOneLayerOfTheCongressMap(t *testing.T) {
	tests := []struct {
		congress int
		vintage  string
		layer    string
	}{
		{119, "ACS2025_Current", layer119},
		{120, "ACS2026_Current", "120th Congressional Districts"},
	}
	for _, tt := range tests {
		var path string
		var q url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path, q = r.URL.Path, r.URL.Query()
			_, _ = w.Write([]byte(`{"result":{"geographies":{"` + tt.layer + `":[]}}}`))
		}))
		lookup := district.NewCensusLookupAt(srv.URL + "/geocoder/geographies/onelineaddress")

		if _, err := lookup.FromCoordinates(context.Background(), 30.2672, -97.7431, tt.congress); err != nil {
			t.Fatalf("congress %d: unexpected error: %v", tt.congress, err)
		}
		srv.Close()
		if path != "/geocoder/geographies/coordinates" {
			t.Errorf("congress %d: path = %q, want the coordinates endpoint", tt.congress, path)
		}
		if q.Get("vintage") != tt.vintage || q.Get("benchmark") != "Public_AR_Current" || q.Get("format") != "json" {
			t.Errorf("congress %d: query %v", tt.congress, q)
		}
		if layers := q["layers"]; len(layers) != 1 || layers[0] != tt.layer {
			t.Errorf("congress %d: layers = %q, want exactly %q", tt.congress, layers, tt.layer)
		}
	}
}

func TestFromCoordinates_UnexpectedLayer(t *testing.T) {
	tests := map[string]string{
		// What the geocoder sends when the vintage lacks the layer: its default layers.
		"default layers": `{"result":{"geographies":{"States":[{"GEOID":"11"}],` +
			`"120th Congressional Districts":[{"GEOID":"1198","CDSESSN":"120"}]}}}`,
		"wrong CDSESSN": `{"result":{"geographies":{"` + layer119 + `":[{"GEOID":"1198","CDSESSN":"120"}]}}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			g := newGeocoder(t, body)

			got, err := g.lookup().FromCoordinates(context.Background(), 38.8977, -77.0365, 119)
			if !errors.Is(err, district.ErrUnexpectedLayer) {
				t.Fatalf("got %+v, %v; want ErrUnexpectedLayer", got, err)
			}
		})
	}
}

func TestFromCoordinates_UnsupportedCongress(t *testing.T) {
	g := newGeocoder(t, `{"result":{"geographies":{}}}`)

	_, err := g.lookup().FromCoordinates(context.Background(), 38.8977, -77.0365, 118)
	if !errors.Is(err, district.ErrUnsupportedCongress) {
		t.Fatalf("got %v, want ErrUnsupportedCongress", err)
	}
	if len(g.queries) != 0 {
		t.Errorf("made %d requests for an unsupported congress, want 0", len(g.queries))
	}
}

func TestFromCoordinates_HTTPErrors(t *testing.T) {
	g := newGeocoder(t, `{"errors":["X coordinate must be between -180 and 180"],"status":"400"}`)
	g.status = http.StatusBadRequest

	if _, err := g.lookup().FromCoordinates(context.Background(), 38.8977, -777, 119); err == nil {
		t.Error("expected an error for a 400 response")
	}

	g.status = http.StatusOK
	g.body = `not json`
	if _, err := g.lookup().FromCoordinates(context.Background(), 38.8977, -77.0365, 119); err == nil {
		t.Error("expected an error for an invalid body")
	}
}

func TestFromCoordinates_ErrorOmitsCoordinates(t *testing.T) {
	g := newGeocoder(t, `{}`)
	g.srv.Close() // connection refused: the client returns a *url.Error holding the URL

	_, err := g.lookup().FromCoordinates(context.Background(), 38.8977, -77.0365, 119)
	if err == nil {
		t.Fatal("expected an error from a closed server")
	}
	for _, part := range []string{"38.8977", "77.0365"} {
		if strings.Contains(err.Error(), part) {
			t.Errorf("error %q contains the coordinates", err)
		}
	}
}

// TestFromCoordinates_Live checks the real Census geocoder's coordinates endpoint. Run it with
// CENSUS_LIVE=1 whenever a congress is added to the layer table.
func TestFromCoordinates_Live(t *testing.T) {
	if os.Getenv("CENSUS_LIVE") != "1" {
		t.Skip("set CENSUS_LIVE=1 to query the live Census geocoder")
	}
	lookup := district.NewCensusLookup()
	for congress, want := range map[int]district.Result{
		119: {State: "TX", District: 37, Source: district.SourceGeocoder},
		120: {State: "TX", District: 10, Source: district.SourceGeocoder},
	} {
		got, err := lookup.FromCoordinates(context.Background(), 30.2747, -97.7404, congress)
		if err != nil {
			t.Errorf("Texas Capitol (%d): %v", congress, err)
			continue
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("Texas Capitol (%d): got %+v, want %+v", congress, got, want)
		}
	}
}

func TestHasMap(t *testing.T) {
	for congress, want := range map[int]bool{118: false, 119: true, 120: true, 121: false} {
		if got := district.HasMap(congress); got != want {
			t.Errorf("HasMap(%d) = %v, want %v", congress, got, want)
		}
	}
}

func TestElectionDate(t *testing.T) {
	tests := map[int]string{
		118: "2022-11-08",
		119: "2024-11-05",
		120: "2026-11-03",
		121: "2028-11-07",
		// November 1, 2032 is a Monday: the election is the next day.
		123: "2032-11-02",
		// November 1, 2016 was a Tuesday: the election is a week later, not that day.
		115: "2016-11-08",
	}
	for congress, want := range tests {
		got := district.ElectionDate(congress)
		if got.Format(time.DateOnly) != want || got.Weekday() != time.Tuesday {
			t.Errorf("ElectionDate(%d) = %s, want %s (a Tuesday)", congress, got, want)
		}
	}
}
