package model

// Passage ranks (bills.passage_rank, #1090): how a bill passed, by its passage entries
// ([BillCardFacts].Passage). The vote deck deals the highest rank first. A bill with no passage
// vote has no rank (NULL), which sorts after every rank when ordered descending.
const (
	// PassageRankVoice is a bill passed only by voice vote or unanimous consent.
	PassageRankVoice = 1
	// PassageRankRoll is a bill with a passage roll call, none of them contested.
	PassageRankRoll = 2
	// PassageRankContested is a bill with a contested passage roll call ([Contested]).
	PassageRankContested = 3
)

// contestedNayPercent is the share of yeas plus nays, in percent, that a roll call's nays must
// reach for it to be contested (the maintainer's decision on #1090).
const contestedNayPercent = 10

// percent is 100%, for comparing a share with contestedNayPercent in whole numbers.
const percent = 100

// PassageRank ranks a bill by its passage entries, each chamber's latest final vote: contested if
// either chamber's roll call is [Contested], else roll if either passed it by roll call, else
// voice if one passed it by voice vote or unanimous consent. It reports false when no entry is a
// passage of a known method.
func PassageRank(entries []PassageEntry) (int, bool) {
	rank := 0
	for _, e := range entries {
		switch {
		case e.Method == PassageRoll && Contested(e):
			rank = max(rank, PassageRankContested)
		case e.Method == PassageRoll:
			rank = max(rank, PassageRankRoll)
		case e.Method == PassageVoice, e.Method == PassageUC:
			rank = max(rank, PassageRankVoice)
		}
	}
	return rank, rank > 0
}

// Contested reports whether a passage roll call had real opposition: nays at least 10% of yeas
// plus nays. A roll call without both totals, or with no yeas or nays, isn't contested.
func Contested(e PassageEntry) bool {
	if e.Yeas == nil || e.Nays == nil {
		return false
	}
	total := *e.Yeas + *e.Nays
	return total > 0 && *e.Nays*percent >= total*contestedNayPercent
}
