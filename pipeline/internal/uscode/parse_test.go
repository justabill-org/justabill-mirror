package uscode_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/uscode"
)

func parseAll(t *testing.T, r io.Reader) ([]uscode.Section, uscode.Title) {
	t.Helper()
	var got []uscode.Section
	title, err := uscode.ParseTitle(r, func(s uscode.Section) error {
		got = append(got, s)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseTitle: %v", err)
	}
	return got, title
}

// TestParseTitleSample parses real title 42 sections from release point 119-111: a repealed
// range, a transferred range, a letter-suffixed section with an en dash (217a–1), and 300gg–11
// with nested subsections, a source credit and notes. The fixture's title-level notes quote
// two sections that aren't the title's own.
func TestParseTitleSample(t *testing.T) {
	t.Parallel()
	f, err := os.Open("testdata/usc42-sample.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, title := parseAll(t, f)

	if title != (uscode.Title{Number: 42, PositiveLaw: false, Sections: 4}) {
		t.Errorf("title = %+v", title)
	}
	want := []struct{ id, number, status string }{
		{"/us/usc/t42/s1...1j", "1...1j", "repealed"},
		{"/us/usc/t42/s71...71l", "71...71l", "transferred"},
		{"/us/usc/t42/s217a-1", "217a-1", uscode.StatusCurrent},
		{"/us/usc/t42/s300gg-11", "300gg-11", uscode.StatusCurrent},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sections, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		s := got[i]
		if s.ID != w.id || s.Number != w.number || s.Status != w.status || s.Title != 42 || s.PositiveLaw {
			t.Errorf("section %d = %s %s %s %d %t, want %s %s %s 42 false",
				i, s.ID, s.Number, s.Status, s.Title, s.PositiveLaw, w.id, w.number, w.status)
		}
	}

	s := got[3]
	if s.Heading != "No lifetime or annual limits" {
		t.Errorf("heading = %q", s.Heading)
	}
	wantLines := []string{
		"(a) Prohibition",
		"(1) In general",
		"A group health plan and a health insurance issuer offering group or individual health insurance " +
			"coverage may not establish—",
		"(A) lifetime limits on the dollar value of benefits for any participant or beneficiary; or",
	}
	if lines := strings.Split(s.Text, "\n"); len(lines) < len(wantLines) || !slices.Equal(lines[:4], wantLines) {
		t.Errorf("text starts %q, want %q", lines[:min(4, len(lines))], wantLines)
	}
	if !strings.Contains(s.Text, "\n(b) Per beneficiary limits\n") {
		t.Errorf("text lacks subsection (b):\n%s", s.Text)
	}
	for _, dropped := range []string{"Pub. L.", "Stat.", "Editorial Notes", "Amendments", "§"} {
		if strings.Contains(s.Text, dropped) {
			t.Errorf("text keeps %q from the number, source credit or notes:\n%s", dropped, s.Text)
		}
	}
	if strings.HasPrefix(got[0].Heading, " ") || !strings.HasPrefix(got[0].Heading, "Repealed.") {
		t.Errorf("repealed heading = %q", got[0].Heading)
	}
	if got[0].Text != "" {
		t.Errorf("repealed text = %q, want empty (its notes are dropped)", got[0].Text)
	}
}

func TestParseTitleMarkup(t *testing.T) {
	t.Parallel()
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<uscDoc xmlns="http://xml.house.gov/schemas/uslm/1.0" xmlns:dc="http://purl.org/dc/elements/1.1/">
<meta><dc:title>Title 7</dc:title><docNumber> 7 </docNumber>
<property role="other">no</property><property role="is-positive-law">yes</property></meta>
<main><title identifier="/us/usc/t7"><num value="7">Title 7—</num><heading>AGRICULTURE</heading>
<section identifier="/us/usc/t7/s2014–1" status="omitted"><num value="2014–1">§ 2014–1.</num>
<heading> Tables and <i>inline</i>   markup </heading>
<content><p>Before the <i>table</i>:</p>
<table><tr><th>Year</th><th>Amount</th></tr><tr><td>2025</td><td>$5</td></tr></table>
<p>Quoting:<quotedContent><section><num>“SEC. 1.</num><content>quoted text</content></section></quotedContent></p>
</content>
<subsection identifier="/us/usc/t7/s2014–1/a"><num value="a">(a)</num><content>Level<ref class="footnoteRef">1</ref>
<footnote>Footnote text.</footnote></content></subsection>
<continuation>After.</continuation>
<notes><note><p>Dropped.</p></note></notes>
</section>
<section identifier="/us/usc/t8/s1"><content>Another title's section.</content></section>
</title></main></uscDoc>`
	got, title := parseAll(t, strings.NewReader(doc))
	if title != (uscode.Title{Number: 7, PositiveLaw: true, Sections: 1}) {
		t.Errorf("title = %+v", title)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sections, want 1: %+v", len(got), got)
	}
	want := uscode.Section{
		ID: "/us/usc/t7/s2014-1", Title: 7, Number: "2014-1", Heading: "Tables and inline markup",
		Text:   "Before the table:\nYear Amount\n2025 $5\nQuoting: “SEC. 1. quoted text\n(a) Level1\nAfter.",
		Status: "omitted", PositiveLaw: true,
	}
	if got[0] != want {
		t.Errorf("section =\n%+v\nwant\n%+v", got[0], want)
	}
}

func TestParseTitleAppendix(t *testing.T) {
	t.Parallel()
	doc := `<uscDoc xmlns="http://xml.house.gov/schemas/uslm/1.0"><meta><docNumber>5a</docNumber></meta>
<appendix identifier="/us/usc/t5a"><section identifier="/us/usc/t5a/s1"><content>x</content></section>
<section identifier="/us/usc/t0/s1"><content>x</content></section></appendix></uscDoc>`
	got, title := parseAll(t, strings.NewReader(doc))
	if len(got) != 0 || title.Number != 0 {
		t.Errorf("appendix gave title %+v and sections %+v, want none", title, got)
	}
}

func TestParseTitleErrors(t *testing.T) {
	t.Parallel()
	_, err := uscode.ParseTitle(strings.NewReader(`<uscDoc><meta><docNumber>1</docNumber></meta><main>`),
		func(uscode.Section) error { return nil })
	if err == nil {
		t.Error("truncated XML: no error")
	}

	stop := errors.New("stop")
	calls := 0
	_, err = uscode.ParseTitle(newTitleReader(1, 10), func(uscode.Section) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("callback error: err %v after %d calls, want stop after 1", err, calls)
	}
}

// TestParseTitleStreams reads a generated 50 MB title through a reader that counts what it
// hands out. If ParseTitle buffered the file, the first section would arrive after all of it
// had been read; it must arrive after a few kilobytes, and the reads must track the sections.
func TestParseTitleStreams(t *testing.T) {
	t.Parallel()
	const sections = 100_000
	r := newTitleReader(26, sections)
	first := -1
	count := 0
	title, err := uscode.ParseTitle(r, func(s uscode.Section) error {
		if count == 0 {
			first = r.read
		}
		count++
		if want := fmt.Sprintf("/us/usc/t26/s%07d", count); s.ID != want {
			return fmt.Errorf("section %d is %s, want %s", count, s.ID, want)
		}
		// The decoder reads ahead by at most its buffer, so it stays within a few sections.
		if ahead := r.read - r.offsetAfter(count); ahead > 64<<10 {
			return fmt.Errorf("section %d: read %d bytes ahead", count, ahead)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != sections || title.Sections != sections {
		t.Errorf("got %d sections (title says %d), want %d", count, title.Sections, sections)
	}
	if r.read < 50<<20 {
		t.Errorf("generated only %d bytes; the test wants a title bigger than any buffer", r.read)
	}
	if first > 64<<10 {
		t.Errorf("first section arrived after %d bytes had been read", first)
	}
}

// titleReader generates a title file with n sections of the same size, on demand.
type titleReader struct {
	title    int
	n        int
	next     int // next section to generate, from 1
	buf      []byte
	read     int
	head     int // size of the header
	section  int // size of each section, padded to the same length
	finished bool
}

const sectionPad = 500

func newTitleReader(title, n int) *titleReader {
	head := fmt.Sprintf(`<uscDoc xmlns="http://xml.house.gov/schemas/uslm/1.0"><meta><docNumber>%d</docNumber>`+
		`<property role="is-positive-law">yes</property></meta><main><title>`, title)
	r := &titleReader{title: title, n: n, next: 1, buf: []byte(head), head: len(head)}
	r.section = len(r.sectionXML(1))
	return r
}

func (r *titleReader) sectionXML(i int) string {
	return fmt.Sprintf(`<section identifier="/us/usc/t%d/s%07d"><num>§ %07d.</num><heading> H</heading>`+
		`<content><p>%s</p></content></section>`, r.title, i, i, strings.Repeat("x", sectionPad))
}

// offsetAfter is the byte offset at the end of section i.
func (r *titleReader) offsetAfter(i int) int { return r.head + i*r.section }

func (r *titleReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		switch {
		case r.next <= r.n:
			r.buf = []byte(r.sectionXML(r.next))
			r.next++
		case !r.finished:
			r.buf = []byte(`</title></main></uscDoc>`)
			r.finished = true
		default:
			return 0, io.EOF
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	r.read += n
	return n, nil
}

func TestNormalizeID(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"/us/usc/t42/s1395w–4":  "/us/usc/t42/s1395w-4",
		"/us/usc/t16/s460qqq—1": "/us/usc/t16/s460qqq-1",
		"/us/usc/t1/s1":         "/us/usc/t1/s1",
	} {
		if got := uscode.NormalizeID(in); got != want {
			t.Errorf("NormalizeID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSectionHash(t *testing.T) {
	t.Parallel()
	s := uscode.Section{ID: "/us/usc/t1/s1", Title: 1, Number: "1", Heading: "H", Text: "T", Status: "current"}
	same := s
	if same.Hash() != s.Hash() || len(s.Hash()) != 64 {
		t.Fatalf("hash %q isn't a stable SHA-256", s.Hash())
	}
	for name, change := range map[string]func(*uscode.Section){
		"heading":      func(s *uscode.Section) { s.Heading = "H2" },
		"text":         func(s *uscode.Section) { s.Text = "T2" },
		"status":       func(s *uscode.Section) { s.Status = "repealed" },
		"positive law": func(s *uscode.Section) { s.PositiveLaw = true },
		"field shift":  func(s *uscode.Section) { s.Heading, s.Text = "HT", "" },
	} {
		c := s
		change(&c)
		if c.Hash() == s.Hash() {
			t.Errorf("%s: hash didn't change", name)
		}
	}
}
