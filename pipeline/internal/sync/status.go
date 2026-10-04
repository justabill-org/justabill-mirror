package sync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// Bill lifecycle stage ranks, ordered from earliest to latest.
const (
	rankIntroduced           = 1
	rankInCommittee          = 2
	rankReported             = 3
	rankPassedHouse          = 4
	rankPassedSenate         = 5
	rankResolvingDifferences = 6
	rankToPresident          = 7
	rankSigned               = 8
	rankVetoed               = 9
	rankBecameLaw            = 10
)

// Lifecycle statuses, as stored in bills.current_status and bill_status_history.
const (
	statusPassedHouse  = "passed_house"
	statusPassedSenate = "passed_senate"
	statusToPresident  = "to_president"
	statusSigned       = "signed"
	statusVetoed       = "vetoed"
	statusBecameLaw    = "became_law"
)

// sourceLibraryOfCongress is the source of the actions whose codes classifyCode reads. Other
// sources reuse some codes for other events: the House floor actions code "Vetoed by
// President." E30000, the Library of Congress's code for a signing (H.R. 131, 119th).
const sourceLibraryOfCongress = "Library of Congress"

// Library of Congress action codes for the lifecycle stages (#659); actionCodeHousePassage and
// actionCodeSenatePassage are in voicevotes.go.
const (
	actionCodeHouseFailed     = "9000"
	actionCodeSenateFailed    = "18000"
	actionCodePresented       = "28000"
	actionCodeSigned          = "E30000"
	actionCodePrivateLaw      = "41000" // a private law's signing and law; #659 counts it as signed
	actionCodeVetoed          = "31000"
	actionCodeHouseVetoFailed = "33000"
	actionCodePublicLaw       = "36000"
	actionCodeBecameLaw       = "E40000"
)

const (
	stepStatusHistory = "status_history"

	// DefaultStatusHistoryPageSize is how many bills RederiveStatusHistory reads per page. The
	// sync_state checkpoint is written after every page.
	DefaultStatusHistoryPageSize = 200

	// statusHistoryProgressPages is how many pages pass between progress logs.
	statusHistoryProgressPages = 10
)

type statusEntry struct {
	status string
	rank   int
	date   time.Time
}

// lifecycleAction is what classifyAction reads from an action, from Congress.gov or from
// bill_actions.
type lifecycleAction struct {
	date       string // YYYY-MM-DD
	text       string
	actionType string
	code       string
	source     string
}

func apiLifecycleActions(actions []congress.Action) []lifecycleAction {
	out := make([]lifecycleAction, 0, len(actions))
	for _, a := range actions {
		la := lifecycleAction{date: a.ActionDate, text: a.Text, actionType: a.Type, code: a.ActionCode}
		if a.SourceSystem != nil {
			la.source = a.SourceSystem.Name
		}
		out = append(out, la)
	}
	return out
}

func storedLifecycleActions(actions []repository.BillActionRow) []lifecycleAction {
	out := make([]lifecycleAction, 0, len(actions))
	for _, a := range actions {
		out = append(out, lifecycleAction{
			date: a.ActionDate.Format(time.DateOnly), text: a.ActionText,
			actionType: deref(a.ActionType), code: deref(a.ActionCode), source: deref(a.SourceSystem),
		})
	}
	return out
}

// deref is *s, or "" for nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// classifyAction returns the lifecycle status and rank for a congressional action, or "" and 0
// if it isn't a lifecycle stage. A Library of Congress action with a code in the maintainer's table
// (#659) is classified by its code alone; any other action by its text.
func classifyAction(a lifecycleAction, currentBestRank int) (string, int) {
	if a.source == sourceLibraryOfCongress {
		if status, rank, ok := classifyCode(a.code); ok {
			return status, rank
		}
	}
	return classifyText(strings.ToLower(a.text), strings.ToLower(a.actionType), currentBestRank)
}

// classifyCode maps a Library of Congress action code to its stage. ok is false for a code it
// doesn't know; a failed passage is known and is no stage.
func classifyCode(code string) (string, int, bool) {
	switch code {
	case actionCodeHousePassage:
		return statusPassedHouse, rankPassedHouse, true
	case actionCodeSenatePassage:
		return statusPassedSenate, rankPassedSenate, true
	case actionCodePresented:
		return statusToPresident, rankToPresident, true
	case actionCodeSigned, actionCodePrivateLaw:
		return statusSigned, rankSigned, true
	case actionCodeVetoed:
		return statusVetoed, rankVetoed, true
	case actionCodePublicLaw, actionCodeBecameLaw:
		return statusBecameLaw, rankBecameLaw, true
	case actionCodeHouseFailed, actionCodeSenateFailed, actionCodeHouseVetoFailed:
		return "", 0, true
	default:
		return "", 0, false
	}
}

// classifyText classifies an action by its lowercased text and type, for actions without a
// code classifyCode knows. Only "signed by president" is a signing: a "President" type alone
// is also a presentment or a veto.
func classifyText(text, actionType string, currentBestRank int) (string, int) {
	switch {
	case strings.Contains(text, "became public law") || strings.Contains(text, "became private law"):
		return statusBecameLaw, rankBecameLaw
	case strings.Contains(text, "vetoed"):
		return statusVetoed, rankVetoed
	case strings.Contains(text, "signed by president"):
		return statusSigned, rankSigned
	case strings.Contains(text, "presented to president") || strings.Contains(text, "sent to president"):
		return statusToPresident, rankToPresident
	case strings.Contains(text, "resolving differences"):
		return "resolving_differences", rankResolvingDifferences
	case passedSenateText(text):
		return statusPassedSenate, rankPassedSenate
	case passedHouseText(text):
		return statusPassedHouse, rankPassedHouse
	case strings.Contains(text, "reported") && actionType == "committee":
		return "reported", rankReported
	case strings.Contains(text, "referred to") && actionType == "introreferral":
		return "in_committee", rankInCommittee
	case strings.Contains(text, "referred to") && currentBestRank < rankInCommittee:
		return "in_committee", rankInCommittee
	case actionType == "introreferral" && strings.Contains(text, "introduced"):
		return "introduced", rankIntroduced
	default:
		return "", 0
	}
}

// failedText reports a failed passage ("Failed of passage/not agreed to in House: On agreeing
// to the resolution Failed ...").
func failedText(text string) bool {
	return strings.Contains(text, "failed") || strings.Contains(text, "not agreed to")
}

// passedSenateText matches the Senate's own passage texts. A bare "agreed to in Senate" isn't
// one: motions to proceed and amendments are agreed to in the Senate too.
func passedSenateText(text string) bool {
	if failedText(text) || strings.Contains(text, "passed house") {
		return false
	}
	return strings.Contains(text, "passed senate") || strings.Contains(text, "passed/agreed to in senate") ||
		strings.Contains(text, "resolution agreed to in senate")
}

// passedHouseText matches the House's passage texts, "Passed/agreed to in House" among them, but
// not a rule's ("Rule H. Res. 436 passed House.").
func passedHouseText(text string) bool {
	if failedText(text) || strings.Contains(text, "passed senate") || strings.HasPrefix(text, "rule ") {
		return false
	}
	return strings.Contains(text, "passed house") || strings.Contains(text, "passed/agreed to in house") ||
		(strings.Contains(text, "on agreeing to the resolution") && strings.Contains(text, "agreed to"))
}

// computeStatusHistory derives the current status and a full audit trail of every lifecycle
// stage a bill has reached, each with the earliest date it was reached, whatever the actions'
// order (Congress.gov lists them newest first). The current status is the highest-ranked stage.
func computeStatusHistory(actions []lifecycleAction) (string, *time.Time, []statusEntry) {
	seen := make(map[string]statusEntry)
	order := make([]string, 0)
	bestRank := 0
	bestStatus := ""

	for _, a := range actions {
		status, rank := classifyAction(a, bestRank)
		if status == "" {
			continue
		}
		parsed, parseErr := time.Parse(time.DateOnly, a.date)
		if parseErr != nil {
			continue
		}
		if e, exists := seen[status]; !exists {
			seen[status] = statusEntry{status: status, rank: rank, date: parsed}
			order = append(order, status)
		} else if parsed.Before(e.date) {
			e.date = parsed
			seen[status] = e
		}
		if rank > bestRank {
			bestRank = rank
			bestStatus = status
		}
	}

	history := make([]statusEntry, 0, len(seen))
	for _, status := range order {
		history = append(history, seen[status])
	}
	if bestStatus == "" {
		return "", nil, history
	}
	bestDate := seen[bestStatus].date
	return bestStatus, &bestDate, history
}

// storeStatusHistory computes a bill's current status and status history from its actions and
// stores them, replacing the stored history. A bill none of whose actions is a stage is left
// alone.
func (s *Service) storeStatusHistory(ctx context.Context, billID string, actions []lifecycleAction) error {
	status, statusDate, history := computeStatusHistory(actions)
	if status == "" {
		return nil
	}
	rows := make([]repository.BillStatusRow, 0, len(history))
	for _, h := range history {
		rows = append(rows, repository.BillStatusRow{
			BillID: billID, Status: h.status, StatusDate: h.date, StatusRank: h.rank,
		})
	}
	if err := s.store.ReplaceBillStatus(ctx, billID, status, statusDate, rows); err != nil {
		return fmt.Errorf("replace bill status: %w", err)
	}
	return nil
}

// statusHistoryCounts totals one RederiveStatusHistory run.
type statusHistoryCounts struct {
	bills      int
	derived    int
	noStage    int
	exceptions int
}

// RederiveStatusHistory derives every stored bill's current status and status history in a
// congress again from its stored actions (#659), with no upstream calls. It is idempotent: each
// bill's history is replaced. It pages by bill_id and checkpoints in sync_state (step
// "status_history"), so an interrupted run resumes after the last finished page. It logs each
// signed or enacted bill that still lacks a House or Senate passage.
func (s *Service) RederiveStatusHistory(ctx context.Context, congressNum, pageSize int) error {
	if pageSize <= 0 {
		pageSize = DefaultStatusHistoryPageSize
	}
	return s.runStep(ctx, stepStatusHistory, congressNum, false, func(ctx context.Context) (int, error) {
		return s.rederiveStatusHistory(ctx, congressNum, pageSize)
	})
}

func (s *Service) rederiveStatusHistory(ctx context.Context, congressNum, pageSize int) (int, error) {
	state, err := s.store.GetSyncState(ctx, stepStatusHistory, congressNum)
	if err != nil {
		return 0, fmt.Errorf("read status history checkpoint: %w", err)
	}
	var c statusHistoryCounts
	after := ""
	if state != nil && state.LastOffset != nil {
		after = *state.LastOffset
		c.bills = state.ItemsSynced
	}
	s.logger.InfoContext(ctx, "deriving status history again", "congress", congressNum, "resume_after", after)

	for pages := 1; ; pages++ {
		page, listErr := s.store.ListStoredBillActions(ctx, congressNum, after, pageSize)
		if listErr != nil {
			return c.bills, fmt.Errorf("list bill actions after %q: %w", after, listErr)
		}
		for _, b := range page {
			if ctx.Err() != nil {
				return c.bills, ctx.Err()
			}
			if err = s.rederiveBill(ctx, b, &c); err != nil {
				return c.bills, err
			}
		}
		if len(page) < pageSize {
			break
		}
		after = page[len(page)-1].BillID
		if err = s.store.SaveSyncCheckpoint(ctx, stepStatusHistory, congressNum, &after, c.bills); err != nil {
			return c.bills, fmt.Errorf("save status history checkpoint: %w", err)
		}
		if pages%statusHistoryProgressPages == 0 {
			s.logger.InfoContext(ctx, "status history progress", "congress", congressNum, "bills", c.bills,
				"after", after)
		}
	}

	s.logger.InfoContext(ctx, "status history derived again", "congress", congressNum, "bills", c.bills,
		"derived", c.derived, "no_stage", c.noStage, "enacted_without_both_passages", c.exceptions)
	return c.bills, nil
}

// rederiveBill derives and stores one bill's status from its stored actions.
func (s *Service) rederiveBill(ctx context.Context, b repository.StoredBillActions, c *statusHistoryCounts) error {
	c.bills++
	actions := storedLifecycleActions(b.Actions)
	status, _, history := computeStatusHistory(actions)
	if status == "" {
		c.noStage++
		return nil
	}
	if err := s.storeStatusHistory(ctx, b.BillID, actions); err != nil {
		return fmt.Errorf("%s: %w", b.BillID, err)
	}
	c.derived++
	if missing := missingPassages(status, history); len(missing) > 0 {
		c.exceptions++
		s.logger.InfoContext(ctx, "signed or enacted bill without both passages",
			"bill_id", b.BillID, "status", status, "missing", strings.Join(missing, ","))
	}
	return nil
}

// missingPassages lists the chamber passages a signed or enacted bill's history lacks; for any
// other status it's empty.
func missingPassages(status string, history []statusEntry) []string {
	if status != statusSigned && status != statusBecameLaw {
		return nil
	}
	var missing []string
	for _, want := range []string{statusPassedHouse, statusPassedSenate} {
		found := false
		for _, h := range history {
			found = found || h.status == want
		}
		if !found {
			missing = append(missing, want)
		}
	}
	return missing
}
