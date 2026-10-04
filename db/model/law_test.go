package model_test

import (
	"testing"

	"github.com/justabill-org/justabill/db/model"
)

func TestParseUSCSectionID(t *testing.T) {
	tests := []struct {
		id      string
		title   int
		section string
		ok      bool
	}{
		{"/us/usc/t42/s1395w-4", 42, "1395w-4", true},
		{"/us/usc/t4/s1", 4, "1", true},
		{model.NonUSCSectionPrefix + "Section 5 of the Social Security Act", 0, "", false},
		{"/us/usc/t5a/s1", 0, "", false},
		{"/us/usc/t0/s1", 0, "", false},
		{"/us/usc/t42/s", 0, "", false},
		{"/us/usc/t42/s1395/a", 0, "", false},
		{"/us/usc/t10/s4271" + model.USCNoteSuffix, 0, "", false},
		{"/us/usc/t42", 0, "", false},
	}
	for _, tt := range tests {
		title, section, ok := model.ParseUSCSectionID(tt.id)
		if title != tt.title || section != tt.section || ok != tt.ok {
			t.Errorf("ParseUSCSectionID(%q) = %d, %q, %v; want %d, %q, %v",
				tt.id, title, section, ok, tt.title, tt.section, tt.ok)
		}
		if tt.ok {
			if got := model.USCSectionID(title, section); got != tt.id {
				t.Errorf("USCSectionID(%d, %q) = %q, want %q", title, section, got, tt.id)
			}
		}
	}
}

func TestParseUSCNoteID(t *testing.T) {
	tests := []struct {
		id      string
		title   int
		section string
		ok      bool
	}{
		{"/us/usc/t10/s4271/note", 10, "4271", true},
		{"/us/usc/t42/s1395w-4/note", 42, "1395w-4", true},
		{"/us/usc/t10/s4271", 0, "", false},
		{"/us/usc/t10/s/note", 0, "", false},
		{"/us/usc/t10/s4271/a/note", 0, "", false},
		{"/us/usc/t10/s4271/note/note", 0, "", false},
		{model.NonUSCSectionPrefix + "Section 5 note", 0, "", false},
	}
	for _, tt := range tests {
		title, section, ok := model.ParseUSCNoteID(tt.id)
		if title != tt.title || section != tt.section || ok != tt.ok {
			t.Errorf("ParseUSCNoteID(%q) = %d, %q, %v; want %d, %q, %v",
				tt.id, title, section, ok, tt.title, tt.section, tt.ok)
		}
	}
}
