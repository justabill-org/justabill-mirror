package sync

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

// newTextRow builds the bill_texts row for a downloaded text (#451). The download is stored
// gzipped in ContentGz; Content holds the download while it fits in a STRING(MAX) cell, else its
// plain text, else nothing. It reports false when the text can't be stored at all (its gzip or
// its sections JSON is over a cell's limit): the row then marks the version fetched with no text.
func newTextRow(
	versionID, format string, data []byte, sections []billtext.Section, fetchedAt time.Time,
) (repository.BillTextRow, bool, error) {
	row := repository.BillTextRow{
		TextVersionID: versionID,
		Format:        format,
		ContentHash:   fmt.Sprintf("%x", sha256.Sum256(data)),
		Sections:      json.RawMessage("[]"),
		FetchedAt:     fetchedAt,
	}
	sectionsJSON, err := json.Marshal(sections)
	if err != nil {
		return repository.BillTextRow{}, false, fmt.Errorf("marshal sections: %w", err)
	}
	gz, err := gzipBytes(data)
	if err != nil {
		return repository.BillTextRow{}, false, err
	}
	if len(gz) > repository.MaxCellBytes || len(sectionsJSON) > repository.MaxCellBytes {
		return row, false, nil
	}
	row.ContentGz = gz
	row.Sections = sectionsJSON
	if content := string(data); fitsTextCell(content) {
		row.Content = content
	} else if plain := plainText(sections); fitsTextCell(plain) {
		row.Content = plain
	}
	return row, true, nil
}

// fitsTextCell reports whether s fits in a STRING(MAX) cell.
func fitsTextCell(s string) bool {
	return len(s) <= repository.MaxTextChars || utf8.RuneCountInString(s) <= repository.MaxTextChars
}

// gzipBytes compresses data at the default level.
func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, fmt.Errorf("gzip bill text: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("gzip bill text: %w", err)
	}
	return buf.Bytes(), nil
}

// plainText renders parsed sections as plain text: each unit's designation and header on one
// line, then its text, then its children, with a blank line between units.
func plainText(sections []billtext.Section) string {
	var b strings.Builder
	writePlainText(&b, sections)
	return strings.TrimSpace(b.String())
}

func writePlainText(b *strings.Builder, sections []billtext.Section) {
	for i := range sections {
		s := &sections[i]
		if head := strings.TrimSpace(s.Enum + " " + s.Header); head != "" {
			b.WriteString(head)
			b.WriteString("\n")
		}
		if text := strings.TrimSpace(s.Content); text != "" {
			b.WriteString(text)
			b.WriteString("\n")
		}
		b.WriteString("\n")
		writePlainText(b, s.Children)
	}
}
