package billtext_test

// Benchmarks for bill text section parsing (#873): a short bill and the biggest fixture, H.R. 1
// of the 119th Congress as enrolled (1.75 MB of XML). Run them with `task bench`.

import (
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

func BenchmarkParseXML(b *testing.B) {
	for _, file := range []string{"BILLS-119hr22ih.xml.gz", "BILLS-119hr1enr.xml.gz"} {
		b.Run(file, func(b *testing.B) {
			data := readBillFixture(b, file)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := billtext.ParseXML(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
