package ai

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"
)

// Defaults for Config, from the #68 design (docs/design/68-ai-summaries-gemini-3.md).
// DefaultModel is gemini-3.8-flash, chosen by the 50-bill eval (#193). It's a short-term
// model (45 days' retirement notice, price doubling in 2027); AI_MODEL=gemini-3.5-flash switches back.
const (
	DefaultModel           = "gemini-3.8-flash"
	DefaultLocation        = "global"
	DefaultThinkingLevel   = "LOW"
	DefaultMaxOutputTokens = 8192
	DefaultMaxInputTokens  = 400_000

	// RequestTypeStandard is Standard PayGo.
	RequestTypeStandard = "standard"
	// RequestTypeFlex is Flex PayGo (Preview): half price, more throttling, global endpoint only.
	RequestTypeFlex = "flex"
	// RequestTypeBatch is a Vertex AI batch prediction job. It's never an AI_REQUEST_TYPE value: the
	// batch import sets it on the results it reads.
	RequestTypeBatch = "batch"

	standardTimeout = 2 * time.Minute
	flexTimeout     = 10 * time.Minute
)

// Config holds the summarizer's settings. The zero value of a field means its default.
type Config struct {
	// Project is the GCP project for Vertex AI. Required by NewSummarizer.
	Project string
	// Location is the Vertex AI location. Gemini 3 models are served from global, us and eu.
	Location string
	// Model is the pinned model ID, stored with each summary.
	Model string
	// ThinkingLevel is MINIMAL, LOW, MEDIUM or HIGH. Thinking tokens count as output.
	ThinkingLevel string
	// MaxOutputTokens caps visible output plus thinking.
	MaxOutputTokens int32
	// MaxInputTokens caps the estimated prompt size; longer bill text is cut at a section edge.
	MaxInputTokens int
	// RequestType is standard or flex.
	RequestType string
	// NoCRSContext leaves the CRS summary out of the prompt, and CRS changes then don't make bills
	// due (AI_CRS_CONTEXT=false; docs/design/197-crs-summaries.md). The zero value sends it.
	NoCRSContext bool
	// NoRuleContext leaves a CRA resolution's disapproved rule out of the prompt, and rule changes
	// then don't make bills due (AI_RULE_CONTEXT=false; docs/design/590-cra-disapproved-rules.md).
	// The zero value sends it.
	NoRuleContext bool
}

// CRSContext reports whether bill prompts carry the bill's CRS summary.
func (c Config) CRSContext() bool { return !c.NoCRSContext }

// RuleContext reports whether bill prompts carry the rule a CRA resolution disapproves.
func (c Config) RuleContext() bool { return !c.NoRuleContext }

// withDefaults returns c with empty fields set to their defaults and names normalized.
func (c Config) withDefaults() Config {
	c.Location = strings.TrimSpace(c.Location)
	if c.Location == "" {
		c.Location = DefaultLocation
	}
	c.Model = strings.TrimSpace(c.Model)
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.ThinkingLevel == "" {
		c.ThinkingLevel = DefaultThinkingLevel
	}
	c.ThinkingLevel = strings.ToUpper(c.ThinkingLevel)
	if c.MaxOutputTokens == 0 {
		c.MaxOutputTokens = DefaultMaxOutputTokens
	}
	if c.MaxInputTokens == 0 {
		c.MaxInputTokens = DefaultMaxInputTokens
	}
	if c.RequestType == "" {
		c.RequestType = RequestTypeStandard
	}
	c.RequestType = strings.ToLower(c.RequestType)
	return c
}

// validate reports the first setting that can't work. c must have its defaults applied.
func (c Config) validate() error {
	levels := []genai.ThinkingLevel{
		genai.ThinkingLevelMinimal, genai.ThinkingLevelLow, genai.ThinkingLevelMedium, genai.ThinkingLevelHigh,
	}
	if !slices.Contains(levels, genai.ThinkingLevel(c.ThinkingLevel)) {
		return fmt.Errorf("AI_THINKING_LEVEL %q: want MINIMAL, LOW, MEDIUM or HIGH", c.ThinkingLevel)
	}
	if c.MaxOutputTokens < 0 {
		return fmt.Errorf("AI_MAX_OUTPUT_TOKENS %d: must be positive", c.MaxOutputTokens)
	}
	if c.MaxInputTokens < 0 {
		return fmt.Errorf("AI_MAX_INPUT_TOKENS %d: must be positive", c.MaxInputTokens)
	}
	switch c.RequestType {
	case RequestTypeStandard:
	case RequestTypeFlex:
		if c.Location != DefaultLocation {
			return fmt.Errorf("AI_REQUEST_TYPE flex needs GCP_LOCATION=global, not %q", c.Location)
		}
	default:
		return fmt.Errorf("AI_REQUEST_TYPE %q: want standard or flex", c.RequestType)
	}
	return nil
}

// timeout is the per-attempt deadline for one GenerateContent call.
func (c Config) timeout() time.Duration {
	if c.RequestType == RequestTypeFlex {
		return flexTimeout
	}
	return standardTimeout
}

// ConfigFrom reads the summarizer's settings with get, which takes an env name in lower case as
// viper.GetString does ("gcp_project" for GCP_PROJECT). Unset values get their defaults; values
// that can't work are an error, so a bad deploy fails at startup rather than on the first call.
func ConfigFrom(get func(key string) string) (Config, error) {
	cfg := Config{
		Project:       strings.TrimSpace(get("gcp_project")),
		Location:      get("gcp_location"),
		Model:         get("ai_model"),
		ThinkingLevel: strings.TrimSpace(get("ai_thinking_level")),
		RequestType:   strings.TrimSpace(get("ai_request_type")),
	}
	if v := strings.TrimSpace(get("ai_max_output_tokens")); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("AI_MAX_OUTPUT_TOKENS %q: want a positive integer", v)
		}
		cfg.MaxOutputTokens = int32(n)
	}
	if v := strings.TrimSpace(get("ai_max_input_tokens")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("AI_MAX_INPUT_TOKENS %q: want a positive integer", v)
		}
		cfg.MaxInputTokens = n
	}
	if v := strings.TrimSpace(get("ai_crs_context")); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("AI_CRS_CONTEXT %q: want true or false", v)
		}
		cfg.NoCRSContext = !on
	}
	if v := strings.TrimSpace(get("ai_rule_context")); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("AI_RULE_CONTEXT %q: want true or false", v)
		}
		cfg.NoRuleContext = !on
	}
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// NewBatchJobs returns the Vertex AI batch job client for project in location, with Application
// Default Credentials (docs/design/198-corpus-resummarization.md).
func NewBatchJobs(ctx context.Context, project, location string) (*genai.Batches, error) {
	if strings.TrimSpace(project) == "" {
		return nil, errors.New("vertex ai batch: GCP project is required")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project: project, Location: location, Backend: genai.BackendVertexAI,
	})
	if err != nil {
		return nil, fmt.Errorf("create vertex ai batch client: %w", err)
	}
	return client.Batches, nil
}
