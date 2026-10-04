package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
)

// The chambers, as congressional_votes.chamber stores them.
const (
	chamberHouse  = "House"
	chamberSenate = "Senate"
)

// officialRollCall is a roll call as the clerk's XML records it, reduced to what spotcheck
// compares. Positions maps each member's official ID to the position the clerk wrote: bioguide
// IDs in the House, LIS IDs in the Senate.
type officialRollCall struct {
	Chamber   string
	Congress  int
	Session   int
	Number    int
	Question  string
	Result    string
	Date      string // YYYY-MM-DD
	Yeas      int
	Nays      int
	Present   int
	NotVoting int
	BillID    string // empty when the roll call isn't on a bill or resolution
	Positions map[string]string
}

// The official XML is parsed with these structs rather than internal/xmlparse, on purpose: a
// bug in the pipeline's parser mustn't be able to confirm itself (design 77).
type houseRollXML struct {
	XMLName   xml.Name `xml:"rollcall-vote"`
	Congress  string   `xml:"vote-metadata>congress"`
	Session   string   `xml:"vote-metadata>session"`
	Roll      string   `xml:"vote-metadata>rollcall-num"`
	LegisNum  string   `xml:"vote-metadata>legis-num"`
	Question  string   `xml:"vote-metadata>vote-question"`
	Result    string   `xml:"vote-metadata>vote-result"`
	Date      string   `xml:"vote-metadata>action-date"`
	Yeas      string   `xml:"vote-metadata>vote-totals>totals-by-vote>yea-total"`
	Nays      string   `xml:"vote-metadata>vote-totals>totals-by-vote>nay-total"`
	Present   string   `xml:"vote-metadata>vote-totals>totals-by-vote>present-total"`
	NotVoting string   `xml:"vote-metadata>vote-totals>totals-by-vote>not-voting-total"`
	Votes     []struct {
		Legislator struct {
			ID string `xml:"name-id,attr"`
		} `xml:"legislator"`
		Vote string `xml:"vote"`
	} `xml:"vote-data>recorded-vote"`
}

type senateRollXML struct {
	XMLName     xml.Name `xml:"roll_call_vote"`
	Congress    string   `xml:"congress"`
	Session     string   `xml:"session"`
	Number      string   `xml:"vote_number"`
	Date        string   `xml:"vote_date"`
	Question    string   `xml:"vote_question_text"`
	Result      string   `xml:"vote_result_text"`
	DocCongress string   `xml:"document>document_congress"`
	DocType     string   `xml:"document>document_type"`
	DocNumber   string   `xml:"document>document_number"`
	AmendsDoc   string   `xml:"amendment>amendment_to_document_number"`
	Yeas        string   `xml:"count>yeas"`
	Nays        string   `xml:"count>nays"`
	Present     string   `xml:"count>present"`
	Absent      string   `xml:"count>absent"`
	Members     []struct {
		LISID string `xml:"lis_member_id"`
		Vote  string `xml:"vote_cast"`
	} `xml:"members>member"`
}

// senateMenuXML is the Senate's list of a session's roll calls.
type senateMenuXML struct {
	XMLName xml.Name `xml:"vote_summary"`
	Numbers []string `xml:"votes>vote>vote_number"`
}

const (
	houseDateLayout  = "2-Jan-2006"
	senateDateLayout = "January 2, 2006"
	isoDate          = "2006-01-02"
)

// parseHouseRoll parses a clerk.house.gov roll-call XML file.
func parseHouseRoll(data []byte) (*officialRollCall, error) {
	var doc houseRollXML
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("house roll XML: %w", err)
	}
	p := numParser{}
	rc := &officialRollCall{
		Chamber:   chamberHouse,
		Congress:  p.int("congress", doc.Congress),
		Number:    p.int("rollcall-num", doc.Roll),
		Question:  squash(doc.Question),
		Result:    squash(doc.Result),
		Yeas:      p.int("yea-total", doc.Yeas),
		Nays:      p.int("nay-total", doc.Nays),
		Present:   p.int("present-total", doc.Present),
		NotVoting: p.int("not-voting-total", doc.NotVoting),
		Positions: make(map[string]string, len(doc.Votes)),
	}
	session, err := rollcall.ParseOrdinalSession(doc.Session)
	if err != nil {
		p.errs = append(p.errs, err)
	}
	rc.Session = session
	rc.Date = p.date("action-date", houseDateLayout, squash(doc.Date))
	if err = p.err(); err != nil {
		return nil, err
	}
	rc.BillID = measureID(rc.Congress, doc.LegisNum)
	for _, v := range doc.Votes {
		if id := strings.TrimSpace(v.Legislator.ID); id != "" {
			rc.Positions[id] = v.Vote
		}
	}
	return rc, nil
}

// parseSenateRoll parses a senate.gov roll-call XML file. The linked bill is the measure voted
// on, or for an amendment the measure it amends; nominations and treaties have none.
func parseSenateRoll(data []byte) (*officialRollCall, error) {
	var doc senateRollXML
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("senate roll XML: %w", err)
	}
	p := numParser{}
	rc := &officialRollCall{
		Chamber:   chamberSenate,
		Congress:  p.int("congress", doc.Congress),
		Session:   p.int("session", doc.Session),
		Number:    p.int("vote_number", doc.Number),
		Question:  squash(doc.Question),
		Result:    squash(doc.Result),
		Yeas:      p.int("yeas", doc.Yeas),
		Nays:      p.int("nays", doc.Nays),
		Present:   p.int("present", doc.Present),
		NotVoting: p.int("absent", doc.Absent),
		Positions: make(map[string]string, len(doc.Members)),
	}
	// "July 1, 2025,  11:56 AM": the date is what comes before the second comma.
	date := squash(doc.Date)
	if i := strings.LastIndex(date, ","); i > 0 {
		date = date[:i]
	}
	rc.Date = p.date("vote_date", senateDateLayout, date)
	if err := p.err(); err != nil {
		return nil, err
	}

	docCongress := rc.Congress
	if c, err := strconv.Atoi(strings.TrimSpace(doc.DocCongress)); err == nil && c > 0 {
		docCongress = c
	}
	rc.BillID = measureID(docCongress, doc.DocType+" "+doc.DocNumber)
	if rc.BillID == "" && isAmendment(doc.DocType) {
		rc.BillID = measureID(docCongress, doc.AmendsDoc)
	}
	for _, m := range doc.Members {
		if id := strings.TrimSpace(m.LISID); id != "" {
			rc.Positions[id] = m.Vote
		}
	}
	return rc, nil
}

// parseSenateMenu returns the roll-call numbers on a session's vote menu.
func parseSenateMenu(data []byte) ([]int, error) {
	var doc senateMenuXML
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("senate vote menu XML: %w", err)
	}
	nums := make([]int, 0, len(doc.Numbers))
	for _, s := range doc.Numbers {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("senate vote menu: vote_number %q: %w", s, err)
		}
		nums = append(nums, n)
	}
	return nums, nil
}

// houseIndexRollRe matches the roll links on the clerk's index page for a year.
var houseIndexRollRe = regexp.MustCompile(`rollnumber=(\d+)`)

// parseHouseIndex returns the highest roll number on the clerk's index page. Roll numbers run
// from 1 with no gaps, so it's also the year's roll-call count.
func parseHouseIndex(page []byte) int {
	highest := 0
	for _, m := range houseIndexRollRe.FindAllSubmatch(page, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil && n > highest {
			highest = n
		}
	}
	return highest
}

// measureRe splits a compacted reference ("hr144", "sconres7") into type and number.
var measureRe = regexp.MustCompile(`^(hr|s|hjres|sjres|hconres|sconres|hres|sres)(\d+)$`)

// measureID turns a bill or resolution reference in either chamber's spelling ("H R 144",
// "H.R. 1", "S.Con.Res. 7") into a bill ID such as hr-119-144. Anything else (a nomination,
// an amendment, "QUORUM", nothing) gives "".
func measureID(congress int, ref string) string {
	m := measureRe.FindStringSubmatch(compact(ref))
	if m == nil || congress <= 0 {
		return ""
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || n <= 0 {
		return ""
	}
	return fmt.Sprintf("%s-%d-%d", m[1], congress, n)
}

func isAmendment(docType string) bool {
	switch compact(docType) {
	case "samdt", "hamdt":
		return true
	default:
		return false
	}
}

// compact lowercases s and drops dots and whitespace: "S.Con.Res. 7" becomes "sconres7".
func compact(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(s, ".", "")), ""))
}

// squash trims s and collapses runs of whitespace to one space.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// numParser parses the XML's number and date fields, collecting every error. An empty number
// field (the Senate writes <present/> for none) is 0.
type numParser struct{ errs []error }

func (p *numParser) int(field, s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: %w", field, err))
	}
	return n
}

func (p *numParser) date(field, layout, s string) string {
	t, err := time.Parse(layout, s)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: %w", field, err))
		return ""
	}
	return t.Format(isoDate)
}

func (p *numParser) err() error {
	if len(p.errs) == 0 {
		return nil
	}
	return fmt.Errorf("official XML: %w", errors.Join(p.errs...))
}
