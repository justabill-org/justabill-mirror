package sync

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

// reparseStore is lawRefsStore that records the sections ReparseTexts writes.
type reparseStore struct {
	lawRefsStore

	sections map[string]json.RawMessage // by version ID
}

func (f *reparseStore) UpdateBillTextSections(_ context.Context, versionID string, s json.RawMessage) error {
	if versionID == f.failVersion {
		return errFakeStore
	}
	if f.sections == nil {
		f.sections = map[string]json.RawMessage{}
	}
	f.sections[versionID] = s
	return nil
}

const nestedXML = `<bill><legis-body><title><enum>I</enum><header>General</header>
<section id="s1"><enum>101.</enum><header>Short title</header><text>This Act may be cited as the
<quote><short-title>Test Act</short-title></quote>.</text></section></title></legis-body></bill>`

func reparseSource(bill, version, format, content string) repository.LawRefSource {
	return repository.LawRefSource{BillID: bill, VersionID: version, Format: format, Content: content}
}

func TestReparseTextsRebuildsSectionsFromStoredText(t *testing.T) {
	store := &reparseStore{lawRefsStore{sources: []repository.LawRefSource{
		reparseSource("hr-119-1", "a", formatXML, nestedXML),
		reparseSource("hr-119-1", "b", formatXML, "<html>not a bill</html>"),
		reparseSource("hr-119-2", "c", formatText, "SEC. 1. SHORT TITLE.\nThis Act may be cited as the Test Act."),
	}}, nil}
	svc := newBackfillService(store)

	if err := svc.ReparseTexts(t.Context(), 119, 2); err != nil {
		t.Fatalf("ReparseTexts: %v", err)
	}
	var a []billtext.Section
	if err := json.Unmarshal(store.sections["a"], &a); err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || len(a[0].Children) != 1 ||
		a[0].Children[0].Content != "This Act may be cited as the “Test Act”." {
		t.Errorf("version a sections = %s", store.sections["a"])
	}
	if got := string(store.sections["b"]); got != "[]" {
		t.Errorf("unparseable version b sections = %s, want []", got)
	}
	if got := string(store.sections["c"]); got == "" || got == "[]" {
		t.Errorf("plain-text version c sections = %q, want parsed", got)
	}
	if want := []string{"hr-119-1 b", "done"}; !reflect.DeepEqual(store.checkpoints, want) {
		t.Errorf("checkpoints = %q, want %q", store.checkpoints, want)
	}
	if store.state.ItemsSynced != 3 {
		t.Errorf("items synced = %d, want 3", store.state.ItemsSynced)
	}
}

func TestReparseTextsResumesAndStopsOnWriteError(t *testing.T) {
	offset := "hr-119-1 a"
	store := &reparseStore{lawRefsStore{
		sources: []repository.LawRefSource{
			reparseSource("hr-119-1", "a", formatXML, nestedXML),
			reparseSource("hr-119-2", "b", formatXML, nestedXML),
			reparseSource("hr-119-3", "c", formatXML, nestedXML),
		},
		state: &repository.SyncStateRow{
			Step: stepReparseTexts, Congress: 119, LastOffset: &offset, ItemsSynced: 1,
		},
		failVersion: "c",
	}, nil}
	svc := newBackfillService(store)

	err := svc.ReparseTexts(t.Context(), 119, 0)
	if !errors.Is(err, errFakeStore) || store.failures != 1 {
		t.Errorf("ReparseTexts = %v with %d failures, want the store error recorded", err, store.failures)
	}
	if _, ok := store.sections["a"]; ok || store.sections["b"] == nil {
		t.Errorf("wrote %v, want only b before the failure", store.sections)
	}
}
