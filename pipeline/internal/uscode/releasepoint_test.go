package uscode_test

import (
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/uscode"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestFindReleasePointPage reads download.shtml as uscode.house.gov served it on 2026-09-28.
func TestFindReleasePointPage(t *testing.T) {
	t.Parallel()
	page, err := os.ReadFile("testdata/download.shtml")
	if err != nil {
		t.Fatal(err)
	}
	rp, err := uscode.FindReleasePoint(page, mustURL(t, uscode.DefaultPageURL))
	if err != nil {
		t.Fatal(err)
	}
	if rp.ID != "119-111" {
		t.Errorf("ID = %q, want 119-111", rp.ID)
	}
	if want := "https://uscode.house.gov/download/releasepoints/us/pl/119/111/xml_uscAll@119-111.zip"; rp.ZipURL != want {
		t.Errorf("ZipURL = %q, want %q", rp.ZipURL, want)
	}
	if rp.LawDate == nil || !rp.LawDate.Equal(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("LawDate = %v, want 2026-09-18", rp.LawDate)
	}
}

// TestFindReleasePointMaintenancePage reads the page uscode.house.gov served, with status 200, in
// place of download.shtml while it was down on 2026-10-04.
func TestFindReleasePointMaintenancePage(t *testing.T) {
	t.Parallel()
	page, err := os.ReadFile("testdata/maintenance.html")
	if err != nil {
		t.Fatal(err)
	}
	_, err = uscode.FindReleasePoint(page, mustURL(t, uscode.DefaultPageURL))
	if !errors.Is(err, uscode.ErrSiteUnavailable) {
		t.Fatalf("err = %v, want ErrSiteUnavailable", err)
	}
}

func TestFindReleasePointVariants(t *testing.T) {
	t.Parallel()
	base := mustURL(t, "https://uscode.house.gov/download/download.shtml")
	cases := []struct {
		name, page, id, zip string
		dated               bool
		err                 error
	}{
		{
			name: "suffixed release point, no date",
			page: `<a href="releasepoints/us/pl/118/158not159/xml_uscAll@118-158not159.zip">[XML]</a>`,
			id:   "118-158not159",
			zip:  "https://uscode.house.gov/download/releasepoints/us/pl/118/158not159/xml_uscAll@118-158not159.zip",
		},
		{
			name: "date for another law is ignored",
			page: `Public Law 119-110 (09/01/2026) Public Law 119-111 (09/18/2026)` +
				`<a href="/download/releasepoints/us/pl/119/111/xml_uscAll@119-111.zip">`,
			id:    "119-111",
			zip:   "https://uscode.house.gov/download/releasepoints/us/pl/119/111/xml_uscAll@119-111.zip",
			dated: true,
		},
		{name: "no link", page: `<a href="xml_usc01@119-111.zip">`, err: uscode.ErrNoReleasePoint},
		{
			name: "maintenance title",
			page: "<html><head><TITLE>\n  Site Under Maintenance </TITLE></head><body></body></html>",
			err:  uscode.ErrSiteUnavailable,
		},
		{
			name: "maintenance outside the title is a missing link",
			page: `<title>Download the US Code</title><p>Scheduled maintenance on Saturday.</p>`,
			err:  uscode.ErrNoReleasePoint,
		},
		{
			name: "a link wins over a maintenance title",
			page: `<title>Under Maintenance</title><a href="releasepoints/us/pl/119/111/xml_uscAll@119-111.zip">`,
			id:   "119-111",
			zip:  "https://uscode.house.gov/download/releasepoints/us/pl/119/111/xml_uscAll@119-111.zip",
		},
		{
			name: "link to another host",
			page: `<a href="https://example.com/x/xml_uscAll@119-111.zip">`,
			err:  uscode.ErrNoReleasePoint,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rp, err := uscode.FindReleasePoint([]byte(tc.page), base)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if rp.ID != tc.id || rp.ZipURL != tc.zip || (rp.LawDate != nil) != tc.dated {
				t.Errorf("got %+v, want %s %s dated=%t", rp, tc.id, tc.zip, tc.dated)
			}
			if tc.dated && !rp.LawDate.Equal(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)) {
				t.Errorf("LawDate = %v, want 2026-09-18", rp.LawDate)
			}
		})
	}
}
