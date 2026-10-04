package diff_test

// Benchmarks for the text diff (#873): a real pair of short versions, and H.R. 1 of the 119th
// Congress as enrolled against a copy with every tenth section reworded and one inserted at the
// top, which renumbers the rest. Run them with `task bench`.

import (
	"compress/gzip"
	"io"
	"os"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/billtext"
	"github.com/justabill-org/justabill/pipeline/internal/diff"
)

// rewordEvery is how often the big benchmark's copy changes a section's text.
const rewordEvery = 10

// readGzipFixture parses a gzipped bill XML fixture.
func readGzipFixture(b *testing.B, path string) []billtext.Section {
	b.Helper()
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		b.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		b.Fatal(err)
	}
	return parse(b, data)
}

// reworded copies sections, rewording every rewordEvery-th unit's content (counted in document
// order through n) and leaving the rest as they were.
func reworded(sections []billtext.Section, n *int) []billtext.Section {
	out := make([]billtext.Section, len(sections))
	for i, s := range sections {
		*n++
		if *n%rewordEvery == 0 && s.Content != "" {
			s.Content += " as amended by this Act"
		}
		s.Children = reworded(s.Children, n)
		out[i] = s
	}
	return out
}

func benchDiff(b *testing.B, old, newer []billtext.Section) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := diff.ComputeDiff(old, newer); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComputeDiff(b *testing.B) {
	b.Run("hr815-ih-eh", func(b *testing.B) {
		benchDiff(b, parseFixture(b, "BILLS-118hr815ih.xml"), parseFixture(b, "BILLS-118hr815eh.xml"))
	})
	b.Run("hr1-enr-reworded", func(b *testing.B) {
		old := readGzipFixture(b, "../billtext/testdata/BILLS-119hr1enr.xml.gz")
		n := 0
		inserted := billtext.Section{ID: "new", Kind: "section", Enum: "Sec. 1.", Header: "Short title",
			Content: "This Act may be cited as the Benchmark Act."}
		newer := append([]billtext.Section{inserted}, reworded(old, &n)...)
		_, stats, err := diff.ComputeDiff(old, newer)
		if err != nil || stats.SectionsAdded == 0 || stats.SectionsModified == 0 {
			b.Fatalf("the copy should add and modify sections: %+v, %v", stats, err)
		}
		benchDiff(b, old, newer)
	})
}
