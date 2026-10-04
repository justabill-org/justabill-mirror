// Package legislators reads members' terms from the congress-legislators dataset
// (github.com/unitedstates/congress-legislators, public domain): per term, the chamber, state,
// district, party and exact start and end dates, plus each member's bioguide and LIS IDs.
//
// Congress.gov's member data is wrong for past congresses: its list shows members who switched
// chambers as senators only, and its detail pages can give the wrong House district. The
// backfill of a past congress takes terms and LIS IDs from here instead
// (docs/design/78-118th-backfill.md, "Sub-decision: where 118th member terms come from").
//
// The files are fetched over HTTPS and treated as untrusted: each is capped at 32 MB, and a
// record that fails validation is logged and skipped.
package legislators

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// DefaultBaseURL serves the dataset's JSON files from its gh-pages branch. To pin a
	// commit, use https://raw.githubusercontent.com/unitedstates/congress-legislators/<sha>
	// with a gh-pages commit.
	DefaultBaseURL = "https://unitedstates.github.io/congress-legislators"

	// ChamberHouse is the House as member_terms stores it.
	ChamberHouse = "House"
	// ChamberSenate is the Senate as member_terms stores it.
	ChamberSenate = "Senate"

	currentFile    = "legislators-current.json"
	historicalFile = "legislators-historical.json"

	// maxBodyBytes caps each file; legislators-historical.json is about 13.5 MB.
	maxBodyBytes = 32 << 20
	httpTimeout  = 2 * time.Minute

	// maxDistrict is the highest House district number (California has 52); 0 is at-large,
	// a delegate or the resident commissioner.
	maxDistrict = 53

	termTypeRep = "rep"
	termTypeSen = "sen"
)

// ErrTooLarge means a file was larger than the 32 MB cap.
var ErrTooLarge = errors.New("legislators: file exceeds the size cap")

// Patterns for the IDs a record must carry.
var (
	bioguidePattern = regexp.MustCompile(`^[A-Z]\d{6}$`)
	lisPattern      = regexp.MustCompile(`^S\d{3}$`)
)

// Legislator is one person's record in the dataset. Fields the pipeline doesn't use are
// ignored.
type Legislator struct {
	ID    ID        `json:"id"`
	Terms []RawTerm `json:"terms"`
}

// ID holds a legislator's identifiers. LIS is set only for people who served in the Senate.
type ID struct {
	Bioguide string `json:"bioguide"`
	LIS      string `json:"lis"`
}

// RawTerm is one term as the dataset gives it. Type is "rep" or "sen", and dates are
// YYYY-MM-DD. A term that ended early (a resignation, a move to the other chamber) ends on
// the member's last day.
type RawTerm struct {
	Type     string `json:"type"`
	Start    string `json:"start"`
	End      string `json:"end"`
	State    string `json:"state"`
	District *int   `json:"district"`
	Party    string `json:"party"`
}

// Client fetches the dataset's JSON files.
type Client struct {
	httpClient *http.Client
	baseURL    string
	maxBytes   int64
}

// New returns a client that reads the files under baseURL, or DefaultBaseURL when it's empty.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		httpClient: &http.Client{Timeout: httpTimeout},
		baseURL:    strings.TrimRight(baseURL, "/"),
		maxBytes:   maxBodyBytes,
	}
}

// Fetch downloads and decodes the current and historical files. It fails if either one can't
// be read in full, so a caller never works from half the dataset.
func (c *Client) Fetch(ctx context.Context) ([]Legislator, error) {
	var all []Legislator
	for _, name := range []string{currentFile, historicalFile} {
		people, err := c.fetchFile(ctx, name)
		if err != nil {
			return nil, err
		}
		all = append(all, people...)
	}
	return all, nil
}

func (c *Client) fetchFile(ctx context.Context, name string) ([]Legislator, error) {
	url := c.baseURL + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("legislators: %s: %w", name, err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("legislators: fetch %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("legislators: fetch %s: HTTP %d", url, resp.StatusCode)
	}
	if resp.ContentLength > c.maxBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes", ErrTooLarge, name, resp.ContentLength)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("legislators: read %s: %w", name, err)
	}
	if int64(len(body)) > c.maxBytes {
		return nil, fmt.Errorf("%w: %s is over %d bytes", ErrTooLarge, name, c.maxBytes)
	}

	var people []Legislator
	if err = json.Unmarshal(body, &people); err != nil {
		return nil, fmt.Errorf("legislators: decode %s: %w", name, err)
	}
	return people, nil
}

// Term is one member's service in one chamber during a congress, clipped to the congress's
// dates.
type Term struct {
	Bioguide string
	// LIS is the member's Senate LIS ID, or "" if they have none. It belongs to the person,
	// so a House term can carry it too (a representative who later became a senator).
	LIS     string
	Chamber string
	State   string
	// District is nil for the Senate and 0 for an at-large seat, a delegate or the resident
	// commissioner.
	District *int
	// Party is the dataset's name for it ("Democrat", "Republican", "Independent").
	Party      string
	Start, End time.Time
}

// Filter selects the terms that overlap one congress.
type Filter struct {
	// Start and End are the congress's dates. A term overlaps when it starts before End and
	// ends after Start, so a term that ends the day the congress begins is left out.
	Start, End time.Time
	// ValidState reports whether a two-letter state or territory code is known.
	ValidState func(code string) bool
}

// Terms returns every member's terms in the congress, one per member and chamber, sorted by
// bioguide ID and chamber, with the number of records it skipped as invalid. Several terms in
// one chamber (an appointment followed by a special election) collapse into one with the
// earliest start and latest end, and the state, district and party of the latest term. Every
// skipped record is logged.
func (f Filter) Terms(ctx context.Context, logger *slog.Logger, people []Legislator) ([]Term, int) {
	var terms []Term
	skipped := 0
	for _, p := range people {
		got, bad := f.personTerms(ctx, logger, p)
		terms = append(terms, got...)
		skipped += bad
	}
	slices.SortFunc(terms, func(a, b Term) int {
		if c := strings.Compare(a.Bioguide, b.Bioguide); c != 0 {
			return c
		}
		return strings.Compare(a.Chamber, b.Chamber)
	})
	return terms, skipped
}

// datedTerm is a raw term with parsed dates.
type datedTerm struct {
	RawTerm

	start, end time.Time
}

// personTerms returns one person's merged terms in the congress and how many of their records
// were skipped.
func (f Filter) personTerms(ctx context.Context, logger *slog.Logger, p Legislator) ([]Term, int) {
	overlapping, skipped := f.overlapping(ctx, logger, p)
	if len(overlapping) == 0 {
		return nil, skipped
	}
	if reason := validateID(p.ID); reason != "" {
		logger.WarnContext(ctx, "skipping congress-legislators record", "bioguide_id", p.ID.Bioguide,
			"lis_id", p.ID.LIS, "reason", reason)
		return nil, skipped + len(overlapping)
	}

	byChamber := map[string]*Term{}
	for _, raw := range overlapping {
		term, reason := f.term(p.ID, raw)
		if reason != "" {
			logger.WarnContext(ctx, "skipping congress-legislators term", "bioguide_id", p.ID.Bioguide,
				"start", raw.Start, "state", raw.State, "reason", reason)
			skipped++
			continue
		}
		merged, ok := byChamber[term.Chamber]
		if !ok {
			byChamber[term.Chamber] = &term
			continue
		}
		mergeTerm(merged, term)
	}

	terms := make([]Term, 0, len(byChamber))
	for _, t := range byChamber {
		terms = append(terms, *t)
	}
	return terms, skipped
}

// overlapping returns the person's terms that overlap the congress, and how many had dates
// that don't parse.
func (f Filter) overlapping(ctx context.Context, logger *slog.Logger, p Legislator) ([]datedTerm, int) {
	var out []datedTerm
	skipped := 0
	for _, raw := range p.Terms {
		start, startErr := time.Parse(time.DateOnly, raw.Start)
		end, endErr := time.Parse(time.DateOnly, raw.End)
		if startErr != nil || endErr != nil || end.Before(start) {
			logger.WarnContext(ctx, "skipping congress-legislators term", "bioguide_id", p.ID.Bioguide,
				"start", raw.Start, "end", raw.End, "reason", "bad dates")
			skipped++
			continue
		}
		if start.Before(f.End) && end.After(f.Start) {
			out = append(out, datedTerm{RawTerm: raw, start: start, end: end})
		}
	}
	return out, skipped
}

// validateID returns why a record's IDs are invalid, or "".
func validateID(id ID) string {
	if !bioguidePattern.MatchString(id.Bioguide) {
		return "bad bioguide ID"
	}
	if id.LIS != "" && !lisPattern.MatchString(id.LIS) {
		return "bad LIS ID"
	}
	return ""
}

// term validates one overlapping term and clips it to the congress. It returns why the term is
// invalid, or "".
func (f Filter) term(id ID, raw datedTerm) (Term, string) {
	if f.ValidState == nil || !f.ValidState(raw.State) {
		return Term{}, "unknown state"
	}
	t := Term{
		Bioguide: id.Bioguide,
		LIS:      id.LIS,
		State:    raw.State,
		Party:    raw.Party,
		Start:    laterOf(raw.start, f.Start),
		End:      earlierOf(raw.end, f.End),
	}
	switch raw.Type {
	case termTypeRep:
		if raw.District == nil || *raw.District < 0 || *raw.District > maxDistrict {
			return Term{}, "bad district"
		}
		t.Chamber = ChamberHouse
		t.District = new(*raw.District)
	case termTypeSen:
		t.Chamber = ChamberSenate
	default:
		return Term{}, "unknown term type"
	}
	return t, ""
}

// mergeTerm widens merged to cover t. The later term's state, district and party win.
func mergeTerm(merged *Term, t Term) {
	if t.Start.After(merged.Start) {
		merged.State, merged.District, merged.Party = t.State, t.District, t.Party
	}
	merged.Start = earlierOf(merged.Start, t.Start)
	merged.End = laterOf(merged.End, t.End)
}

func earlierOf(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
