package sync

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// loadTextVersions reads a Congress.gov /text response recorded in testdata.
func loadTextVersions(t *testing.T, name string) []congress.TextVersion {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var resp congress.TextVersionsResponse
	if err = json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.TextVersions
}

// wantVersion is a row's code, Eastern date ("" for NULL) and sort_order.
type wantVersion struct {
	code  string
	date  string
	order int
}

func gotVersions(rows []repository.TextVersionRow) []wantVersion {
	got := make([]wantVersion, 0, len(rows))
	for _, r := range rows {
		date := ""
		if r.Date != nil {
			date = r.Date.Format(time.DateOnly)
		}
		got = append(got, wantVersion{r.VersionCode, date, r.SortOrder})
	}
	return got
}

// codesByOrder returns the codes oldest first.
func codesByOrder(rows []repository.TextVersionRow) []string {
	sorted := slices.Clone(rows)
	slices.SortFunc(sorted, func(a, b repository.TextVersionRow) int { return a.SortOrder - b.SortOrder })
	codes := make([]string, 0, len(sorted))
	for _, r := range sorted {
		codes = append(codes, r.VersionCode)
	}
	return codes
}

func TestTextVersionRows_RecordedResponses(t *testing.T) {
	tests := []struct {
		fixture string
		want    []wantVersion // in API order, newest first
		chrono  []string
	}{
		{
			fixture: "text-versions-119-hjres-35.json",
			want: []wantVersion{
				{"enr", "", 4}, {"rds", "2025-02-27", 3}, {"eh", "2025-02-26", 2},
				{"ih", "2025-02-04", 1}, {"pl", "2025-03-14", 5},
			},
			chrono: []string{"ih", "eh", "rds", "enr", "pl"},
		},
		{
			fixture: "text-versions-118-hr-2670.json",
			want: []wantVersion{
				{"enr", "", 6}, {"eas", "2023-07-27", 5}, {"rds", "2023-07-26", 4}, {"eh", "2023-07-14", 3},
				{"rh", "2023-06-30", 2}, {"ih", "2023-04-18", 1}, {"pl", "2023-12-22", 7},
			},
			chrono: []string{"ih", "rh", "eh", "rds", "eas", "enr", "pl"},
		},
		{
			fixture: "text-versions-118-hr-815.json",
			want: []wantVersion{
				{"enr", "", 6}, {"eah", "2024-04-20", 5}, {"eas", "2024-02-13", 4}, {"pcs", "2023-03-21", 3},
				{"eh", "2023-03-07", 2}, {"ih", "2023-02-02", 1}, {"pl", "2024-04-24", 7},
			},
			chrono: []string{"ih", "eh", "pcs", "eas", "eah", "enr", "pl"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			rows := TextVersionRows(t.Context(), slog.New(slog.DiscardHandler), "bill-1",
				loadTextVersions(t, tt.fixture))
			if got := gotVersions(rows); !slices.Equal(got, tt.want) {
				t.Errorf("rows =\n %v\nwant\n %v", got, tt.want)
			}
			if got := codesByOrder(rows); !slices.Equal(got, tt.chrono) {
				t.Errorf("chronological codes = %v, want %v", got, tt.chrono)
			}
			for _, r := range rows {
				if r.BillID != "bill-1" || r.VersionType == "" || !json.Valid(r.Formats) {
					t.Errorf("row %+v: want bill-1, a type and JSON formats", r)
				}
			}
		})
	}
}

func textVersion(date, typ string, urls ...string) congress.TextVersion {
	tv := congress.TextVersion{Date: date, Type: typ}
	for _, u := range urls {
		tv.Formats = append(tv.Formats, struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		}{Type: "Formatted XML", URL: u})
	}
	return tv
}

func TestVersionCode(t *testing.T) {
	tests := []struct {
		name string
		tv   congress.TextVersion
		want string
	}{
		{"BILLS suffix wins over the type", textVersion("", "Reported to Senate",
			"https://www.congress.gov/119/bills/hr2400/BILLS-119hr2400rs.xml"), "rs"},
		{"suffix is lowercased", textVersion("", "Whatever",
			"https://www.congress.gov/118/bills/hr815/BILLS-118HR815EAS.htm"), "eas"},
		{"query string is ignored", textVersion("", "Whatever",
			"https://example.test/BILLS-118sjres9is.pdf?download=1"), "is"},
		{"first matching format", textVersion("", "Whatever",
			"https://example.test/other.txt", "https://example.test/BILLS-118hr1ih.xml"), "ih"},
		{"PLAW URL", textVersion("", "Enrolled Bill",
			"https://www.congress.gov/118/plaws/publ50/PLAW-118publ50.htm"), "pl"},
		{"public law without formats", textVersion("", "Public Law"), "pl"},
		{"too-long suffix falls back to the type", textVersion("", "Engrossed Amendment Senate",
			"https://example.test/BILLS-118hr815abcdef.xml"), "eas"},
		{"type map: to/in variants", textVersion("", "Reported to Senate"), "rs"},
		{"type map: GovInfo spelling", textVersion("", "Received in (House)"), "rdh"},
		{"type map: enrolled", textVersion("", "Enrolled Bill"), "enr"},
		{"type map: multi-word", textVersion("", "Ordered to be Printed with House Amendment"), "pwah"},
		{"unknown type is snake case", textVersion("", "Star Print Senate"), "star_print_senate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionCode(tt.tv); got != tt.want {
				t.Errorf("versionCode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEasternDate(t *testing.T) {
	eastern := easternLocation()
	tests := []struct {
		raw    string
		want   string
		wantOK bool
	}{
		{"2023-07-14T04:00:00Z", "2023-07-14", true}, // midnight EDT
		{"2025-02-04T05:00:00Z", "2025-02-04", true}, // midnight EST
		{"2025-03-15T03:59:59Z", "2025-03-14", true}, // Public Law, 23:59:59 EDT the day before
		{"2023-12-23T04:59:59Z", "2023-12-22", true}, // Public Law, 23:59:59 EST the day before
		{"2024-04-20T00:00:00-04:00", "2024-04-20", true},
		{"2024-04-20", "2024-04-20", true},
		{"", "", true},
		{"yesterday", "", false},
	}
	for _, tt := range tests {
		got, ok := easternDate(tt.raw, eastern)
		gotStr := ""
		if got != nil {
			gotStr = got.Format(time.DateOnly)
			if got.Location() != time.UTC || got.Hour() != 0 {
				t.Errorf("easternDate(%q) = %v, want midnight UTC", tt.raw, got)
			}
		}
		if gotStr != tt.want || ok != tt.wantOK {
			t.Errorf("easternDate(%q) = %q, %v; want %q, %v", tt.raw, gotStr, ok, tt.want, tt.wantOK)
		}
	}
}

func TestTextVersionRows_DuplicateCodesKeepNewest(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	rows := TextVersionRows(t.Context(), logger, "hr-118-1", []congress.TextVersion{
		textVersion("2023-03-02T05:00:00Z", "Reported in House", "https://example.test/BILLS-118hr1rh.xml"),
		textVersion("2023-03-01T05:00:00Z", "Reported in House", "https://example.test/BILLS-118hr1rh.htm"),
		textVersion("2023-01-09T05:00:00Z", "Introduced in House", "https://example.test/BILLS-118hr1ih.xml"),
	})

	want := []wantVersion{{"rh", "2023-03-02", 2}, {"ih", "2023-01-09", 1}}
	if got := gotVersions(rows); !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") ||
		!strings.Contains(out, "dropped text versions with duplicate codes") ||
		!strings.Contains(out, "version_codes=[rh]") {
		t.Errorf("logs = %s, want a WARN naming rh", out)
	}
}

func TestTextVersionRows_Ordering(t *testing.T) {
	tests := []struct {
		name     string
		versions []congress.TextVersion // newest first, as the API lists them
		chrono   []string
	}{
		{
			name: "same date breaks ties by reverse API position",
			versions: []congress.TextVersion{
				textVersion("2023-07-14T04:00:00Z", "Received in Senate"),
				textVersion("2023-07-14T04:00:00Z", "Engrossed in House"),
				textVersion("2023-04-18T04:00:00Z", "Introduced in House"),
			},
			chrono: []string{"ih", "eh", "rds"},
		},
		{
			name: "undated version goes just after its next-older neighbor",
			versions: []congress.TextVersion{
				textVersion("2023-07-26T04:00:00Z", "Received in Senate"),
				textVersion("", "Placed on Calendar House"),
				textVersion("", "Reported in House"),
				textVersion("2023-07-14T04:00:00Z", "Engrossed in House"),
				textVersion("2023-04-18T04:00:00Z", "Introduced in House"),
			},
			chrono: []string{"ih", "eh", "rh", "pch", "rds"},
		},
		{
			name: "undated oldest version goes first",
			versions: []congress.TextVersion{
				textVersion("2023-07-14T04:00:00Z", "Engrossed in House"),
				textVersion("", "Introduced in House"),
			},
			chrono: []string{"ih", "eh"},
		},
		{
			name: "undated neighbors skip the enrolled bill and the public law",
			versions: []congress.TextVersion{
				textVersion("", "Public Print"),
				textVersion("", "Enrolled Bill"),
				textVersion("2023-07-14T04:00:00Z", "Engrossed in House"),
				textVersion("", "Public Law"),
			},
			chrono: []string{"eh", "pp", "enr", "pl"},
		},
		{
			name: "dated enrolled bill sorts by date, public law stays last",
			versions: []congress.TextVersion{
				textVersion("2023-08-01T04:00:00Z", "Enrolled Bill"),
				textVersion("2023-07-14T04:00:00Z", "Engrossed in House"),
				textVersion("2023-07-01T04:00:00Z", "Public Law"),
			},
			chrono: []string{"eh", "enr", "pl"},
		},
		{name: "empty", versions: nil, chrono: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := TextVersionRows(t.Context(), slog.New(slog.DiscardHandler), "b", tt.versions)
			if got := codesByOrder(rows); !slices.Equal(got, tt.chrono) {
				t.Errorf("chronological codes = %v, want %v", got, tt.chrono)
			}
		})
	}
}

func TestTypeKey(t *testing.T) {
	for in, want := range map[string]string{
		"Reported in (Senate)":                   "reported senate",
		"Re-engrossed Amendment (House)":         "re engrossed amendment house",
		"Returned to House by Unanimous Consent": "returned house unanimous consent",
		"  ":                                     "",
	} {
		if got := typeKey(in); got != want {
			t.Errorf("typeKey(%q) = %q, want %q", in, got, want)
		}
	}
}
