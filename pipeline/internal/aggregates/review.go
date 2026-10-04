package aggregates

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/justabill-org/justabill/db/model"
)

// Holds and releases for the aggregates CLI (docs/design/89-aggregate-analytics.md, item 6).
// A cell on hold keeps what it showed until a human releases it; [Publish] reads the result:
//
//   - A manual hold on a shown cell makes it held (its numbers stay, marked under review), and
//     on a never-shown cell leaves it suppressed with the reason, so it isn't published.
//   - A release makes a held cell published again with its old numbers, and a never-shown cell
//     suppressed. Both get the released marker, so the next run publishes their new numbers
//     without the anomaly rules holding them again at once.
//
// A cell that falls below its minimum loses any hold at the next run, as [Publish] documents.

// Reviser is the part of the store the hold and release commands use.
type Reviser interface {
	ReviseVoteAggregates(
		ctx context.Context, billIDs []string, revise func(stored []model.VoteAggregate) ([]model.VoteAggregate, error),
	) error
}

// ErrNoCells is returned when a hold or release names no stored cell.
var ErrNoCells = errors.New("no stored aggregate cell")

var (
	stateKeyPattern    = regexp.MustCompile(`^[A-Z]{2}$`)
	districtKeyPattern = regexp.MustCompile(`^[A-Z]{2}-\d{1,2}$`)
)

// ParseCell reads a cell name from the command line, for the bill: "national", a state ("CA")
// or a district ("CA-12", at-large "AK-0"), in any case.
func ParseCell(billID, name string) (CellKey, error) {
	key := strings.ToUpper(strings.TrimSpace(name))
	switch {
	case key == "NATIONAL":
		return CellKey{BillID: billID, Scope: scopeNational}, nil
	case stateKeyPattern.MatchString(key):
		return CellKey{BillID: billID, Scope: scopeState, ScopeKey: key}, nil
	case districtKeyPattern.MatchString(key):
		return CellKey{BillID: billID, Scope: scopeDistrict, ScopeKey: key}, nil
	default:
		return CellKey{}, fmt.Errorf("cell %q: want national, a state (CA) or a district (CA-12)", name)
	}
}

// Change is what a hold or release did to one cell.
type Change struct {
	Before model.VoteAggregate
	After  model.VoteAggregate
	// Changed is false when the cell was already in the state asked for (After equals Before).
	Changed bool
}

// Hold puts the bill's stored cells (or only cell, when it's not nil) on a manual hold, in one
// transaction. It returns ErrNoCells when there's nothing to hold.
func Hold(ctx context.Context, store Reviser, billID string, cell *CellKey) ([]Change, error) {
	return revise(ctx, store, billID, cell, holdCell)
}

// Release releases the bill's cells on hold (or only cell), in one transaction. It returns
// ErrNoCells when there's no stored cell to release.
func Release(ctx context.Context, store Reviser, billID string, cell *CellKey) ([]Change, error) {
	return revise(ctx, store, billID, cell, releaseCell)
}

func revise(
	ctx context.Context,
	store Reviser,
	billID string,
	cell *CellKey,
	change func(model.VoteAggregate) (model.VoteAggregate, bool),
) ([]Change, error) {
	// The transaction may call the function more than once; only the last call counts.
	var changes []Change
	err := store.ReviseVoteAggregates(ctx, []string{billID},
		func(stored []model.VoteAggregate) ([]model.VoteAggregate, error) {
			changes = nil
			var writes []model.VoteAggregate
			for i := range stored {
				if stored[i].BillID != billID || (cell != nil && keyOf(&stored[i]) != *cell) {
					continue
				}
				after, changed := change(stored[i])
				changes = append(changes, Change{Before: stored[i], After: after, Changed: changed})
				if changed {
					writes = append(writes, after)
				}
			}
			if len(changes) == 0 {
				return nil, ErrNoCells
			}
			return writes, nil
		})
	if err != nil {
		return nil, fmt.Errorf("bill %s: %w", billID, err)
	}
	return changes, nil
}

// holdCell puts a cell on a manual hold, and reports false for a cell already on hold.
func holdCell(c model.VoteAggregate) (model.VoteAggregate, bool) {
	if onHold(&c) {
		return c, false
	}
	manual := model.HoldReasonManual
	c.HoldReason = &manual
	if c.Status == statusPublished {
		c.Status = statusHeld
	}
	return c, true
}

// releaseCell releases a cell on hold, and reports false for a cell that isn't.
func releaseCell(c model.VoteAggregate) (model.VoteAggregate, bool) {
	if !onHold(&c) {
		return c, false
	}
	released := model.HoldReasonReleased
	c.HoldReason = &released
	if c.Status == statusHeld {
		c.Status = statusPublished
	}
	return c, true
}
