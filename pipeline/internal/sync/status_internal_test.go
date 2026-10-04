package sync

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

const houseFloor = "House floor actions"

// loc is a Library of Congress action with a code.
func loc(code, text string) lifecycleAction {
	return lifecycleAction{code: code, text: text, source: sourceLibraryOfCongress}
}

// The codes in #659's table, each with real texts from Congress.gov's 119th actions (fetched
// 2026-10-02; the bill each is from is named).
func TestClassifyAction_LibraryOfCongressCodes(t *testing.T) {
	tests := []struct {
		name   string
		action lifecycleAction
		want   string
	}{
		{"8000 hr-4405", loc("8000", "Passed/agreed to in House: On motion to suspend the rules and pass the bill "+
			"Agreed to by recorded vote (2/3 required): 427 - 1 (Roll no. 289). (text: CR H4725)"), statusPassedHouse},
		{"8000 hres-189", loc("8000", "Passed/agreed to in House: On agreeing to the resolution Agreed to by the Yeas "+
			"and Nays: 224 - 198, 2 Present (Roll no. 62). (text: 03/05/2025 CR H998)"), statusPassedHouse},
		{"17000 s-3424", loc("17000", "Passed/agreed to in Senate: Introduced in the Senate, read twice, considered, "+
			"read the third time, and passed without amendment by Unanimous Consent."), statusPassedSenate},
		{"17000 hr-5334", loc("17000", "Passed/agreed to in Senate: Passed Senate with an amendment and an amendment "+
			"to the Title by Yea-Nay Vote. 86 - 11. Record Vote Number: 224."), statusPassedSenate},
		{"28000 hjres-104", loc("28000", "Presented to President."), statusToPresident},
		{"E30000 hjres-104", loc("E30000", "Signed by President."), statusSigned},
		{"41000 hr-3377", loc("41000", "Signed by President."), statusSigned},
		{"36000 s-5", loc("36000", "Became Public Law No: 119-1."), statusBecameLaw},
		{"36000 on the signing, hr-1", loc("36000", "Signed by President."), statusBecameLaw},
		{"E40000 sjres-18", loc("E40000", "Became Public Law No: 119-10."), statusBecameLaw},
		{"E40000 hr-3377", loc("E40000", "Became Private Law No: 119-1."), statusBecameLaw},
		{"31000 hr-131", loc("31000", "Vetoed by President."), statusVetoed},

		// Failed passages are no stage, whatever their text says.
		{"9000 hconres-61", loc("9000", "Failed of passage/not agreed to in House On agreeing to the resolution "+
			"Failed by the Yeas and Nays: 210 - 216 (Roll no. 345)."), ""},
		{"9000 hr-9238", loc("9000", "Failed of passage/not agreed to in House On motion to suspend the rules and "+
			"pass the bill Failed by the Yeas and Nays: (2/3 required): 198 - 218 (Roll no. 221)."), ""},
		{"18000 sjres-10", loc("18000", "Failed of passage/not agreed to in Senate: Failed of passage in Senate by "+
			"Yea-Nay Vote. 47 - 52. Record Vote Number: 95."), ""},
		{"18000 hr-5371", loc("18000", "Failed of passage/not agreed to in Senate: Under the order of 9/18/2025, not "+
			"having achieved 60 votes in the affirmative, failed of passage in Senate by Yea-Nay Vote. 44 - 48. "+
			"Record Vote Number: 528."), ""},
		{"33000 hr-504", loc("33000", "Failed of passage in House over veto On passage, the objections of the "+
			"President to the contrary notwithstanding Failed by the Yeas and Nays: (2/3 required): 236 - 188 "+
			"(Roll no. 8)."), ""},

		// The House floor actions source codes a veto E30000, the Library of Congress's code for a
		// signing: only the Library of Congress's codes count, so its text decides (hr-131).
		{"House floor E30000 veto", lifecycleAction{code: "E30000", text: "Vetoed by President.", source: houseFloor,
			actionType: "President"}, statusVetoed},
		{"House floor E20000", lifecycleAction{code: "E20000", text: "Presented to President.", source: houseFloor,
			actionType: "Floor"}, statusToPresident},
		// A Library of Congress code outside the table falls back to the text.
		{"19500 hr-1", loc("19500", "Resolving differences -- House actions: On motion that the House agree to the "+
			"Senate amendment Agreed to by recorded vote: 218 - 214 (Roll no. 190)."), "resolving_differences"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := classifyAction(tt.action, 0); got != tt.want {
				t.Errorf("classifyAction(%+v) = %q, want %q", tt.action, got, tt.want)
			}
		})
	}
}

// Actions without a code the table knows: other sources, and older stored rows.
func TestClassifyAction_Text(t *testing.T) {
	tests := []struct {
		text, actionType string
		best             int
		want             string
	}{
		{text: "Became Public Law No: 119-1.", want: statusBecameLaw},
		{text: "Became Private Law No: 119-1.", want: statusBecameLaw},
		{text: "Vetoed by President.", want: statusVetoed},
		{text: "Signed by President.", want: statusSigned},
		// A President type alone isn't a signing (#659).
		{text: "Presented to President.", actionType: "President", want: statusToPresident},
		{text: "The Chair laid before the House the veto message from the President.", actionType: "President"},
		{text: "Resolving differences -- House actions", want: "resolving_differences"},
		{text: "Passed Senate without amendment by Unanimous Consent. (consideration: CR S4551-4552)",
			want: statusPassedSenate},
		{text: "Resolution agreed to in Senate without amendment by Unanimous Consent.", want: statusPassedSenate},
		{text: "Passed/agreed to in House: On motion to suspend the rules and pass the bill Agreed to by voice vote.",
			want: statusPassedHouse},
		{text: "Passed House (Amended) by recorded vote.", want: statusPassedHouse},
		{text: "On agreeing to the resolution Agreed to by voice vote.", want: statusPassedHouse},
		// Not passages: motions agreed to, a rule passing, and failures.
		{text: "Motion to proceed to consideration of measure agreed to in Senate by Yea-Nay Vote. 51 - 49."},
		{text: "Motion by Senator Thune to reconsider the vote by which H.R. 5371 failed of passage " +
			"(Record Vote No. 528) agreed to in Senate by Voice Vote."},
		{text: "Rule H. Res. 436 passed House."},
		{text: "On agreeing to the resolution Failed by the Yeas and Nays: 210 - 216 (Roll no. 345)."},
		{text: "Failed of passage/not agreed to in House On agreeing to the resolution Failed by the Yeas and Nays."},
		{text: "Failed of passage/not agreed to in Senate: Failed of passage in Senate by Yea-Nay Vote. 47 - 52."},
		{text: "Reported by the Committee on Rules.", actionType: "Committee", want: "reported"},
		{text: "Referred to the Committee on Ways and Means.", actionType: "IntroReferral", want: "in_committee"},
		{text: "Referred to the Subcommittee on Health.", actionType: "Committee", want: "in_committee"},
		{text: "Referred to the Subcommittee on Health.", actionType: "Committee", best: rankReported},
		{text: "Introduced in House", actionType: "IntroReferral", want: "introduced"},
		{text: "Motion to reconsider laid on the table Agreed to without objection."},
	}
	for _, tt := range tests {
		a := lifecycleAction{text: tt.text, actionType: tt.actionType}
		if got, _ := classifyAction(a, tt.best); got != tt.want {
			t.Errorf("classifyAction(%q, %q, %d) = %q, want %q", tt.text, tt.actionType, tt.best, got, tt.want)
		}
	}
}

func historyStatuses(h []statusEntry) map[string]string {
	out := make(map[string]string, len(h))
	for _, e := range h {
		out[e.status] = e.date.Format(time.DateOnly)
	}
	return out
}

// S. 4530's case from #659: the House passage is only "Passed/agreed to in House", and the
// Senate's motion to proceed came days before its passage. Actions are newest first.
func TestComputeStatusHistory_EarliestDateOfEachStage(t *testing.T) {
	actions := []lifecycleAction{
		{date: "2026-05-28", code: "36000", text: "Became Public Law No: 119-90.", source: sourceLibraryOfCongress},
		{date: "2026-05-28", code: "E30000", text: "Signed by President.", source: sourceLibraryOfCongress},
		{date: "2026-05-21", code: "28000", text: "Presented to President.", source: sourceLibraryOfCongress},
		{date: "2026-05-19", code: "8000", source: sourceLibraryOfCongress, text: "Passed/agreed to in House: On " +
			"motion to suspend the rules and pass the bill Agreed to by voice vote."},
		{date: "2026-05-19", code: "H11100", text: "Rule H. Res. 900 passed House.", source: houseFloor},
		{date: "2026-03-10", text: "Passed Senate without amendment by Unanimous Consent.", source: "Senate"},
		{date: "2026-03-10", code: "17000", source: sourceLibraryOfCongress,
			text: "Passed/agreed to in Senate: Passed Senate without amendment by Unanimous Consent."},
		{date: "2026-03-05", text: "Motion to proceed to consideration of measure agreed to in Senate by Voice Vote.",
			source: "Senate"},
		{date: "2026-03-01", text: "Introduced in Senate", actionType: "IntroReferral"},
		{date: "soon", text: "Became Public Law No: 119-1."},
	}
	status, date, history := computeStatusHistory(actions)
	if status != statusBecameLaw || date == nil || date.Format(time.DateOnly) != "2026-05-28" {
		t.Errorf("current status = %q on %v, want became_law on 2026-05-28", status, date)
	}
	want := map[string]string{
		"introduced": "2026-03-01", statusPassedSenate: "2026-03-10", statusPassedHouse: "2026-05-19",
		statusToPresident: "2026-05-21", statusSigned: "2026-05-28", statusBecameLaw: "2026-05-28",
	}
	got := historyStatuses(history)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("history[%s] = %q, want %q (history %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("history = %v, want exactly %v", got, want)
	}
	if missing := missingPassages(status, history); len(missing) != 0 {
		t.Errorf("missingPassages = %v, want none", missing)
	}
}

// H.R. 131 (119th): vetoed, and the override failed in the House. It is never signed.
func TestComputeStatusHistory_VetoIsNotSigned(t *testing.T) {
	status, _, history := computeStatusHistory([]lifecycleAction{
		{date: "2026-01-08", code: "33000", source: sourceLibraryOfCongress, text: "Failed of passage in House over " +
			"veto On passage, the objections of the President to the contrary notwithstanding Failed"},
		{date: "2025-12-29", code: "E30000", text: "Vetoed by President.", source: houseFloor, actionType: "President"},
		{date: "2025-12-29", code: "31000", text: "Vetoed by President.", source: sourceLibraryOfCongress},
		{date: "2025-12-17", code: "28000", text: "Presented to President.", source: sourceLibraryOfCongress},
	})
	got := historyStatuses(history)
	if status != statusVetoed || got[statusSigned] != "" || got[statusVetoed] != "2025-12-29" {
		t.Errorf("computeStatusHistory = %q, %v; want vetoed on 2025-12-29 and never signed", status, got)
	}
}

func TestMissingPassages(t *testing.T) {
	house := []statusEntry{{status: statusPassedHouse}}
	if got := missingPassages(statusBecameLaw, house); !slices.Equal(got, []string{statusPassedSenate}) {
		t.Errorf("missingPassages(became_law, house only) = %v, want [passed_senate]", got)
	}
	if got := missingPassages(statusToPresident, nil); got != nil {
		t.Errorf("missingPassages(to_president) = %v, want nil: only signed and enacted bills are checked", got)
	}
}

// statusStore is the store RederiveStatusHistory uses: stored bills and their actions, and
// what it wrote.
type statusStore struct {
	repository.PipelineStore

	bills       []repository.StoredBillActions
	checkpoint  *string
	items       int
	written     map[string]string
	history     map[string][]repository.BillStatusRow
	listAfter   []string
	succeeded   bool
	failListing bool
}

func (f *statusStore) GetSyncState(_ context.Context, step string, c int) (*repository.SyncStateRow, error) {
	return &repository.SyncStateRow{Step: step, Congress: c, LastOffset: f.checkpoint, ItemsSynced: f.items}, nil
}

func (f *statusStore) ListStoredBillActions(
	_ context.Context, _ int, after string, limit int,
) ([]repository.StoredBillActions, error) {
	f.listAfter = append(f.listAfter, after)
	if f.failListing {
		return nil, errors.New("spanner down")
	}
	var out []repository.StoredBillActions
	for _, b := range f.bills {
		if b.BillID > after && len(out) < limit {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *statusStore) ReplaceBillStatus(
	_ context.Context, billID, status string, _ *time.Time, rows []repository.BillStatusRow,
) error {
	if f.written == nil {
		f.written, f.history = map[string]string{}, map[string][]repository.BillStatusRow{}
	}
	f.written[billID] = status
	f.history[billID] = rows
	return nil
}

func (f *statusStore) SaveSyncCheckpoint(_ context.Context, _ string, _ int, offset *string, items int) error {
	f.checkpoint, f.items = offset, items
	return nil
}

func (f *statusStore) RecordSyncSuccess(context.Context, repository.SyncRun) error {
	f.succeeded = true
	return nil
}

func (f *statusStore) RecordSyncFailure(context.Context, repository.SyncRun) error { return nil }

// storedAction is a stored Library of Congress action.
func storedAction(date, code, text string) repository.BillActionRow {
	source := sourceLibraryOfCongress
	a := repository.BillActionRow{ActionText: text, ActionCode: &code, SourceSystem: &source}
	a.ActionDate, _ = time.Parse(time.DateOnly, date)
	return a
}

func TestRederiveStatusHistory(t *testing.T) {
	store := &statusStore{bills: []repository.StoredBillActions{
		{BillID: "hr-119-1", Actions: []repository.BillActionRow{
			storedAction("2026-05-28", "E40000", "Became Public Law No: 119-90."),
			storedAction("2026-05-19", "8000", "Passed/agreed to in House: On passage"),
			storedAction("2026-03-10", "17000", "Passed/agreed to in Senate: Passed Senate"),
		}},
		{BillID: "hr-119-2"},
		{BillID: "hr-119-3", Actions: []repository.BillActionRow{
			storedAction("2026-05-28", "E30000", "Signed by President."),
			storedAction("2026-05-20", "28000", "Presented to President."),
			storedAction("2026-03-10", "17000", "Passed/agreed to in Senate: Passed"),
		}},
	}}
	var logs strings.Builder
	s := &Service{store: store, logger: slog.New(slog.NewTextHandler(&logs, nil))}

	if err := s.RederiveStatusHistory(t.Context(), 119, 2); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{
		"hr-119-1": statusBecameLaw,
		"hr-119-3": statusSigned,
	}; !maps.Equal(
		store.written,
		want,
	) {
		t.Errorf("written = %v, want %v (hr-119-2 has no actions and is left alone)", store.written, want)
	}
	if !store.succeeded || !slices.Equal(store.listAfter, []string{"", "hr-119-2"}) {
		t.Errorf("succeeded=%v pages after %v; want a success over pages after \"\" and hr-119-2",
			store.succeeded, store.listAfter)
	}
	out := logs.String()
	if !strings.Contains(out, "bill_id=hr-119-3") || !strings.Contains(out, "missing=passed_house") ||
		strings.Contains(out, "bill_id=hr-119-1 ") || !strings.Contains(out, "enacted_without_both_passages=1") {
		t.Errorf("logs don't name hr-119-3 alone as signed without a House passage:\n%s", out)
	}
}

func TestRederiveStatusHistory_ResumesAndFails(t *testing.T) {
	after := "hr-119-1"
	store := &statusStore{checkpoint: &after, items: 1, bills: []repository.StoredBillActions{
		{
			BillID:  "hr-119-1",
			Actions: []repository.BillActionRow{storedAction("2026-01-01", "8000", "Passed/agreed to in House")},
		},
		{
			BillID:  "hr-119-2",
			Actions: []repository.BillActionRow{storedAction("2026-01-01", "17000", "Passed/agreed to in Senate")},
		},
	}}
	s := &Service{store: store, logger: slog.New(slog.DiscardHandler)}
	if err := s.RederiveStatusHistory(t.Context(), 119, 0); err != nil {
		t.Fatal(err)
	}
	if _, redone := store.written["hr-119-1"]; redone || store.written["hr-119-2"] != statusPassedSenate {
		t.Errorf("written = %v, want only hr-119-2, after the checkpoint", store.written)
	}

	failing := &statusStore{failListing: true}
	s.store = failing
	if err := s.RederiveStatusHistory(t.Context(), 119, 0); err == nil || failing.succeeded {
		t.Errorf("RederiveStatusHistory with a failing list = %v, succeeded=%v; want an error", err, failing.succeeded)
	}
}
