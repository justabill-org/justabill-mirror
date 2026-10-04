package handler_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/api/internal/district"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
)

// whiteHouse is a point in DC, the body POST /reps gets from "use my location" there.
const whiteHouse = `{"lat": 38.8977, "lon": -77.0365}`

// postReps calls FindReps with a raw JSON body, logging to logs.
func postReps(t *testing.T, h *handler.Handler, body string, logs *bytes.Buffer) *httptest.ResponseRecorder {
	t.Helper()
	h.SetLogger(slog.New(slog.NewJSONHandler(logs, nil)))
	w := httptest.NewRecorder()
	h.FindReps(w, httptest.NewRequest(http.MethodPost, "/api/v1/reps", strings.NewReader(body)))
	return w
}

// dcAtLarge is DC's delegate district as the geocoder reports it.
func dcAtLarge() district.Result {
	return district.Result{State: "DC", District: 0, AtLarge: true, Source: district.SourceGeocoder}
}

// A point gets the same response an address does: DC's delegate, an at-large district, and the
// election block from the next congress's map, which is looked up by the same point.
func TestFindReps_Coordinates(t *testing.T) {
	dl := &mockDistrictLookup{results: []district.Result{dcAtLarge()}}
	members := &mockMemberRepoForReps{districtMembers: map[string][]model.Member{
		"DC-0": {{BioguideID: "N000147", LastName: "Norton"}},
	}}
	h := newRepsHandler(dl, members)

	var logs bytes.Buffer
	w := postReps(t, h, whiteHouse, &logs)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	var body struct {
		repsBody

		Election *electionBody `json:"election"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Reps) != 1 || body.Reps[0].BioguideID != "N000147" {
		t.Errorf("reps = %+v, want DC's delegate", body.Reps)
	}
	if body.Senators == nil || len(body.Senators) != 0 {
		t.Errorf("senators = %v, want an empty list", body.Senators)
	}
	if len(body.Districts) != 1 {
		t.Fatalf("districts = %+v, want DC at large", body.Districts)
	}
	if d := body.Districts[0]; d.State != "DC" || d.District != 0 || !d.AtLarge || d.Congress != 119 {
		t.Errorf("district = %+v, want DC-0 at large in the 119th", d)
	}
	if e := body.Election; e == nil || e.Congress != 120 || e.Changed || len(e.Districts) != 1 {
		t.Errorf("election = %+v, want the 120th's DC-0, unchanged", e)
	}
	if got := dl.congresses(); !slices.Equal(got, []int{119, 120}) {
		t.Errorf("looked up congresses %v, want [119 120]", got)
	}
	want := point{38.8977, -77.0365}
	if got := dl.pointsAsked(); len(got) != 2 || got[0] != want || got[1] != want {
		t.Errorf("points looked up = %v, want %v for both maps", got, want)
	}
}

// The log line says a point was used and nothing more: neither number reaches the log or the
// response.
func TestFindReps_CoordinatesNeverLoggedOrEchoed(t *testing.T) {
	dl := &mockDistrictLookup{results: []district.Result{dcAtLarge()}}
	h := newRepsHandler(dl, &mockMemberRepoForReps{})

	var logs bytes.Buffer
	w := postReps(t, h, whiteHouse, &logs)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"reps lookup","input":"coordinates"`) {
		t.Errorf("want input=coordinates on the reps lookup line, got %s", out)
	}
	for _, part := range []string{"38.8977", "77.0365", "38.89", "77.03"} {
		if strings.Contains(out, part) {
			t.Errorf("log contains %q from the point: %s", part, out)
		}
		if strings.Contains(w.Body.String(), part) {
			t.Errorf("response contains %q from the point: %s", part, w.Body)
		}
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"lat", "lon"} {
		if _, ok := keys[k]; ok {
			t.Errorf("response has a %s field: %s", k, w.Body)
		}
	}
}

// An address-only body logs input=address and never asks for a point.
func TestFindReps_AddressLogsInput(t *testing.T) {
	dl := &mockDistrictLookup{results: []district.Result{tx(37)}}
	h := newRepsHandler(dl, &mockMemberRepoForReps{})

	var logs bytes.Buffer
	if w := postReps(t, h, `{"address":"`+austin+`"}`, &logs); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(logs.String(), `"msg":"reps lookup","input":"address"`) {
		t.Errorf("want input=address on the reps lookup line, got %s", logs.String())
	}
	if got := dl.pointsAsked(); len(got) != 0 {
		t.Errorf("looked up points %v for an address", got)
	}
}

// A point in no district (open ocean, another country) gets empty lists, as an unmatched
// address does.
func TestFindReps_CoordinatesNoMatch(t *testing.T) {
	h := newRepsHandler(&mockDistrictLookup{}, &mockMemberRepoForReps{})

	var logs bytes.Buffer
	w := postReps(t, h, `{"lat": 30, "lon": -40}`, &logs)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		repsBody

		Election *electionBody `json:"election"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Reps == nil || body.Senators == nil || body.Districts == nil ||
		len(body.Reps)+len(body.Senators)+len(body.Districts) != 0 {
		t.Errorf("got %s, want empty reps, senators and districts", w.Body)
	}
	if body.Election != nil {
		t.Errorf("election = %+v, want none", body.Election)
	}
	if !strings.Contains(logs.String(), `"district_source":"none"`) {
		t.Errorf("want district_source none in the log, got %s", logs.String())
	}
}

// Half a point, a point out of range, both an address and a point, or neither is a 400 with a
// plain message that never reaches the geocoder.
func TestFindReps_BadPlace(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"neither", `{}`, "address or lat and lon is required"},
		{"blank address", `{"address":"  "}`, "address or lat and lon is required"},
		{"null point", `{"lat":null,"lon":null}`, "address or lat and lon is required"},
		{"address and point", `{"address":"` + austin + `","lat":30.27,"lon":-97.74}`,
			"send address or lat and lon, not both"},
		{"address and lat", `{"address":"` + austin + `","lat":30.27}`, "send address or lat and lon, not both"},
		{"only lat", `{"lat":30.27}`, "lat and lon are both required"},
		{"only lon", `{"lon":-97.74}`, "lat and lon are both required"},
		{"lat under -90", `{"lat":-90.5,"lon":0}`, "lat must be from -90 to 90"},
		{"lat over 90", `{"lat":91,"lon":0}`, "lat must be from -90 to 90"},
		{"lon under -180", `{"lat":0,"lon":-180.01}`, "lon must be from -180 to 180"},
		{"lon over 180", `{"lat":0,"lon":181}`, "lon must be from -180 to 180"},
		{"lat not a number", `{"lat":"38.9","lon":-77}`, "invalid request body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dl := &mockDistrictLookup{}
			h := newRepsHandler(dl, &mockMemberRepoForReps{})

			w := postReps(t, h, tt.body, &bytes.Buffer{})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error != tt.want {
				t.Errorf("error = %q, want %q", body.Error, tt.want)
			}
			if got := dl.congresses(); len(got) != 0 {
				t.Errorf("looked up congresses %v for a bad body", got)
			}
		})
	}
}

// The edges of the ranges, and a blank address beside a point, are accepted.
func TestFindReps_CoordinateEdges(t *testing.T) {
	for _, body := range []string{
		`{"lat":90,"lon":180}`,
		`{"lat":-90,"lon":-180}`,
		`{"lat":0,"lon":0}`,
		`{"address":"","lat":38.8977,"lon":-77.0365}`,
	} {
		h := newRepsHandler(&mockDistrictLookup{}, &mockMemberRepoForReps{})
		if w := postReps(t, h, body, &bytes.Buffer{}); w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200: %s", body, w.Code, w.Body)
		}
	}
}
