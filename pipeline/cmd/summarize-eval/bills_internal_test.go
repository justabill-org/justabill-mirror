package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestCommittedBillList holds the list to the design's mix: 15 bills with floor votes, 10
// resolutions, 10 long bills including H.R. 1, and 15 at random.
func TestCommittedBillList(t *testing.T) {
	bills, err := loadBills(filepath.Join("testdata", "bills.json"))
	if err != nil {
		t.Fatal(err)
	}
	const wantBills = 50
	if len(bills) != wantBills {
		t.Fatalf("list has %d bills, want %d", len(bills), wantBills)
	}
	counts := make(map[string]int)
	hasHR1 := false
	for _, b := range bills {
		counts[b.Category]++
		hasHR1 = hasHR1 || b.BillID == "hr-119-1"
		if b.Title == "" || b.Version == "" || b.VersionName == "" {
			t.Errorf("%s lacks a title or version", b.BillID)
		}
	}
	want := map[string]int{categoryFloorVote: 15, categoryResolution: 10, categoryLong: 10, categoryRandom: 15}
	for c, n := range want {
		if counts[c] != n {
			t.Errorf("%d bills in %s, want %d", counts[c], c, n)
		}
	}
	if !hasHR1 {
		t.Error("the list lacks H.R. 1")
	}
}

func TestLoadBillsRejectsBadEntries(t *testing.T) {
	good := `{"bill_id":"hr-119-1","category":"long","package_id":"BILLS-119hr1enr"}`
	tests := map[string]string{
		"not json":          `{`,
		"duplicate":         `[` + good + `,` + good + `]`,
		"missing id":        `[{"category":"long","package_id":"BILLS-119hr1enr"}]`,
		"bad package":       `[{"bill_id":"hr-119-1","category":"long","package_id":"../etc/passwd"}]`,
		"unknown category":  `[{"bill_id":"hr-119-1","category":"popular","package_id":"BILLS-119hr1enr"}]`,
		"file doesn't open": "",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bills.json")
			if content != "" {
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadBills(path); err == nil {
				t.Error("want an error")
			}
		})
	}
}

type fakeFetcher struct {
	urls []string
	err  error
}

func (f *fakeFetcher) FetchText(_ context.Context, textURL string) ([]byte, error) {
	f.urls = append(f.urls, textURL)
	if f.err != nil {
		return nil, f.err
	}
	return []byte("<bill>" + textURL + "</bill>"), nil
}

func TestTextCacheDownloadsOnlyWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "BILLS-119hr1enr.xml"),
		[]byte("<bill>cached</bill>"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	fetch := &fakeFetcher{}
	cache := &textCache{dir: dir, baseURL: "https://api.example", fetch: fetch}
	bills := []evalBill{
		{BillID: "hr-119-1", PackageID: "BILLS-119hr1enr"},
		{BillID: "s-119-5", PackageID: "BILLS-119s5is"},
	}
	texts, err := cache.load(t.Context(), bills)
	if err != nil {
		t.Fatal(err)
	}
	if texts["hr-119-1"] != "<bill>cached</bill>" {
		t.Errorf("hr-119-1 = %q, want the cached text", texts["hr-119-1"])
	}
	wantURL := "https://api.example/packages/BILLS-119s5is/xml"
	if len(fetch.urls) != 1 || fetch.urls[0] != wantURL {
		t.Errorf("fetched %v, want only %s", fetch.urls, wantURL)
	}
	if _, err = os.Stat(filepath.Join(dir, "BILLS-119s5is.xml")); err != nil {
		t.Errorf("downloaded text wasn't cached: %v", err)
	}

	// A second load makes no requests.
	if _, err = cache.load(t.Context(), bills); err != nil || len(fetch.urls) != 1 {
		t.Errorf("second load: err %v, %d fetches; want none", err, len(fetch.urls)-1)
	}
}

func TestTextCacheReportsFetchErrors(t *testing.T) {
	cache := &textCache{dir: t.TempDir(), fetch: &fakeFetcher{err: errors.New("503")}}
	if _, err := cache.load(t.Context(), []evalBill{{BillID: "s-119-5", PackageID: "BILLS-119s5is"}}); err == nil {
		t.Error("want the fetch error")
	}
}

func TestBillContext(t *testing.T) {
	b := evalBill{
		BillID: "hr-119-1", Congress: 119, Type: "hr", Number: 1, Title: "An Act",
		Version: "enr", VersionName: "Enrolled Bill",
	}
	bc := b.billContext("<bill/>")
	if bc.BillID != "hr-119-1" || bc.Congress != 119 || bc.BillType != "hr" || bc.Number != 1 ||
		bc.Title != "An Act" || bc.VersionCode != "enr" || bc.VersionName != "Enrolled Bill" || bc.Text != "<bill/>" {
		t.Errorf("billContext = %+v", bc)
	}
}

// The CRS eval's list is 30 bills of the main list that have a CRS summary (design 197).
func TestCommittedCRSBillList(t *testing.T) {
	crs, err := loadBills(filepath.Join("testdata", "bills-crs.json"))
	if err != nil {
		t.Fatal(err)
	}
	all, err := loadBills(filepath.Join("testdata", "bills.json"))
	if err != nil {
		t.Fatal(err)
	}
	const wantBills = 30
	if len(crs) != wantBills {
		t.Fatalf("CRS list has %d bills, want %d", len(crs), wantBills)
	}
	for _, b := range crs {
		if !slices.Contains(all, b) {
			t.Errorf("%s isn't on the main list as it is there", b.BillID)
		}
	}
}
