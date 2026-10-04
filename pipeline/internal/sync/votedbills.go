package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

// errUnknownBill means Congress.gov answered 404 for a bill's detail: it has no such bill.
var errUnknownBill = errors.New("bill unknown to Congress.gov")

// SyncVotedBills loads the bills a congress's stored roll calls name that aren't stored yet
// (docs/design/77-launch-data-load.md, item 2). Votes keep their bill_id whether or not the
// bill is stored, so after this every roll call links to its bill, whichever loaded first. A
// bill that fails stays missing, so the next run retries it; an unresolved one is skipped.
// A limit above 0 syncs only the first limit missing bills (local runs) and, like SyncBills,
// leaves sync_state alone on success.
func (s *Service) SyncVotedBills(ctx context.Context, congressNum, limit int) error {
	return s.runStep(ctx, stepVotedBills, congressNum, limit > 0, func(ctx context.Context) (int, error) {
		ids, err := s.store.ListMissingVotedBillIDs(ctx, congressNum)
		if err != nil {
			return 0, fmt.Errorf("list missing voted bills: %w", err)
		}
		s.logger.InfoContext(ctx, "syncing voted bills", "congress", congressNum, "missing", len(ids),
			"limit", limit)
		if limit > 0 && len(ids) > limit {
			ids = ids[:limit]
		}
		return s.SyncBillsByID(ctx, ids), nil
	})
}

// SyncBillsByID syncs the bills in ids ("hr-119-1") through the bills step's path: detail,
// sub-resources, then MarkBillSynced. Each bill is fetched once, under the congress its ID
// names. An ID that doesn't parse, or whose detail Congress.gov answers 404 for, is logged as
// unresolved_bill_ref and skipped; any other failure is logged as in SyncBills. It returns
// how many bills synced.
func (s *Service) SyncBillsByID(ctx context.Context, ids []string) int {
	bills := make([]congress.BillSummary, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	unresolved := 0
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		bs, err := billSummaryFromID(id)
		if err != nil {
			s.logger.WarnContext(ctx, "unresolved_bill_ref", "bill", id, "error", err)
			unresolved++
			continue
		}
		bills = append(bills, bs)
	}

	progress := newBillProgress(s.logger, s.requestCount, len(bills), 0)
	progress.unresolved.n.Add(int64(unresolved))

	workerPool(ctx, bills, defaultCongressWorkers, func(ctx context.Context, bs congress.BillSummary) {
		err := s.syncOneBill(ctx, bs, bs.Congress)
		switch {
		case errors.Is(err, errUnknownBill):
			s.logger.WarnContext(ctx, "unresolved_bill_ref", "bill", billID(bs, bs.Congress), "error", err)
			progress.addUnresolved(ctx)
		case err != nil:
			s.logger.WarnContext(ctx, "sync bill failed", "bill", billID(bs, bs.Congress), "error", err)
			progress.add(ctx, false)
		default:
			progress.add(ctx, true)
		}
	})

	progress.log(ctx, "bills by ID synced")
	s.tally.addBills(progress.done.get(), progress.failed.get(), progress.unresolved.get())
	return progress.done.get()
}

// billSummaryFromID turns a stored bill ID into the list entry syncOneBill takes. The ID must
// be canonical (one of the eight bill types, no leading zeros), so the bill syncs under the
// same ID the votes hold.
func billSummaryFromID(id string) (congress.BillSummary, error) {
	congressNum, billType, number, err := parseBillID(id)
	if err != nil {
		return congress.BillSummary{}, err
	}
	bs := congress.BillSummary{Congress: congressNum, Type: billType, Number: strconv.Itoa(number)}
	if _, known := billTypeCitations[billType]; !known || congressNum <= 0 || number <= 0 ||
		billID(bs, congressNum) != id {
		return congress.BillSummary{}, fmt.Errorf("not a canonical bill ID: %q", id)
	}
	return bs, nil
}

// notFound reports whether a Congress.gov request failed with HTTP 404, whether the client
// or the upstream transport reported it.
func notFound(err error) bool {
	if errors.Is(err, congress.ErrNotFound) {
		return true
	}
	se, ok := errors.AsType[*upstream.StatusError](err)
	return ok && se.Status == http.StatusNotFound
}
