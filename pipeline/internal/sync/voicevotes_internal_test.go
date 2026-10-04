package sync

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// readActions loads a recorded Congress.gov actions page from testdata.
func readActions(t *testing.T, name string) []congress.Action {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var page congress.ActionsResponse
	if err = json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	return page.Actions
}

// Three laws that passed the House by voice vote and the Senate by unanimous consent. Each
// House passage is in the actions twice (floor action H37300 and code 8000).
func TestDetectUnrecordedPassages_RealActions(t *testing.T) {
	tests := []struct {
		file   string
		billID string
		want   []string
	}{
		{
			file: "hr-119-1276-actions.json", billID: "hr-119-1276",
			want: []string{"senate-119-uc-hr-119-1276-20260807", "house-119-voice-hr-119-1276-20251209"},
		},
		{
			file: "hr-119-2196-actions.json", billID: "hr-119-2196",
			want: []string{"senate-119-uc-hr-119-2196-20260807", "house-119-voice-hr-119-2196-20260316"},
		},
		{
			file: "s-119-858-actions.json", billID: "s-119-858",
			want: []string{"house-119-voice-s-119-858-20260831", "senate-119-uc-s-119-858-20260325"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.billID, func(t *testing.T) {
			actions := readActions(t, tt.file)
			passages, skipped := detectUnrecordedPassages(actions)
			if skipped != 0 {
				t.Errorf("skipped %d passage actions, want 0", skipped)
			}
			got := make([]string, 0, len(passages))
			for _, p := range passages {
				got = append(got, p.voteID(tt.billID, 119))
			}
			if len(got) != len(tt.want) {
				t.Fatalf("vote IDs = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("vote ID %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// HR 1 passed both chambers on roll calls; the XML vote sync records those.
func TestDetectUnrecordedPassages_SkipsRollCalls(t *testing.T) {
	passages, skipped := detectUnrecordedPassages(readHR1Actions(t))
	if len(passages) != 0 {
		t.Errorf("passages = %+v, want none", passages)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want the House and Senate roll-call passages", skipped)
	}
}

func readHR1Actions(t *testing.T) []congress.Action {
	t.Helper()
	var page congress.ActionsResponse
	if err := json.Unmarshal(hr1Actions(t), &page); err != nil {
		t.Fatal(err)
	}
	return page.Actions
}

func TestDetectUnrecordedPassages_Edges(t *testing.T) {
	actions := []congress.Action{
		// Not a passage code, even though the text says voice vote.
		{ActionDate: "2026-01-05", ActionCode: "H37300", Text: "Agreed to by voice vote."},
		{ActionDate: "2026-01-05", Text: "Passed Senate without amendment by Unanimous Consent."},
		// A passage code with an unreadable date or an unknown method is skipped.
		{ActionDate: "January 5", ActionCode: "8000", Text: "Passed/agreed to in House: Agreed to by voice vote."},
		{ActionDate: "2026-01-05", ActionCode: "8000", Text: "Passed/agreed to in House: Agreed to."},
		{
			ActionDate: "2026-01-06", ActionCode: "17000",
			Text: "Passed/agreed to in Senate: Passed Senate without objection.",
		},
	}
	passages, skipped := detectUnrecordedPassages(actions)
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if len(passages) != 1 || passages[0].chamber != chamberSenate || passages[0].method != passageUC {
		t.Fatalf("passages = %+v, want one Senate uc passage", passages)
	}
}

func TestPassageMethod(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"Passed/agreed to in House: On motion to suspend the rules and pass the bill Agreed to by voice vote.",
			"voice"},
		{"Passed/agreed to in Senate: Passed Senate without amendment by Unanimous Consent.", "uc"},
		{"Passed/agreed to in Senate: Passed Senate without amendment by unanimous consent.", "uc"},
		{"Passed/agreed to in House: On agreeing to the resolution Agreed to without objection.", "uc"},
		{"Passed/agreed to in House: On passage Passed by the Yeas and Nays: 215 - 214 (Roll no. 145).", ""},
		{"Passed/agreed to in Senate: Passed Senate with an amendment by Yea-Nay Vote. Record Vote Number: 372.", ""},
		{"Passed/agreed to in House: On passage Passed by recorded vote: 230 - 196 (Roll no. 99).", ""},
		{"Passed/agreed to in Senate: Passed Senate with an amendment by Yea-Nay Vote. 51 - 50.", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := passageMethod(tt.text); got != tt.want {
			t.Errorf("passageMethod(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

// One vote row per unrecorded passage and no member votes: actionsStore panics on
// UpsertMemberVote.
func TestSyncVoiceVotes_StoresUnrecordedPassages(t *testing.T) {
	actions := readActions(t, "hr-119-1276-actions.json")
	store := &actionsStore{}
	s := &Service{store: store, logger: slog.New(slog.DiscardHandler)}

	s.syncVoiceVotes(t.Context(), "hr-119-1276", 119, actions)

	if len(store.votes) != 2 {
		t.Fatalf("stored %d vote rows, want 2: %+v", len(store.votes), store.votes)
	}
	house := store.votes[1]
	if house.ID != "house-119-voice-hr-119-1276-20251209" || house.Chamber != chamberHouse {
		t.Errorf("house row = %s in %s", house.ID, house.Chamber)
	}
	if house.BillID == nil || *house.BillID != "hr-119-1276" {
		t.Errorf("bill_id = %v, want hr-119-1276", house.BillID)
	}
	if house.VoteDate.Format("2006-01-02") != "2025-12-09" {
		t.Errorf("vote date = %v", house.VoteDate)
	}
	const wantQ = "Voice Vote: Passed/agreed to in House: On motion to suspend the rules and pass the bill, " +
		"as amended Agreed to by voice vote. (text: CR H5073)"
	if house.Question == nil || *house.Question != wantQ {
		t.Errorf("question = %v, want %q", house.Question, wantQ)
	}
	for _, v := range store.votes {
		if v.Result == nil || *v.Result != "Passed" {
			t.Errorf("%s: result = %v, want Passed", v.ID, v.Result)
		}
		if v.RollNumber != nil || v.Session != nil || v.Yeas != nil || v.Nays != nil ||
			v.Present != nil || v.NotVoting != nil {
			t.Errorf("%s: roll number, session and counts should be NULL: %+v", v.ID, v)
		}
	}
	senate := store.votes[0]
	if senate.Question == nil || *senate.Question !=
		"Unanimous Consent: Passed/agreed to in Senate: Passed Senate without amendment by Unanimous Consent." {
		t.Errorf("senate question = %v", senate.Question)
	}
}
