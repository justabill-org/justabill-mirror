package main

import (
	"slices"
	"testing"
)

func TestCheckerFlagsTermsTheBillDoesNotUse(t *testing.T) {
	c := newChecker()
	tests := []struct {
		name        string
		summary     string
		source      string
		wantLoaded  []string
		wantParties []string
	}{
		{
			name:       "loaded terms",
			summary:    "This landmark bill makes Common Sense reforms and is controversial.",
			source:     "A BILL To amend title 5.",
			wantLoaded: []string{"common-sense", "controversial", "landmark"},
		},
		{
			name:    "the bill uses the term, in capitals",
			summary: "The Common-Sense Law Enforcement Act would repeal a 2022 law.",
			source:  "This Act may be cited as the COMMON-SENSE LAW ENFORCEMENT ACT.",
		},
		{
			name:        "party names",
			summary:     "Republicans and Democrats disagree; the GOP whip counted votes.",
			source:      "A BILL To amend title 5.",
			wantParties: []string{"Democrat", "GOP", "Republican"},
		},
		{
			name:    "lower-case democratic isn't a party",
			summary: "It supports democratic institutions and a historical society.",
			source:  "A BILL",
		},
		{
			name:        "a party name the bill uses is quoting",
			summary:     "It renames the Democratic Party headquarters post office; Republicans are not mentioned.",
			source:      "the Democratic Party",
			wantParties: []string{"Republican"},
		},
		{
			name:       "hyphen and space spellings",
			summary:    "A job killing mandate and a much needed fix.",
			source:     "A BILL",
			wantLoaded: []string{"job-killing", "much-needed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loaded, parties := c.check(tt.summary, tt.source)
			if !slices.Equal(loaded, tt.wantLoaded) {
				t.Errorf("loaded terms = %v, want %v", loaded, tt.wantLoaded)
			}
			if !slices.Equal(parties, tt.wantParties) {
				t.Errorf("party names = %v, want %v", parties, tt.wantParties)
			}
		})
	}
}
