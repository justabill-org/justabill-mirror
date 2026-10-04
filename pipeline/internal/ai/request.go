package ai

import (
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/genai"
)

// flexHeader asks Vertex AI for Flex PayGo.
const flexHeader = "X-Vertex-Ai-Llm-Shared-Request-Type"

// Request is one GenerateContent call: the model, the prompt and the request config, with the
// provenance stored alongside its answer. The synchronous call and a Vertex AI batch input line are
// both built from it, so both send the same request (docs/design/198-corpus-resummarization.md).
type Request struct {
	Model          string
	PromptVersion  string
	RequestType    string
	InputTruncated bool
	Contents       []*genai.Content
	// Config holds the system instruction, JSON schema, thinking level, output cap and safety
	// settings, plus the Flex header when RequestType is flex.
	Config *genai.GenerateContentConfig
}

// BillRequest is the request that summarizes one bill with prompt bill-v4. Empty settings in c get
// their defaults. With the CRS context off, bc's CRS summary is left out, and with the rule context
// off, its disapproved rule.
func (c Config) BillRequest(bc BillContext) (*Request, error) {
	if strings.TrimSpace(bc.Text) == "" {
		return nil, fmt.Errorf("bill %s: no text to summarize", bc.BillID)
	}
	c = c.withDefaults()
	if !c.CRSContext() {
		bc.CRSSummary = nil
	}
	if !c.RuleContext() {
		bc.Rule = nil
	}
	prompt, truncated := billPrompt(bc, c.MaxInputTokens)
	return c.request(PromptVersionBill, systemInstructionBill, prompt, billSummarySchema(), truncated), nil
}

// diffRequest is the request that summarizes a change between versions with prompt diff-v2.
func (c Config) diffRequest(dc DiffContext) (*Request, error) {
	if strings.TrimSpace(dc.Diff) == "" {
		return nil, fmt.Errorf("diff %s: no changes to summarize", dc.DiffID)
	}
	c = c.withDefaults()
	prompt, truncated := diffPrompt(dc, c.MaxInputTokens)
	return c.request(PromptVersionDiff, systemInstructionDiff, prompt, diffSummarySchema(), truncated), nil
}

func (c Config) request(promptVersion, system, prompt string, schema *genai.Schema, truncated bool) *Request {
	return &Request{
		Model:          c.Model,
		PromptVersion:  promptVersion,
		RequestType:    c.RequestType,
		InputTruncated: truncated,
		Contents:       genai.Text(prompt),
		Config:         c.requestConfig(system, schema),
	}
}

// provenance is the part of a Result the request fixes before the call.
func (r *Request) provenance() Result {
	return Result{
		Model:          r.Model,
		PromptVersion:  r.PromptVersion,
		RequestType:    r.RequestType,
		InputTruncated: r.InputTruncated,
	}
}

// requestConfig is the request config: system instruction, JSON schema, thinking level, output cap
// and explicit safety settings. No temperature: Google advises the default on Gemini 3.
func (c Config) requestConfig(system string, schema *genai.Schema) *genai.GenerateContentConfig {
	categories := []genai.HarmCategory{
		genai.HarmCategoryHateSpeech,
		genai.HarmCategoryDangerousContent,
		genai.HarmCategoryHarassment,
		genai.HarmCategorySexuallyExplicit,
	}
	safety := make([]*genai.SafetySetting, 0, len(categories))
	for _, cat := range categories {
		safety = append(safety, &genai.SafetySetting{Category: cat, Threshold: genai.HarmBlockThresholdOff})
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(system, genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    schema,
		ThinkingConfig:    &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevel(c.ThinkingLevel)},
		MaxOutputTokens:   c.MaxOutputTokens,
		SafetySettings:    safety,
	}
	if c.RequestType == RequestTypeFlex {
		config.HTTPOptions = &genai.HTTPOptions{Headers: http.Header{flexHeader: []string{RequestTypeFlex}}}
	}
	return config
}
