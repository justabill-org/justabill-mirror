package sync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/repository"
)

// recomputeStore adds stored diffs to textsStore: ReplaceBillTextDiff records what it gets and
// answers with canned results.
type recomputeStore struct {
	textsStore

	stored    []repository.DiffPair
	storedErr error
	replaced  []repository.BillTextDiffRow
	replace   repository.DiffReplacement
}

func (f *recomputeStore) QueryStoredDiffPairs(context.Context) ([]repository.DiffPair, error) {
	return f.stored, f.storedErr
}

func (f *recomputeStore) ReplaceBillTextDiff(
	_ context.Context, d repository.BillTextDiffRow,
) (repository.DiffReplacement, error) {
	f.replaced = append(f.replaced, d)
	return f.replace, nil
}

func TestSweepDiffs_RecomputeReplacesStoredDiffs(t *testing.T) {
	// v1 → v2 changed its only section; v2 → v3 differs only in XML ids, so its diff is empty now.
	store := &recomputeStore{
		sections: map[string]json.RawMessage{
			"v1": json.RawMessage(sectionsOld),
			"v2": json.RawMessage(sectionsNew),
			"v3": json.RawMessage(strings.ReplaceAll(sectionsNew, "sec1", "H9F3A")),
		},
		stored: []repository.DiffPair{
			{BillID: "hr-119-1", FromVersionID: "v1", ToVersionID: "v2"},
			{BillID: "hr-119-1", FromVersionID: "v2", ToVersionID: "v3"},
			{BillID: "hr-119-1", FromVersionID: "v3", ToVersionID: "gone"},
		},
		replace: repository.DiffReplacement{Found: true, Changed: true, SummaryDeleted: true},
	}
	svc, logs := textsService(&store.textsStore)
	svc.store = store

	if err := svc.SweepDiffs(t.Context(), true); err != nil {
		t.Fatalf("SweepDiffs: %v", err)
	}
	if len(store.replaced) != 2 || store.replaced[0].ToVersionID != "v2" || store.replaced[1].ToVersionID != "v3" {
		t.Fatalf("replaced = %+v, want v1 → v2 and v2 → v3", store.replaced)
	}
	if store.replaced[0].IsEmpty || !store.replaced[1].IsEmpty {
		t.Errorf("is_empty = %t, %t; want false, true", store.replaced[0].IsEmpty, store.replaced[1].IsEmpty)
	}
	var content []map[string]string
	if err := json.Unmarshal(store.replaced[0].DiffContent, &content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 || content[0]["type"] != "modified" || content[0]["new_text"] != "This Act is the New Act." {
		t.Errorf("replaced content = %s", store.replaced[0].DiffContent)
	}
	for _, want := range []string{
		`level=WARN msg="diffs recomputed" stored_diffs=3 unchanged=0 replaced=1 emptied=1 failed=1 deleted_summaries=2`,
		`msg="recompute diff failed"`,
		`msg="diff sweep done"`, // the ordinary sweep follows
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
}

func TestSweepDiffs_WithoutRecomputeOnlySweeps(t *testing.T) {
	store := &recomputeStore{storedErr: errors.New("must not be called")}
	svc, logs := textsService(&store.textsStore)
	svc.store = store
	if err := svc.SweepDiffs(t.Context(), false); err != nil {
		t.Fatalf("SweepDiffs: %v", err)
	}
	if !strings.Contains(logs.String(), `msg="diff sweep done"`) || strings.Contains(logs.String(), "recomputed") {
		t.Errorf("logs = %s", logs)
	}
}

func TestSweepDiffs_RecomputeErrors(t *testing.T) {
	store := &recomputeStore{storedErr: errors.New("boom")}
	svc, _ := textsService(&store.textsStore)
	svc.store = store
	if err := svc.SweepDiffs(t.Context(), true); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want the query error", err)
	}

	store = &recomputeStore{stored: []repository.DiffPair{{BillID: "b", FromVersionID: "v1", ToVersionID: "v2"}}}
	svc, _ = textsService(&store.textsStore)
	svc.store = store
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := svc.SweepDiffs(ctx, true); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestSweepDiffs_RecomputeCountsUnchanged(t *testing.T) {
	store := &recomputeStore{
		sections: map[string]json.RawMessage{
			"v1": json.RawMessage(sectionsOld), "v2": json.RawMessage(sectionsNew),
		},
		stored:  []repository.DiffPair{{BillID: "b", FromVersionID: "v1", ToVersionID: "v2"}},
		replace: repository.DiffReplacement{Found: true},
	}
	svc, logs := textsService(&store.textsStore)
	svc.store = store
	if err := svc.SweepDiffs(t.Context(), true); err != nil {
		t.Fatalf("SweepDiffs: %v", err)
	}
	if !strings.Contains(logs.String(), `level=INFO msg="diffs recomputed" stored_diffs=1 unchanged=1 replaced=0`) {
		t.Errorf("logs = %s", logs)
	}
}
