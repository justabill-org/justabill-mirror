package handler_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/justabill-org/justabill/api/internal/district"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
)

const austin = "1100 Congress Ave, Austin, TX 78701"

type electionBody struct {
	Congress     int    `json:"congress"`
	ElectionDate string `json:"election_date"`
	Districts    []struct {
		State    string `json:"state"`
		District int    `json:"district"`
		AtLarge  bool   `json:"at_large"`
		Congress int    `json:"congress"`
		Source   string `json:"source"`
	} `json:"districts"`
	Changed bool `json:"changed"`
}

// repsReply is a FindReps response: its status, its JSON keys, and what the handler logged.
type repsReply struct {
	code     int
	keys     map[string]json.RawMessage
	election *electionBody
	logs     string
}

// callReps runs FindReps for the Texas Capitol with a JSON log capture.
func callReps(t *testing.T, h *handler.Handler) repsReply {
	t.Helper()
	var logs bytes.Buffer
	h.SetLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	w := httptest.NewRecorder()
	h.FindReps(w, repsPost(t, austin))

	reply := repsReply{code: w.Code}
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &reply.keys); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if raw, ok := reply.keys["election"]; ok {
			reply.election = &electionBody{}
			if err := json.Unmarshal(raw, reply.election); err != nil {
				t.Fatalf("decoding election: %v", err)
			}
		}
	}
	reply.logs = logs.String()
	return reply
}

// checkOutcome asserts the reps lookup log line's election_district and that no line carries
// the address.
func checkOutcome(t *testing.T, r repsReply, want string) {
	t.Helper()
	if !strings.Contains(r.logs, `"election_district":"`+want+`"`) {
		t.Errorf("want election_district %q in the log, got %s", want, r.logs)
	}
	for _, part := range []string{"Congress Ave", "Austin", "78701"} {
		if strings.Contains(r.logs, part) {
			t.Errorf("log contains %q from the address: %s", part, r.logs)
		}
	}
}

func tx(n int) district.Result {
	return district.Result{State: "TX", District: n, Source: district.SourceGeocoder}
}

// The Texas Capitol is TX-37 in the 119th's map and TX-10 in the 120th's: the reps come from
// today's district and the election block names the new one.
func TestFindReps_ElectionChanged(t *testing.T) {
	dl := &mockDistrictLookup{byCongress: map[int]lookupAnswer{
		119: {results: []district.Result{tx(37)}},
		120: {results: []district.Result{tx(10)}},
	}}
	members := &mockMemberRepoForReps{districtMembers: map[string][]model.Member{
		"TX-37": {{BioguideID: "C001131", LastName: "Casar"}},
		"TX-10": {{BioguideID: "M001157", LastName: "McCaul"}},
	}}
	h := newRepsHandler(dl, members)

	r := callReps(t, h)
	if r.code != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.code)
	}
	if got := dl.congresses(); !slices.Equal(got, []int{119, 120}) {
		t.Errorf("looked up congresses %v, want [119 120]", got)
	}
	var reps []model.Member
	_ = json.Unmarshal(r.keys["reps"], &reps)
	if len(reps) != 1 || reps[0].BioguideID != "C001131" {
		t.Errorf("reps = %+v, want only TX-37's member", reps)
	}
	e := r.election
	if e == nil {
		t.Fatalf("no election block in %v", r.keys)
	}
	if e.Congress != 120 || e.ElectionDate != "2026-11-03" || !e.Changed {
		t.Errorf("election = %+v, want congress 120, 2026-11-03, changed", e)
	}
	if len(e.Districts) != 1 {
		t.Fatalf("election districts = %+v, want TX-10", e.Districts)
	}
	if d := e.Districts[0]; d.State != "TX" || d.District != 10 || d.AtLarge || d.Congress != 120 ||
		d.Source != "geocoder" {
		t.Errorf("election district = %+v, want TX-10 in the 120th", d)
	}
	checkOutcome(t, r, "changed")
}

// changed compares sets of (state, district) pairs, and makes no claim when either is empty.
func TestFindReps_ElectionChangedComparesSets(t *testing.T) {
	tests := []struct {
		name          string
		current, next []district.Result
		want          bool
		outcome       string
	}{
		{"same district", []district.Result{tx(7)}, []district.Result{tx(7)}, false, "same"},
		{"same set in another order", []district.Result{tx(10), tx(21)}, []district.Result{tx(21), tx(10)},
			false, "same"},
		{"a district added", []district.Result{tx(10)}, []district.Result{tx(10), tx(21)}, true, "changed"},
		{"same count, other districts", []district.Result{tx(10), tx(21)}, []district.Result{tx(10), tx(35)},
			true, "changed"},
		{"current map matched nothing", nil, []district.Result{tx(10)}, false, "same"},
		{"source differs, seat doesn't",
			[]district.Result{{State: "WY", AtLarge: true, Source: district.SourceGeocoder}},
			[]district.Result{{State: "WY", AtLarge: true, Source: district.SourceState}}, false, "same"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dl := &mockDistrictLookup{byCongress: map[int]lookupAnswer{
				119: {results: tt.current},
				120: {results: tt.next},
			}}
			r := callReps(t, newRepsHandler(dl, &mockMemberRepoForReps{}))
			if r.code != http.StatusOK || r.election == nil {
				t.Fatalf("status = %d, election = %v; want 200 with an election block", r.code, r.election)
			}
			if r.election.Changed != tt.want {
				t.Errorf("changed = %v, want %v", r.election.Changed, tt.want)
			}
			checkOutcome(t, r, tt.outcome)
		})
	}
}

// Whatever goes wrong with the next map, the response is the usual 200 without an election key.
func TestFindReps_ElectionMissingOrFailed(t *testing.T) {
	tests := []struct {
		name    string
		next    lookupAnswer
		outcome string
	}{
		{"lookup error", lookupAnswer{err: errors.New("census geocoder returned status 500")}, "error"},
		{"unexpected layer", lookupAnswer{err: district.ErrUnexpectedLayer}, "error"},
		{"no match", lookupAnswer{results: []district.Result{}}, "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dl := &mockDistrictLookup{byCongress: map[int]lookupAnswer{
				119: {results: []district.Result{tx(37)}},
				120: tt.next,
			}}
			r := callReps(t, newRepsHandler(dl, &mockMemberRepoForReps{}))
			if r.code != http.StatusOK {
				t.Fatalf("status = %d, want 200", r.code)
			}
			if _, ok := r.keys["election"]; ok {
				t.Errorf("response has an election key: %s", r.keys["election"])
			}
			if _, ok := r.keys["districts"]; !ok {
				t.Errorf("response lost its districts: %v", r.keys)
			}
			checkOutcome(t, r, tt.outcome)
		})
	}
}

// A next-map lookup still running when the 2-second grace runs out is cancelled, and its
// goroutine ends: synctest fails the test if any goroutine is left blocked.
func TestFindReps_ElectionTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dl := &mockDistrictLookup{byCongress: map[int]lookupAnswer{
			119: {results: []district.Result{tx(37)}},
			120: {block: true},
		}}

		start := time.Now()
		r := callReps(t, newRepsHandler(dl, &mockMemberRepoForReps{}))
		if r.code != http.StatusOK {
			t.Fatalf("status = %d, want 200", r.code)
		}
		if waited := time.Since(start); waited != 2*time.Second {
			t.Errorf("FindReps waited %s for the next map, want the 2s grace", waited)
		}
		if _, ok := r.keys["election"]; ok {
			t.Errorf("response has an election key: %s", r.keys["election"])
		}
		checkOutcome(t, r, "timeout")
		synctest.Wait()
	})
}

// With no map for the next congress (the 120th is current), only the current map is asked.
func TestFindReps_NoNextMap(t *testing.T) {
	dl := &mockDistrictLookup{results: []district.Result{tx(10)}}
	h := newRepsHandler(dl, &mockMemberRepoForReps{})
	h.Congresses = &listCongressRepo{congresses: []model.Congress{{Number: 120, IsCurrent: true}, {Number: 119}}}

	r := callReps(t, h)
	if r.code != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.code)
	}
	if got := dl.congresses(); !slices.Equal(got, []int{120}) {
		t.Errorf("looked up congresses %v, want only the current 120", got)
	}
	if _, ok := r.keys["election"]; ok {
		t.Errorf("response has an election key: %s", r.keys["election"])
	}
	checkOutcome(t, r, "none")
}

// A failed current lookup is still a 502, and the next lookup is cancelled rather than left
// running (synctest fails the test if its goroutine is left blocked).
func TestFindReps_CurrentLookupErrorCancelsNext(t *testing.T) {
	for name, next := range map[string]lookupAnswer{
		"next answers": {results: []district.Result{tx(10)}},
		"next blocks":  {block: true},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dl := &mockDistrictLookup{
					byCongress: map[int]lookupAnswer{119: {err: errors.New("census API down")}, 120: next},
				}
				start := time.Now()
				r := callReps(t, newRepsHandler(dl, &mockMemberRepoForReps{}))
				if r.code != http.StatusBadGateway {
					t.Errorf("status = %d, want 502", r.code)
				}
				if waited := time.Since(start); waited != 0 {
					t.Errorf("FindReps waited %s for the next map after the current one failed", waited)
				}
				synctest.Wait()
			})
		})
	}
}
