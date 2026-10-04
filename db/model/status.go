package model

// IsBillStatus reports whether s is a value a bill's current_status can hold: one of the
// lifecycle stages the pipeline derives from its actions (classifyAction in
// pipeline/internal/sync), which web/src/lib/types.ts mirrors as BillStatus.
func IsBillStatus(s string) bool {
	switch s {
	case "introduced", "in_committee", "reported", "passed_house", "passed_senate",
		"resolving_differences", "to_president", "signed", "vetoed", "became_law":
		return true
	default:
		return false
	}
}
