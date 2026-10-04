package sync

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/xmlparse"
)

func TestBillIDFromRef(t *testing.T) {
	tests := []struct {
		ref  string
		want string // "" when no bill
	}{
		// House legis-num spellings.
		{"H R 144", "hr-119-144"},
		{"S 5", "s-119-5"},
		{"H J RES 7", "hjres-119-7"},
		{"S J RES 82", "sjres-119-82"},
		{"H CON RES 14", "hconres-119-14"},
		{"S CON RES 7", "sconres-119-7"},
		{"H RES 53", "hres-119-53"},
		{"S RES 12", "sres-119-12"},
		// Senate document spellings.
		{"H.R. 1", "hr-119-1"},
		{"S. 2296", "s-119-2296"},
		{"H.J.Res. 25", "hjres-119-25"},
		{"S.J.Res. 82", "sjres-119-82"},
		{"H.Con.Res. 14", "hconres-119-14"},
		{"S.Con.Res. 7", "sconres-119-7"},
		{"H.Res. 5", "hres-119-5"},
		{"S.Res. 412", "sres-119-412"},
		// Whitespace and case don't matter.
		{"  h r   0144 ", "hr-119-144"},
		// Not bills.
		{"PN 25-37", ""},
		{"PN25-37", ""},
		{"QUORUM", ""},
		{"S.Amdt. 3937", ""},
		{"H.Amdt. 12", ""},
		{"Treaty Doc. 118-1", ""},
		{"", ""},
		{"H R", ""},
		{"H R 0", ""},
		{"H R 12a", ""},
		{"HR-119-1", ""},
	}
	for _, tt := range tests {
		got, ok := billIDFromRef(119, tt.ref)
		if ok != (tt.want != "") || got != tt.want {
			t.Errorf("billIDFromRef(119, %q) = (%q, %v), want %q", tt.ref, got, ok, tt.want)
		}
	}

	if got, ok := billIDFromRef(0, "H R 1"); ok {
		t.Errorf("billIDFromRef(0, ...) = %q, want no bill without a congress", got)
	}
}

func loadSenateFixture(t *testing.T, name string) *xmlparse.SenateVoteResult {
	t.Helper()
	data, err := os.ReadFile("../xmlparse/testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	result, err := xmlparse.ParseSenateVote(data)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return result
}

func TestSenateBillID_RealVotes(t *testing.T) {
	tests := []struct {
		file string
		want string
	}{
		{"senate_vote_119_1_00372.xml", "hr-119-1"},      // passage of H.R. 1
		{"senate_vote_119_1_00616.xml", "hr-119-5371"},   // S.Amdt. 3937 to H.R. 5371
		{"senate_vote_119_1_00072.xml", "sconres-119-7"}, // S.Amdt. 407 to S.Con.Res. 7
		{"senate_vote_119_1_00607.xml", ""},              // nomination PN25-37
		{"real_senate_vote001.xml", ""},                  // cloture on a motion; no <document> in the fixture
	}
	for _, tt := range tests {
		got, ok := senateBillID(119, loadSenateFixture(t, tt.file))
		if ok != (tt.want != "") || got != tt.want {
			t.Errorf("%s: senateBillID = (%q, %v), want %q", tt.file, got, ok, tt.want)
		}
	}
}

func TestSenateBillID_UsesDocumentCongress(t *testing.T) {
	r := &xmlparse.SenateVoteResult{Document: xmlparse.SenateDocument{Congress: 118, Type: "S.", Number: "4"}}
	if got, _ := senateBillID(119, r); got != "s-118-4" {
		t.Errorf("senateBillID = %q, want s-118-4 (the document's congress)", got)
	}

	r = &xmlparse.SenateVoteResult{Document: xmlparse.SenateDocument{Type: "S.", Number: "4"}}
	if got, _ := senateBillID(119, r); got != "s-119-4" {
		t.Errorf("senateBillID = %q, want s-119-4 (the vote's congress when the document has none)", got)
	}

	// An amendment to something that isn't a bill (a treaty, a nomination) has no link.
	r = &xmlparse.SenateVoteResult{
		Document:            xmlparse.SenateDocument{Congress: 119, Type: "S.Amdt."},
		AmendmentToDocument: "Treaty Doc. 119-1",
	}
	if got, ok := senateBillID(119, r); ok {
		t.Errorf("senateBillID = %q, want no link", got)
	}
}

// voteRowStore records roll calls and their member votes. It has no bill lookups: a vote is
// linked to its bill whether or not the bill has been synced. Any other PipelineStore method
// panics through the nil embedded interface.
type voteRowStore struct {
	repository.PipelineStore

	rows  []repository.CongressionalVoteRow
	votes map[string]string // member ID → stored value
	err   error             // StoreRollCall's answer; on an error it stores nothing
}

func newVoteRowStore() *voteRowStore {
	return &voteRowStore{votes: map[string]string{}}
}

func (f *voteRowStore) StoreRollCall(
	_ context.Context, v repository.CongressionalVoteRow, members []repository.MemberVoteRow,
) error {
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, v)
	for _, m := range members {
		f.votes[m.MemberID] = m.Vote
	}
	return nil
}

func TestStoreRollCall_Normalizes(t *testing.T) {
	store := newVoteRowStore()
	s := &Service{store: store, logger: slog.New(slog.DiscardHandler)}

	err := s.storeRollCall(context.Background(), repository.CongressionalVoteRow{ID: "house-119-roll021"},
		[]memberPosition{
			{memberID: "A000370", vote: "No"},
			{memberID: "A000055", vote: "Aye"},
			{memberID: "B000001", vote: " yea "},
			{memberID: "C000001", vote: "Present, Giving Live Pair"},
			{memberID: "G000578", vote: "Not Voting"},
			{memberID: "J000299", vote: "Johnson (LA) "},
		}, 0)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"A000370": "Nay",
		"A000055": "Yea",
		"B000001": "Yea",
		"C000001": "Present",
		"G000578": "Not Voting",
		"J000299": "Johnson (LA)",
	}
	for id, v := range want {
		if store.votes[id] != v {
			t.Errorf("member %s stored %q, want %q", id, store.votes[id], v)
		}
	}
	if len(store.votes) != len(want) {
		t.Errorf("stored %d member votes, want %d", len(store.votes), len(want))
	}
}

func loadHouseFixture(t *testing.T, name string) *xmlparse.HouseVoteResult {
	t.Helper()
	data, err := os.ReadFile("../xmlparse/testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	result, err := xmlparse.ParseHouseVote(data)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return result
}

func TestStoreHouseVote_RecordedVote(t *testing.T) {
	store := newVoteRowStore()
	s := &Service{store: store, logger: slog.New(slog.DiscardHandler)}

	result := loadHouseFixture(t, "house_2025_roll021_recorded.xml")
	if err := s.storeHouseVote(context.Background(), 119, 1, 2025, 21, result); err != nil {
		t.Fatalf("storeHouseVote: %v", err)
	}

	if len(store.rows) != 1 {
		t.Fatalf("stored %d vote rows, want 1", len(store.rows))
	}
	row := store.rows[0]
	if row.ID != "house-119-s1-roll021" || row.BillID == nil || *row.BillID != "hres-119-53" {
		t.Errorf("row = %s bill %v, want house-119-s1-roll021 linked to hres-119-53", row.ID, row.BillID)
	}
	want := map[string]string{"A000370": "Nay", "A000055": "Yea", "B001298": "Not Voting"}
	for id, v := range want {
		if store.votes[id] != v {
			t.Errorf("member %s stored %q, want %q", id, store.votes[id], v)
		}
	}
}

// A failed write fails the roll call, so the sync counts it failed and the next one fetches it
// again, instead of logging and moving on with a vote row that has some or none of its members.
func TestStoreHouseVote_WriteErrorFails(t *testing.T) {
	store := newVoteRowStore()
	store.err = errors.New("spanner: deadline exceeded")
	s := &Service{store: store, logger: slog.New(slog.DiscardHandler)}

	result := loadHouseFixture(t, "house_2025_roll021_recorded.xml")
	err := s.storeHouseVote(context.Background(), 119, 1, 2025, 21, result)
	if !errors.Is(err, store.err) {
		t.Errorf("storeHouseVote = %v, want the store's error", err)
	}
}

func TestStoreHouseVote_SpeakerElection(t *testing.T) {
	store := newVoteRowStore()
	var logs bytes.Buffer
	s := &Service{store: store, logger: slog.New(slog.NewJSONHandler(&logs, nil))}

	result := loadHouseFixture(t, "house_2025_roll002_speaker.xml")
	if err := s.storeHouseVote(context.Background(), 119, 1, 2025, 2, result); err != nil {
		t.Fatalf("storeHouseVote: %v", err)
	}

	if store.rows[0].BillID != nil {
		t.Errorf("Speaker election linked to %q, want no bill", *store.rows[0].BillID)
	}
	if store.votes["A000055"] != "Johnson (LA)" || store.votes["A000370"] != "Jeffries" {
		t.Errorf("candidate names not stored as cast: %v", store.votes)
	}
	if !strings.Contains(logs.String(), `"unrecognized_values":3`) {
		t.Errorf("log lacks unrecognized_values=3: %s", logs.String())
	}
}

func TestStoreHouseVote_Quorum(t *testing.T) {
	store := newVoteRowStore()
	s := &Service{store: store, logger: slog.New(slog.DiscardHandler)}

	result := loadHouseFixture(t, "house_2025_roll001_quorum.xml")
	if err := s.storeHouseVote(context.Background(), 119, 1, 2025, 1, result); err != nil {
		t.Fatalf("storeHouseVote: %v", err)
	}

	if store.rows[0].BillID != nil {
		t.Errorf("quorum call linked to %q, want no bill", *store.rows[0].BillID)
	}
	if store.votes["A000370"] != "Present" {
		t.Errorf("A000370 stored %q, want Present", store.votes["A000370"])
	}
}

func TestStoreSenateVote_Links(t *testing.T) {
	senators := fixtureSenators()

	tests := []struct {
		file       string
		voteNum    int
		wantBillID string
	}{
		{"senate_vote_119_1_00372.xml", 372, "hr-119-1"},
		{"senate_vote_119_1_00616.xml", 616, "hr-119-5371"},
		{"senate_vote_119_1_00072.xml", 72, "sconres-119-7"},
		{"senate_vote_119_1_00607.xml", 607, ""},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			store := newVoteRowStore()
			s := &Service{store: store, logger: slog.New(slog.DiscardHandler)}

			stored, err := s.storeSenateVote(context.Background(), 119, 1, tt.voteNum,
				loadSenateFixture(t, tt.file), senators)
			if err != nil {
				t.Fatalf("storeSenateVote: %v", err)
			}

			row := store.rows[0]
			got := ""
			if row.BillID != nil {
				got = *row.BillID
			}
			if got != tt.wantBillID || stored.linked != (tt.wantBillID != "") || len(stored.unmatched) != 0 {
				t.Errorf("bill_id = %q, stored = %+v; want %q and every senator placed", got, stored, tt.wantBillID)
			}
		})
	}
}

func TestStoreSenateVote_DateAndValues(t *testing.T) {
	senators := fixtureSenators()
	store := newVoteRowStore()
	s := &Service{store: store, logger: slog.New(slog.DiscardHandler)}

	// vote_date is "July 1, 2025,  11:56 AM", with two spaces before the time.
	if _, err := s.storeSenateVote(context.Background(), 119, 1, 372,
		loadSenateFixture(t, "senate_vote_119_1_00372.xml"), senators); err != nil {
		t.Fatalf("storeSenateVote: %v", err)
	}

	wantDate := time.Date(2025, 7, 1, 11, 56, 0, 0, time.UTC)
	if !store.rows[0].VoteDate.Equal(wantDate) {
		t.Errorf("VoteDate = %v, want %v", store.rows[0].VoteDate, wantDate)
	}
	if store.votes["L000570"] != "Nay" || store.votes["B001299"] != "Yea" || len(store.votes) != 4 {
		t.Errorf("member votes = %v", store.votes)
	}
}
