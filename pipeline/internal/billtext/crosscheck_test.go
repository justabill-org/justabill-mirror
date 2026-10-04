package billtext_test

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

// TestUSLMCrossCheck measures ParseLawRefs against GovInfo's USLM for enrolled bills, which
// tags amending verbs (<amendingAction>) and US Code references (<ref href="/us/usc/…">). It
// runs only when BILLTEXT_CROSSCHECK_DIR names a directory of pairs downloaded from GovInfo:
// BILLS-…enr.xml (bill DTD) and BILLS-…enr.uslm.xml (USLM). See the wiki page Dev-Pipeline,
// "Law references", for the download loop.
//
// Per bill and US Code section, USLM says the bill changes the section when a reference to it
// shares a text block (chapeau, content, continuation) with an amendingAction outside quoted
// content, or is in the list below a chapeau with one, and cites it otherwise. The test logs
// how often ParseLawRefs agrees. Statutory notes ("10 U.S.C. 4271 note") are compared as their
// section, since USLM links them to it.
func TestUSLMCrossCheck(t *testing.T) {
	dir := os.Getenv("BILLTEXT_CROSSCHECK_DIR")
	if dir == "" {
		t.Skip("BILLTEXT_CROSSCHECK_DIR not set")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.uslm.xml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no *.uslm.xml pairs in %s", dir)
	}
	var tally crossTally
	for _, uslmPath := range files {
		dtd, readErr := os.ReadFile(strings.TrimSuffix(uslmPath, ".uslm.xml") + ".xml")
		if readErr != nil {
			continue
		}
		uslm, readErr := os.ReadFile(uslmPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		refs, parseErr := billtext.ParseLawRefs(dtd)
		if parseErr != nil {
			t.Errorf("%s: %v", uslmPath, parseErr)
			continue
		}
		truth, parseErr := uslmChanges(uslm)
		if parseErr != nil {
			t.Errorf("%s: %v", uslmPath, parseErr)
			continue
		}
		tally.add(t, filepath.Base(uslmPath), ours(refs), truth)
	}
	tally.report(t)
}

// ours returns, per US Code section, whether the parsed references change it.
func ours(refs []billtext.LawRef) map[string]bool {
	out := map[string]bool{}
	for _, r := range refs {
		if id := strings.TrimSuffix(r.SectionID, billtext.USCNoteSuffix); strings.HasPrefix(id, "/us/usc/") {
			out[id] = out[id] || r.Kind != model.LawRefCites
		}
	}
	return out
}

type crossTally struct {
	bills, both, agree, changeBoth, changeUSLM, changeOurs, onlyUSLM, onlyOurs int
}

func (c *crossTally) add(t *testing.T, name string, got, want map[string]bool) {
	t.Helper()
	c.bills++
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			c.onlyUSLM++
			continue
		}
		c.both++
		switch {
		case g == w:
			c.agree++
			if w {
				c.changeBoth++
			}
		case w:
			c.changeUSLM++
			t.Logf("%s %s: USLM changes, ours cites", name, id)
		default:
			c.changeOurs++
			t.Logf("%s %s: ours changes, USLM cites", name, id)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			c.onlyOurs++
		}
	}
}

func (c *crossTally) report(t *testing.T) {
	t.Helper()
	pct := func(n, d int) float64 {
		if d == 0 {
			return 0
		}
		return 100 * float64(n) / float64(d)
	}
	t.Logf("bills %d; sections found by both %d, agreeing on changes vs cites %d (%.1f%%)",
		c.bills, c.both, c.agree, pct(c.agree, c.both))
	t.Logf("changed per USLM and ours %d; USLM changes but ours cites %d; ours changes but USLM cites %d",
		c.changeBoth, c.changeUSLM, c.changeOurs)
	t.Logf("sections only USLM references %d; only ours %d", c.onlyUSLM, c.onlyOurs)
}

// uslmChanges reads a USLM bill and returns, per US Code section it references outside quoted
// content, whether a reference shares a text block with an amendingAction, or sits below a
// chapeau that has one ("is amended—" followed by its list of amendments).
func uslmChanges(data []byte) (map[string]bool, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	u := uslmReader{out: map[string]bool{}}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return u.out, nil
		}
		if err != nil {
			return nil, err
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			u.start(tk)
		case xml.EndElement:
			u.end(tk.Name.Local)
		}
	}
}

// uslmBlock is an open text block: the US Code sections it references and whether it has an
// amendingAction.
type uslmBlock struct {
	refs   []string
	action bool
}

type uslmReader struct {
	out        map[string]bool
	quoted     int
	blocks     []*uslmBlock
	listAction []bool // per open element: its chapeau has an amendingAction
}

func isUSLMBlock(name string) bool {
	return name == "chapeau" || name == "content" || name == "continuation" || name == "text"
}

func (u *uslmReader) start(t xml.StartElement) {
	u.listAction = append(u.listAction, false)
	name := t.Name.Local
	switch {
	case name == "quotedContent" || name == "quotedText":
		u.quoted++
	case isUSLMBlock(name):
		u.blocks = append(u.blocks, &uslmBlock{})
	case u.quoted > 0 || len(u.blocks) == 0:
	case name == "amendingAction":
		u.blocks[len(u.blocks)-1].action = true
	case name == "ref":
		if id := uslmSection(hrefOf(t)); id != "" {
			u.blocks[len(u.blocks)-1].refs = append(u.blocks[len(u.blocks)-1].refs, id)
		}
	}
}

func (u *uslmReader) end(name string) {
	u.listAction = u.listAction[:len(u.listAction)-1]
	switch {
	case name == "quotedContent" || name == "quotedText":
		u.quoted--
	case isUSLMBlock(name):
		b := u.blocks[len(u.blocks)-1]
		u.blocks = u.blocks[:len(u.blocks)-1]
		changes := b.action || u.quoted == 0 && slices.Contains(u.listAction, true)
		for _, id := range b.refs {
			u.out[id] = u.out[id] || changes
		}
		if name == "chapeau" && b.action && u.quoted == 0 && len(u.listAction) > 0 {
			u.listAction[len(u.listAction)-1] = true
		}
	}
}

func hrefOf(t xml.StartElement) string {
	for _, a := range t.Attr {
		if a.Name.Local == "href" {
			return a.Value
		}
	}
	return ""
}

// uslmSection maps "/us/usc/t42/s1395w–4/t" to "/us/usc/t42/s1395w-4".
func uslmSection(href string) string {
	parts := strings.Split(strings.TrimPrefix(href, "/us/usc/"), "/")
	if !strings.HasPrefix(href, "/us/usc/t") || len(parts) < 2 || !strings.HasPrefix(parts[1], "s") {
		return ""
	}
	return billtext.USCSectionID(strings.TrimPrefix(parts[0], "t"), strings.TrimPrefix(parts[1], "s"))
}
