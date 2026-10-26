package scoring_test

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/scoring"
)

// goldenLines calls fn with each case line of a tab-separated golden file, split into its
// columns, and the line's number.
func goldenLines(t *testing.T, path string, fn func(line int, cols []string)) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		cols := strings.Split(text, "\t")
		if len(cols) != goldenColumns {
			t.Fatalf("%s line %d: want %d tab-separated columns, got %d", path, line, goldenColumns, len(cols))
		}
		fn(line, cols)
	}
	if err = sc.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestIsQuorumCallGolden checks every case in testdata/quorum.tsv: the House's quorum-call
// forms, their case and whitespace variants, and votes that aren't quorum calls.
func TestIsQuorumCallGolden(t *testing.T) {
	counts := make(map[string]int)
	goldenLines(t, "testdata/quorum.tsv", func(line int, cols []string) {
		chamber, question, want := cols[0], cols[1], cols[2]
		if want != "quorum" && want != "vote" {
			t.Fatalf("line %d: want column is %q, not quorum or vote", line, want)
		}
		counts[chamber+" "+want]++
		if got := scoring.IsQuorumCall(chamber, question); got != (want == "quorum") {
			t.Errorf("line %d: IsQuorumCall(%q, %q) = %v, want %s (%s)", line, chamber, question, got, want, cols[3])
		}
	})
	for _, k := range []string{"House quorum", "House vote", "Senate vote"} {
		if counts[k] == 0 {
			t.Errorf("golden file has no %q cases", k)
		}
	}
}

// TestIsQuorumCallScorecardQuestions runs IsQuorumCall over every real question form in the
// scorecard's golden file: only the lines the Clerk typed QUORUM are quorum calls, so the
// Speaker election, every other House form and every Senate question count as votes.
func TestIsQuorumCallScorecardQuestions(t *testing.T) {
	quorum := 0
	goldenLines(t, "testdata/questions.tsv", func(line int, cols []string) {
		chamber, question, source := cols[0], cols[1], cols[3]
		want := chamber == "House" && strings.HasSuffix(source, "(QUORUM)")
		if want {
			quorum++
		}
		if got := scoring.IsQuorumCall(chamber, question); got != want {
			t.Errorf("line %d: IsQuorumCall(%q, %q) = %v, want %v (%s)", line, chamber, question, got, want, source)
		}
	})
	if quorum == 0 {
		t.Error("questions.tsv has no QUORUM lines")
	}
}

func TestIsQuorumCallFolding(t *testing.T) {
	tests := []struct {
		chamber, question string
		want              bool
	}{
		{"house", "call by states", true},
		{" HOUSE ", "Call\nof the\tHouse", true},
		// A quorum call is the whole question, not a prefix of a longer one.
		{"House", "Call of the House Motion", false},
		{"House", "On Motion to Dispense with the Call of the House", false},
		// Unknown chamber or empty question.
		{"Joint", "Call by States", false},
		{"", "Call of the House", false},
		{"House", "", false},
		{"House", "   ", false},
	}
	for _, tt := range tests {
		if got := scoring.IsQuorumCall(tt.chamber, tt.question); got != tt.want {
			t.Errorf("IsQuorumCall(%q, %q) = %v, want %v", tt.chamber, tt.question, got, tt.want)
		}
	}
}
