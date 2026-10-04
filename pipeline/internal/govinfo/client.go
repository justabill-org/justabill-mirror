// Package govinfo is a client for the GovInfo API (api.govinfo.gov): changes to the BILLS
// collection, package summaries, the bill text files they link to, and searches for US Code
// sections and for GAO reports about a bill.
package govinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/redact"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

const defaultBaseURL = "https://api.govinfo.gov"

// Client interacts with the GovInfo API for bill text retrieval.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient creates a Client with the default base URL. httpClient is the pipeline's
// upstream client, which paces and retries every request and adds the API key for
// api.govinfo.gov only, so a URL taken from a response body can't carry it elsewhere.
func NewClient(httpClient *http.Client) *Client {
	return NewClientWithBaseURL(httpClient, defaultBaseURL)
}

// NewClientWithBaseURL creates a Client with a custom base URL (for testing).
func NewClientWithBaseURL(httpClient *http.Client, baseURL string) *Client {
	return &Client{httpClient: httpClient, baseURL: baseURL}
}

// buildURL returns the request URL for path. The upstream client sends the key as a
// header, never in the URL.
func (c *Client) buildURL(path string) string {
	return c.baseURL + path
}

// Package represents a GovInfo document package.
type Package struct {
	PackageID      string `json:"packageId"`
	LastModified   string `json:"lastModified"`
	PackageLink    string `json:"packageLink"`
	DocClass       string `json:"docClass"`
	Title          string `json:"title"`
	Congress       string `json:"congress"`
	DateIssued     string `json:"dateIssued"`
	CollectionCode string `json:"collectionCode"`
}

// CollectionResponse is the response from GET /collections/{collection}.
type CollectionResponse struct {
	Packages []Package `json:"packages"`
	NextPage string    `json:"nextPage"`
	Count    int       `json:"count"`
}

// PackageSummary is the response from GET /packages/{packageId}/summary.
type PackageSummary struct {
	PackageID  string `json:"packageId"`
	Title      string `json:"title"`
	Congress   string `json:"congress"`
	DocClass   string `json:"docClass"`
	DateIssued string `json:"dateIssued"`
	Download   *struct {
		TxtLink string `json:"txtLink"`
		XMLLink string `json:"xmlLink"`
		PDFLink string `json:"pdfLink"`
	} `json:"download"`
}

const (
	// collectionPageSize is the Collections Service's documented maximum page size.
	collectionPageSize = 1000
	// maxCollectionPages caps PollChanges at 20,000 packages; only a runaway loop reaches it.
	maxCollectionPages = 20
)

// ErrPageCap means a collection had more than maxCollectionPages pages of changes. Nothing
// is returned, so the caller keeps its watermark instead of skipping the rest.
var ErrPageCap = errors.New("govinfo: page cap reached")

// PollChanges returns every package in collection modified since the given time. It pages
// with pageSize and offsetMark, taking the next offsetMark from the response's nextPage
// (which must be on the base URL's scheme and host) and building its own URL. It stops when
// a page has no nextPage or no packages: an exactly full last page still has a nextPage.
// Count is the number of packages returned, not any page's count. Any page error returns an
// error and no packages, and more than maxCollectionPages pages return ErrPageCap.
func (c *Client) PollChanges(
	ctx context.Context, collection string, since time.Time,
) (*CollectionResponse, error) {
	base, parseErr := url.Parse(c.baseURL)
	if parseErr != nil {
		return nil, fmt.Errorf("parsing base URL: %w", parseErr)
	}
	endpoint := c.buildURL(fmt.Sprintf("/collections/%s/%s", collection, since.UTC().Format(time.RFC3339)))
	all := &CollectionResponse{}
	offsetMark := "*"
	for page := 1; page <= maxCollectionPages; page++ {
		query := url.Values{"pageSize": {strconv.Itoa(collectionPageSize)}, "offsetMark": {offsetMark}}
		body, err := c.doGet(ctx, endpoint+"?"+query.Encode())
		if err != nil {
			return nil, fmt.Errorf("collection page %d: %w", page, err)
		}

		var resp CollectionResponse
		if err = json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("parsing collection page %d: %w", page, err)
		}
		all.Packages = append(all.Packages, resp.Packages...)
		if resp.NextPage == "" || len(resp.Packages) == 0 {
			all.Count = len(all.Packages)
			return all, nil
		}
		if offsetMark, err = nextOffsetMark(base, resp.NextPage); err != nil {
			return nil, fmt.Errorf("collection page %d: %w", page, err)
		}
	}
	return nil, fmt.Errorf("%w: more than %d pages of %d", ErrPageCap, maxCollectionPages, collectionPageSize)
}

// nextOffsetMark returns the offsetMark from a nextPage URL. The URL comes from a response
// body, so it's only trusted on base's scheme and host and is never fetched itself.
func nextOffsetMark(base *url.URL, nextPage string) (string, error) {
	next, err := url.Parse(nextPage)
	if err != nil {
		return "", errors.New("unparseable nextPage")
	}
	if next.Scheme != base.Scheme || next.Host != base.Host {
		return "", fmt.Errorf("nextPage on unexpected origin %s://%s", next.Scheme, next.Host)
	}
	mark := next.Query().Get("offsetMark")
	if mark == "" {
		return "", errors.New("nextPage has no offsetMark")
	}
	return mark, nil
}

// FetchPackageSummary downloads the summary metadata for a package.
func (c *Client) FetchPackageSummary(
	ctx context.Context, packageID string,
) (*PackageSummary, error) {
	url := c.buildURL(fmt.Sprintf("/packages/%s/summary", packageID))

	body, err := c.doGet(ctx, url)
	if err != nil {
		return nil, err
	}

	var resp PackageSummary
	if err = json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing package summary: %w", err)
	}
	return &resp, nil
}

// FetchText downloads the text content from a URL (e.g. txtLink or xmlLink), with the
// longer attempt timeout for downloads. The upstream client sends the key only when
// textURL is on api.govinfo.gov, and refuses hosts it doesn't know.
func (c *Client) FetchText(ctx context.Context, textURL string) ([]byte, error) {
	return c.doGet(upstream.WithAttemptTimeout(ctx, upstream.DownloadTimeout), textURL)
}

// SearchResult represents a single GovInfo search hit.
type SearchResult struct {
	PackageID    string `json:"packageId"`
	GranuleID    string `json:"granuleId,omitempty"`
	Title        string `json:"title"`
	LastModified string `json:"lastModified"`
}

// SearchResponse is the response from POST /search.
type SearchResponse struct {
	Count      int            `json:"count"`
	Results    []SearchResult `json:"results"`
	NextPage   string         `json:"nextPage,omitempty"`
	OffsetMark string         `json:"offsetMark,omitempty"`
}

// SearchUSCode searches for a US Code section by title and section number.
func (c *Client) SearchUSCode(ctx context.Context, title, section int) (*SearchResponse, error) {
	query := fmt.Sprintf("collection:USCODE usctitlenum:%d uscsectionnum:%d", title, section)
	return c.search(ctx, query)
}

// SearchGAOReports searches for GAO reports related to a bill citation (e.g. "h.r. 43").
func (c *Client) SearchGAOReports(ctx context.Context, billCitation string) (*SearchResponse, error) {
	query := fmt.Sprintf(`collection:GAOREPORTS billscitation:"%s"`, billCitation)
	return c.search(ctx, query)
}

// FetchGranuleHTM downloads the HTML text of a granule (e.g. a US Code section).
func (c *Client) FetchGranuleHTM(ctx context.Context, packageID, granuleID string) ([]byte, error) {
	url := c.buildURL(fmt.Sprintf("/packages/%s/granules/%s/htm", packageID, granuleID))
	return c.doGet(upstream.WithAttemptTimeout(ctx, upstream.DownloadTimeout), url)
}

const defaultSearchPageSize = 10

func (c *Client) search(ctx context.Context, query string) (*SearchResponse, error) {
	body := map[string]any{
		"query":      query,
		"pageSize":   defaultSearchPageSize,
		"offsetMark": "*",
	}
	return c.doPost(ctx, c.buildURL("/search"), body)
}

func (c *Client) doPost(ctx context.Context, url string, body any) (*SearchResponse, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	// A search has no side effects, so the upstream client may retry it.
	req, err := http.NewRequestWithContext(upstream.WithIdempotent(ctx), http.MethodPost, url,
		bytes.NewReader(data))
	if err != nil {
		return nil, redact.URLError(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, redact.URLError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("govinfo search error: %s", resp.Status)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result SearchResponse
	if err = json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parsing search response: %w", err)
	}
	return &result, nil
}

// doGet performs an HTTP GET. Pacing and retries are the upstream client's job.
func (c *Client) doGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, redact.URLError(err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, redact.URLError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("govinfo API error: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}
