package xmlparse

import (
	"encoding/xml"
	"strings"
)

// SenateMember is one sitting senator in the Senate's member feed
// (senate.gov/legislative/LIS_MEMBER/cvc_member_data.xml): the Senate's own mapping from LIS
// member ID to bioguide ID.
type SenateMember struct {
	LISID      string
	BioguideID string
	FirstName  string
	LastName   string
	State      string
}

type xmlSenators struct {
	XMLName  xml.Name          `xml:"senators"`
	Senators []xmlSenateRecord `xml:"senator"`
}

type xmlSenateRecord struct {
	LISID      string `xml:"lis_member_id,attr"`
	First      string `xml:"name>first"`
	Last       string `xml:"name>last"`
	State      string `xml:"state"`
	BioguideID string `xml:"bioguideId"`
}

// ParseSenateMembers parses the Senate's member feed. Fields are trimmed but not validated:
// the caller checks the ID formats.
func ParseSenateMembers(data []byte) ([]SenateMember, error) {
	var doc xmlSenators
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	members := make([]SenateMember, 0, len(doc.Senators))
	for _, s := range doc.Senators {
		members = append(members, SenateMember{
			LISID:      strings.TrimSpace(s.LISID),
			BioguideID: strings.TrimSpace(s.BioguideID),
			FirstName:  strings.TrimSpace(s.First),
			LastName:   strings.TrimSpace(s.Last),
			State:      strings.TrimSpace(s.State),
		})
	}
	return members, nil
}
