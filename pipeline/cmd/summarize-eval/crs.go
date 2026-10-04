package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/htmltext"
)

// The CRS eval (docs/design/197-crs-summaries.md, "Testing and monitoring"): with --crs each
// model runs twice, without the CRS summary (as bill-v2 did) and with it, reported as <model> and
// <model>+crs, and every summary gets the share of its 8-word sequences found in the CRS text.
const (
	crsSuffix = "+crs"
	// copyGram is the length of the word sequences counted as copied, and copyTarget the share of
	// them the design accepts.
	copyGram   = 8
	copyTarget = 0.10

	// cacheFileMode is the mode of a cached file: readable only by its owner.
	cacheFileMode = 0o600
)

// crsFetcher lists a bill's CRS summaries; *congress.Client does.
type crsFetcher interface {
	BillSummaries(ctx context.Context, congress int, billType string, number int) ([]congress.CRSSummary, error)
}

// crsCache keeps each bill's CRS summaries on disk outside the repo, so a rerun makes no
// Congress.gov requests.
type crsCache struct {
	dir   string
	fetch crsFetcher
	pause time.Duration
}

// load returns each bill's latest CRS summary as the pipeline would give it to the model. A bill
// without one is left out.
func (c *crsCache) load(ctx context.Context, bills []evalBill) (map[string]*ai.CRSContext, error) {
	if err := os.MkdirAll(c.dir, 0o750); err != nil {
		return nil, fmt.Errorf("create crs cache: %w", err)
	}
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		return nil, fmt.Errorf("open crs cache: %w", err)
	}
	defer root.Close()
	out := make(map[string]*ai.CRSContext, len(bills))
	fetched := 0
	for _, b := range bills {
		summaries, didFetch, loadErr := c.summaries(ctx, root, b, fetched > 0)
		if loadErr != nil {
			return nil, fmt.Errorf("crs summaries of %s: %w", b.BillID, loadErr)
		}
		if didFetch {
			fetched++
		}
		if cs := latestCRS(summaries); cs != nil {
			out[b.BillID] = cs
		}
	}
	return out, nil
}

// summaries reads b's cached summaries, or fetches and caches them (after the pause, when pause is
// set). It reports whether it fetched.
func (c *crsCache) summaries(
	ctx context.Context, root *os.Root, b evalBill, pause bool,
) ([]congress.CRSSummary, bool, error) {
	name := b.BillID + ".crs.json"
	var summaries []congress.CRSSummary
	data, err := root.ReadFile(name)
	if err == nil {
		if err = json.Unmarshal(data, &summaries); err != nil {
			return nil, false, fmt.Errorf("parse cached %s: %w", name, err)
		}
		return summaries, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("read cached %s: %w", name, err)
	}
	if pause {
		if err = sleepContext(ctx, c.pause); err != nil {
			return nil, false, err
		}
	}
	if summaries, err = c.fetch.BillSummaries(ctx, b.Congress, b.Type, b.Number); err != nil {
		return nil, true, err
	}
	if data, err = json.Marshal(summaries); err == nil {
		err = root.WriteFile(name, data, cacheFileMode)
	}
	if err != nil {
		return nil, true, fmt.Errorf("cache %s: %w", name, err)
	}
	return summaries, true, nil
}

// latestCRS is the summary of the latest action, and of those the one updated last, rendered to
// plain text as the pipeline stores it; nil when there's none.
func latestCRS(summaries []congress.CRSSummary) *ai.CRSContext {
	if len(summaries) == 0 {
		return nil
	}
	latest := slices.MaxFunc(summaries, func(a, b congress.CRSSummary) int {
		return cmp.Or(cmp.Compare(a.ActionDate, b.ActionDate), cmp.Compare(a.UpdateDate, b.UpdateDate))
	})
	text := htmltext.Render(latest.Text)
	if text == "" {
		return nil
	}
	date, _ := time.Parse(time.DateOnly, latest.ActionDate)
	return &ai.CRSContext{VersionDesc: latest.ActionDesc, ActionDate: date, Text: text}
}

// wordPattern is a word for the copy check: letters, digits and apostrophes.
var wordPattern = regexp.MustCompile(`[\p{L}\p{N}']+`)

func words(s string) []string {
	s = strings.ToLower(strings.ReplaceAll(s, "’", "'"))
	return wordPattern.FindAllString(s, -1)
}

// copiedShare is the share of summary's n-word sequences that also appear in source, ignoring
// case and punctuation; 0 when summary has fewer than n words.
func copiedShare(summary, source string, n int) float64 {
	sw, src := words(summary), words(source)
	if len(sw) < n {
		return 0
	}
	grams := make(map[string]bool, len(src))
	for i := 0; i+n <= len(src); i++ {
		grams[strings.Join(src[i:i+n], " ")] = true
	}
	copied, total := 0, 0
	for i := 0; i+n <= len(sw); i++ {
		total++
		if grams[strings.Join(sw[i:i+n], " ")] {
			copied++
		}
	}
	return float64(copied) / float64(total)
}
