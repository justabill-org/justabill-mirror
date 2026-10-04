package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/xmlparse"
)

const xmlTestdata = "../../internal/xmlparse/testdata"

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(xmlTestdata, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// withoutPositions returns rc with Positions cleared, and the position count.
func withoutPositions(rc *officialRollCall) (officialRollCall, int) {
	c := *rc
	c.Positions = nil
	return c, len(rc.Positions)
}

func TestParseHouseRoll(t *testing.T) {
	tests := []struct {
		file    string
		want    officialRollCall
		members int
		some    map[string]string
	}{
		{
			file: "house_2025_roll021_recorded.xml",
			want: officialRollCall{
				Chamber: chamberHouse, Congress: 119, Session: 1, Number: 21,
				Question: "On Agreeing to the Resolution", Result: "Passed", Date: "2025-01-22",
				Yeas: 213, Nays: 204, Present: 0, NotVoting: 16, BillID: "hres-119-53",
			},
			members: 3,
			some:    map[string]string{"A000370": "No", "A000055": "Aye", "B001298": "Not Voting"},
		},
		{
			file: "real_house_roll014.xml",
			want: officialRollCall{
				Chamber: chamberHouse, Congress: 119, Session: 1, Number: 14,
				Question: "On Motion to Suspend the Rules and Pass", Result: "Passed", Date: "2025-01-15",
				Yeas: 423, NotVoting: 10, BillID: "hr-119-144",
			},
			members: 10,
		},
		{
			// The Speaker election: no bill, candidates' names as positions.
			file: "house_2025_roll002_speaker.xml",
			want: officialRollCall{
				Chamber: chamberHouse, Congress: 119, Session: 1, Number: 2,
				Question: "Election of the Speaker", Result: "Johnson (LA)", Date: "2025-01-03",
			},
			members: 3,
			some:    map[string]string{"A000370": "Jeffries", "M001184": "Emmer"},
		},
		{
			file: "house_vote.xml",
			want: officialRollCall{
				Chamber: chamberHouse, Congress: 119, Session: 1, Number: 23,
				Question: "On Passage", Result: "Passed", Date: "2025-01-15",
				Yeas: 220, Nays: 210, Present: 1, NotVoting: 4,
			},
			members: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			rc, err := parseHouseRoll(readTestdata(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			got, members := withoutPositions(rc)
			if !reflect.DeepEqual(got, tt.want) || members != tt.members {
				t.Errorf("got %+v with %d members\nwant %+v with %d", got, members, tt.want, tt.members)
			}
			for id, vote := range tt.some {
				if rc.Positions[id] != vote {
					t.Errorf("position of %s = %q, want %q", id, rc.Positions[id], vote)
				}
			}
		})
	}
}

func TestParseSenateRoll(t *testing.T) {
	tests := []struct {
		file    string
		want    officialRollCall
		members int
	}{
		{
			// An amendment vote links to the measure it amends; empty <present/> is 0.
			file: "senate_vote_119_1_00072.xml",
			want: officialRollCall{
				Chamber: chamberSenate, Congress: 119, Session: 1, Number: 72,
				Question: "On the Amendment S.Amdt. 407 to S.Con.Res. 7 (No short title on file)",
				Result:   "Amendment Rejected (49-51)", Date: "2025-02-20",
				Yeas: 49, Nays: 51, BillID: "sconres-119-7",
			},
			members: 3,
		},
		{
			file: "senate_vote_119_1_00372.xml",
			want: officialRollCall{
				Chamber: chamberSenate, Congress: 119, Session: 1, Number: 372,
				Question: "On Passage of the Bill H.R. 1",
				Result:   "Bill Passed (50-50, Vice President of the United States, voted Yea)", Date: "2025-07-01",
				Yeas: 50, Nays: 50, BillID: "hr-119-1",
			},
			members: 4,
		},
		{
			// A nomination has no bill.
			file: "senate_vote_119_1_00607.xml",
			want: officialRollCall{
				Chamber: chamberSenate, Congress: 119, Session: 1, Number: 607,
				Question: "On the Nomination PN25-37", Result: "Nomination Confirmed (57-43)", Date: "2025-11-05",
				Yeas: 57, Nays: 43,
			},
			members: 3,
		},
		{
			file: "senate_vote_119_1_00616.xml",
			want: officialRollCall{
				Chamber: chamberSenate, Congress: 119, Session: 1, Number: 616,
				Question: "On the Amendment S.Amdt. 3937 to H.R. 5371 (No short title on file)",
				Result:   "Amendment Agreed to (60-40)", Date: "2025-11-10",
				Yeas: 60, Nays: 40, BillID: "hr-119-5371",
			},
			members: 3,
		},
		{
			// No <document> at all.
			file: "real_senate_vote001.xml",
			want: officialRollCall{
				Chamber: chamberSenate, Congress: 119, Session: 1, Number: 1,
				Question: "On Cloture on the Motion to Proceed S. 5",
				Result:   "Cloture on the Motion to Proceed Agreed to", Date: "2025-01-09",
				Yeas: 84, Nays: 9, NotVoting: 6,
			},
			members: 10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			rc, err := parseSenateRoll(readTestdata(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			got, members := withoutPositions(rc)
			if !reflect.DeepEqual(got, tt.want) || members != tt.members {
				t.Errorf("got %+v with %d members\nwant %+v with %d", got, members, tt.want, tt.members)
			}
		})
	}
	rc, err := parseSenateRoll(readTestdata(t, "senate_vote_119_1_00072.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Positions["S428"] != "Yea" || rc.Positions["S429"] != "Nay" {
		t.Errorf("positions = %v, want S428 Yea and S429 Nay", rc.Positions)
	}
}

// bothParsers parses a testdata file with the independent parser and with internal/xmlparse, and
// returns ours plus xmlparse's totals (yea, nay, present, not voting) and positions.
func bothParsers(t *testing.T, name string) (*officialRollCall, [4]int, map[string]string) {
	t.Helper()
	data := readTestdata(t, name)
	var (
		ours   *officialRollCall
		err    error
		totals [4]int
		votes  []xmlparse.IndividualVote
	)
	if strings.Contains(name, "senate") {
		theirs, pErr := xmlparse.ParseSenateVote(data)
		if pErr != nil {
			t.Fatalf("%s: xmlparse: %v", name, pErr)
		}
		ours, err = parseSenateRoll(data)
		totals = [4]int{theirs.YeaTotal, theirs.NayTotal, theirs.PresentTotal, theirs.AbsentTotal}
		votes = theirs.Votes
	} else {
		theirs, pErr := xmlparse.ParseHouseVote(data)
		if pErr != nil {
			t.Fatalf("%s: xmlparse: %v", name, pErr)
		}
		ours, err = parseHouseRoll(data)
		totals = [4]int{theirs.YeaTotal, theirs.NayTotal, theirs.PresentTotal, theirs.NotVotingTotal}
		votes = theirs.Votes
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	positions := map[string]string{}
	for _, v := range votes {
		if v.MemberID != "" {
			positions[v.MemberID] = v.Vote
		}
	}
	return ours, totals, positions
}

// The independent parser and internal/xmlparse must read every testdata file the same way;
// a disagreement is a bug in one of them.
func TestParsersAgree(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(xmlTestdata, "*.xml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no testdata: %v", err)
	}
	for _, path := range files {
		name := filepath.Base(path)
		ours, totals, positions := bothParsers(t, name)
		if got := [4]int{ours.Yeas, ours.Nays, ours.Present, ours.NotVoting}; got != totals {
			t.Errorf("%s: totals %v, xmlparse %v", name, got, totals)
		}
		if !reflect.DeepEqual(ours.Positions, positions) {
			t.Errorf("%s: positions %v, xmlparse %v", name, ours.Positions, positions)
		}
	}
}

func TestParseRollErrors(t *testing.T) {
	tests := []struct {
		name  string
		parse func([]byte) (*officialRollCall, error)
		data  string
		want  string
	}{
		{"house not XML", parseHouseRoll, "<html>", "house roll XML"},
		{"house wrong root", parseHouseRoll, "<roll_call_vote/>", "house roll XML"},
		{
			"house bad total",
			parseHouseRoll,
			`<rollcall-vote><vote-metadata><congress>119</congress><session>1st</session><rollcall-num>1</rollcall-num>` +
				`<action-date>3-Jan-2025</action-date><vote-totals><totals-by-vote><yea-total>many</yea-total>` +
				`</totals-by-vote></vote-totals></vote-metadata></rollcall-vote>`,
			"yea-total",
		},
		{"house bad session", parseHouseRoll,
			`<rollcall-vote><vote-metadata><session>first</session><action-date>3-Jan-2025</action-date>` +
				`</vote-metadata></rollcall-vote>`, "session"},
		{"house bad date", parseHouseRoll,
			`<rollcall-vote><vote-metadata><session>1st</session><action-date>Jan 3</action-date>` +
				`</vote-metadata></rollcall-vote>`, "action-date"},
		{"senate wrong root", parseSenateRoll, "<rollcall-vote/>", "senate roll XML"},
		{"senate bad date", parseSenateRoll,
			`<roll_call_vote><vote_date>soon</vote_date></roll_call_vote>`, "vote_date"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.parse([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}

func TestParseSenateMenu(t *testing.T) {
	got, err := parseSenateMenu([]byte(`<vote_summary><votes>
		<vote><vote_number>00244</vote_number></vote><vote><vote_number> 00002 </vote_number></vote>
		</votes></vote_summary>`))
	if err != nil || !slices.Equal(got, []int{244, 2}) {
		t.Errorf("got %v, %v; want [244 2]", got, err)
	}
	if _, err = parseSenateMenu([]byte(`<vote_summary><votes><vote><vote_number>x</vote_number></vote>` +
		`</votes></vote_summary>`)); err == nil {
		t.Error("bad vote_number: want an error")
	}
	if _, err = parseSenateMenu([]byte(`<html/>`)); err == nil {
		t.Error("wrong root: want an error")
	}
}

func TestParseHouseIndex(t *testing.T) {
	page := `<a href="/Votes/2026314?rollnumber=314">314</a> <a href="x?rollnumber=9">9</a>` +
		`<a href="x?rollnumber=313">313</a>`
	if got := parseHouseIndex([]byte(page)); got != 314 {
		t.Errorf("got %d, want 314", got)
	}
	if got := parseHouseIndex([]byte("no votes")); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestMeasureID(t *testing.T) {
	tests := []struct {
		congress int
		ref      string
		want     string
	}{
		{119, "H R 144", "hr-119-144"},
		{119, "H.R. 1", "hr-119-1"},
		{119, "S.Con.Res. 7", "sconres-119-7"},
		{119, "H CON RES 14", "hconres-119-14"},
		{119, "S.J.Res. 82", "sjres-119-82"},
		{119, "H RES 53", "hres-119-53"},
		{118, "S. 5", "s-118-5"},
		{119, "PN25-37", ""},
		{119, "S.Amdt. 3937", ""},
		{119, "QUORUM", ""},
		{119, "", ""},
		{119, "H R 0", ""},
		{0, "H R 1", ""},
	}
	for _, tt := range tests {
		if got := measureID(tt.congress, tt.ref); got != tt.want {
			t.Errorf("measureID(%d, %q) = %q, want %q", tt.congress, tt.ref, got, tt.want)
		}
	}
}
