package model_test

import (
	"encoding/json"
	"testing"

	"github.com/justabill-org/justabill/db/model"
)

func TestBillSummaryJSON_WhoItAffects(t *testing.T) {
	who := "Post office users."
	short := "Short."
	tests := []struct {
		name    string
		summary model.BillSummary
		want    map[string]any
	}{
		{
			name:    "who_it_affects carries the text, with no why_it_matters alias",
			summary: model.BillSummary{BillID: "hr-119-1", ShortSummary: &short, WhoItAffects: &who},
			want: map[string]any{
				"bill_id": "hr-119-1", "short_summary": short, "who_it_affects": who,
			},
		},
		{
			name:    "no key without the text",
			summary: model.BillSummary{BillID: "hr-119-1", ShortSummary: &short},
			want:    map[string]any{"bill_id": "hr-119-1", "short_summary": short},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A pointer, as the API serves it, and a value, as in the list's map, encode the same.
			for _, v := range []any{tt.summary, &tt.summary} {
				got := marshalToMap(t, v)
				if len(got) != len(tt.want) {
					t.Errorf("keys = %v, want %v", got, tt.want)
				}
				for k, w := range tt.want {
					if got[k] != w {
						t.Errorf("%s = %v, want %v", k, got[k], w)
					}
				}
			}
		})
	}
}

// marshalToMap encodes v as JSON and decodes it into a map of its keys.
func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return got
}

func TestBillSummaryJSON_DecodesNewName(t *testing.T) {
	var s model.BillSummary
	if err := json.Unmarshal([]byte(`{"bill_id":"hr-119-1","who_it_affects":"Farmers."}`),
		&s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.WhoItAffects == nil || *s.WhoItAffects != "Farmers." {
		t.Errorf("WhoItAffects = %v, want Farmers.", s.WhoItAffects)
	}
}
