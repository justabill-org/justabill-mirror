package batchjsonl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/genai"

	"github.com/justabill-org/justabill/pipeline/internal/ai"
)

// maxReasonLen caps the per-line error text kept in a Line's reason, as the synchronous path caps
// call errors.
const maxReasonLen = 300

// Line is one output line, matched to its manifest item and classified.
type Line struct {
	Item

	// Summary is set for every outcome, with RequestType batch; the summary fields are set only
	// when its Outcome is ok.
	Summary *ai.BillSummary
	// Err is an *ai.AttemptError when the outcome isn't ok.
	Err error
	// Failed is set when Vertex AI answered the line with an error status or no response: the
	// request failed before any model answer, so it's worth trying again at once.
	Failed bool
}

// Import matches a batch job's output lines to the manifest written with its input.
type Import struct {
	items     map[int]Item
	seen      map[int]bool
	unmatched int
}

// ReadManifest reads a manifest written by an Encoder.
func ReadManifest(r io.Reader) (*Import, error) {
	im := &Import{items: map[int]Item{}, seen: map[int]bool{}}
	err := eachLine(r, func(raw []byte) error {
		var item Item
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("manifest line %d: %w", len(im.items)+1, err)
		}
		if item.Line <= 0 || item.BillID == "" {
			return fmt.Errorf("manifest line %d: no line number or bill", len(im.items)+1)
		}
		if _, dup := im.items[item.Line]; dup {
			return fmt.Errorf("manifest line %d: line number %d twice", len(im.items)+1, item.Line)
		}
		im.items[item.Line] = item
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	return im, nil
}

// Len is the number of manifest items.
func (im *Import) Len() int { return len(im.items) }

// Read reads one output file and calls fn for each line whose label names a manifest item, in the
// file's order. Call it once per output file. A line that isn't JSON or whose label names no item
// is skipped and counted in Unmatched: without its label it can't be tied to a bill. A line seen
// again is passed to fn again (the import is idempotent). An error from fn stops Read and is
// returned.
func (im *Import) Read(output io.Reader, fn func(Line) error) error {
	return eachLine(output, func(raw []byte) error {
		item, out, ok := im.match(raw)
		if !ok {
			im.unmatched++
			return nil
		}
		im.seen[item.Line] = true
		summary, attemptErr := classify(item, out)
		return fn(Line{Item: item, Summary: summary, Err: attemptErr, Failed: out.failed()})
	})
}

// match decodes an output line and finds its manifest item through the line label.
func (im *Import) match(raw []byte) (Item, outputLine, bool) {
	var out outputLine
	if json.Unmarshal(raw, &out) != nil {
		return Item{}, out, false
	}
	n, err := strconv.Atoi(out.Request.Labels[LabelKey])
	if err != nil {
		return Item{}, out, false
	}
	item, ok := im.items[n]
	return item, out, ok
}

// Missing is the manifest items no output line has matched so far, in line order. After the last
// output file they're the bills the job didn't answer, which go back to the synchronous queue.
func (im *Import) Missing() []Item {
	var missing []Item
	for n, item := range im.items {
		if !im.seen[n] {
			missing = append(missing, item)
		}
	}
	slices.SortFunc(missing, func(a, b Item) int { return a.Line - b.Line })
	return missing
}

// Unmatched is how many output lines couldn't be tied to a manifest item.
func (im *Import) Unmatched() int { return im.unmatched }

// outputLine is one line of batch output: the request as sent, the response, and a status that is
// empty on success and holds the error otherwise. Only the request's labels are read.
type outputLine struct {
	Status  string `json:"status"`
	Request struct {
		Labels map[string]string `json:"labels"`
	} `json:"request"`
	Response json.RawMessage `json:"response"`
}

// failed reports whether the line carries an error status or no response.
func (out outputLine) failed() bool {
	return out.Status != "" || len(out.Response) == 0 || bytes.Equal(out.Response, []byte("null"))
}

// classify gives an output line the outcome the synchronous path gives the same answer. A line
// with an error status or no response is an error, as a failed call is.
func classify(item Item, out outputLine) (*ai.BillSummary, error) {
	prov := ai.Result{
		Model:          item.Model,
		PromptVersion:  item.PromptVersion,
		RequestType:    ai.RequestTypeBatch,
		InputTruncated: item.InputTruncated,
	}
	if out.Status != "" {
		return failed(prov, statusReason(out.Status))
	}
	if out.failed() {
		return failed(prov, "no response")
	}
	var resp genai.GenerateContentResponse
	if err := json.Unmarshal(out.Response, &resp); err != nil {
		return failed(prov, truncate("decode response: "+err.Error()))
	}
	return ai.ReadBillResponse(prov, &resp)
}

// failed is an error outcome with reason.
func failed(prov ai.Result, reason string) (*ai.BillSummary, error) {
	summary := &ai.BillSummary{Result: prov}
	summary.Outcome, summary.Reason = ai.OutcomeError, reason
	return summary, &ai.AttemptError{Outcome: ai.OutcomeError, Reason: reason}
}

// statusReason is a line's error status in the synchronous path's form ("http 400
// INVALID_ARGUMENT") when it embeds an API error, and the status itself, capped, when it doesn't.
func statusReason(status string) string {
	if i := strings.IndexByte(status, '{'); i >= 0 {
		var body struct {
			Error struct {
				Code   int    `json:"code"`
				Status string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(status[i:]), &body) == nil && body.Error.Code != 0 {
			return fmt.Sprintf("http %d %s", body.Error.Code, body.Error.Status)
		}
	}
	return truncate("batch: " + status)
}

func truncate(s string) string {
	if len(s) > maxReasonLen {
		s = strings.ToValidUTF8(s[:maxReasonLen], "")
	}
	return s
}

// eachLine calls fn with each non-blank line of r. Lines have no length limit: an output line
// echoes its request, bill text included.
func eachLine(r io.Reader, fn func([]byte) error) error {
	br := bufio.NewReader(r)
	for {
		raw, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(raw)) > 0 {
			if fnErr := fn(raw); fnErr != nil {
				return fnErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read line: %w", err)
		}
	}
}
