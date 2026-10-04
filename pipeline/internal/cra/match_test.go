package cra_test

import (
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/cra"
)

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "en dash and colon",
			in:   "Consumer Financial Protection Circular 2023–02: Reopening Deposit Accounts",
			want: "consumer financial protection circular 2023 02 reopening deposit accounts",
		},
		{
			name: "hyphen",
			in:   "consumer financial protection circular 2023-02 reopening deposit accounts",
			want: "consumer financial protection circular 2023 02 reopening deposit accounts",
		},
		{
			name: "curly quotes",
			in:   "Definition of “Waters of the United States”",
			want: "definition of waters of the united states",
		},
		{
			name: "straight quotes",
			in:   `Definition of "Waters of the United States"`,
			want: "definition of waters of the united states",
		},
		{name: "entity", in: "Safety &amp; Soundness", want: "safety soundness"},
		{name: "semicolon", in: "Rules; Withdrawal", want: "rules withdrawal"},
		{name: "case", in: "OVERDRAFT Lending", want: "overdraft lending"},
		{name: "whitespace", in: "  Overdraft \t Lending\n ", want: "overdraft lending"},
		{name: "empty", in: "", want: ""},
		{name: "punctuation only", in: " :;–— ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cra.NormalizeTitle(tt.in); got != tt.want {
				t.Errorf("NormalizeTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeTitle_Equal(t *testing.T) {
	a := cra.NormalizeTitle("Consumer Financial Protection Circular 2023–02: Reopening Deposit Accounts")
	b := cra.NormalizeTitle("consumer financial protection circular 2023 02 reopening deposit accounts")
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
}

func TestTitleOverlap(t *testing.T) {
	tests := []struct {
		name       string
		resolution string
		document   string
		want       bool
	}{
		{
			name: "S.J.Res. 67 interim final rule suffix",
			resolution: "National Emission Standards for Hazardous Air Pollutants: Integrated Iron and Steel " +
				"Manufacturing Facilities Technology Review: Interim Final Rule",
			document: "National Emission Standards for Hazardous Air Pollutants: Integrated Iron and Steel " +
				"Manufacturing Facilities Technology Review",
			want: true,
		},
		{
			name:       "unrelated document",
			resolution: "Overdraft Lending: Very Large Financial Institutions",
			document:   "Revising Consolidated Return Regulations and Controlled Foreign Corporations",
			want:       false,
		},
		{
			name:       "exactly half",
			resolution: "Overdraft Lending Very Large",
			document:   "Overdraft Lending",
			want:       true,
		},
		{
			name:       "just under half",
			resolution: "Overdraft Lending Very Large Institutions",
			document:   "Overdraft Lending",
			want:       false,
		},
		{
			name:       "stop words don't count",
			resolution: "The Rule of the Road",
			document:   "Rule Road",
			want:       true,
		},
		{
			name:       "repeated words count once",
			resolution: "Lending Lending Lending Overdraft",
			document:   "Lending",
			want:       true,
		},
		{
			name:       "punctuation and case",
			resolution: "Circular 2023–02: Reopening",
			document:   "circular 2023-02 reopening",
			want:       true,
		},
		{name: "empty resolution title", resolution: "", document: "Overdraft Lending", want: false},
		{name: "only stop words", resolution: "Of the And", document: "of the and", want: false},
		{name: "empty document title", resolution: "Overdraft Lending", document: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cra.TitleOverlap(tt.resolution, tt.document); got != tt.want {
				t.Errorf("TitleOverlap(%q, %q) = %v, want %v", tt.resolution, tt.document, got, tt.want)
			}
		})
	}
}

func TestAgencyMatches(t *testing.T) {
	tests := []struct {
		name       string
		resolution string
		documents  []string
		want       bool
	}{
		{
			name:       "CFPB word order",
			resolution: "Bureau of Consumer Financial Protection",
			documents:  []string{"Consumer Financial Protection Bureau"},
			want:       true,
		},
		{name: "department", resolution: "Department of Energy", documents: []string{"Energy Department"}, want: true},
		{
			name:       "bureau",
			resolution: "Bureau of Land Management",
			documents:  []string{"Land Management Bureau"},
			want:       true,
		},
		{
			name:       "only generic words shared",
			resolution: "Federal Trade Commission",
			documents:  []string{"Federal Reserve System"},
			want:       false,
		},
		{
			name:       "different department",
			resolution: "Department of Energy",
			documents:  []string{"Interior Department"},
			want:       false,
		},
		{
			name:       "any of several agencies",
			resolution: "Department of Energy",
			documents:  []string{"Interior Department", "Energy Department"},
			want:       true,
		},
		{name: "no document agencies", resolution: "Department of Energy", documents: nil, want: false},
		{name: "empty resolution agency", resolution: "", documents: []string{"Energy Department"}, want: false},
		{
			name:       "case and entities",
			resolution: "ENVIRONMENTAL PROTECTION AGENCY",
			documents:  []string{"Environmental Protection Agency"},
			want:       true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cra.AgencyMatches(tt.resolution, tt.documents); got != tt.want {
				t.Errorf("AgencyMatches(%q, %q) = %v, want %v", tt.resolution, tt.documents, got, tt.want)
			}
		})
	}
}

func TestIsWithdrawal(t *testing.T) {
	tests := []struct {
		title, action string
		want          bool
	}{
		{
			title:  "Interpretive Rules, Policy Statements, and Advisory Opinions; Withdrawal",
			action: "Notice.",
			want:   true,
		},
		{title: "Overdraft Lending", action: "Proposed rule; withdrawal.", want: true},
		{title: "Overdraft Lending", action: "Final rule; withdrawn.", want: true},
		{title: "Overdraft Lending: Very Large Financial Institutions", action: "Final rule.", want: false},
		{title: "", action: "", want: false},
	}
	for _, tt := range tests {
		if got := cra.IsWithdrawal(tt.title, tt.action); got != tt.want {
			t.Errorf("IsWithdrawal(%q, %q) = %v, want %v", tt.title, tt.action, got, tt.want)
		}
	}
}

func TestIsAmendment(t *testing.T) {
	tests := []struct {
		action string
		want   bool
	}{
		{action: "Final rule; delay of effective date.", want: true},
		{action: "Final rule; correction.", want: true},
		{action: "Correcting amendments.", want: true},
		{action: "Final rule; technical amendments.", want: true},
		{action: "Proposed rule; extension of comment period.", want: true},
		{action: "FINAL RULE; CORRECTION.", want: true},
		{action: "Final rule.", want: false},
		{action: "Interim final rule; request for comment.", want: false},
		{action: "Final rule; official interpretation.", want: false},
		{action: "", want: false},
	}
	for _, tt := range tests {
		if got := cra.IsAmendment(tt.action); got != tt.want {
			t.Errorf("IsAmendment(%q) = %v, want %v", tt.action, got, tt.want)
		}
	}
}
