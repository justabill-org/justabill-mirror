package uscode

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"
)

// DefaultPageURL is the OLRC page that links the current release point. There's no stable
// "latest" URL for the files themselves.
const DefaultPageURL = "https://uscode.house.gov/download/download.shtml"

// The all-titles XML link, releasepoints/us/pl/119/111/xml_uscAll@119-111.zip, and the page's
// heading, "Public Law 119-111 (09/18/2026)". Release point names can carry a suffix
// ("118-158not159", "114-86u1").
var (
	allTitlesLinkPattern = regexp.MustCompile(`href="([^"]*/xml_uscAll@(\d{2,3}-[0-9A-Za-z]{1,40})\.zip)"`)
	lawDatePattern       = regexp.MustCompile(`Public Law (\d{2,3}-[0-9A-Za-z]{1,40}) \((\d{2}/\d{2}/\d{4})\)`)
	// maintenanceTitlePattern matches the title of the page the site serves, with status 200, while
	// it's down: "Under Maintenance" (2026-10-03 and 04).
	maintenanceTitlePattern = regexp.MustCompile(`(?is)<title>[^<]*\bmaintenance\b[^<]*</title>`)
)

// Errors from [FindReleasePoint].
var (
	// ErrNoReleasePoint means the download page had no usable all-titles XML link.
	ErrNoReleasePoint = errors.New("uscode: no all-titles XML link on the download page")
	// ErrSiteUnavailable means the site served its maintenance page in place of the download page.
	ErrSiteUnavailable = errors.New("uscode: uscode.house.gov is unavailable (a maintenance page)")
)

// ReleasePoint is the current release point as the download page links it.
type ReleasePoint struct {
	// ID names the release point for the last public law it includes: "119-111".
	ID string
	// ZipURL is the absolute URL of the all-titles USLM zip.
	ZipURL string
	// LawDate is the date the page gives for that law, or nil if it gives none.
	LawDate *time.Time
}

// FindReleasePoint finds the current release point on the download page at pageURL. The zip
// link must resolve to the page's own scheme and host. A page with no link whose title says
// maintenance is [ErrSiteUnavailable]; any other page with no usable link, [ErrNoReleasePoint].
func FindReleasePoint(page []byte, pageURL *url.URL) (ReleasePoint, error) {
	m := allTitlesLinkPattern.FindSubmatch(page)
	if m == nil {
		if maintenanceTitlePattern.Match(page) {
			return ReleasePoint{}, ErrSiteUnavailable
		}
		return ReleasePoint{}, ErrNoReleasePoint
	}
	zipURL, err := pageURL.Parse(string(m[1]))
	if err != nil {
		return ReleasePoint{}, fmt.Errorf("%w: %w", ErrNoReleasePoint, err)
	}
	if zipURL.Scheme != pageURL.Scheme || zipURL.Host != pageURL.Host {
		return ReleasePoint{}, fmt.Errorf("%w: link %s leaves %s", ErrNoReleasePoint, zipURL, pageURL.Host)
	}
	rp := ReleasePoint{ID: string(m[2]), ZipURL: zipURL.String()}
	for _, d := range lawDatePattern.FindAllSubmatch(page, -1) {
		if string(d[1]) != rp.ID {
			continue
		}
		if t, dateErr := time.Parse("01/02/2006", string(d[2])); dateErr == nil {
			rp.LawDate = &t
			break
		}
	}
	return rp, nil
}
