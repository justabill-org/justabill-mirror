package sync

import (
	"slices"
	"testing"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

func TestBillLaws(t *testing.T) {
	public := model.BillLaw{Type: model.BillLawTypePublic, Number: "119-95"}
	private := model.BillLaw{Type: model.BillLawTypePrivate, Number: "117-3"}
	tests := []struct {
		name string
		laws []congress.Law
		want []model.BillLaw
	}{
		{"not law", nil, nil},
		{"public law", []congress.Law{{Type: "Public Law", Number: "119-95"}}, []model.BillLaw{public}},
		{"private law", []congress.Law{{Type: "Private Law", Number: "117-3"}}, []model.BillLaw{private}},
		{
			"spacing and case", []congress.Law{{Type: " public law ", Number: " 119-95 "}},
			[]model.BillLaw{public},
		},
		{
			"repeats dropped, order kept",
			[]congress.Law{{Type: "Private Law", Number: "117-3"}, {Type: "Public Law", Number: "119-95"},
				{Type: "Private Law", Number: "117-3"}},
			[]model.BillLaw{private, public},
		},
		{
			"unknown type or malformed number dropped",
			[]congress.Law{{Type: "Treaty", Number: "119-1"}, {Type: "Public Law", Number: "95"},
				{Type: "Public Law", Number: "119-95; drop"}, {Type: "Public Law", Number: ""},
				{Type: "Public Law", Number: "119-95"}},
			[]model.BillLaw{public},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BillLaws(&congress.BillDetail{Laws: tt.laws})
			if !slices.Equal(got, tt.want) {
				t.Errorf("BillLaws = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSyncOneBill_StoresLaws checks that a bill sync stores the laws the detail lists (#709),
// shaped as Congress.gov sends them (S. 4530 of the 119th, read 2026-10-03), and none for a
// bill that isn't law.
func TestSyncOneBill_StoresLaws(t *testing.T) {
	store := &billSyncStore{}
	s := newBillSyncService(t, store, &billAPI{n: 2, bodies: map[string]string{
		"/bill/119/hr/1": `{"bill":{"congress":119,"number":"1","type":"HR","title":"Bill",
			"latestAction":{"actionDate":"2026-05-29","text":"Became Public Law No: 119-95."},
			"laws":[{"number":"119-95","type":"Public Law"}]}}`,
	}})

	for _, n := range []int{1, 2} {
		if err := s.syncOneBill(t.Context(), hrSummary(n), 119); err != nil {
			t.Fatalf("syncOneBill(%d): %v", n, err)
		}
	}
	want := []model.BillLaw{{Type: model.BillLawTypePublic, Number: "119-95"}}
	if got := store.rows["hr-119-1"].Laws; !slices.Equal(got, want) {
		t.Errorf("hr-119-1 laws = %v, want %v", got, want)
	}
	if got := store.rows["hr-119-2"].Laws; got != nil {
		t.Errorf("hr-119-2 laws = %v, want none", got)
	}
}
