package billtext_test

import (
	"os"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

func TestParseXML_RealBill_HR144(t *testing.T) {
	data, err := os.ReadFile("testdata/real_hr144_ih.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	sections, err := billtext.ParseXML(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}

	// Section 1: Short title
	if sections[0].ID != "H92772330397C48FBB26F23A38D5F5228" {
		t.Errorf("section 1 ID = %q", sections[0].ID)
	}
	if sections[0].Header != "Short title" {
		t.Errorf("section 1 header = %q", sections[0].Header)
	}
	if sections[0].Content == "" {
		t.Error("section 1 should have content")
	}

	// Section 2: Has 3 subsections
	if sections[1].Header != "Salary disclosure; exception to report elimination" {
		t.Errorf("section 2 header = %q", sections[1].Header)
	}
	if len(sections[1].Children) != 3 {
		t.Errorf("expected 3 subsections, got %d", len(sections[1].Children))
	}
	if sections[1].Children[0].Header != "Report on compensation" {
		t.Errorf("subsection 1 header = %q", sections[1].Children[0].Header)
	}
}

func TestParsePlainText_RealBill_HR134(t *testing.T) {
	data, err := os.ReadFile("testdata/real_hr134_ih.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	sections, err := billtext.ParsePlainText(string(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sections) != 4 {
		t.Fatalf("expected 4 sections, got %d", len(sections))
	}

	if sections[0].Header != "SHORT TITLE." {
		t.Errorf("section 1 header = %q", sections[0].Header)
	}
	if sections[1].Header != "MANDATORY DETENTION OF CERTAIN ALIENS CHARGED WITH SEXUAL ASSAULT." {
		t.Errorf("section 2 header = %q", sections[1].Header)
	}

	// Each section should have non-empty content
	for i, s := range sections {
		if s.Content == "" {
			t.Errorf("section %d has empty content", i+1)
		}
	}
}
