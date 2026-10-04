package ai

import (
	"strings"
	"testing"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestConfigFrom_Defaults(t *testing.T) {
	got, err := ConfigFrom(envFrom(map[string]string{"gcp_project": "p"}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Project: "p", Location: "global", Model: "gemini-3.8-flash", ThinkingLevel: "LOW",
		MaxOutputTokens: 8192, MaxInputTokens: 400_000, RequestType: "standard",
	}
	if got != want {
		t.Errorf("ConfigFrom = %+v, want %+v", got, want)
	}
	if !got.CRSContext() {
		t.Error("CRS context is off by default, want on")
	}
	if !got.RuleContext() {
		t.Error("rule context is off by default, want on")
	}
	if got.timeout() != standardTimeout {
		t.Errorf("timeout = %s, want %s", got.timeout(), standardTimeout)
	}
}

func TestConfigFrom_Overrides(t *testing.T) {
	got, err := ConfigFrom(envFrom(map[string]string{
		"gcp_project": "p", "gcp_location": "global", "ai_model": "gemini-3.5-flash",
		"ai_thinking_level": "medium", "ai_max_output_tokens": "4096", "ai_max_input_tokens": "100000",
		"ai_request_type": "Flex", "ai_crs_context": "false", "ai_rule_context": "false",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Project: "p", Location: "global", Model: "gemini-3.5-flash", ThinkingLevel: "MEDIUM",
		MaxOutputTokens: 4096, MaxInputTokens: 100_000, RequestType: "flex", NoCRSContext: true,
		NoRuleContext: true,
	}
	if got != want {
		t.Errorf("ConfigFrom = %+v, want %+v", got, want)
	}
	if got.timeout() != flexTimeout {
		t.Errorf("flex timeout = %s, want %s", got.timeout(), flexTimeout)
	}
}

func TestConfigFrom_Invalid(t *testing.T) {
	tests := map[string]struct {
		env  map[string]string
		want string
	}{
		"thinking level": {map[string]string{"ai_thinking_level": "LOTS"}, "AI_THINKING_LEVEL"},
		"request type":   {map[string]string{"ai_request_type": "batch"}, "AI_REQUEST_TYPE"},
		"flex off global": {
			map[string]string{"ai_request_type": "flex", "gcp_location": "us"},
			"GCP_LOCATION=global",
		},
		"output not a number": {map[string]string{"ai_max_output_tokens": "lots"}, "AI_MAX_OUTPUT_TOKENS"},
		"output zero":         {map[string]string{"ai_max_output_tokens": "0"}, "AI_MAX_OUTPUT_TOKENS"},
		"input negative":      {map[string]string{"ai_max_input_tokens": "-5"}, "AI_MAX_INPUT_TOKENS"},
		"crs context":         {map[string]string{"ai_crs_context": "sometimes"}, "AI_CRS_CONTEXT"},
		"rule context":        {map[string]string{"ai_rule_context": "maybe"}, "AI_RULE_CONTEXT"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ConfigFrom(envFrom(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ConfigFrom error = %v, want one naming %s", err, tt.want)
			}
		})
	}
}

func TestNewSummarizer_NeedsProject(t *testing.T) {
	if _, err := NewSummarizer(t.Context(), Config{}); err == nil {
		t.Error("NewSummarizer without a project succeeded")
	}
}

func TestNewBatchJobs_NeedsProject(t *testing.T) {
	if _, err := NewBatchJobs(t.Context(), " ", "global"); err == nil {
		t.Error("NewBatchJobs without a project succeeded")
	}
}
