package spannerdb

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/testdb"
)

func TestBillListFilter(t *testing.T) {
	congress, billType, status, chamber := 119, "hr", "passed_house", "House"
	search, user, area := "water", "u1", "Armed Forces and National Security"

	where, p := billListFilter(model.ListParams{})
	if where != "WHERE TRUE" || len(p) != 0 {
		t.Fatalf("no filters: got %q %v", where, p)
	}

	where, p = billListFilter(model.ListParams{
		Congress: &congress, BillType: &billType, Statuses: []string{status}, Chamber: &chamber,
		Search: &search, UnvotedBy: &user, PolicyArea: &area,
	})
	for _, clause := range []string{
		"congress = @congress", "bill_type = @billType", "current_status = @status",
		"origin_chamber = @chamber", "SEARCH(t.title_tokens, @search, dialect=>'words')", "uv.user_id = @unvotedBy",
		"policy_area_id = @policyArea",
	} {
		if !strings.Contains(where, clause) {
			t.Errorf("where %q lacks %q", where, clause)
		}
	}
	want := map[string]any{
		"congress": int64(congress), "billType": billType, "status": status, "chamber": chamber,
		"search": search, "unvotedBy": user, "policyArea": "armed-forces-and-national-security",
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("params = %v, want %v", p, want)
	}

	where, p = billListFilter(model.ListParams{Statuses: []string{status}, StatusMode: "past"})
	if !strings.Contains(where, "bill_status_history") || strings.Contains(where, "current_status") {
		t.Errorf("past mode: where %q", where)
	}
	if p["status"] != status {
		t.Errorf("past mode: params %v", p)
	}
}

// TestBillListFilterStatuses checks that several statuses become one IN UNNEST list (#712), and
// that one status keeps the plain equality, so its query is the one it always was.
func TestBillListFilterStatuses(t *testing.T) {
	several := []string{"became_law", "signed"}
	tests := []struct {
		name   string
		params model.ListParams
		clause string
		want   map[string]any
	}{
		{"one", model.ListParams{Statuses: []string{"signed"}}, " AND current_status = @status",
			map[string]any{"status": "signed"}},
		{"several", model.ListParams{Statuses: several}, " AND current_status IN UNNEST(@statuses)",
			map[string]any{"statuses": several}},
		{"several past", model.ListParams{Statuses: several, StatusMode: "past"},
			" AND bills.bill_id IN (SELECT h.bill_id FROM bill_status_history h WHERE h.status IN UNNEST(@statuses))",
			map[string]any{"statuses": several}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, p := billListFilter(tt.params)
			if want := "WHERE TRUE" + tt.clause; where != want {
				t.Errorf("where = %q, want %q", where, want)
			}
			if !reflect.DeepEqual(p, tt.want) {
				t.Errorf("params = %v, want %v", p, tt.want)
			}
		})
	}
}

// TestBillSearchIDsIndexable checks the search's shape (#619): Spanner runs SEARCH only through a
// search index, so each SEARCH must sit alone in the WHERE of a SELECT from its indexed table, the
// two joined by UNION DISTINCT, never ORed with a predicate on another table.
func TestBillSearchIDsIndexable(t *testing.T) {
	branches := strings.Split(billSearchIDs, "UNION DISTINCT")
	want := []string{
		"SELECT t.bill_id FROM bills t WHERE SEARCH(t.title_tokens, @search, dialect=>'words')",
		"SELECT s.bill_id FROM bill_summaries s WHERE SEARCH(s.summary_tokens, @search, dialect=>'words')",
	}
	if len(branches) != len(want) {
		t.Fatalf("billSearchIDs has %d branches, want %d: %q", len(branches), len(want), billSearchIDs)
	}
	for i, b := range branches {
		if got := strings.Join(strings.Fields(b), " "); got != want[i] {
			t.Errorf("branch %d = %q, want %q", i, got, want[i])
		}
	}
	search := "water"
	where, _ := billListFilter(model.ListParams{Search: &search})
	if strings.Contains(where, " OR ") || strings.Contains(where, "EXISTS") {
		t.Errorf("where %q ORs the search with another predicate", where)
	}
}

func TestBillListOrder(t *testing.T) {
	sortKey := func(s string) *string { return &s }
	tests := []struct {
		sort *string
		want string
	}{
		{nil, "ORDER BY introduced_date DESC, bill_id"},
		{sortKey("introduced_date"), "ORDER BY introduced_date DESC, bill_id"},
		{sortKey("updated_at"), "ORDER BY updated_at DESC, bill_id"},
		{sortKey("number"), "ORDER BY congress DESC, number DESC, bill_type, bill_id"},
		{sortKey("latest_action"), "ORDER BY latest_action_date DESC, bill_id"},
		{sortKey("bogus"), "ORDER BY introduced_date DESC, bill_id"},
	}
	for _, tt := range tests {
		if got := billListOrder(tt.sort); got != tt.want {
			t.Errorf("billListOrder(%v) = %q, want %q", tt.sort, got, tt.want)
		}
	}
}

// TestBillListSource checks which index a list's page reads (#760, #868): within a congress, with
// no search and no past-status filter, the default order forces the introduced-date index, the
// latest_action order the latest-action index, or the status index when the list filters on
// current status, and updated_at with a status filter the status index too; every other list
// reads bills with no hint, as it did before.
func TestBillListSource(t *testing.T) {
	str := func(s string) *string { return &s }
	congress := 119
	const (
		introduced = "bills@{FORCE_INDEX=idx_bills_list_introduced}"
		latest     = "bills@{FORCE_INDEX=idx_bills_list_latest_action}"
		byStatus   = "bills@{FORCE_INDEX=idx_bills_list_status}"
	)
	tests := []struct {
		name          string
		params        model.ListParams
		from, orderBy string
	}{
		{"congress", model.ListParams{Congress: &congress},
			introduced, "ORDER BY introduced_date DESC, bill_id"},
		{"congress, unknown sort", model.ListParams{Congress: &congress, Sort: str("bogus")},
			introduced, "ORDER BY introduced_date DESC, bill_id"},
		{"congress, latest action", model.ListParams{Congress: &congress, Sort: str("latest_action")},
			latest, "ORDER BY latest_action_date DESC, bill_id"},
		{"congress and every index-stored filter", model.ListParams{
			Congress: &congress, BillType: str("hr"), Statuses: []string{"a", "b"}, StatusMode: "at",
			Chamber: str("house"), PolicyArea: str("health"), UnvotedBy: str("u1"),
		}, introduced, "ORDER BY introduced_date DESC, bill_id"},
		{"no congress", model.ListParams{}, "bills", "ORDER BY introduced_date DESC, bill_id"},
		{"no congress, latest action", model.ListParams{Sort: str("latest_action")},
			"bills", "ORDER BY latest_action_date DESC, bill_id"},
		{"search", model.ListParams{Congress: &congress, Search: str("water")},
			"bills", "ORDER BY introduced_date DESC, bill_id"},
		{"past mode", model.ListParams{Congress: &congress, Statuses: []string{"a"}, StatusMode: "past"},
			"bills", "ORDER BY introduced_date DESC, bill_id"},
		{"number", model.ListParams{Congress: &congress, Sort: str("number")},
			"bills", "ORDER BY congress DESC, number DESC, bill_type, bill_id"},
		{"updated_at", model.ListParams{Congress: &congress, Sort: str("updated_at")},
			"bills", "ORDER BY updated_at DESC, bill_id"},
		{"status, latest action", model.ListParams{Congress: &congress, Sort: str("latest_action"),
			Statuses: []string{"in_committee"}}, byStatus, "ORDER BY latest_action_date DESC, bill_id"},
		{"statuses and every index-stored filter, latest action", model.ListParams{
			Congress: &congress, Sort: str("latest_action"), BillType: str("hr"),
			Statuses: []string{"became_law", "signed"}, StatusMode: "at", Chamber: str("house"),
			PolicyArea: str("health"), UnvotedBy: str("u1"),
		}, byStatus, "ORDER BY latest_action_date DESC, bill_id"},
		{"status, updated_at", model.ListParams{Congress: &congress, Sort: str("updated_at"),
			Statuses: []string{"became_law"}}, byStatus, "ORDER BY updated_at DESC, bill_id"},
		{"status, number", model.ListParams{Congress: &congress, Sort: str("number"),
			Statuses: []string{"became_law"}}, "bills", "ORDER BY congress DESC, number DESC, bill_type, bill_id"},
		{"no congress, status, latest action", model.ListParams{Sort: str("latest_action"),
			Statuses: []string{"became_law"}}, "bills", "ORDER BY latest_action_date DESC, bill_id"},
		{"search, status, latest action", model.ListParams{Congress: &congress, Sort: str("latest_action"),
			Search: str("water"), Statuses: []string{"became_law"}},
			"bills", "ORDER BY latest_action_date DESC, bill_id"},
		{"past mode, latest action", model.ListParams{Congress: &congress, Sort: str("latest_action"),
			Statuses: []string{"became_law"}, StatusMode: "past"},
			"bills", "ORDER BY latest_action_date DESC, bill_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, orderBy := billListSource(tt.params)
			if from != tt.from || orderBy != tt.orderBy {
				t.Errorf("billListSource = %q, %q; want %q, %q", from, orderBy, tt.from, tt.orderBy)
			}
		})
	}
}

// TestBillCountSource checks which index a count reads (#868): List's COUNT(*) and
// CountByStatus's GROUP BY, within a congress with no search and no past-status filter, read the
// status index whatever the sort, so a status's count seeks to its bills and the counts by status
// read the index alone; every other count reads bills, as before.
func TestBillCountSource(t *testing.T) {
	str := func(s string) *string { return &s }
	congress := 119
	const byStatus = "bills@{FORCE_INDEX=idx_bills_list_status}"
	tests := []struct {
		name   string
		params model.ListParams
		want   string
	}{
		{"congress", model.ListParams{Congress: &congress}, byStatus},
		{"status, newest introduced", model.ListParams{Congress: &congress, Statuses: []string{"became_law"}},
			byStatus},
		{"statuses and every index-stored filter, number", model.ListParams{
			Congress: &congress, Sort: str("number"), BillType: str("hr"), Statuses: []string{"a", "b"},
			StatusMode: "at", Chamber: str("house"), PolicyArea: str("health"), UnvotedBy: str("u1"),
		}, byStatus},
		{"no congress", model.ListParams{Statuses: []string{"became_law"}}, "bills"},
		{"search", model.ListParams{Congress: &congress, Search: str("water")}, "bills"},
		{"past mode", model.ListParams{Congress: &congress, Statuses: []string{"a"}, StatusMode: "past"}, "bills"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := billCountSource(tt.params); got != tt.want {
				t.Errorf("billCountSource = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalChamber(t *testing.T) {
	for in, want := range map[string]string{
		"house": "House", "HOUSE": "House", "House": "House",
		"senate": "Senate", "SeNaTe": "Senate", "joint": "joint", "": "",
	} {
		if got := canonicalChamber(in); got != want {
			t.Errorf("canonicalChamber(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSearchError checks that only an InvalidArgument from a list with a search becomes
// [repository.ErrInvalidSearch] (#452). The InvalidArgument is a real one from the client: a
// parameter of a type Spanner can't encode.
func TestSearchError(t *testing.T) {
	client := testdb.New(t)
	stmt := spanner.Statement{SQL: "SELECT 1", Params: map[string]any{"bad": struct{ C chan int }{}}}
	_, invalid := client.Single().Query(t.Context(), stmt).Next()
	if got := spanner.ErrCode(invalid).String(); got != "InvalidArgument" {
		t.Fatalf("setup: code = %s (%v), want InvalidArgument", got, invalid)
	}
	other := errors.New("spanner: unavailable")
	search := "h.r. 1"

	tests := []struct {
		name   string
		params model.ListParams
		err    error
		want   bool
	}{
		{"invalid search", model.ListParams{Search: &search}, invalid, true},
		{"invalid without search", model.ListParams{}, invalid, false},
		{"other error", model.ListParams{Search: &search}, other, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := searchError(tt.params, tt.err)
			if errors.Is(got, repository.ErrInvalidSearch) != tt.want {
				t.Errorf("searchError = %v, want ErrInvalidSearch=%v", got, tt.want)
			}
			if !errors.Is(got, tt.err) {
				t.Errorf("searchError = %v, lost the cause %v", got, tt.err)
			}
		})
	}
}
