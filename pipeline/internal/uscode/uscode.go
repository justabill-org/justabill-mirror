// Package uscode loads the current US Code from the Office of the Law Revision Counsel (OLRC):
// it finds the latest release point on uscode.house.gov, downloads the all-titles USLM zip,
// stream-parses each title into one row per section and writes the sections whose content
// changed (docs/design/149-law-aware-assistant.md, "Loader").
//
// The US Code is public domain. Nothing here uses Congress.gov or GovInfo quota.
package uscode

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// StatusCurrent is the status of a section whose USLM element has no status attribute. The
// others come from the attribute as written: repealed, transferred, omitted, reserved and so on.
const StatusCurrent = "current"

// Section is one section of a US Code title, ready for usc_sections.
type Section struct {
	// ID is the USLM identifier with its dashes in ASCII ("/us/usc/t42/s1395w-4").
	ID string
	// Title is the title number (42).
	Title int
	// Number is the section number from the identifier ("1395w-4", or "1...1j" for a range).
	Number string
	// Heading is the section's heading without the "§ 1." number; empty if it has none.
	Heading string
	// Text is the section's plain text: one line per paragraph or subdivision, subsection
	// enumerators kept ("(a)", "(1)"), and notes and source credits left out.
	Text string
	// Status is [StatusCurrent] or the USLM status attribute.
	Status string
	// PositiveLaw is true when the title is enacted as positive law.
	PositiveLaw bool
}

// Hash returns the hex SHA-256 of everything in s that usc_sections stores, so an unchanged
// section hashes the same at every release point.
func (s Section) Hash() string {
	h := sha256.New()
	for _, f := range []string{
		strconv.Itoa(s.Title), s.Number, s.Heading, s.Text, s.Status, strconv.FormatBool(s.PositiveLaw),
	} {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NormalizeID turns the dashes in a USLM identifier into ASCII hyphens, so "/us/usc/t42/s1395w–4"
// (en dash) becomes "/us/usc/t42/s1395w-4" and joins with the hyphenated citations in bill XML.
func NormalizeID(id string) string {
	return dashes().Replace(id)
}

func dashes() *strings.Replacer {
	return strings.NewReplacer(
		"‐", "-", // hyphen
		"‑", "-", // non-breaking hyphen
		"‒", "-", // figure dash
		"–", "-", // en dash, what USLM uses
		"—", "-", // em dash
	)
}
