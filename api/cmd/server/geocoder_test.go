package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/api/internal/district"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// currentCongress is a CongressRepo with the 119th current.
type currentCongress struct{}

func (currentCongress) List(context.Context) ([]model.Congress, error) {
	return []model.Congress{{Number: 119, IsCurrent: true}}, nil
}

// texasMembers implements the MemberRepo methods find-my-reps uses: TX-37 has
// one representative and Texas no senators. Any other method panics on the nil
// embedded interface.
type texasMembers struct {
	repository.MemberRepo
}

func (texasMembers) GetByDistrict(_ context.Context, state string, dist int) ([]model.Member, error) {
	if state == "TX" && dist == 37 {
		return []model.Member{{BioguideID: "D000399", LastName: "Doggett"}}, nil
	}
	return nil, nil
}

func (texasMembers) GetSenators(context.Context, string) ([]model.Member, error) { return nil, nil }

// setGeocoderEnv sets CENSUS_GEOCODER_URL for one test, or unsets it when
// value is empty, and points viper at the environment.
func setGeocoderEnv(t *testing.T, value string) {
	t.Helper()
	t.Setenv("CENSUS_GEOCODER_URL", value)
	if value == "" {
		_ = os.Unsetenv("CENSUS_GEOCODER_URL")
	}
	viper.Reset()
	viper.AutomaticEnv()
	t.Cleanup(viper.Reset)
}

func TestCensusGeocoderURL(t *testing.T) {
	const stubURL = "http://localhost:8089/geocoder"
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
		wantLog bool
	}{
		{name: "unset is the Census Bureau", want: district.CensusGeocoderURL},
		{name: "a stub", value: stubURL, want: stubURL, wantLog: true},
		{name: "not a URL", value: "localhost:8089", wantErr: true},
		{name: "no host", value: "http:///geocoder", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setGeocoderEnv(t, tt.value)
			var logs bytes.Buffer
			got, err := censusGeocoderURL(slog.New(slog.NewJSONHandler(&logs, nil)))
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "CENSUS_GEOCODER_URL") {
					t.Fatalf("censusGeocoderURL() = %q, %v; want an error naming CENSUS_GEOCODER_URL", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("censusGeocoderURL() = %q, %v; want %q", got, err, tt.want)
			}
			if logged := strings.Contains(logs.String(), "CENSUS_GEOCODER_URL"); logged != tt.wantLog {
				t.Errorf("logged the override = %v, want %v: %s", logged, tt.wantLog, logs.String())
			}
		})
	}
}

// TestRepsUseConfiguredGeocoder: with CENSUS_GEOCODER_URL set, POST /reps
// geocodes against it, still pinned to the 119th's vintage and layer for the
// reps and to the 120th's for the election block.
func TestRepsUseConfiguredGeocoder(t *testing.T) {
	answers := map[string][]byte{}
	for vintage, name := range map[string]string{"ACS2025_Current": "austin_tx", "ACS2026_Current": "acs2026_austin_tx"} {
		b, err := os.ReadFile("../../internal/district/testdata/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		answers[vintage] = b
	}
	var (
		mu    sync.Mutex
		calls []*http.Request
	)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r)
		mu.Unlock()
		_, _ = w.Write(answers[r.URL.Query().Get("vintage")])
	}))
	t.Cleanup(stub.Close)
	setGeocoderEnv(t, stub.URL+"/geocoder")

	log := slog.New(slog.DiscardHandler)
	geocoder, err := censusGeocoderURL(log)
	if err != nil {
		t.Fatal(err)
	}
	h := handler.New(nil,
		handler.WithDistrict(district.NewCensusLookupAt(geocoder)),
		handler.WithCongresses(currentCongress{}),
		handler.WithMembers(texasMembers{}),
	)
	h.SetLogger(log)
	r := buildRouter(h, log, nil, devEdge(t, 0))

	rr := do(t, r, http.MethodPost, "/api/v1/reps", "", `{"address":"1100 Congress Ave, Austin, TX"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
	}
	var body struct {
		Reps     []model.Member `json:"reps"`
		Election struct {
			Districts []struct {
				State    string `json:"state"`
				District int    `json:"district"`
			} `json:"districts"`
			Changed bool `json:"changed"`
		} `json:"election"`
	}
	if err = json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Reps) != 1 || body.Reps[0].BioguideID != "D000399" {
		t.Errorf("reps = %+v, want TX-37's", body.Reps)
	}
	if e := body.Election; !e.Changed || len(e.Districts) != 1 || e.Districts[0].District != 10 {
		t.Errorf("election = %+v, want TX-10, changed", e)
	}
	checkGeocoderCalls(t, calls)
}

// TestRepsByCoordinatesUseConfiguredGeocoder: POST /reps with a point asks the coordinates
// endpoint next to CENSUS_GEOCODER_URL, pinned to each congress's vintage and layer, and answers
// as it does for an address.
func TestRepsByCoordinatesUseConfiguredGeocoder(t *testing.T) {
	answers := map[string][]byte{}
	for vintage, name := range map[string]string{
		"ACS2025_Current": "coordinates_austin_tx", "ACS2026_Current": "coordinates_acs2026_austin_tx",
	} {
		b, err := os.ReadFile("../../internal/district/testdata/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		answers[vintage] = b
	}
	var (
		mu    sync.Mutex
		paths []string
	)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Query().Get("x") != "-97.7431" || r.URL.Query().Get("y") != "30.2672" {
			http.Error(w, "want x=-97.7431 and y=30.2672", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(answers[r.URL.Query().Get("vintage")])
	}))
	t.Cleanup(stub.Close)
	setGeocoderEnv(t, stub.URL+"/geocoder/geographies/onelineaddress")

	log := slog.New(slog.DiscardHandler)
	geocoder, err := censusGeocoderURL(log)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	r := repsRouter(t, district.NewCensusLookupAt(geocoder), &logs)

	rr := do(t, r, http.MethodPost, "/api/v1/reps", "", `{"lat":30.2672,"lon":-97.7431}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
	}
	var body struct {
		Reps     []model.Member `json:"reps"`
		Election struct {
			Changed bool `json:"changed"`
		} `json:"election"`
	}
	if err = json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Reps) != 1 || body.Reps[0].BioguideID != "D000399" || !body.Election.Changed {
		t.Errorf("body = %+v, want TX-37's rep and a changed election district", body)
	}
	for _, p := range paths {
		if p != "/geocoder/geographies/coordinates" {
			t.Errorf("stub got %s, want the coordinates endpoint", p)
		}
	}
	if len(paths) != 2 {
		t.Errorf("stub got %d requests, want 2 (the 119th's map and the 120th's)", len(paths))
	}
	if strings.Contains(logs.String(), "97.74") || strings.Contains(logs.String(), "30.26") {
		t.Errorf("log contains the point: %s", logs.String())
	}
}

// checkGeocoderCalls asserts one request for each congress's map, both to the configured URL.
func checkGeocoderCalls(t *testing.T, calls []*http.Request) {
	t.Helper()
	if len(calls) != 2 {
		t.Fatalf("stub got %d requests, want 2 (the 119th's map and the 120th's)", len(calls))
	}
	want := map[string]string{
		"ACS2025_Current": "119th Congressional Districts",
		"ACS2026_Current": "120th Congressional Districts",
	}
	for _, c := range calls {
		q := c.URL.Query()
		layer, ok := want[q.Get("vintage")]
		if c.URL.Path != "/geocoder" || !ok || q.Get("layers") != layer {
			t.Errorf("stub request = %s, want /geocoder with a pinned vintage and its layer", c.URL)
		}
		delete(want, q.Get("vintage"))
	}
}
