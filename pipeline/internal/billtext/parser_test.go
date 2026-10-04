package billtext_test

import (
	"os"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

func TestParseXML(t *testing.T) {
	data, err := os.ReadFile("testdata/sample_bill.xml")
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

	// First section: Short title
	if sections[0].Header == "" {
		t.Error("expected first section to have a header")
	}
	if sections[0].Content == "" {
		t.Error("expected first section to have content")
	}
	if sections[0].ID == "" {
		t.Error("expected first section to have an ID")
	}

	// Second section has a subsection child
	if len(sections[1].Children) != 1 {
		t.Errorf("expected second section to have 1 child, got %d", len(sections[1].Children))
	}
}

func TestParseXML_Resolution(t *testing.T) {
	data, err := os.ReadFile("testdata/sample_resolution.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	sections, err := billtext.ParseXML(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections (legis-body + resolving-body), got %d", len(sections))
	}
	if sections[0].ID != "S1" {
		t.Errorf("expected first section ID 'S1', got %q", sections[0].ID)
	}
	if sections[1].ID != "S2" {
		t.Errorf("expected second section ID 'S2', got %q", sections[1].ID)
	}
}

func TestParseXML_Empty(t *testing.T) {
	_, err := billtext.ParseXML([]byte(""))
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseXML_InvalidXML(t *testing.T) {
	_, err := billtext.ParseXML([]byte("<broken"))
	if err == nil {
		t.Error("expected error for invalid XML")
	}
}

func TestParsePlainText(t *testing.T) {
	data, err := os.ReadFile("testdata/sample_bill.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	sections, err := billtext.ParsePlainText(string(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(sections))
	}

	for i, s := range sections {
		if s.Header == "" {
			t.Errorf("section %d: expected non-empty header", i)
		}
		if s.Content == "" {
			t.Errorf("section %d: expected non-empty content", i)
		}
	}
}

func TestParsePlainText_Empty(t *testing.T) {
	sections, err := billtext.ParsePlainText("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sections) != 0 {
		t.Errorf("expected 0 sections for empty input, got %d", len(sections))
	}
}
