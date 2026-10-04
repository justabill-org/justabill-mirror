package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/ai"
)

// Categories of the committed bill list (design 68, "Testing and monitoring" > Eval).
const (
	categoryFloorVote  = "floor_vote"
	categoryResolution = "resolution"
	categoryLong       = "long"
	categoryRandom     = "random"
	// categoryCRA is a Congressional Review Act resolution with the rule it disapproves (--rule,
	// testdata/bills-cra.json).
	categoryCRA = "cra"
)

// evalBill is one entry of testdata/bills.json: a bill and the GovInfo package of the text
// version the eval summarizes (the latest one when the list was drawn).
type evalBill struct {
	BillID      string `json:"bill_id"`
	Congress    int    `json:"congress"`
	Type        string `json:"type"`
	Number      int    `json:"number"`
	Category    string `json:"category"`
	PackageID   string `json:"package_id"`
	Version     string `json:"version"`
	VersionName string `json:"version_name"`
	Title       string `json:"title"`
	// Rule is the rule a CRA resolution disapproves, as the pipeline's matcher found it; set
	// exactly for categoryCRA.
	Rule *evalRule `json:"rule"`
}

// packageIDPattern is a GovInfo BILLS package ID, e.g. BILLS-119hr1enr.
var packageIDPattern = regexp.MustCompile(`^BILLS-\d+[a-z]+\d+[a-z]+$`)

// loadBills reads and checks the bill list.
func loadBills(path string) ([]evalBill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read bill list: %w", err)
	}
	var bills []evalBill
	if err = json.Unmarshal(data, &bills); err != nil {
		return nil, fmt.Errorf("parse bill list %s: %w", path, err)
	}
	seen := make(map[string]bool, len(bills))
	for _, b := range bills {
		switch {
		case b.BillID == "" || seen[b.BillID]:
			return nil, fmt.Errorf("bill list %s: missing or duplicate bill_id %q", path, b.BillID)
		case !packageIDPattern.MatchString(b.PackageID):
			return nil, fmt.Errorf("bill list %s: %s has package_id %q", path, b.BillID, b.PackageID)
		case b.Category != categoryFloorVote && b.Category != categoryResolution &&
			b.Category != categoryLong && b.Category != categoryRandom && b.Category != categoryCRA:
			return nil, fmt.Errorf("bill list %s: %s has category %q", path, b.BillID, b.Category)
		case (b.Category == categoryCRA) != (b.Rule != nil):
			return nil, fmt.Errorf("bill list %s: %s: a rule goes with category %s, and only there", path, b.BillID,
				categoryCRA)
		}
		if b.Rule != nil {
			if err = b.Rule.validate(); err != nil {
				return nil, fmt.Errorf("bill list %s: %s: %w", path, b.BillID, err)
			}
		}
		seen[b.BillID] = true
	}
	return bills, nil
}

// billContext is what the summarizer gets for b: the fields #194's LoadBillContext will fill
// from Spanner, as far as the list has them, and the stored text as GovInfo serves it.
func (b evalBill) billContext(text string) ai.BillContext {
	return ai.BillContext{
		BillID:      b.BillID,
		Congress:    b.Congress,
		BillType:    b.Type,
		Number:      b.Number,
		Title:       b.Title,
		VersionCode: b.Version,
		VersionName: b.VersionName,
		Text:        text,
	}
}

// textFetcher downloads a URL; *govinfo.Client does, adding the key on api.govinfo.gov.
type textFetcher interface {
	FetchText(ctx context.Context, textURL string) ([]byte, error)
}

// textCache keeps bill XML on disk outside the repo, so a rerun makes no GovInfo requests.
type textCache struct {
	dir     string
	baseURL string
	fetch   textFetcher
	// pause is the wait between two GovInfo downloads.
	pause time.Duration
}

// load returns the XML of every bill, downloading only what isn't cached.
func (c *textCache) load(ctx context.Context, bills []evalBill) (map[string]string, error) {
	if err := os.MkdirAll(c.dir, 0o750); err != nil {
		return nil, fmt.Errorf("create text cache: %w", err)
	}
	texts := make(map[string]string, len(bills))
	fetched := 0
	for _, b := range bills {
		path := filepath.Join(c.dir, b.PackageID+".xml")
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			if fetched > 0 {
				if err = sleepContext(ctx, c.pause); err != nil {
					return nil, err
				}
			}
			fetched++
			data, err = c.fetch.FetchText(ctx, c.baseURL+"/packages/"+b.PackageID+"/xml")
			if err == nil {
				err = os.WriteFile(path, data, 0o600)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("text for %s (%s): %w", b.BillID, b.PackageID, err)
		}
		texts[b.BillID] = string(data)
	}
	return texts, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}
