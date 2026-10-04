package sync

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

// bigBillXML is bill XML of about n bytes whose markup outweighs its text, like the NDAA's.
func bigBillXML(n int) []byte {
	var b strings.Builder
	b.WriteString(`<bill><legis-body>`)
	for i := 1; b.Len() < n; i++ {
		fmt.Fprintf(&b, `<section id="H%08X" section-type="subsequent-section" display-inline="no-display-inline">`+
			`<enum>%d.</enum><header display-inline="yes-display-inline">Section %d</header>`+
			`<text display-inline="yes-display-inline">The Secretary shall act.</text></section>`, i, i, i)
	}
	b.WriteString(`</legis-body></bill>`)
	return []byte(b.String())
}

func unzip(t *testing.T, gz []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func parsedSections(t *testing.T, data []byte) []billtext.Section {
	t.Helper()
	sections, err := billtext.ParseXML(data)
	if err != nil {
		t.Fatal(err)
	}
	return sections
}

func TestNewTextRow_SmallTextKeepsTheDownload(t *testing.T) {
	data := []byte(`<bill><legis-body><section id="S1"><enum>1.</enum><header>Short title</header>` +
		`<text>This Act is the Test Act.</text></section></legis-body></bill>`)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	row, ok, err := newTextRow("v1", formatXML, data, parsedSections(t, data), now)
	if err != nil || !ok {
		t.Fatalf("newTextRow = %v, %v", ok, err)
	}
	if row.Content != string(data) || unzip(t, row.ContentGz) != string(data) {
		t.Error("content and content_gz should both hold the download")
	}
	if row.ContentHash != fmt.Sprintf("%x", sha256.Sum256(data)) || row.TextVersionID != "v1" ||
		row.Format != formatXML || !row.FetchedAt.Equal(now) {
		t.Errorf("row = %+v", row)
	}
	var sections []billtext.Section
	if err = json.Unmarshal(row.Sections, &sections); err != nil || len(sections) != 1 {
		t.Errorf("sections = %s (%v)", row.Sections, err)
	}
}

func TestNewTextRow_LongXMLStoresPlainTextAndGzip(t *testing.T) {
	data := bigBillXML(repository.MaxTextChars + 1_000_000)
	row, ok, err := newTextRow("v1", formatXML, data, parsedSections(t, data), time.Now())
	if err != nil || !ok {
		t.Fatalf("newTextRow = %v, %v", ok, err)
	}
	if unzip(t, row.ContentGz) != string(data) {
		t.Error("content_gz should unzip to the download")
	}
	if len(row.ContentGz) > len(data)/5 {
		t.Errorf("gzip is %d bytes of %d", len(row.ContentGz), len(data))
	}
	if row.Content == "" || strings.Contains(row.Content, "<") || len(row.Content) > repository.MaxTextChars {
		t.Errorf("content should be the plain text under the cell limit, got %d chars", len(row.Content))
	}
	if !strings.HasPrefix(row.Content, "Sec. 1. Section 1\nThe Secretary shall act.\n\nSec. 2. Section 2") {
		t.Errorf("plain text starts %q", row.Content[:60])
	}
}

func TestNewTextRow_PlainTextOverTheLimitLeavesContentEmpty(t *testing.T) {
	data := []byte(strings.Repeat("SEC. 1. The Secretary shall act.\n", repository.MaxTextChars/30))
	sections := []billtext.Section{{ID: "s1", Header: "All", Content: string(data)}}
	row, ok, err := newTextRow("v1", formatText, data, sections, time.Now())
	if err != nil || !ok {
		t.Fatalf("newTextRow = %v, %v", ok, err)
	}
	if row.Content != "" || unzip(t, row.ContentGz) != string(data) {
		t.Errorf("content = %d chars, want empty with the download in content_gz", len(row.Content))
	}
}

func TestNewTextRow_TooLargeToStore(t *testing.T) {
	noise := make([]byte, repository.MaxCellBytes+1)
	_, _ = rand.Read(noise)
	big := strings.Repeat("x", repository.MaxCellBytes)
	for name, tc := range map[string]struct {
		data     []byte
		sections []billtext.Section
	}{
		"gzip over the cell limit":     {data: noise},
		"sections over the cell limit": {data: []byte("x"), sections: []billtext.Section{{Content: big}}},
	} {
		t.Run(name, func(t *testing.T) {
			row, ok, err := newTextRow("v1", formatText, tc.data, tc.sections, time.Now())
			if err != nil || ok {
				t.Fatalf("newTextRow = %v, %v; want not stored", ok, err)
			}
			if row.Content != "" || row.ContentGz != nil || string(row.Sections) != "[]" || row.ContentHash == "" {
				t.Errorf("row = content %d, gz %d, sections %s, hash %q", len(row.Content), len(row.ContentGz),
					row.Sections, row.ContentHash)
			}
		})
	}
}

func TestPlainText_NestedUnits(t *testing.T) {
	got := plainText([]billtext.Section{{
		Enum: "Title I", Header: "General",
		Children: []billtext.Section{{Enum: "Sec. 101.", Header: "Findings", Content: "Congress finds."}},
	}, {Content: "Loose text."}})
	want := "Title I General\n\nSec. 101. Findings\nCongress finds.\n\nLoose text."
	if got != want {
		t.Errorf("plainText = %q, want %q", got, want)
	}
}

// syncOneText runs sync-texts over one unfetched version whose download is body.
func syncOneText(t *testing.T, body []byte, format string) (*textsStore, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	formats, err := json.Marshal([]textFormat{{Type: format, URL: srv.URL + "/text"}})
	if err != nil {
		t.Fatal(err)
	}
	store := &textsStore{
		unfetched: []repository.TextVersionRef{{ID: "es", BillID: "s-119-2296", VersionCode: "es", Formats: formats}},
		sections:  map[string]json.RawMessage{},
	}
	svc, logs := textsService(store)
	svc.http = srv.Client()
	if _, err = svc.syncBillTexts(t.Context(), 119, 0); err != nil {
		t.Fatalf("syncBillTexts: %v", err)
	}
	if len(store.stored) != 1 {
		t.Fatalf("stored %d rows, want 1", len(store.stored))
	}
	return store, logs.String()
}

func TestSyncBillTexts_StoresALongTextGzipped(t *testing.T) {
	body := bigBillXML(repository.MaxTextChars + 1_000_000)
	body = bytes.Replace(body, []byte(`<text display-inline="yes-display-inline">The Secretary shall act.</text>`),
		[]byte(`<text>Section 102(f) of the Family and Medical Leave Act of 1993 (<external-xref legal-doc="usc" `+
			`parsable-cite="usc/29/2612">29 U.S.C. 2612(f)</external-xref>) is repealed.</text>`), 1)
	store, logs := syncOneText(t, body, formatXML)

	row := store.stored[0]
	if unzip(t, row.ContentGz) != string(body) || row.Content == "" || len(row.Content) > repository.MaxTextChars {
		t.Errorf("row: content %d chars, gzip %d bytes", len(row.Content), len(row.ContentGz))
	}
	if len(store.lawRefs["es"]) != 1 {
		t.Errorf("law refs = %+v, want the one in the download", store.lawRefs["es"])
	}
	if strings.Contains(logs, "too large") {
		t.Errorf("logged too large:\n%s", logs)
	}
}

func TestSyncBillTexts_MarksATooLargeTextFetched(t *testing.T) {
	noise := make([]byte, repository.MaxCellBytes+1)
	_, _ = rand.Read(noise)
	store, logs := syncOneText(t, noise, formatText)

	if row := store.stored[0]; row.ContentGz != nil || row.Content != "" || row.TextVersionID != "es" {
		t.Errorf("row: content %d chars, gzip %d bytes; want an empty marker", len(row.Content), len(row.ContentGz))
	}
	if !strings.Contains(logs, "bill text too large to store") || !strings.Contains(logs, "text_version_id=es") {
		t.Errorf("want a too-large log line, got:\n%s", logs)
	}
	if _, ok := store.lawRefs["es"]; ok {
		t.Error("law refs stored for a text that wasn't")
	}
}
