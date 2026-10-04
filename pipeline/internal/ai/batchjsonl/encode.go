// Package batchjsonl writes bill summary requests as Vertex AI batch prediction input (JSONL) and
// reads the job's output back: each output line is matched to its bill through the label its
// echoed request carries, and classified by ai.ReadBillResponse, as the synchronous
// ai.Summarizer.SummarizeBill would classify the same response
// (docs/design/198-corpus-resummarization.md, "The Batch path").
package batchjsonl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"

	"google.golang.org/genai"

	"github.com/justabill-org/justabill/pipeline/internal/ai"
)

// LabelKey is the request label that carries an input line's number. Vertex AI promises neither
// the output's order nor a custom key, but it echoes each request, labels included.
const LabelKey = "jab_line"

// Bill is the text version of a bill that one input line summarizes.
type Bill struct {
	BillID      string `json:"bill_id"`
	VersionID   string `json:"version_id"`
	VersionCode string `json:"version_code"`
	// ContentHash is the hash of the text the request was built from; the summary is stored with it.
	ContentHash string `json:"content_hash"`
	// CRSHash is the content hash of the CRS summary the request carried, empty for none; the
	// summary is stored with it, so a newer CRS summary queues the bill again.
	CRSHash string `json:"crs_hash,omitempty"`
	// RuleHash is the context hash of the disapproved rule the request carried, empty for none; the
	// summary is stored with it, so a changed rule queues the bill again.
	RuleHash string `json:"rule_hash,omitempty"`
}

// Item is one manifest line: an input line's number, its bill, and the provenance of its request.
type Item struct {
	Bill

	Line           int    `json:"line"`
	Model          string `json:"model"`
	PromptVersion  string `json:"prompt_version"`
	InputTruncated bool   `json:"input_truncated"`
}

// Encoder writes batch input lines and their manifest lines, numbering them from 1.
type Encoder struct {
	input    *json.Encoder
	manifest *json.Encoder
	lines    int
}

// NewEncoder returns an Encoder that writes the batch input to input and the manifest to manifest.
func NewEncoder(input, manifest io.Writer) *Encoder {
	in, man := json.NewEncoder(input), json.NewEncoder(manifest)
	in.SetEscapeHTML(false)
	man.SetEscapeHTML(false)
	return &Encoder{input: in, manifest: man}
}

// Encode writes req as the next input line, labeled with its number, and the manifest line that
// maps the number to b. req is built by ai.Config.BillRequest, as the synchronous path's is.
func (e *Encoder) Encode(b Bill, req *ai.Request) (Item, error) {
	if req == nil {
		return Item{}, errors.New("encode batch line: no request")
	}
	item := Item{
		Bill:           b,
		Line:           e.lines + 1,
		Model:          req.Model,
		PromptVersion:  req.PromptVersion,
		InputTruncated: req.InputTruncated,
	}
	body, err := requestBody(req, item.Line)
	if err != nil {
		return Item{}, fmt.Errorf("encode batch line %d (%s): %w", item.Line, b.BillID, err)
	}
	if err = e.input.Encode(inputLine{Request: body}); err != nil {
		return Item{}, fmt.Errorf("write batch line %d: %w", item.Line, err)
	}
	if err = e.manifest.Encode(item); err != nil {
		return Item{}, fmt.Errorf("write manifest line %d: %w", item.Line, err)
	}
	e.lines = item.Line
	return item, nil
}

// Lines is how many lines have been written.
func (e *Encoder) Lines() int { return e.lines }

// inputLine is one line of batch input: a GenerateContentRequest in Vertex AI's REST form.
type inputLine struct {
	Request *requestJSON `json:"request"`
}

// requestJSON is the GenerateContentRequest body the Vertex AI backend of the genai SDK sends for a
// GenerateContent call, plus labels. The fields that belong to the request rather than to its
// generationConfig (system instruction, safety settings, labels) sit at the top level, as the SDK
// puts them.
type requestJSON struct {
	Contents          []*genai.Content        `json:"contents"`
	SystemInstruction *genai.Content          `json:"systemInstruction,omitempty"`
	GenerationConfig  *genai.GenerationConfig `json:"generationConfig,omitempty"`
	SafetySettings    []*genai.SafetySetting  `json:"safetySettings,omitempty"`
	Labels            map[string]string       `json:"labels"`
}

// requestBody is req in REST form, labeled with its line number. The generation config goes
// through the SDK's own Vertex AI conversion. A batch request carries no headers, so a Flex setting
// on req is dropped: batch has its own price.
func requestBody(req *ai.Request, line int) (*requestJSON, error) {
	if req.Config == nil {
		return nil, errors.New("request has no config")
	}
	rest := *req.Config
	rest.SystemInstruction, rest.SafetySettings, rest.Labels, rest.HTTPOptions = nil, nil, nil, nil
	gen, err := rest.ToGenerationConfig(genai.BackendVertexAI)
	if err != nil {
		return nil, fmt.Errorf("convert generation config: %w", err)
	}
	labels := make(map[string]string, len(req.Config.Labels)+1)
	maps.Copy(labels, req.Config.Labels)
	labels[LabelKey] = strconv.Itoa(line)
	return &requestJSON{
		Contents:          req.Contents,
		SystemInstruction: req.Config.SystemInstruction,
		GenerationConfig:  gen,
		SafetySettings:    req.Config.SafetySettings,
		Labels:            labels,
	}, nil
}
