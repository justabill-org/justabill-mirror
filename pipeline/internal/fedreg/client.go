// Package fedreg is a client for the Federal Register API (www.federalregister.gov/api/v1): the
// documents published on a day, and phrase searches for documents by title. It reads the rules
// that CRA resolutions disapprove (docs/design/590-cra-disapproved-rules.md). The API needs no
// key.
package fedreg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/redact"
)

const defaultBaseURL = "https://www.federalregister.gov/api/v1"

// Document types, as the API filters on them (conditions[type][]).
const (
	TypeRule         = "RULE"
	TypeNotice       = "NOTICE"
	TypeProposedRule = "PRORULE"
)

// Document types, as the API names them in a document's type field.
const (
	DocRule         = "Rule"
	DocNotice       = "Notice"
	DocProposedRule = "Proposed Rule"
)

const (
	// maxPerPage is the API's largest page.
	maxPerPage = 1000
	// defaultMaxPages bounds a query that doesn't set Query.MaxPages.
	defaultMaxPages = 5
	dateLayout      = time.DateOnly
)

// Docket patterns: a Regulations.gov docket ID is the agency's code, a year and a sequence, with
// sub-codes before or after the year ("CFPB-2024-0002", "EPA-HQ-OAR-2018-0815",
// "ED-2024-OPE-0069"); docketLabelPattern is the label before it.
var (
	docketIDPattern    = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:-[A-Z][A-Z0-9]*)*-\d{4}(?:-[A-Z][A-Z0-9]*)*-\d{4,}$`)
	docketLabelPattern = regexp.MustCompile(`(?i)^docket\s+(?:no\.?|id:?|number:?)\s*`)
)

// ErrPageCap means a query had more pages than its Query.MaxPages. Nothing is returned unless
// the query sets Truncate.
var ErrPageCap = errors.New("fedreg: page cap reached")

// fields are the document fields every query asks for, so a response carries nothing else.
func fields() []string {
	return []string{
		"document_number", "citation", "volume", "start_page", "end_page", "type", "action", "title",
		"agencies", "publication_date", "effective_on", "abstract", "html_url", "pdf_url", "docket_ids",
		"regulations_dot_gov_info", "regulation_id_numbers", "correction_of",
	}
}

// Client reads documents from the Federal Register API.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient creates a Client for the Federal Register API. httpClient is the pipeline's upstream
// client, which paces and retries every request.
func NewClient(httpClient *http.Client) *Client {
	return NewClientWithBaseURL(httpClient, defaultBaseURL)
}

// NewClientWithBaseURL creates a Client with a custom base URL, the equivalent of
// https://www.federalregister.gov/api/v1 (for testing).
func NewClientWithBaseURL(httpClient *http.Client, baseURL string) *Client {
	return &Client{httpClient: httpClient, baseURL: baseURL}
}

// Agency is one agency of a document. Name is empty for an agency the Federal Register doesn't
// list, which has only RawName.
type Agency struct {
	Name    string `json:"name"`
	RawName string `json:"raw_name"`
	Slug    string `json:"slug"`
}

// Document is a Federal Register document, with the fields the pipeline stores. Text fields are
// as the API sends them, HTML entities included.
type Document struct {
	DocumentNumber      string   `json:"document_number"`
	Citation            string   `json:"citation"`
	Volume              int      `json:"volume"`
	StartPage           int      `json:"start_page"`
	EndPage             int      `json:"end_page"`
	Type                string   `json:"type"`
	Action              string   `json:"action"`
	Title               string   `json:"title"`
	Agencies            []Agency `json:"agencies"`
	PublicationDate     string   `json:"publication_date"`
	EffectiveOn         string   `json:"effective_on"`
	Abstract            string   `json:"abstract"`
	HTMLURL             string   `json:"html_url"`
	PDFURL              string   `json:"pdf_url"`
	DocketIDs           []string `json:"docket_ids"`
	RegulationIDNumbers []string `json:"regulation_id_numbers"`
	// CorrectionOf is the API URL of the document this one corrects; empty when it corrects none.
	CorrectionOf string `json:"correction_of"`
	// RegulationsDotGov is regulations_dot_gov_info. The API sends an object, empty when it has
	// nothing; it's kept raw so another shape can't fail the whole page.
	RegulationsDotGov json.RawMessage `json:"regulations_dot_gov_info"`
}

// DocketID is the document's Regulations.gov docket: regulations_dot_gov_info.docket_id, or else
// the first docket_ids entry shaped like a Regulations.gov docket ID once a "Docket No." or
// "Docket ID:" label is dropped ("Docket No. CFPB-2024-0002"). Other agencies' dockets, such as
// the FCC's "GN Docket No. 18-122", aren't on Regulations.gov, so they give "".
func (d Document) DocketID() string {
	var info struct {
		DocketID string `json:"docket_id"`
	}
	if len(d.RegulationsDotGov) > 0 && json.Unmarshal(d.RegulationsDotGov, &info) == nil && info.DocketID != "" {
		return info.DocketID
	}
	for _, id := range d.DocketIDs {
		id = strings.TrimSpace(docketLabelPattern.ReplaceAllString(id, ""))
		if docketIDPattern.MatchString(id) {
			return id
		}
	}
	return ""
}

// Query selects documents. Set PublishedOn for one day's documents, or Term for a phrase search
// (sent quoted), narrowed by Year or PublishedBy. The newest come first.
type Query struct {
	// Term is a phrase the document must contain; empty for none.
	Term string
	// PublishedOn, when set, keeps the documents published that day.
	PublishedOn time.Time
	// PublishedBy, when set, keeps the documents published on or before that day.
	PublishedBy time.Time
	// Year, when set, keeps the documents published that year.
	Year int
	// Types are the document types to keep (TypeRule, …); none means every type.
	Types []string
	// PerPage is the page size, up to 1,000 (0: 1,000).
	PerPage int
	// MaxPages is how many pages to read before failing with ErrPageCap (0: 5).
	MaxPages int
	// Truncate makes a query with more than MaxPages pages return the documents it read along
	// with ErrPageCap, instead of none.
	Truncate bool
}

// values returns the query's URL parameters.
func (q Query) values() url.Values {
	v := url.Values{"order": {"newest"}, "fields[]": fields()}
	perPage := q.PerPage
	if perPage <= 0 || perPage > maxPerPage {
		perPage = maxPerPage
	}
	v.Set("per_page", strconv.Itoa(perPage))
	if q.Term != "" {
		v.Set("conditions[term]", `"`+q.Term+`"`)
	}
	if !q.PublishedOn.IsZero() {
		v.Set("conditions[publication_date][is]", q.PublishedOn.Format(dateLayout))
	}
	if !q.PublishedBy.IsZero() {
		v.Set("conditions[publication_date][lte]", q.PublishedBy.Format(dateLayout))
	}
	if q.Year > 0 {
		v.Set("conditions[publication_date][year]", strconv.Itoa(q.Year))
	}
	for _, t := range q.Types {
		v.Add("conditions[type][]", t)
	}
	return v
}

// page is one page of /documents.json. results is absent when nothing matches.
type page struct {
	Count       int        `json:"count"`
	Results     []Document `json:"results"`
	NextPageURL string     `json:"next_page_url"`
}

// Documents returns every document the query selects, reading its pages in turn. The next page's
// URL comes from the response, so it's followed only on the base URL's scheme and host. Any
// failed page returns an error and no documents.
func (c *Client) Documents(ctx context.Context, q Query) ([]Document, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	maxPages := q.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}
	next := c.baseURL + "/documents.json?" + q.values().Encode()
	var docs []Document
	for n := 1; n <= maxPages; n++ {
		var p page
		if err = c.getJSON(ctx, next, &p); err != nil {
			return nil, fmt.Errorf("documents page %d: %w", n, err)
		}
		docs = append(docs, p.Results...)
		if p.NextPageURL == "" || len(p.Results) == 0 {
			return docs, nil
		}
		if next, err = sameOrigin(base, p.NextPageURL); err != nil {
			return nil, fmt.Errorf("documents page %d: %w", n, err)
		}
	}
	if q.Truncate {
		return docs, fmt.Errorf("%w: more than %d pages", ErrPageCap, maxPages)
	}
	return nil, fmt.Errorf("%w: more than %d pages", ErrPageCap, maxPages)
}

// sameOrigin returns nextURL when it's on base's scheme and host.
func sameOrigin(base *url.URL, nextURL string) (string, error) {
	u, err := url.Parse(nextURL)
	if err != nil {
		return "", errors.New("unparseable next_page_url")
	}
	if u.Scheme != base.Scheme || u.Host != base.Host {
		return "", fmt.Errorf("next_page_url on unexpected origin %s://%s", u.Scheme, u.Host)
	}
	return nextURL, nil
}

// getJSON GETs target and decodes its JSON body into dst. Pacing, retries and the body cap are
// the upstream client's job.
func (c *Client) getJSON(ctx context.Context, target string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return redact.URLError(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return redact.URLError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("federal register API: %s", resp.Status)
	}
	if err = json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
