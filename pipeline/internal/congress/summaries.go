package congress

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// CRSSummary is one Congressional Research Service summary from the /summaries list: the summary
// of one version of a bill. Text is HTML as CRS published it, possibly invalid. Dates are as
// Congress.gov sends them: ActionDate is YYYY-MM-DD, the others RFC 3339.
type CRSSummary struct {
	ActionDate string `json:"actionDate"`
	ActionDesc string `json:"actionDesc"`
	Bill       struct {
		Congress int    `json:"congress"`
		Type     string `json:"type"`
		Number   string `json:"number"`
	} `json:"bill"`
	CurrentChamber        string `json:"currentChamber"`
	LastSummaryUpdateDate string `json:"lastSummaryUpdateDate"`
	Text                  string `json:"text"`
	UpdateDate            string `json:"updateDate"`
	VersionCode           string `json:"versionCode"`
}

// SummariesPage is one page of [Client.ListSummaries]. Each item is decoded on its own, so one
// malformed summary lands in Malformed and doesn't lose the page.
type SummariesPage struct {
	Summaries []CRSSummary
	// Malformed has an error for each item that didn't decode.
	Malformed []error
	// HasNext is true when the page carries pagination.next.
	HasNext bool
}

// Items is how many summaries the page listed, malformed ones included.
func (p *SummariesPage) Items() int { return len(p.Summaries) + len(p.Malformed) }

// ListSummaries fetches one page of up to 250 CRS summaries of a congress's bills updated from
// from to to, oldest update first. Without fromDateTime Congress.gov lists only the last day, so
// both bounds are always sent; to fixed at a run's start keeps the list from growing while it's
// paged.
func (c *Client) ListSummaries(
	ctx context.Context, congress int, from, to time.Time, offset int,
) (*SummariesPage, error) {
	const stamp = "2006-01-02T15:04:05Z"
	url := c.buildURL(fmt.Sprintf("/summaries/%d", congress)) +
		fmt.Sprintf("&fromDateTime=%s&toDateTime=%s&sort=updateDate+asc&limit=%d&offset=%d",
			from.UTC().Format(stamp), to.UTC().Format(stamp), pageLimit, offset)

	body, err := c.doGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("summaries page at offset %d: %w", offset, err)
	}
	var raw struct {
		Pagination pagination        `json:"pagination"`
		Summaries  []json.RawMessage `json:"summaries"`
	}
	if err = json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parsing summaries page at offset %d: %w", offset, err)
	}
	page := &SummariesPage{HasNext: raw.Pagination.Next != ""}
	for i, item := range raw.Summaries {
		var s CRSSummary
		if err = json.Unmarshal(item, &s); err != nil {
			page.Malformed = append(page.Malformed, fmt.Errorf("summary %d at offset %d: %w", i, offset, err))
			continue
		}
		page.Summaries = append(page.Summaries, s)
	}
	return page, nil
}

// BillSummaries fetches every CRS summary of one bill from /bill/{congress}/{type}/{number}/summaries
// (at most 250; a bill has a handful). Its items carry no bill and no lastSummaryUpdateDate. The
// sync reads the /summaries list instead; summarize-eval uses this for a fixed list of bills.
func (c *Client) BillSummaries(ctx context.Context, congress int, billType string, number int) ([]CRSSummary, error) {
	url := c.buildURL(fmt.Sprintf("/bill/%d/%s/%d/summaries", congress, billType, number)) +
		fmt.Sprintf("&limit=%d", pageLimit)
	body, err := c.doGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("summaries of %s %d: %w", billType, number, err)
	}
	var raw struct {
		Summaries []CRSSummary `json:"summaries"`
	}
	if err = json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parsing summaries of %s %d: %w", billType, number, err)
	}
	return raw.Summaries, nil
}
