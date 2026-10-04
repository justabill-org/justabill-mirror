package aggregates_test

import (
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/pipeline/internal/aggregates"
)

// t0 is the tests' run time.
func t0() time.Time { return time.Date(2026, time.October, 20, 15, 0, 0, 0, time.UTC) }

func hourAgo() time.Time { return t0().Add(-time.Hour) }

func districtCell() aggregates.CellKey {
	return aggregates.CellKey{BillID: "hr-119-1", Scope: model.AggregateScopeDistrict, ScopeKey: "CA-12"}
}

func nationalCell() aggregates.CellKey {
	return aggregates.CellKey{BillID: "hr-119-1", Scope: model.AggregateScopeNational}
}

// publishedCell is a district cell published an hour ago at 50% of 60+, from 30 Yea and 30 Nay.
func publishedCell() *model.VoteAggregate {
	k, at := districtCell(), hourAgo()
	return &model.VoteAggregate{
		BillID: k.BillID, Scope: k.Scope, ScopeKey: k.ScopeKey,
		Status: model.AggregateStatusPublished, YeaPct: new(50), NayPct: new(50),
		VotersFloor: new(60), PublishedAt: &at, ComputedAt: at, BasisYea: new(30), BasisNay: new(30),
	}
}

func withStatus(c *model.VoteAggregate, status, reason string) *model.VoteAggregate {
	c.Status = status
	if reason != "" {
		c.HoldReason = &reason
	}
	return c
}

func suppressedCell(reason string) *model.VoteAggregate {
	k := districtCell()
	c := &model.VoteAggregate{
		BillID: k.BillID, Scope: k.Scope, ScopeKey: k.ScopeKey,
		Status: model.AggregateStatusSuppressed, ComputedAt: hourAgo(),
	}
	if reason != "" {
		c.HoldReason = &reason
	}
	return c
}

// want is the expected cell, in brief: status, the shown numbers (-1 when absent), whether it
// was published in this run, the hold reason, and whether the hold is new.
type want struct {
	status             string
	yeaPct, voters     int
	publishedNow       bool
	holdReason         string
	newHold            bool
	basisYea, basisNay int // checked when publishedNow
}

func check(t *testing.T, got aggregates.Outcome, w want) {
	t.Helper()
	c := got.Cell
	if c.Status != w.status {
		t.Errorf("status = %q, want %q", c.Status, w.status)
	}
	if yea := intOr(c.YeaPct); yea != w.yeaPct {
		t.Errorf("yea_pct = %d, want %d", yea, w.yeaPct)
	}
	if c.YeaPct != nil && *c.YeaPct+*c.NayPct != 100 {
		t.Errorf("yea_pct + nay_pct = %d, want 100", *c.YeaPct+*c.NayPct)
	}
	if voters := intOr(c.VotersFloor); voters != w.voters {
		t.Errorf("voters_floor = %d, want %d", voters, w.voters)
	}
	if now := c.PublishedAt != nil && c.PublishedAt.Equal(t0()); now != w.publishedNow {
		t.Errorf("published now = %v, want %v (published_at %v)", now, w.publishedNow, c.PublishedAt)
	}
	if w.publishedNow && (intOr(c.BasisYea) != w.basisYea || intOr(c.BasisNay) != w.basisNay) {
		t.Errorf("basis = %d/%d, want %d/%d", intOr(c.BasisYea), intOr(c.BasisNay), w.basisYea, w.basisNay)
	}
	reason := ""
	if c.HoldReason != nil {
		reason = *c.HoldReason
	}
	if reason != w.holdReason {
		t.Errorf("hold_reason = %q, want %q", reason, w.holdReason)
	}
	if (got.NewHold != "") != w.newHold || (w.newHold && got.NewHold != w.holdReason) {
		t.Errorf("new hold = %q, want %v (%q)", got.NewHold, w.newHold, w.holdReason)
	}
	if !c.ComputedAt.Equal(t0()) {
		t.Errorf("computed_at = %v, want %v", c.ComputedAt, t0())
	}
}

// intOr returns *p, or -1 when p is nil.
func intOr(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

func TestPublish(t *testing.T) {
	const (
		pub  = model.AggregateStatusPublished
		sup  = model.AggregateStatusSuppressed
		held = model.AggregateStatusHeld
	)
	tests := []struct {
		name   string
		key    aggregates.CellKey
		prev   *model.VoteAggregate
		counts aggregates.Counts
		want   want
	}{
		{
			name: "49 votes in a district are suppressed", key: districtCell(),
			counts: aggregates.Counts{Yea: 30, Nay: 19, New: 49},
			want:   want{status: sup, yeaPct: -1, voters: -1},
		},
		{
			name: "50 votes in a district are published, rounded", key: districtCell(),
			counts: aggregates.Counts{Yea: 33, Nay: 17, New: 50},
			want:   want{status: pub, yeaPct: 66, voters: 50, publishedNow: true, basisYea: 33, basisNay: 17},
		},
		{
			name: "a half percent rounds up", key: districtCell(),
			counts: aggregates.Counts{Yea: 101, Nay: 99, New: 200},
			want:   want{status: pub, yeaPct: 51, voters: 200, publishedNow: true, basisYea: 101, basisNay: 99},
		},
		{
			name: "99 votes nationally are suppressed", key: nationalCell(),
			counts: aggregates.Counts{Yea: 60, Nay: 39, New: 99},
			want:   want{status: sup, yeaPct: -1, voters: -1},
		},
		{
			name: "100 votes nationally are published", key: nationalCell(),
			counts: aggregates.Counts{Yea: 60, Nay: 47, New: 107},
			want:   want{status: pub, yeaPct: 56, voters: 100, publishedNow: true, basisYea: 60, basisNay: 47},
		},
		{
			name: "9 changed votes keep the old numbers", key: districtCell(), prev: publishedCell(),
			counts: aggregates.Counts{Yea: 34, Nay: 35, New: 9},
			want:   want{status: pub, yeaPct: 50, voters: 60},
		},
		{
			name: "10 changed votes republish", key: districtCell(), prev: publishedCell(),
			counts: aggregates.Counts{Yea: 35, Nay: 35, New: 10},
			want:   want{status: pub, yeaPct: 50, voters: 70, publishedNow: true, basisYea: 35, basisNay: 35},
		},
		{
			name: "a changed vote counts twice", key: districtCell(), prev: publishedCell(),
			counts: aggregates.Counts{Yea: 25, Nay: 35, New: 5},
			want:   want{status: pub, yeaPct: 42, voters: 60, publishedNow: true, basisYea: 25, basisNay: 35},
		},
		{
			name: "a cell falling below the minimum is suppressed", key: districtCell(), prev: publishedCell(),
			counts: aggregates.Counts{Yea: 20, Nay: 29},
			want:   want{status: sup, yeaPct: -1, voters: -1},
		},
		{
			name: "a burst holds the cell with its old numbers", key: districtCell(), prev: publishedCell(),
			counts: aggregates.Counts{Yea: 91, Nay: 30, Recent: 61, Baseline: 168, New: 61},
			want:   want{status: held, yeaPct: 50, voters: 60, holdReason: model.HoldReasonBurst, newHold: true},
		},
		{
			name: "a burst of 50 or fewer votes isn't a hold", key: districtCell(), prev: publishedCell(),
			counts: aggregates.Counts{Yea: 40, Nay: 40, Recent: 50, Baseline: 0, New: 20},
			want:   want{status: pub, yeaPct: 50, voters: 80, publishedNow: true, basisYea: 40, basisNay: 40},
		},
		{
			name: "5 times the hourly mean isn't a burst", key: districtCell(), prev: publishedCell(),
			counts: aggregates.Counts{Yea: 60, Nay: 60, Recent: 60, Baseline: 168 * 12, New: 60},
			want:   want{status: pub, yeaPct: 50, voters: 120, publishedNow: true, basisYea: 60, basisNay: 60},
		},
		{
			name: "over 40% of new votes from young accounts holds", key: districtCell(), prev: publishedCell(),
			counts: aggregates.Counts{Yea: 40, Nay: 40, New: 20, Young: 9},
			want: want{
				status: held, yeaPct: 50, voters: 60, holdReason: model.HoldReasonYoungAccounts, newHold: true,
			},
		},
		{
			name: "40% of new votes from young accounts publishes", key: districtCell(), prev: publishedCell(),
			counts: aggregates.Counts{Yea: 40, Nay: 40, New: 20, Young: 8},
			want:   want{status: pub, yeaPct: 50, voters: 80, publishedNow: true, basisYea: 40, basisNay: 40},
		},
		{
			name: "a 15-point swing holds", key: districtCell(), prev: publishedCell(),
			counts: aggregates.Counts{Yea: 65, Nay: 35, New: 40},
			want:   want{status: held, yeaPct: 50, voters: 60, holdReason: model.HoldReasonSwing, newHold: true},
		},
		{
			name: "a 14-point swing publishes", key: districtCell(), prev: publishedCell(),
			counts: aggregates.Counts{Yea: 64, Nay: 36, New: 40},
			want:   want{status: pub, yeaPct: 64, voters: 100, publishedNow: true, basisYea: 64, basisNay: 36},
		},
		{
			name: "a held cell stays held", key: districtCell(),
			prev:   withStatus(publishedCell(), held, model.HoldReasonSwing),
			counts: aggregates.Counts{Yea: 40, Nay: 40, New: 20},
			want:   want{status: held, yeaPct: 50, voters: 60, holdReason: model.HoldReasonSwing},
		},
		{
			name: "a manual hold stays", key: districtCell(),
			prev:   withStatus(publishedCell(), held, model.HoldReasonManual),
			counts: aggregates.Counts{Yea: 40, Nay: 40, New: 20},
			want:   want{status: held, yeaPct: 50, voters: 60, holdReason: model.HoldReasonManual},
		},
		{
			name: "a held cell falling below the minimum is suppressed", key: districtCell(),
			prev:   withStatus(publishedCell(), held, model.HoldReasonBurst),
			counts: aggregates.Counts{Yea: 20, Nay: 20},
			want:   want{status: sup, yeaPct: -1, voters: -1},
		},
		{
			name: "a released cell publishes once without the hold rules", key: districtCell(),
			prev:   withStatus(publishedCell(), pub, model.HoldReasonReleased),
			counts: aggregates.Counts{Yea: 65, Nay: 35, Recent: 70, New: 40, Young: 40},
			want:   want{status: pub, yeaPct: 65, voters: 100, publishedNow: true, basisYea: 65, basisNay: 35},
		},
		{
			name: "a released cell waits for enough changes", key: districtCell(),
			prev:   withStatus(publishedCell(), pub, model.HoldReasonReleased),
			counts: aggregates.Counts{Yea: 31, Nay: 30, New: 1},
			want:   want{status: pub, yeaPct: 50, voters: 60, holdReason: model.HoldReasonReleased},
		},
		{
			name: "an anomaly before the first publish keeps the cell suppressed", key: districtCell(),
			counts: aggregates.Counts{Yea: 40, Nay: 20, New: 60, Young: 30},
			want: want{
				status: sup, yeaPct: -1, voters: -1, holdReason: model.HoldReasonYoungAccounts, newHold: true,
			},
		},
		{
			name: "a cell held before its first publish stays suppressed", key: districtCell(),
			prev:   suppressedCell(model.HoldReasonYoungAccounts),
			counts: aggregates.Counts{Yea: 40, Nay: 20, New: 60},
			want:   want{status: sup, yeaPct: -1, voters: -1, holdReason: model.HoldReasonYoungAccounts},
		},
		{
			name: "a released suppressed cell publishes", key: districtCell(),
			prev:   suppressedCell(model.HoldReasonReleased),
			counts: aggregates.Counts{Yea: 40, Nay: 20, New: 60, Young: 30},
			want:   want{status: pub, yeaPct: 67, voters: 60, publishedNow: true, basisYea: 40, basisNay: 20},
		},
		{
			name: "a suppressed cell reaching the minimum publishes at once", key: districtCell(),
			prev:   suppressedCell(""),
			counts: aggregates.Counts{Yea: 25, Nay: 25, New: 50},
			want:   want{status: pub, yeaPct: 50, voters: 50, publishedNow: true, basisYea: 25, basisNay: 25},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check(t, aggregates.Publish(tt.key, tt.prev, tt.counts, aggregates.DefaultConfig(), t0()), tt.want)
		})
	}
}

func TestPublishWaitsAnHourBetweenPublishes(t *testing.T) {
	prev := publishedCell()
	recent := t0().Add(-59 * time.Minute)
	prev.PublishedAt = &recent
	got := aggregates.Publish(districtCell(), prev, aggregates.Counts{Yea: 45, Nay: 45, New: 30},
		aggregates.DefaultConfig(), t0())
	check(t, got, want{status: model.AggregateStatusPublished, yeaPct: 50, voters: 60})
	if !got.Cell.PublishedAt.Equal(recent) {
		t.Errorf("published_at = %v, want %v", got.Cell.PublishedAt, recent)
	}
}

// TestPublishHidesOneVote is the design's privacy test: one more vote on a published cell
// doesn't change what's published next, whichever way it goes.
func TestPublishHidesOneVote(t *testing.T) {
	cfg := aggregates.DefaultConfig()
	first := aggregates.Publish(districtCell(), nil, aggregates.Counts{Yea: 41, Nay: 32, New: 73}, cfg, hourAgo()).Cell
	for _, counts := range []aggregates.Counts{
		{Yea: 42, Nay: 32, New: 1},
		{Yea: 41, Nay: 33, New: 1},
		{Yea: 40, Nay: 33, New: 1}, // a changed vote
	} {
		next := aggregates.Publish(districtCell(), &first, counts, cfg, t0().Add(24*time.Hour)).Cell
		if *next.YeaPct != *first.YeaPct || *next.NayPct != *first.NayPct ||
			*next.VotersFloor != *first.VotersFloor || !next.PublishedAt.Equal(*first.PublishedAt) {
			t.Errorf("after %+v: published %d%%/%d%% of %d+ at %v, want %d%%/%d%% of %d+ at %v", counts,
				*next.YeaPct, *next.NayPct, *next.VotersFloor, next.PublishedAt,
				*first.YeaPct, *first.NayPct, *first.VotersFloor, first.PublishedAt)
		}
	}
}
