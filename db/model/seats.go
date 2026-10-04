package model

// houseSeats returns the number of House seats of every state, DC and territory, keyed by postal
// code: the 2020 apportionment, which holds from the 118th Congress through the 122nd (until the
// 2030 census). DC and the five territories each have one non-voting seat.
func houseSeats() map[string]int {
	//nolint:mnd // the apportionment's seat counts
	return map[string]int{
		"AL": 7, "AK": 1, "AZ": 9, "AR": 4, "CA": 52, "CO": 8, "CT": 5, "DE": 1, "FL": 28, "GA": 14,
		"HI": 2, "ID": 2, "IL": 17, "IN": 9, "IA": 4, "KS": 4, "KY": 6, "LA": 6, "ME": 2, "MD": 8,
		"MA": 9, "MI": 13, "MN": 8, "MS": 4, "MO": 8, "MT": 2, "NE": 3, "NV": 4, "NH": 2, "NJ": 12,
		"NM": 3, "NY": 26, "NC": 14, "ND": 1, "OH": 15, "OK": 5, "OR": 6, "PA": 17, "RI": 2, "SC": 7,
		"SD": 1, "TN": 9, "TX": 38, "UT": 4, "VT": 1, "VA": 11, "WA": 10, "WV": 2, "WI": 8, "WY": 1,
		"DC": 1, "PR": 1, "GU": 1, "VI": 1, "AS": 1, "MP": 1,
	}
}

// HouseSeats returns how many House seats a state, DC or territory has, given its upper-case
// postal code ("CA"), and false for anything else, lower case included.
func HouseSeats(state string) (int, bool) {
	n, ok := houseSeats()[state]
	return n, ok
}

// ValidDistrict reports whether district is one of state's House seats, numbered as the users
// table and the aggregate cells number them: 1 to N in a state with N > 1 seats, and 0 for an
// at-large state, DC or a territory.
func ValidDistrict(state string, district int) bool {
	n, ok := HouseSeats(state)
	if !ok {
		return false
	}
	if n == 1 {
		return district == 0
	}
	return district >= 1 && district <= n
}
