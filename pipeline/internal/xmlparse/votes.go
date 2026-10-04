// Package xmlparse parses the House Clerk's and the Senate's roll-call vote XML into each
// member's vote and the measure the vote was on.
package xmlparse

import (
	"encoding/xml"
	"strconv"
	"strings"
)

// IndividualVote represents a single legislator's vote.
type IndividualVote struct {
	MemberID string `json:"member_id"`
	Name     string `json:"name"`
	// FirstName and LastName are the Senate XML's separate name fields; House votes leave
	// them empty.
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Party     string `json:"party"`
	State     string `json:"state"`
	Vote      string `json:"vote"`
}

// HouseVoteResult holds parsed data from a House roll call vote XML.
type HouseVoteResult struct {
	Congress       int              `json:"congress"`
	Session        string           `json:"session"`
	RollNumber     int              `json:"roll_number"`
	Question       string           `json:"question"`
	Result         string           `json:"result"`
	VoteDate       string           `json:"vote_date"`
	LegisNum       string           `json:"legis_num"`
	VoteType       string           `json:"vote_type"`
	VoteDesc       string           `json:"vote_desc"`
	YeaTotal       int              `json:"yea_total"`
	NayTotal       int              `json:"nay_total"`
	PresentTotal   int              `json:"present_total"`
	NotVotingTotal int              `json:"not_voting_total"`
	Votes          []IndividualVote `json:"votes"`
}

// SenateVoteResult holds parsed data from a Senate roll call vote XML.
type SenateVoteResult struct {
	Congress   int            `json:"congress"`
	Session    int            `json:"session"`
	VoteNumber int            `json:"vote_number"`
	Question   string         `json:"question"`
	Result     string         `json:"result"`
	VoteDate   string         `json:"vote_date"`
	VoteTitle  string         `json:"vote_title"`
	Document   SenateDocument `json:"document"`
	// AmendmentToDocument is the measure an amendment vote amends, e.g. "H.R. 5371"
	// (from <amendment_to_document_number>). Empty unless Document.Type is an amendment.
	AmendmentToDocument string           `json:"amendment_to_document"`
	YeaTotal            int              `json:"yea_total"`
	NayTotal            int              `json:"nay_total"`
	PresentTotal        int              `json:"present_total"`
	AbsentTotal         int              `json:"absent_total"`
	Votes               []IndividualVote `json:"votes"`
}

// SenateDocument is the matter a Senate roll call is about: a measure ("H.R." "1"), an
// amendment ("S.Amdt.", number empty) or a nomination ("PN" "25-37").
type SenateDocument struct {
	Congress int    `json:"congress"`
	Type     string `json:"type"`
	Number   string `json:"number"`
}

type xmlHouseRollCall struct {
	XMLName  xml.Name         `xml:"rollcall-vote"`
	Metadata xmlHouseMetadata `xml:"vote-metadata"`
	VoteData xmlHouseVoteData `xml:"vote-data"`
}

type xmlHouseMetadata struct {
	Congress   string             `xml:"congress"`
	Session    string             `xml:"session"`
	RollNum    string             `xml:"rollcall-num"`
	Question   string             `xml:"vote-question"`
	VoteResult string             `xml:"vote-result"`
	ActionDate string             `xml:"action-date"`
	LegisNum   string             `xml:"legis-num"`
	VoteType   string             `xml:"vote-type"`
	VoteDesc   string             `xml:"vote-desc"`
	VoteTotals xmlHouseVoteTotals `xml:"vote-totals"`
}

type xmlHouseVoteTotals struct {
	TotalsByVote xmlHouseTotalsByVote `xml:"totals-by-vote"`
}

type xmlHouseTotalsByVote struct {
	YeaTotal       int `xml:"yea-total"`
	NayTotal       int `xml:"nay-total"`
	PresentTotal   int `xml:"present-total"`
	NotVotingTotal int `xml:"not-voting-total"`
}

type xmlHouseVoteData struct {
	Votes []xmlHouseRecordedVote `xml:"recorded-vote"`
}

type xmlHouseRecordedVote struct {
	Legislator xmlHouseLegislator `xml:"legislator"`
	Vote       string             `xml:"vote"`
}

type xmlHouseLegislator struct {
	NameID string `xml:"name-id,attr"`
	Party  string `xml:"party,attr"`
	State  string `xml:"state,attr"`
	Name   string `xml:",chardata"`
}

type xmlSenateRollCall struct {
	XMLName    xml.Name           `xml:"roll_call_vote"`
	Congress   string             `xml:"congress"`
	Session    string             `xml:"session"`
	VoteNumber string             `xml:"vote_number"`
	VoteDate   string             `xml:"vote_date"`
	VoteTitle  string             `xml:"vote_title"`
	Question   string             `xml:"vote_question_text"`
	Result     string             `xml:"vote_result_text"`
	Document   xmlSenateDocument  `xml:"document"`
	Amendment  xmlSenateAmendment `xml:"amendment"`
	Count      xmlSenateCount     `xml:"count"`
	Members    xmlSenateMembers   `xml:"members"`
}

type xmlSenateDocument struct {
	Congress string `xml:"document_congress"`
	Type     string `xml:"document_type"`
	Number   string `xml:"document_number"`
}

type xmlSenateAmendment struct {
	ToDocumentNumber string `xml:"amendment_to_document_number"`
}

type xmlSenateCount struct {
	Yeas    int `xml:"yeas"`
	Nays    int `xml:"nays"`
	Present int `xml:"present"`
	Absent  int `xml:"absent"`
}

type xmlSenateMembers struct {
	Members []xmlSenateMember `xml:"member"`
}

type xmlSenateMember struct {
	FullName    string `xml:"member_full"`
	LastName    string `xml:"last_name"`
	FirstName   string `xml:"first_name"`
	Party       string `xml:"party"`
	State       string `xml:"state"`
	LisMemberID string `xml:"lis_member_id"`
	VoteCast    string `xml:"vote_cast"`
}

// ParseHouseVote parses a House roll call vote XML document.
func ParseHouseVote(data []byte) (*HouseVoteResult, error) {
	var doc xmlHouseRollCall
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	congress, _ := strconv.Atoi(doc.Metadata.Congress)
	rollNum, _ := strconv.Atoi(doc.Metadata.RollNum)

	result := &HouseVoteResult{
		Congress:       congress,
		Session:        doc.Metadata.Session,
		RollNumber:     rollNum,
		Question:       doc.Metadata.Question,
		Result:         doc.Metadata.VoteResult,
		VoteDate:       doc.Metadata.ActionDate,
		LegisNum:       doc.Metadata.LegisNum,
		VoteType:       doc.Metadata.VoteType,
		VoteDesc:       doc.Metadata.VoteDesc,
		YeaTotal:       doc.Metadata.VoteTotals.TotalsByVote.YeaTotal,
		NayTotal:       doc.Metadata.VoteTotals.TotalsByVote.NayTotal,
		PresentTotal:   doc.Metadata.VoteTotals.TotalsByVote.PresentTotal,
		NotVotingTotal: doc.Metadata.VoteTotals.TotalsByVote.NotVotingTotal,
	}

	for _, rv := range doc.VoteData.Votes {
		result.Votes = append(result.Votes, IndividualVote{
			MemberID: rv.Legislator.NameID,
			Name:     rv.Legislator.Name,
			Party:    rv.Legislator.Party,
			State:    rv.Legislator.State,
			Vote:     rv.Vote,
		})
	}

	return result, nil
}

// ParseSenateVote parses a Senate roll call vote XML document.
func ParseSenateVote(data []byte) (*SenateVoteResult, error) {
	var doc xmlSenateRollCall
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	congress, _ := strconv.Atoi(doc.Congress)
	session, _ := strconv.Atoi(doc.Session)
	voteNum, _ := strconv.Atoi(doc.VoteNumber)
	docCongress, _ := strconv.Atoi(strings.TrimSpace(doc.Document.Congress))

	result := &SenateVoteResult{
		Congress:   congress,
		Session:    session,
		VoteNumber: voteNum,
		Question:   doc.Question,
		Result:     doc.Result,
		VoteDate:   doc.VoteDate,
		VoteTitle:  doc.VoteTitle,
		Document: SenateDocument{
			Congress: docCongress,
			Type:     strings.TrimSpace(doc.Document.Type),
			Number:   strings.TrimSpace(doc.Document.Number),
		},
		AmendmentToDocument: strings.TrimSpace(doc.Amendment.ToDocumentNumber),
		YeaTotal:            doc.Count.Yeas,
		NayTotal:            doc.Count.Nays,
		PresentTotal:        doc.Count.Present,
		AbsentTotal:         doc.Count.Absent,
	}

	for _, m := range doc.Members.Members {
		result.Votes = append(result.Votes, IndividualVote{
			MemberID:  m.LisMemberID,
			Name:      m.FirstName + " " + m.LastName,
			FirstName: strings.TrimSpace(m.FirstName),
			LastName:  strings.TrimSpace(m.LastName),
			Party:     m.Party,
			State:     m.State,
			Vote:      m.VoteCast,
		})
	}

	return result, nil
}
