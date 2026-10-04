package scoring_test

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/scoring"
)

const goldenColumns = 4

// TestIsFinalVoteGolden checks every question form in testdata/questions.tsv: real House Clerk
// and Senate question strings with the expected answer.
func TestIsFinalVoteGolden(t *testing.T) {
	f, err := os.Open("testdata/questions.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	seen := make(map[string]bool)
	counts := make(map[string]int)
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		cols := strings.Split(text, "\t")
		if len(cols) != goldenColumns {
			t.Fatalf("line %d: want %d tab-separated columns, got %d", line, goldenColumns, len(cols))
		}
		chamber, question, want := cols[0], cols[1], cols[2]
		if want != "final" && want != "excluded" {
			t.Fatalf("line %d: want column is %q, not final or excluded", line, want)
		}
		key := chamber + "\t" + question
		if seen[key] {
			t.Errorf("line %d: duplicate question %q", line, question)
		}
		seen[key] = true
		counts[chamber+" "+want]++

		if got := scoring.IsFinalVote(chamber, question); got != (want == "final") {
			t.Errorf("line %d: IsFinalVote(%q, %q) = %v, want %s (%s)", line, chamber, question, got, want, cols[3])
		}
	}
	if err = sc.Err(); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"House final", "House excluded", "Senate final", "Senate excluded"} {
		if counts[k] == 0 {
			t.Errorf("golden file has no %q cases", k)
		}
	}
}

func TestIsFinalVoteFolding(t *testing.T) {
	tests := []struct {
		chamber, question string
		want              bool
	}{
		{"House", "  on   PASSAGE ", true},
		{"house", "On Passage", true},
		{"HOUSE", "On Motion to Suspend the Rules and\nPass", true},
		{"Senate", "On Passage of the Bill\n         ", true},
		{"senate", "ON THE MOTION (MOTION TO CONCUR IN THE HOUSE AMENDMENT TO S. 1071)", true},
		// The allowlist is per chamber.
		{"Senate", "On Passage", false},
		{"House", "On Passage of the Bill H.R. 1", true},
		{"House", "On the Joint Resolution S.J.Res. 11", false},
		// A bare Senate "On the Motion" and procedural motions about a concurrence don't count.
		{"Senate", "On the Motion", false},
		{
			"Senate",
			"On the Motion (Motion to Waive All Applicable Budgetary Discipline Re: The Motion to Concur)",
			false,
		},
		{"Senate", "On the Cloture Motion (Motion to Concur in the House Amendment to S. 178)", false},
		{"Senate", "On the Resolution of Ratification Treaty Doc. 114-12", false},
		{"Senate", "on the resolution  of ratification", false},
		// Unknown chamber or empty question.
		{"Joint", "On Passage", false},
		{"", "On Passage", false},
		{"House", "", false},
		{"Senate", "   ", false},
	}
	for _, tt := range tests {
		if got := scoring.IsFinalVote(tt.chamber, tt.question); got != tt.want {
			t.Errorf("IsFinalVote(%q, %q) = %v, want %v", tt.chamber, tt.question, got, tt.want)
		}
	}
}

func TestNormalizeVote(t *testing.T) {
	tests := []struct {
		raw, want string
	}{
		{"Yea", scoring.Yea},
		{"YEA", scoring.Yea},
		{" yea\n", scoring.Yea},
		{"Aye", scoring.Yea},
		{"aye", scoring.Yea},
		{"Nay", scoring.Nay},
		{"No", scoring.Nay},
		{"NO ", scoring.Nay},
		{"Present", scoring.Present},
		{"Present, Giving Live Pair", scoring.Present},
		{"present,  giving live   pair", scoring.Present},
		{"Not Voting", scoring.NotVoting},
		{"not  voting", scoring.NotVoting},
		{"NOT VOTING", scoring.NotVoting},
		// Already-normalized values map to themselves.
		{scoring.NotVoting, scoring.NotVoting},
		{scoring.Present, scoring.Present},
		// Anything else.
		{"Guilty", scoring.Other},
		{"Not Guilty", scoring.Other},
		{"Johnson (LA)", scoring.Other},
		{"Jeffries", scoring.Other},
		{"", scoring.Other},
		{scoring.Other, scoring.Other},
	}
	for _, tt := range tests {
		if got := scoring.NormalizeVote(tt.raw); got != tt.want {
			t.Errorf("NormalizeVote(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

// rc builds a candidate roll call for a bill.
func rc(bill, chamber string, session, roll int, date, question, vote string) scoring.Candidate {
	return scoring.Candidate{
		BillID:     bill,
		VoteID:     chamber + "-" + date + "-" + question,
		Chamber:    chamber,
		Congress:   119,
		Session:    session,
		RollNumber: roll,
		VoteDate:   day(date),
		Question:   question,
		Vote:       vote,
	}
}

type pick struct {
	bill, vote string
	roll       int
}

func picks(ps []scoring.Candidate) []pick {
	out := make([]pick, 0, len(ps))
	for _, p := range ps {
		out = append(out, pick{bill: p.BillID, vote: p.Vote, roll: p.RollNumber})
	}
	return out
}

func TestPickPositions(t *testing.T) {
	tests := []struct {
		name       string
		candidates []scoring.Candidate
		want       []pick
	}{
		{
			name: "procedural and passage on the same day: roll number breaks the tie",
			candidates: []scoring.Candidate{
				rc("hr1", "House", 1, 97, "2025-05-22", "On Motion to Recommit", "No"),
				rc("hr1", "House", 1, 96, "2025-05-22", "On Agreeing to the Amendment", "Aye"),
				rc("hr1", "House", 1, 98, "2025-05-22", "On Passage", "Aye"),
				rc("hr1", "House", 1, 99, "2025-05-22", "On Motion to Table", "No"),
			},
			want: []pick{{"hr1", scoring.Yea, 98}},
		},
		{
			name: "failed suspension, then passage under a rule",
			candidates: []scoring.Candidate{
				rc("hr2", "House", 1, 40, "2025-03-03", "On Motion to Suspend the Rules and Pass", "Nay"),
				rc("hr2", "House", 1, 55, "2025-03-10", "On Passage", "Yea"),
			},
			want: []pick{{"hr2", scoring.Yea, 55}},
		},
		{
			name: "passage, then concurring in the Senate amendment",
			candidates: []scoring.Candidate{
				rc("hr1", "House", 1, 145, "2025-05-22", "On Passage", "Yea"),
				rc("hr1", "House", 1, 190, "2025-07-03", "On Motion to Concur in the Senate Amendment", "Nay"),
			},
			want: []pick{{"hr1", scoring.Nay, 190}},
		},
		{
			name: "passage, then a veto override",
			candidates: []scoring.Candidate{
				rc("hr504", "House", 1, 300, "2025-11-01", "On Motion to Suspend the Rules and Pass", "Nay"),
				rc("hr504", "House", 2, 8, "2026-01-08",
					"Passage, Objections of the President To The Contrary Notwithstanding", "Yea"),
			},
			want: []pick{{"hr504", scoring.Yea, 8}},
		},
		{
			name: "Senate passage upon reconsideration",
			candidates: []scoring.Candidate{
				rc("hr5371", "Senate", 1, 528, "2025-09-30", "On Passage of the Bill H.R. 5371", "Nay"),
				rc("hr5371", "Senate", 1, 529, "2025-09-30", "On the Motion to Reconsider H.R. 5371", "Yea"),
				rc("hr5371", "Senate", 1, 535, "2025-10-01", "On Passage of the Bill H.R. 5371", "Yea"),
				rc("hr5371", "Senate", 1, 618, "2025-11-10", "On Passage of the Bill H.R. 5371", "Not Voting"),
			},
			want: []pick{{"hr5371", scoring.NotVoting, 618}},
		},
		{
			name: "only procedural votes: no position",
			candidates: []scoring.Candidate{
				rc("sjres59", "Senate", 1, 380, "2025-06-27", "On the Motion to Discharge S.J.Res. 59", "Yea"),
				rc("s5", "Senate", 1, 5, "2025-01-09", "On the Cloture Motion S. 5", "Yea"),
				rc("s1071", "Senate", 1, 640, "2025-12-15",
					"On the Motion (Motion to Waive All Applicable Budgetary Discipline Re: Amdt. No. 1)", "Nay"),
			},
			want: []pick{},
		},
		{
			name: "session breaks a same-date tie",
			candidates: []scoring.Candidate{
				rc("hr9", "House", 2, 1, "2026-01-03", "On Passage", "Nay"),
				rc("hr9", "House", 1, 400, "2026-01-03", "On Passage", "Yea"),
			},
			want: []pick{{"hr9", scoring.Nay, 1}},
		},
		{
			name: "rows without a bill are ignored",
			candidates: []scoring.Candidate{
				rc("", "House", 1, 2, "2025-01-03", "On Passage", "Yea"),
			},
			want: []pick{},
		},
		{
			name: "several bills come back newest first, one each",
			candidates: []scoring.Candidate{
				rc("hr1", "House", 1, 98, "2025-05-22", "On Passage", "Aye"),
				rc("hres5", "House", 1, 6, "2025-01-03", "On Agreeing to the Resolution", "Nay"),
				rc(
					"hr3",
					"House",
					1,
					150,
					"2025-06-01",
					"On Motion to Suspend the Rules and Pass, as Amended",
					"Present",
				),
				rc("hr4", "House", 1, 150, "2025-06-01", "On Passage", "Johnson (LA)"),
			},
			want: []pick{
				{"hr3", scoring.Present, 150},
				{"hr4", scoring.Other, 150},
				{"hr1", scoring.Yea, 98},
				{"hres5", scoring.Nay, 6},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := picks(scoring.PickPositions(tt.candidates))
			if len(got) != len(tt.want) {
				t.Fatalf("PickPositions = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("PickPositions[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestPickPositionsDifferentCongresses(t *testing.T) {
	old := rc("hr1-118", "House", 2, 10, "2024-02-01", "On Passage", "Yea")
	old.Congress = 118
	cur := rc("hr1-119", "House", 1, 98, "2025-05-22", "On Passage", "Nay")

	got := scoring.PickPositions([]scoring.Candidate{old, cur})
	if len(got) != 2 {
		t.Fatalf("PickPositions returned %d positions, want one per congress's bill", len(got))
	}
	if got[0].Congress != 119 || got[1].Congress != 118 {
		t.Errorf("congresses = %d, %d; want 119, 118", got[0].Congress, got[1].Congress)
	}
}

func TestPickPositionsKeepsFields(t *testing.T) {
	in := rc("hr1", "House", 1, 98, "2025-05-22", "On Passage", "Aye")
	in.BillTitle = "One Big Beautiful Bill Act"

	got := scoring.PickPositions([]scoring.Candidate{in})
	want := in
	want.Vote = scoring.Yea
	if len(got) != 1 || got[0] != want {
		t.Errorf("PickPositions = %+v, want %+v", got, want)
	}
	if in.Vote != "Aye" {
		t.Errorf("PickPositions changed its input: vote %q", in.Vote)
	}
}
