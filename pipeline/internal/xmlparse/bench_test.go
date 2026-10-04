package xmlparse_test

// Benchmarks for the roll-call parsers (#873), on full-size votes: the fixtures' members repeated
// to a full House (435) and a full Senate (100). Run them with `task bench`.

import (
	"bytes"
	"os"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/xmlparse"
)

const (
	houseSeats  = 435
	senateSeats = 100
)

// fullRollCall reads a fixture and repeats its first <elem> element until the document holds n,
// in place of the fixture's own: the parser's work grows with the members, not their names.
func fullRollCall(b *testing.B, name, elem string, n int) []byte {
	b.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		b.Fatal(err)
	}
	open, closing := []byte("<"+elem+">"), []byte("</"+elem+">")
	first, last := bytes.Index(data, open), bytes.LastIndex(data, closing)
	end := bytes.Index(data, closing)
	if first < 0 || last < 0 {
		b.Fatalf("%s has no <%s>", name, elem)
	}
	one := data[first : end+len(closing)]
	var out bytes.Buffer
	out.Write(data[:first])
	for range n {
		out.Write(one)
		out.WriteByte('\n')
	}
	out.Write(data[last+len(closing):])
	return out.Bytes()
}

func BenchmarkParseHouseVote(b *testing.B) {
	data := fullRollCall(b, "house_2025_roll021_recorded.xml", "recorded-vote", houseSeats)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		v, err := xmlparse.ParseHouseVote(data)
		if err != nil || len(v.Votes) != houseSeats {
			b.Fatalf("ParseHouseVote: %v (votes: %d)", err, len(v.Votes))
		}
	}
}

func BenchmarkParseSenateVote(b *testing.B) {
	data := fullRollCall(b, "senate_vote_119_1_00372.xml", "member", senateSeats)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		v, err := xmlparse.ParseSenateVote(data)
		if err != nil || len(v.Votes) != senateSeats {
			b.Fatalf("ParseSenateVote: %v (votes: %d)", err, len(v.Votes))
		}
	}
}
