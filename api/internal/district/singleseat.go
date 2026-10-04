package district

import (
	"strings"
)

// SourceState marks a result read from the address's state or ZIP code because the geocoder
// found no district. It is only given for jurisdictions that are a single district, where the
// answer can't be wrong.
const SourceState = "state"

const (
	zipLen      = 5
	zipPlus4Len = 10 // "12345-6789"

	// USPS ZIP codes of the Island Areas, which the Census address ranges don't cover. The ranges
	// are exact because the neighbouring 969xx codes belong to Palau, Micronesia and the Marshall
	// Islands, which have no seat in Congress.
	zipAmericanSamoa = "96799"
	zipGuamFirst     = "96910"
	zipGuamLast      = "96932"
	zipMarianasFirst = "96950"
	zipMarianasLast  = "96952"
	zipPrefixVI      = "008"
)

// singleSeatNames returns the 12 jurisdictions with exactly one House seat (the at-large states,
// DC and the territories), keyed by postal abbreviation, with their names as written in addresses.
func singleSeatNames() map[string]string {
	return map[string]string{
		"AK": "ALASKA", "DE": "DELAWARE", "ND": "NORTH DAKOTA", "SD": "SOUTH DAKOTA",
		"VT": "VERMONT", "WY": "WYOMING", "DC": "DISTRICT OF COLUMBIA", "PR": "PUERTO RICO",
		"GU": "GUAM", "VI": "VIRGIN ISLANDS", "AS": "AMERICAN SAMOA", "MP": "NORTHERN MARIANA ISLANDS",
	}
}

// singleSeat finds a single-seat jurisdiction in an address the geocoder couldn't match: first
// from a trailing state ("…, GU 96910", "…, Juneau, Alaska"), then from an Island Area ZIP code
// ("…, Pago Pago 96799"). A trailing state with more than one district gives no answer, whatever
// the ZIP says. It reads the address locally and sends it nowhere.
func singleSeat(address string) (string, bool) {
	tokens := strings.Fields(strings.NewReplacer(",", " ", ".", "").Replace(strings.ToUpper(address)))
	zip := ""
	if n := len(tokens); n > 0 && isZIP(tokens[n-1]) {
		zip = tokens[n-1][:zipLen]
		tokens = tokens[:n-1]
	}

	names := singleSeatNames()
	if n := len(tokens); n > 0 {
		last := tokens[n-1]
		if isStateAbbr(last) {
			_, single := names[last]
			return last, single
		}
	}
	tail := " " + strings.Join(tokens, " ")
	for abbr, name := range names {
		if strings.HasSuffix(tail, " "+name) {
			return abbr, true
		}
	}
	return islandAreaByZIP(zip)
}

// isStateAbbr reports whether s is the postal abbreviation of a state, DC or a territory.
func isStateAbbr(s string) bool {
	for _, abbr := range fipsToState {
		if abbr == s {
			return true
		}
	}
	return false
}

// isZIP reports whether s is a 5-digit ZIP code or a ZIP+4.
func isZIP(s string) bool {
	switch len(s) {
	case zipLen:
		return allDigits(s)
	case zipPlus4Len:
		return allDigits(s[:zipLen]) && s[zipLen] == '-' && allDigits(s[zipLen+1:])
	default:
		return false
	}
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// islandAreaByZIP returns the Island Area a ZIP code belongs to: AS, GU, MP or VI.
func islandAreaByZIP(zip string) (string, bool) {
	switch {
	case zip == "":
		return "", false
	case zip == zipAmericanSamoa:
		return "AS", true
	case zip >= zipGuamFirst && zip <= zipGuamLast:
		return "GU", true
	case zip >= zipMarianasFirst && zip <= zipMarianasLast:
		return "MP", true
	case strings.HasPrefix(zip, zipPrefixVI):
		return "VI", true
	default:
		return "", false
	}
}
