package model

// ListParams holds common query parameters for list endpoints.
//
// Statuses filters bills by their current_status, any of them (#712). With StatusMode "past" it's
// the stages a bill has reached at least once instead; the API allows only one status then.
type ListParams struct {
	Offset     int      `json:"offset"`
	Limit      int      `json:"limit"`
	Congress   *int     `json:"congress,omitempty"`
	BillType   *string  `json:"bill_type,omitempty"`
	Statuses   []string `json:"statuses,omitempty"`
	StatusMode string   `json:"status_mode,omitempty"` // "at" (exact, default) or "past" (reached at least)
	Chamber    *string  `json:"chamber,omitempty"`
	PolicyArea *string  `json:"policy_area,omitempty"` // Congress.gov policy area name, any case
	Search     *string  `json:"search,omitempty"`
	State      *string  `json:"state,omitempty"`
	District   *int     `json:"district,omitempty"`
	UnvotedBy  *string  `json:"unvoted_by,omitempty"` // user ID to exclude voted bills
	Sort       *string  `json:"sort,omitempty"`       // sort field
}

// ListResult wraps a paginated result set.
type ListResult[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// BillCounts is GET /bills/counts (#713): how many bills a list's filters match, by current
// status. ByStatus holds only the statuses some bill has; a bill with no status yet is in Total
// alone, so Total can exceed the sum of ByStatus.
type BillCounts struct {
	ByStatus map[string]int `json:"by_status"`
	Total    int            `json:"total"`
}
