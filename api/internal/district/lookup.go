// Package district resolves a street address, or a point given as latitude and longitude, to
// its congressional districts with the US Census Bureau geocoder.
package district

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

	"github.com/justabill-org/justabill/obs"
)

const (
	censusBenchmark      = "Public_AR_Current"
	censusRequestTimeout = 10 * time.Second

	congress119 = 119
	congress120 = 120

	// Census district codes (the last two digits of GEOID) for single-seat jurisdictions:
	// "00" for at-large states, "98" for DC and the territories' non-voting seats.
	codeAtLarge   = "00"
	codeNonVoting = "98"
	maxDistrict   = 53
	geoidLen      = 4
	stateFIPSLen  = 2

	// The general election that elects congress N is held in year electionYearBase + 2N.
	electionYearBase = 1786
	yearsPerCongress = 2
	daysPerWeek      = 7
)

// SourceGeocoder marks a result read from the Census geocoder's district layer.
const SourceGeocoder = "geocoder"

// CensusGeocoderURL is the Census Bureau geocoder's one-line-address endpoint,
// which NewCensusLookup calls. Its sibling coordinates endpoint answers FromCoordinates.
const CensusGeocoderURL = "https://geocoding.geo.census.gov/geocoder/geographies/onelineaddress"

const (
	addressEndpoint     = "/onelineaddress"
	coordinatesEndpoint = "/coordinates"
)

var (
	// ErrUnsupportedCongress means the layer table has no district map for the congress.
	ErrUnsupportedCongress = errors.New("no district map for congress")
	// ErrUnexpectedLayer means the geocoder answered with a layer other than the one asked
	// for, which it does (with HTTP 200) when the vintage doesn't have that layer.
	ErrUnexpectedLayer = errors.New("census geocoder returned an unexpected district layer")
)

// Result holds the state and congressional district resolved from an address or a point.
type Result struct {
	State    string // 2-letter state abbreviation
	District int    // congressional district number (0 for at-large and non-voting seats)
	AtLarge  bool   // the state or territory has a single House seat
	Source   string // where the answer came from: SourceGeocoder or SourceState
}

// Lookup resolves addresses and points to congressional districts in a given congress's map.
type Lookup interface {
	FromAddress(ctx context.Context, address string, congress int) ([]Result, error)
	FromCoordinates(ctx context.Context, lat, lon float64, congress int) ([]Result, error)
}

// layer names the Census geocoder vintage and layer that hold one congress's district map.
type layer struct {
	congress int
	vintage  string
	name     string
}

// layerFor returns the district map for a congress. Adding a congress is one case; a congress
// with no case gets an error, never another congress's map. find-my-reps also looks the address
// up in the next congress's map while there is one (docs/design/237-election-district.md): with
// no 121 case, that stops by itself when the 120th becomes current, and adding the 121st's map
// turns it back on for the 2028 election.
func layerFor(congress int) (layer, bool) {
	switch congress {
	case congress119:
		return layer{congress: congress, vintage: "ACS2025_Current", name: "119th Congressional Districts"}, true
	case congress120:
		return layer{congress: congress, vintage: "ACS2026_Current", name: "120th Congressional Districts"}, true
	default:
		return layer{}, false
	}
}

// HasMap reports whether the layer table has a district map for the congress.
func HasMap(congress int) bool {
	_, ok := layerFor(congress)
	return ok
}

// ElectionDate returns the day of the general election that elects the congress: the Tuesday
// after the first Monday in November of year 1786 + 2 × congress (120 → 2026-11-03), at
// midnight UTC.
func ElectionDate(congress int) time.Time {
	nov1 := time.Date(electionYearBase+yearsPerCongress*congress, time.November, 1, 0, 0, 0, 0, time.UTC)
	// The first Monday is this many days after November 1, and the election one day later.
	toMonday := (int(time.Monday) - int(nov1.Weekday()) + daysPerWeek) % daysPerWeek
	return nov1.AddDate(0, 0, toMonday+1)
}

// censusDistrict is one record of a congressional district layer.
type censusDistrict struct {
	GEOID   string `json:"GEOID"`   // state FIPS + 2-digit district code, e.g. "4837"
	Session string `json:"CDSESSN"` // the congress the map belongs to, e.g. "119"
}

// geographies maps a layer name to its records at one place.
type geographies map[string][]censusDistrict

// censusResponse models the relevant parts of the Census geocoder JSON response.
type censusResponse struct {
	Result struct {
		// AddressMatches is the one-line-address endpoint's answer: a match per place the
		// address could be.
		AddressMatches []struct {
			Geographies geographies `json:"geographies"`
		} `json:"addressMatches"`
		// Geographies is the coordinates endpoint's answer: the layers at the point, and an
		// empty object when the point is in none (open ocean, another country).
		Geographies geographies `json:"geographies"`
	} `json:"result"`
}

// CensusLookup implements Lookup using the US Census Bureau Geocoder API.
type CensusLookup struct {
	client  *http.Client
	baseURL string // the one-line-address endpoint
	// coordsURL is the coordinates endpoint, a sibling of baseURL (see coordinatesURL).
	coordsURL string
}

// NewCensusLookup creates a CensusLookup with default settings.
func NewCensusLookup() *CensusLookup {
	return NewCensusLookupAt(CensusGeocoderURL)
}

// NewCensusLookupAt creates a CensusLookup with default settings that sends its
// requests to baseURL instead of the Census Bureau, such as a stub for the
// browser smoke tests (CENSUS_GEOCODER_URL). baseURL is the one-line-address
// endpoint; coordinates go to its sibling (see FromCoordinates). The vintage
// and layer pinning still apply: only the base URL changes. Each request is a
// CLIENT span through [obs.HTTPTransport], which sends the geocoder no trace
// headers and records the URL without its query (the address or the point).
func NewCensusLookupAt(baseURL string) *CensusLookup {
	return NewCensusLookupWithURL(
		&http.Client{Timeout: censusRequestTimeout, Transport: obs.HTTPTransport(nil)}, baseURL)
}

// NewCensusLookupWithURL creates a CensusLookup with a custom HTTP client and base URL (for testing).
func NewCensusLookupWithURL(client *http.Client, baseURL string) *CensusLookup {
	return &CensusLookup{
		client:    client,
		baseURL:   baseURL,
		coordsURL: coordinatesURL(baseURL),
	}
}

// coordinatesURL returns the coordinates endpoint next to a one-line-address endpoint:
// ".../geographies/onelineaddress" becomes ".../geographies/coordinates", and a base URL that
// doesn't end in "/onelineaddress" (a stub's root) gets "/coordinates" added.
func coordinatesURL(baseURL string) string {
	return strings.TrimSuffix(baseURL, addressEndpoint) + coordinatesEndpoint
}

// FromAddress resolves a street address (e.g., "100 Broadway, New York, NY 10005") to the
// congressional districts of the given congress. It asks the Census geocoder for that
// congress's district layer only and rejects any other layer. When the geocoder finds no district
// and the address names a single-seat state or territory, it returns that seat (SourceState).
// Errors never contain the address.
func (c *CensusLookup) FromAddress(ctx context.Context, address string, congress int) ([]Result, error) {
	l, ok := layerFor(congress)
	if !ok {
		return nil, fmt.Errorf("%w %d", ErrUnsupportedCongress, congress)
	}

	q := layerQuery(l)
	q.Set("address", address)
	cr, err := c.get(ctx, c.baseURL, q)
	if err != nil {
		return nil, err
	}

	matches := make([]geographies, 0, len(cr.Result.AddressMatches))
	for _, m := range cr.Result.AddressMatches {
		matches = append(matches, m.Geographies)
	}
	results, err := parseResults(matches, l)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		if state, single := singleSeat(address); single {
			results = []Result{{State: state, District: 0, AtLarge: true, Source: SourceState}}
		}
	}
	return results, nil
}

// FromCoordinates resolves a point (latitude and longitude in degrees, WGS84) to the
// congressional districts of the given congress, from the Census geocoder's coordinates
// endpoint (x is the longitude, y the latitude). It asks for that congress's district layer
// only and rejects any other layer, as FromAddress does. A point in no district (open ocean,
// another country) gets no results and no error; the single-seat fallback doesn't apply, since
// there's no address to read a state from. Errors never contain the coordinates.
func (c *CensusLookup) FromCoordinates(ctx context.Context, lat, lon float64, congress int) ([]Result, error) {
	l, ok := layerFor(congress)
	if !ok {
		return nil, fmt.Errorf("%w %d", ErrUnsupportedCongress, congress)
	}

	q := layerQuery(l)
	q.Set("x", strconv.FormatFloat(lon, 'f', -1, 64))
	q.Set("y", strconv.FormatFloat(lat, 'f', -1, 64))
	cr, err := c.get(ctx, c.coordsURL, q)
	if err != nil {
		return nil, err
	}
	// The geocoder answers a point in no district with no layers at all, not an empty layer.
	if len(cr.Result.Geographies) == 0 {
		return nil, nil
	}
	return parseResults([]geographies{cr.Result.Geographies}, l)
}

// layerQuery is the query that pins a request to one congress's vintage and district layer.
func layerQuery(l layer) url.Values {
	q := url.Values{}
	q.Set("benchmark", censusBenchmark)
	q.Set("vintage", l.vintage)
	q.Set("layers", l.name)
	q.Set("format", "json")
	return q
}

// get calls a geocoder endpoint and decodes its answer. Errors never contain the query, which
// holds the address or the point.
func (c *CensusLookup) get(ctx context.Context, endpoint string, q url.Values) (censusResponse, error) {
	var cr censusResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return cr, fmt.Errorf("creating census request: %w", redactURL(err))
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return cr, fmt.Errorf("calling census geocoder: %w", redactURL(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return cr, fmt.Errorf("census geocoder returned status %d", resp.StatusCode)
	}

	if decodeErr := json.NewDecoder(resp.Body).Decode(&cr); decodeErr != nil {
		return cr, fmt.Errorf("decoding census response: %w", decodeErr)
	}
	return cr, nil
}

// redactURL drops the request URL, which carries the address or the point, from a *[url.Error].
func redactURL(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// parseResults extracts district results from the geographies of each place a response matched.
// Every match must carry the requested layer, and every record in it must belong to the
// requested congress.
func parseResults(matches []geographies, l layer) ([]Result, error) {
	session := strconv.Itoa(l.congress)
	seen := make(map[Result]bool)
	var results []Result

	for _, match := range matches {
		districts, ok := match[l.name]
		if !ok {
			return nil, fmt.Errorf("%w: want %q, got %s", ErrUnexpectedLayer, l.name, layerNames(match))
		}
		for _, cd := range districts {
			if cd.Session != session {
				return nil, fmt.Errorf("%w: %q record has CDSESSN %q, want %q",
					ErrUnexpectedLayer, l.name, cd.Session, session)
			}
			r, valid := decodeGEOID(cd.GEOID)
			if !valid || seen[r] {
				continue
			}
			seen[r] = true
			results = append(results, r)
		}
	}

	return results, nil
}

// decodeGEOID turns a district GEOID (state FIPS + district code) into a Result. Codes "00" and
// "98" are single-seat jurisdictions (district 0). It reports false for anything else that isn't
// a district number, such as "ZZ" (water with no district), and for unknown states.
func decodeGEOID(geoid string) (Result, bool) {
	if len(geoid) != geoidLen {
		return Result{}, false
	}
	state, known := fipsToState[geoid[:stateFIPSLen]]
	if !known {
		return Result{}, false
	}
	code := geoid[stateFIPSLen:]
	if code == codeAtLarge || code == codeNonVoting {
		return Result{State: state, District: 0, AtLarge: true, Source: SourceGeocoder}, true
	}
	n, err := strconv.Atoi(code)
	if err != nil || n < 1 || n > maxDistrict {
		return Result{}, false
	}
	return Result{State: state, District: n, Source: SourceGeocoder}, true
}

// layerNames lists a response's layer names for error messages.
func layerNames(g geographies) string {
	names := make([]string, 0, len(g))
	for name := range g {
		names = append(names, name)
	}
	slices.Sort(names)
	return fmt.Sprintf("%q", names)
}

//nolint:gochecknoglobals // lookup table
var fipsToState = map[string]string{
	"01": "AL", "02": "AK", "04": "AZ", "05": "AR", "06": "CA",
	"08": "CO", "09": "CT", "10": "DE", "11": "DC", "12": "FL",
	"13": "GA", "15": "HI", "16": "ID", "17": "IL", "18": "IN",
	"19": "IA", "20": "KS", "21": "KY", "22": "LA", "23": "ME",
	"24": "MD", "25": "MA", "26": "MI", "27": "MN", "28": "MS",
	"29": "MO", "30": "MT", "31": "NE", "32": "NV", "33": "NH",
	"34": "NJ", "35": "NM", "36": "NY", "37": "NC", "38": "ND",
	"39": "OH", "40": "OK", "41": "OR", "42": "PA", "44": "RI",
	"45": "SC", "46": "SD", "47": "TN", "48": "TX", "49": "UT",
	"50": "VT", "51": "VA", "53": "WA", "54": "WV", "55": "WI",
	"56": "WY", "60": "AS", "66": "GU", "69": "MP", "72": "PR",
	"78": "VI",
}
