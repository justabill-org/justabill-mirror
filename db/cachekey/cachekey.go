// Package cachekey names the API's Redis cache keys that another service has to know. The API
// writes them, and the pipeline deletes them after a sync changes the data behind them, so both
// spell a key the same way from one place.
package cachekey

// BillDetailPrefix starts the key of the API's cached GET /api/v1/bills/{id} response.
const BillDetailPrefix = "bills:detail:"

// BillDetail returns the key of the API's cached detail response for bill id: bills:detail:hr-119-1.
func BillDetail(id string) string {
	return BillDetailPrefix + id
}

// BillLawChangesPrefix starts the key of the API's cached GET /api/v1/bills/{id}/law-changes
// response.
const BillLawChangesPrefix = "bills:law-changes:"

// BillLawChanges returns the key of the API's cached law-changes response for bill id:
// bills:law-changes:hr-119-1.
func BillLawChanges(id string) string {
	return BillLawChangesPrefix + id
}
