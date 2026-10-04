// Package congress is a client for the Congress.gov API (api.congress.gov/v3): bills and their
// actions, cosponsors, committees, subjects, text versions, amendments and related bills, and
// members.
package congress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/redact"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

const (
	defaultBaseURL = "https://api.congress.gov/v3"

	// pageLimit is the documented maximum page size for Congress.gov lists.
	pageLimit = 250
	// maxPages caps a sub-resource at 10,000 items; only a runaway loop reaches it.
	maxPages = 40
)

// ErrPageCap means a sub-resource had more than maxPages pages. Nothing is returned, so a
// truncated list is never stored.
var ErrPageCap = errors.New("congress: page cap reached")

// ErrNotFound means Congress.gov answered 404: it has no such bill or resource.
var ErrNotFound = errors.New("congress: not found")

// pagination is the paging block of a Congress.gov list response.
type pagination struct {
	Count int    `json:"count"`
	Next  string `json:"next"`
}

// Client interacts with the Congress.gov API.
type Client struct {
	httpClient *http.Client
	baseURL    string
	requests   atomic.Int64
}

// NewClient creates a Client with the default base URL. httpClient is the pipeline's
// upstream client, which paces and retries every request and adds the API key.
func NewClient(httpClient *http.Client) *Client {
	return NewClientWithBaseURL(httpClient, defaultBaseURL)
}

// NewClientWithBaseURL creates a Client with a custom base URL (for testing).
func NewClientWithBaseURL(httpClient *http.Client, baseURL string) *Client {
	return &Client{httpClient: httpClient, baseURL: baseURL}
}

// Requests returns how many requests the client has made, not counting the upstream
// client's retries. The backfill's progress line reports it as requests per second.
func (c *Client) Requests() int64 { return c.requests.Load() }

// buildURL returns the request URL for path. The upstream client sends the key as a
// header, never in the URL.
func (c *Client) buildURL(path string) string {
	return c.baseURL + path + "?format=json"
}

// --- Response types ---

// BillSummary is the abbreviated bill info returned by list endpoints.
type BillSummary struct {
	Congress      int           `json:"congress"`
	Number        string        `json:"number"`
	Type          string        `json:"type"`
	Title         string        `json:"title"`
	OriginChamber string        `json:"originChamber"`
	URL           string        `json:"url"`
	LatestAction  *LatestAction `json:"latestAction"`
}

// LatestAction is a bill's most recent action. The list and detail responses both carry it.
type LatestAction struct {
	ActionDate string `json:"actionDate"`
	Text       string `json:"text"`
}

// BillsResponse is the response from GET /bill/{congress}.
type BillsResponse struct {
	Bills      []BillSummary `json:"bills"`
	Pagination struct {
		Count int `json:"count"`
	} `json:"pagination"`
}

// BillDetail is the full bill info returned by the detail endpoint.
type BillDetail struct {
	Congress       int    `json:"congress"`
	Number         string `json:"number"`
	Type           string `json:"type"`
	Title          string `json:"title"`
	OriginChamber  string `json:"originChamber"`
	IntroducedDate string `json:"introducedDate"`
	PolicyArea     *struct {
		Name string `json:"name"`
	} `json:"policyArea"`
	Sponsors     []Sponsor     `json:"sponsors"`
	LatestAction *LatestAction `json:"latestAction"`
	Laws         []Law         `json:"laws"`
}

// Law is a law a bill became, as the bill detail lists it: {"type": "Public Law", "number":
// "119-95"}. Bills that aren't law have none.
type Law struct {
	Type   string `json:"type"`
	Number string `json:"number"`
}

// Sponsor is a bill sponsor from the Congress API.
type Sponsor struct {
	BioguideID string `json:"bioguideId"`
	FullName   string `json:"fullName"`
	Party      string `json:"party"`
	State      string `json:"state"`
}

// BillDetailResponse wraps a single bill detail.
type BillDetailResponse struct {
	Bill BillDetail `json:"bill"`
}

// Member is a member from the Congress API.
type Member struct {
	BioguideID string `json:"bioguideId"`
	Name       string `json:"name"`
	State      string `json:"state"`
	District   *int   `json:"district"`
	PartyName  string `json:"partyName"`
	Depiction  *struct {
		ImageURL string `json:"imageUrl"`
	} `json:"depiction"`
	Terms struct {
		Item []MemberTerm `json:"item"`
	} `json:"terms"`
}

// MemberTerm is one stretch of a member's service in a chamber, as the member list gives it:
// years only. Chamber is "House of Representatives" or "Senate". EndYear is nil while the
// member still serves; one who left mid-congress has the year they left.
type MemberTerm struct {
	Chamber   string `json:"chamber"`
	StartYear int    `json:"startYear"`
	EndYear   *int   `json:"endYear"`
}

// MembersResponse is the response from GET /member/congress/{congress}.
type MembersResponse struct {
	Members    []Member `json:"members"`
	Pagination struct {
		Count int    `json:"count"`
		Next  string `json:"next"`
	} `json:"pagination"`
}

// MemberDetail is the full member info returned by the member detail endpoint.
type MemberDetail struct {
	BioguideID  string `json:"bioguideId"`
	Identifiers *struct {
		LisID string `json:"lisId"`
	} `json:"identifiers"`
}

// MemberDetailResponse wraps a single member detail.
type MemberDetailResponse struct {
	Member MemberDetail `json:"member"`
}

// Action is a bill action from the Congress API.
type Action struct {
	ActionDate   string `json:"actionDate"`
	Text         string `json:"text"`
	Type         string `json:"type"`
	ActionCode   string `json:"actionCode"`
	SourceSystem *struct {
		Code *int   `json:"code"`
		Name string `json:"name"`
	} `json:"sourceSystem"`
}

// ActionsResponse is the response from GET /bill/{congress}/{type}/{number}/actions.
type ActionsResponse struct {
	Actions []Action `json:"actions"`
}

// TextVersion is a bill text version from the Congress API.
type TextVersion struct {
	Date    string `json:"date"`
	Type    string `json:"type"`
	Formats []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"formats"`
}

// TextVersionsResponse is the response from GET /bill/{congress}/{type}/{number}/text.
type TextVersionsResponse struct {
	TextVersions []TextVersion `json:"textVersions"`
}

// Cosponsor is a bill cosponsor from the Congress API.
type Cosponsor struct {
	BioguideID  string `json:"bioguideId"`
	FullName    string `json:"fullName"`
	Party       string `json:"party"`
	State       string `json:"state"`
	SponsoredAt string `json:"sponsorshipDate"`
	IsOriginal  bool   `json:"isOriginalCosponsor"`
	WithdrawnAt string `json:"sponsorshipWithdrawnDate,omitempty"`
}

// CosponsorsResponse is the response from GET /bill/{congress}/{type}/{number}/cosponsors.
type CosponsorsResponse struct {
	Cosponsors []Cosponsor `json:"cosponsors"`
}

// Committee is a bill committee from the Congress API.
type Committee struct {
	Name       string              `json:"name"`
	SystemCode string              `json:"systemCode"`
	Chamber    string              `json:"chamber"`
	Type       string              `json:"type"`
	Activities []CommitteeActivity `json:"activities"`
}

// CommitteeActivity is one committee action on a bill, such as "Referred To".
type CommitteeActivity struct {
	Name string `json:"name"`
	Date string `json:"date"`
}

// CommitteesResponse is the response from GET /bill/{congress}/{type}/{number}/committees.
type CommitteesResponse struct {
	Committees []Committee `json:"committees"`
}

// SubjectsResponse is the response from GET /bill/{congress}/{type}/{number}/subjects.
type SubjectsResponse struct {
	Subjects struct {
		PolicyArea *struct {
			Name string `json:"name"`
		} `json:"policyArea"`
		LegislativeSubjects []LegislativeSubject `json:"legislativeSubjects"`
	} `json:"subjects"`
}

// LegislativeSubject is one CRS legislative subject term of a bill.
type LegislativeSubject struct {
	Name string `json:"name"`
}

// AmendmentSummary is a bill amendment from the Congress API.
type AmendmentSummary struct {
	Congress     int    `json:"congress"`
	Number       string `json:"number"`
	Type         string `json:"type"`
	Description  string `json:"description"`
	Purpose      string `json:"purpose"`
	LatestAction *struct {
		ActionDate string `json:"actionDate"`
		Text       string `json:"text"`
	} `json:"latestAction"`
	Chamber string `json:"chamber"`
}

// AmendmentsResponse is the response from GET /bill/{congress}/{type}/{number}/amendments.
type AmendmentsResponse struct {
	Amendments []AmendmentSummary `json:"amendments"`
}

// RelatedBillRelationship describes how two bills are related.
type RelatedBillRelationship struct {
	Type         string `json:"type"`
	IdentifiedBy string `json:"identifiedBy"`
}

// RelatedBillEntry is a related bill from the Congress API.
type RelatedBillEntry struct {
	Congress            int                       `json:"congress"`
	Number              int                       `json:"number"`
	Type                string                    `json:"type"`
	Title               string                    `json:"title"`
	RelationshipDetails []RelatedBillRelationship `json:"relationshipDetails"`
}

// RelatedBillsResponse is the response from GET /bill/{congress}/{type}/{number}/relatedbills.
type RelatedBillsResponse struct {
	RelatedBills []RelatedBillEntry `json:"relatedBills"`
}

// --- API methods ---

// ListBills fetches a page of bills for a given congress.
func (c *Client) ListBills(
	ctx context.Context, congress int, offset, limit int,
) (*BillsResponse, error) {
	url := c.buildURL(fmt.Sprintf("/bill/%d", congress))
	url += fmt.Sprintf("&offset=%d&limit=%d", offset, limit)

	body, err := c.doGet(ctx, url)
	if err != nil {
		return nil, err
	}

	var resp BillsResponse
	if err = json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing bills response: %w", err)
	}
	return &resp, nil
}

// ListBillsUpdatedSince fetches bills updated after a given time.
func (c *Client) ListBillsUpdatedSince(
	ctx context.Context, congress int, since time.Time, offset, limit int,
) (*BillsResponse, error) {
	url := c.buildURL(fmt.Sprintf("/bill/%d", congress))
	url += fmt.Sprintf("&offset=%d&limit=%d&fromDateTime=%s",
		offset, limit, since.UTC().Format("2006-01-02T15:04:05Z"))

	body, err := c.doGet(ctx, url)
	if err != nil {
		return nil, err
	}

	var resp BillsResponse
	if err = json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing bills response: %w", err)
	}
	return &resp, nil
}

// GetBill fetches detailed bill metadata.
func (c *Client) GetBill(
	ctx context.Context, congress int, billType string, number int,
) (*BillDetail, error) {
	url := c.buildURL(fmt.Sprintf("/bill/%d/%s/%d", congress, billType, number))

	body, err := c.doGet(ctx, url)
	if err != nil {
		return nil, err
	}

	var resp BillDetailResponse
	if err = json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing bill detail response: %w", err)
	}
	return &resp.Bill, nil
}

// ListMembers fetches a page of members for a given congress.
func (c *Client) ListMembers(
	ctx context.Context, congress, offset, limit int,
) (*MembersResponse, error) {
	url := c.buildURL(fmt.Sprintf("/member/congress/%d", congress))
	url += fmt.Sprintf("&offset=%d&limit=%d", offset, limit)

	body, err := c.doGet(ctx, url)
	if err != nil {
		return nil, err
	}

	var resp MembersResponse
	if err = json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing members response: %w", err)
	}
	return &resp, nil
}

// GetMemberDetail fetches detailed member info including identifiers.
func (c *Client) GetMemberDetail(
	ctx context.Context, bioguideID string,
) (*MemberDetail, error) {
	url := c.buildURL(fmt.Sprintf("/member/%s", bioguideID))

	body, err := c.doGet(ctx, url)
	if err != nil {
		return nil, err
	}

	var resp MemberDetailResponse
	if err = json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing member detail response: %w", err)
	}
	return &resp.Member, nil
}

// GetBillActions fetches every action for a bill, newest first.
func (c *Client) GetBillActions(
	ctx context.Context, congress int, billType string, number int,
) (*ActionsResponse, error) {
	actions, err := fetchAll(ctx, c, billPath(congress, billType, number, "actions"),
		listOf[Action]("actions"))
	if err != nil {
		return nil, fmt.Errorf("actions: %w", err)
	}
	return &ActionsResponse{Actions: actions}, nil
}

// GetBillTextVersions fetches every text version for a bill.
func (c *Client) GetBillTextVersions(
	ctx context.Context, congress int, billType string, number int,
) (*TextVersionsResponse, error) {
	versions, err := fetchAll(ctx, c, billPath(congress, billType, number, "text"),
		listOf[TextVersion]("textVersions"))
	if err != nil {
		return nil, fmt.Errorf("text versions: %w", err)
	}
	return &TextVersionsResponse{TextVersions: versions}, nil
}

// GetBillCosponsors fetches every cosponsor of a bill.
func (c *Client) GetBillCosponsors(
	ctx context.Context, congress int, billType string, number int,
) (*CosponsorsResponse, error) {
	cosponsors, err := fetchAll(ctx, c, billPath(congress, billType, number, "cosponsors"),
		listOf[Cosponsor]("cosponsors"))
	if err != nil {
		return nil, fmt.Errorf("cosponsors: %w", err)
	}
	return &CosponsorsResponse{Cosponsors: cosponsors}, nil
}

// GetBillCommittees fetches every committee of a bill.
func (c *Client) GetBillCommittees(
	ctx context.Context, congress int, billType string, number int,
) (*CommitteesResponse, error) {
	committees, err := fetchAll(ctx, c, billPath(congress, billType, number, "committees"),
		listOf[Committee]("committees"))
	if err != nil {
		return nil, fmt.Errorf("committees: %w", err)
	}
	return &CommitteesResponse{Committees: committees}, nil
}

// GetBillSubjects fetches every legislative subject of a bill. The policy area is the first
// one any page reports.
func (c *Client) GetBillSubjects(
	ctx context.Context, congress int, billType string, number int,
) (*SubjectsResponse, error) {
	var resp SubjectsResponse
	subjects, err := fetchAll(ctx, c, billPath(congress, billType, number, "subjects"),
		func(body []byte) ([]LegislativeSubject, pagination, error) {
			var page struct {
				SubjectsResponse

				Pagination pagination `json:"pagination"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return nil, pagination{}, err
			}
			if resp.Subjects.PolicyArea == nil {
				resp.Subjects.PolicyArea = page.Subjects.PolicyArea
			}
			return page.Subjects.LegislativeSubjects, page.Pagination, nil
		})
	if err != nil {
		return nil, fmt.Errorf("subjects: %w", err)
	}
	resp.Subjects.LegislativeSubjects = subjects
	return &resp, nil
}

// GetBillAmendments fetches every amendment to a bill.
func (c *Client) GetBillAmendments(
	ctx context.Context, congress int, billType string, number int,
) (*AmendmentsResponse, error) {
	amendments, err := fetchAll(ctx, c, billPath(congress, billType, number, "amendments"),
		listOf[AmendmentSummary]("amendments"))
	if err != nil {
		return nil, fmt.Errorf("amendments: %w", err)
	}
	return &AmendmentsResponse{Amendments: amendments}, nil
}

// GetBillRelatedBills fetches every related bill of a bill.
func (c *Client) GetBillRelatedBills(
	ctx context.Context, congress int, billType string, number int,
) (*RelatedBillsResponse, error) {
	related, err := fetchAll(ctx, c, billPath(congress, billType, number, "relatedbills"),
		listOf[RelatedBillEntry]("relatedBills"))
	if err != nil {
		return nil, fmt.Errorf("related bills: %w", err)
	}
	return &RelatedBillsResponse{RelatedBills: related}, nil
}

func billPath(congress int, billType string, number int, resource string) string {
	return fmt.Sprintf("/bill/%d/%s/%d/%s", congress, billType, number, resource)
}

// listOf decodes the items under key and the pagination block of one list page.
func listOf[T any](key string) func(body []byte) ([]T, pagination, error) {
	return func(body []byte) ([]T, pagination, error) {
		var page map[string]json.RawMessage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, pagination{}, err
		}
		var pg pagination
		if raw, ok := page["pagination"]; ok {
			if err := json.Unmarshal(raw, &pg); err != nil {
				return nil, pagination{}, err
			}
		}
		var items []T
		if raw, ok := page[key]; ok {
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, pagination{}, err
			}
		}
		return items, pg, nil
	}
}

// fetchAll pages through a sub-resource at path, pageLimit items per request, and returns
// every item in upstream order, or an error and no items. It stops when a page has no
// pagination.next or no items: pagination.count isn't reliable, and a short page can still
// have a next page. It fails with ErrPageCap after maxPages pages.
func fetchAll[T any](
	ctx context.Context, c *Client, path string,
	items func(body []byte) ([]T, pagination, error),
) ([]T, error) {
	var all []T
	for page := range maxPages {
		offset := page * pageLimit
		url := c.buildURL(path) + fmt.Sprintf("&limit=%d&offset=%d", pageLimit, offset)

		body, err := c.doGet(ctx, url)
		if err != nil {
			return nil, fmt.Errorf("page at offset %d: %w", offset, err)
		}
		got, pg, err := items(body)
		if err != nil {
			return nil, fmt.Errorf("parsing page at offset %d: %w", offset, err)
		}
		all = append(all, got...)
		if pg.Next == "" || len(got) == 0 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("%w: more than %d pages of %d", ErrPageCap, maxPages, pageLimit)
}

// doGet performs an HTTP GET. Pacing and retries are the upstream client's job.
func (c *Client) doGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, redact.URLError(err)
	}

	c.requests.Add(1)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		err = redact.URLError(err)
		if se, ok := errors.AsType[*upstream.StatusError](err); ok && se.Status == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: congress API error: %s", ErrNotFound, resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("congress API error: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}
