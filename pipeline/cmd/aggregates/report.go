package main

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/pipeline/internal/aggregates"
)

// columnGap is the spaces between the report's columns.
const columnGap = 2

// writeReport prints a dry run's totals, then its cells: those on hold or newly held, or every
// cell with all. It prints only what a cell would show (rounded numbers), never raw counts.
func writeReport(w io.Writer, res *aggregates.Result, at time.Time, all bool) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Dry run of aggregate-votes at %s (nothing written)\n", at.Format(time.RFC3339))
	fmt.Fprintf(&b, "Cells: %d published, %d suppressed, %d held\n", res.Cells[model.AggregateStatusPublished],
		res.Cells[model.AggregateStatusSuppressed], res.Cells[model.AggregateStatusHeld])
	fmt.Fprintf(&b, "New holds: %s\n", counts(res.Holds))
	fmt.Fprintf(&b, "Votes: %d eligible; ineligible: %s\n", res.Eligible, counts(res.Ineligible))
	fmt.Fprintf(&b, "rep_alignment rows: %d\n", res.Alignments)

	var rows []aggregates.Outcome
	for _, d := range res.Decided {
		if all || d.NewHold != "" || d.Cell.Status == model.AggregateStatusHeld || d.Cell.HoldReason != nil {
			rows = append(rows, d)
		}
	}
	if len(rows) == 0 {
		b.WriteString("\nNo cells on hold.\n")
		_, err := io.WriteString(w, b.String())
		return err
	}
	b.WriteString("\n")
	tw := tabwriter.NewWriter(&b, 0, 0, columnGap, ' ', 0)
	fmt.Fprintln(tw, "BILL\tCELL\tSTATUS\tYEA\tNAY\tVOTERS\tHOLD")
	for _, d := range rows {
		c := d.Cell
		hold := ""
		if c.HoldReason != nil {
			hold = *c.HoldReason
		}
		if d.NewHold != "" {
			hold += " (new)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.BillID, cellName(c.Scope, c.ScopeKey), c.Status,
			pct(c.YeaPct), pct(c.NayPct), floor(c.VotersFloor), hold)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// counts prints a map of counts as "a 1, b 2" in key order, or "none".
func counts(m map[string]int) string {
	parts := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if m[k] > 0 {
			parts = append(parts, k+" "+strconv.Itoa(m[k]))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func cellName(scope, scopeKey string) string {
	if scope == model.AggregateScopeNational {
		return "national"
	}
	return scopeKey
}

func pct(p *int) string {
	if p == nil {
		return "-"
	}
	return strconv.Itoa(*p) + "%"
}

func floor(p *int) string {
	if p == nil {
		return "-"
	}
	return strconv.Itoa(*p) + "+"
}

// writeChanges prints what a hold or release did to each cell, and returns how many changed.
func writeChanges(w io.Writer, changes []aggregates.Change) int {
	slices.SortFunc(changes, func(a, b aggregates.Change) int {
		return cmp.Or(cmp.Compare(scopeOrder(a.Before.Scope), scopeOrder(b.Before.Scope)),
			cmp.Compare(a.Before.ScopeKey, b.Before.ScopeKey))
	})
	changed := 0
	for _, c := range changes {
		name := c.Before.BillID + " " + cellName(c.Before.Scope, c.Before.ScopeKey)
		if !c.Changed {
			_, _ = fmt.Fprintf(w, "%s: unchanged, %s\n", name, state(&c.Before))
			continue
		}
		changed++
		_, _ = fmt.Fprintf(w, "%s: %s -> %s\n", name, state(&c.Before), state(&c.After))
	}
	return changed
}

// scopeOrder sorts national cells first, then states, then districts.
func scopeOrder(scope string) int {
	switch scope {
	case model.AggregateScopeNational:
		return 0
	case model.AggregateScopeState:
		return 1
	default:
		return 2 //nolint:mnd // the last of three
	}
}

// state is a cell's status and hold reason, e.g. "held (burst)".
func state(c *model.VoteAggregate) string {
	if c.HoldReason == nil {
		return c.Status
	}
	return c.Status + " (" + *c.HoldReason + ")"
}
