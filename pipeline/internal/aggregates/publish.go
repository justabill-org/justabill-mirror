package aggregates

import (
	"math"
	"time"

	"github.com/justabill-org/justabill/db/model"
)

const (
	scopeNational = model.AggregateScopeNational
	scopeState    = model.AggregateScopeState
	scopeDistrict = model.AggregateScopeDistrict

	statusPublished  = model.AggregateStatusPublished
	statusSuppressed = model.AggregateStatusSuppressed
	statusHeld       = model.AggregateStatusHeld

	// votersRounding is what published voter counts are rounded down to ("340+ users").
	votersRounding = 10
	percent        = 100
)

// CellKey names one cell: a bill in one scope. ScopeKey is "" for the national cell, a state
// ("CA") or a district ("CA-12", at-large "AK-0").
type CellKey struct {
	BillID   string
	Scope    string
	ScopeKey string
}

func keyOf(c *model.VoteAggregate) CellKey {
	return CellKey{BillID: c.BillID, Scope: c.Scope, ScopeKey: c.ScopeKey}
}

// Counts are one cell's eligible votes in a snapshot.
type Counts struct {
	Yea int
	Nay int
	// Recent counts votes in the last hour, and Baseline those in the 7 days before it.
	Recent   int
	Baseline int
	// New counts votes since the cell's last publish (all of them if it was never published),
	// and Young how many of those came from young accounts.
	New   int
	Young int
}

// Outcome is what [Publish] decided for one cell.
type Outcome struct {
	// Cell is the cell to write.
	Cell model.VoteAggregate
	// NewHold names the rule (a model.HoldReason* constant) when this run put the cell on hold,
	// and is "" otherwise. A cell already on hold isn't a new hold.
	NewHold string
}

// Publish applies the publication rules and anomaly holds to one cell
// (docs/design/89-aggregate-analytics.md). key names the cell; prev
// is the stored cell, or nil. In order:
//
//  1. Fewer eligible votes than the scope's minimum: suppressed, with no numbers.
//  2. On hold (held, or suppressed with a hold reason before it was ever shown): unchanged until
//     a human releases it.
//  3. Shown, but fewer than RepublishMinChanges changed votes, or less than RepublishMinInterval
//     since the last publish: the old numbers stay.
//  4. An anomaly rule trips (burst, young accounts, swing) and the cell wasn't just released:
//     held with its old numbers, or suppressed with the reason if it was never shown.
//  5. Otherwise the rounded numbers are published.
//
// It's pure: the same inputs always give the same cell.
func Publish(key CellKey, prev *model.VoteAggregate, c Counts, cfg Config, now time.Time) Outcome {
	cell := model.VoteAggregate{BillID: key.BillID, Scope: key.Scope, ScopeKey: key.ScopeKey, ComputedAt: now}
	if prev != nil {
		cell.YeaPct, cell.NayPct, cell.VotersFloor = prev.YeaPct, prev.NayPct, prev.VotersFloor
		cell.PublishedAt, cell.BasisYea, cell.BasisNay = prev.PublishedAt, prev.BasisYea, prev.BasisNay
		cell.HoldReason, cell.Status = prev.HoldReason, prev.Status
	}
	total := c.Yea + c.Nay
	if total < cfg.minVotes(key.Scope) {
		// Keep the basis and publish time for the republish rule, but nothing that could be shown.
		cell.Status, cell.YeaPct, cell.NayPct, cell.VotersFloor, cell.HoldReason = statusSuppressed, nil, nil, nil, nil
		return Outcome{Cell: cell}
	}
	if onHold(prev) {
		return Outcome{Cell: cell}
	}
	shown := prev != nil && prev.Status == statusPublished && prev.YeaPct != nil && prev.PublishedAt != nil
	if shown && !republishDue(prev, c, cfg, now) {
		return Outcome{Cell: cell}
	}
	yeaPct := int(math.Round(float64(c.Yea*percent) / float64(total))) // whole percent, halves up
	released := prev != nil && prev.HoldReason != nil && *prev.HoldReason == model.HoldReasonReleased
	if !released {
		if reason := anomaly(prev, shown, c, yeaPct, cfg); reason != "" {
			cell.HoldReason = &reason
			if shown {
				cell.Status = statusHeld
			} else {
				cell.Status, cell.YeaPct, cell.NayPct, cell.VotersFloor = statusSuppressed, nil, nil, nil
			}
			return Outcome{Cell: cell, NewHold: reason}
		}
	}
	nayPct, floor := percent-yeaPct, total/votersRounding*votersRounding
	yea, nay, at := c.Yea, c.Nay, now
	cell.Status, cell.YeaPct, cell.NayPct, cell.VotersFloor = statusPublished, &yeaPct, &nayPct, &floor
	cell.PublishedAt, cell.BasisYea, cell.BasisNay, cell.HoldReason = &at, &yea, &nay, nil
	return Outcome{Cell: cell}
}

// onHold reports whether prev is waiting for a human: held, or suppressed with a hold reason
// other than released.
func onHold(prev *model.VoteAggregate) bool {
	if prev == nil {
		return false
	}
	if prev.Status == statusHeld {
		return true
	}
	return prev.Status == statusSuppressed && prev.HoldReason != nil && *prev.HoldReason != model.HoldReasonReleased
}

// republishDue reports whether a shown cell has changed enough, and waited long enough, to be
// republished. Changes are measured against the counts it was last published from.
func republishDue(prev *model.VoteAggregate, c Counts, cfg Config, now time.Time) bool {
	changes := abs(c.Yea-deref(prev.BasisYea)) + abs(c.Nay-deref(prev.BasisNay))
	return changes >= cfg.RepublishMinChanges && now.Sub(*prev.PublishedAt) >= cfg.RepublishMinInterval
}

// anomaly returns the first hold rule the counts trip, or "".
func anomaly(prev *model.VoteAggregate, shown bool, c Counts, yeaPct int, cfg Config) string {
	// Recent > factor × (Baseline / hours in the baseline), without dividing.
	baselineHours := int(baselineWindow / burstWindow)
	if c.Recent > cfg.BurstMinVotes && c.Recent*baselineHours > cfg.BurstFactor*c.Baseline {
		return model.HoldReasonBurst
	}
	if c.New > 0 && c.Young*percent > cfg.YoungShareMaxPct*c.New {
		return model.HoldReasonYoungAccounts
	}
	if shown && abs(yeaPct-*prev.YeaPct) >= cfg.SwingPoints {
		return model.HoldReasonSwing
	}
	return ""
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
