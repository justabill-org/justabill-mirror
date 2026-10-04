package batchjsonl_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/ai/batchjsonl"
)

func TestEncode_Errors(t *testing.T) {
	enc := batchjsonl.NewEncoder(io.Discard, io.Discard)
	if _, err := enc.Encode(batchjsonl.Bill{BillID: "hr-119-1"}, nil); err == nil {
		t.Error("nil request: want an error")
	}
	if _, err := enc.Encode(batchjsonl.Bill{BillID: "hr-119-1"}, &ai.Request{}); err == nil {
		t.Error("request without config: want an error")
	}
	if enc.Lines() != 0 {
		t.Errorf("Lines() = %d after failed encodes, want 0", enc.Lines())
	}

	req, err := ai.Config{}.BillRequest(testBills()[0])
	if err != nil {
		t.Fatal(err)
	}
	broken := batchjsonl.NewEncoder(failingWriter{}, io.Discard)
	if _, err = broken.Encode(batchjsonl.Bill{BillID: "hr-119-1"}, req); err == nil {
		t.Error("failing input writer: want an error")
	}
	broken = batchjsonl.NewEncoder(io.Discard, failingWriter{})
	if _, err = broken.Encode(batchjsonl.Bill{BillID: "hr-119-1"}, req); err == nil {
		t.Error("failing manifest writer: want an error")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestBillRequest_NoText(t *testing.T) {
	if _, err := (ai.Config{}).BillRequest(ai.BillContext{BillID: "hr-119-1", Text: " \n"}); err == nil {
		t.Error("want an error for a bill with no text")
	}
}

func TestReadManifest_Errors(t *testing.T) {
	tests := map[string]string{
		"not json":       "{\n",
		"no line number": `{"bill_id":"hr-119-1"}` + "\n",
		"no bill":        `{"line":1}` + "\n",
		"duplicate line": `{"bill_id":"hr-119-1","line":1}` + "\n" + `{"bill_id":"hr-119-2","line":1}` + "\n",
	}
	for name, manifest := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := batchjsonl.ReadManifest(strings.NewReader(manifest)); err == nil {
				t.Error("want an error")
			}
		})
	}
	if _, err := batchjsonl.ReadManifest(io.MultiReader(strings.NewReader(`{"bill_id":"a","line":1}`),
		errReader{})); err == nil {
		t.Error("read error: want an error")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// TestImport_EdgeLines covers a line longer than [bufio.Scanner]'s limit with no trailing newline,
// a line with neither status nor response, a response that isn't JSON, a status without an API
// error in it, and a line seen twice.
func TestImport_EdgeLines(t *testing.T) {
	manifest := `{"bill_id":"hr-119-1","line":1,"model":"m","prompt_version":"bill-v4"}
{"bill_id":"hr-119-2","line":2,"model":"m","prompt_version":"bill-v4"}

{"bill_id":"hr-119-3","line":3,"model":"m","prompt_version":"bill-v4"}
{"bill_id":"hr-119-4","line":4,"model":"m","prompt_version":"bill-v4"}
`
	im, err := batchjsonl.ReadManifest(strings.NewReader(manifest))
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 200_000)
	output := `{"status":"","request":{"labels":{"jab_line":"2"}}}
{"status":"","request":{"labels":{"jab_line":"3"}},"response":"not a response"}
{"status":"","request":{"labels":{"jab_line":"2"}},"response":null}
{"status":"Internal error occurred.","request":{"labels":{"jab_line":"4"}}}
{"status":"","request":{"contents":[{"parts":[{"text":"` + long + `"}]}],"labels":{"jab_line":"1"}},` +
		`"response":{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"OTHER"}]}}`

	got := map[int][]string{}
	err = im.Read(strings.NewReader(output), func(l batchjsonl.Line) error {
		got[l.Line] = append(got[l.Line], string(l.Summary.Outcome)+": "+l.Summary.Reason)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int][]string{
		1: {"error: OTHER"},
		2: {"error: no response", "error: no response"},
		3: {"error: decode response: json: cannot unmarshal string"},
		4: {"error: batch: Internal error occurred."},
	}
	for n, w := range want {
		// Reasons are compared by prefix: a JSON error names the SDK's internal types.
		if len(got[n]) != len(w) || !strings.HasPrefix(strings.Join(got[n], "|"), strings.Join(w, "|")) {
			t.Errorf("line %d: got %q, want %q", n, got[n], w)
		}
	}
	if len(im.Missing()) != 0 || im.Unmatched() != 0 {
		t.Errorf("Missing() = %v, Unmatched() = %d, want none", im.Missing(), im.Unmatched())
	}
}

func TestImport_StopsOnCallbackError(t *testing.T) {
	im, err := batchjsonl.ReadManifest(strings.NewReader(`{"bill_id":"hr-119-1","line":1}` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("spanner unavailable")
	line := `{"status":"oops","request":{"labels":{"jab_line":"1"}}}` + "\n"
	calls := 0
	err = im.Read(strings.NewReader(line+line), func(batchjsonl.Line) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("Read = %v after %d calls, want the callback's error after 1", err, calls)
	}
	if err = im.Read(errReader{}, func(batchjsonl.Line) error { return nil }); err == nil {
		t.Error("read error: want an error")
	}
}
