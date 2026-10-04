package sync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// Library of Congress action codes that mark a bill's passage in each chamber, whatever the
// method. Unlike the floor actions (House H37300 and the Senate's uncoded ones), each passage
// has exactly one of them.
const (
	actionCodeHousePassage  = "8000"
	actionCodeSenatePassage = "17000"
)

// Methods of an unrecorded passage, as they appear in its vote ID.
const (
	passageVoice = "voice"
	passageUC    = "uc"
)

// unrecordedPassage is a passage without a roll call: a voice vote or unanimous consent.
type unrecordedPassage struct {
	chamber string
	method  string
	date    time.Time
	action  string
}

// voteID is {chamber}-{congress}-{method}-{bill_id}-{yyyymmdd}, e.g.
// house-119-voice-hr-119-1276-20251209. The bill in the ID keeps two bills passed the same
// way on the same day apart.
func (p unrecordedPassage) voteID(billID string, congressNum int) string {
	return fmt.Sprintf("%s-%d-%s-%s-%s",
		strings.ToLower(p.chamber), congressNum, p.method, billID, p.date.Format("20060102"))
}

// question is the vote's question: the method's label and the action text.
func (p unrecordedPassage) question() string {
	label := "Voice Vote"
	if p.method == passageUC {
		label = "Unanimous Consent"
	}
	return label + ": " + p.action
}

// passageChamber returns the chamber a passage action code belongs to, or "".
func passageChamber(code string) string {
	switch code {
	case actionCodeHousePassage:
		return chamberHouse
	case actionCodeSenatePassage:
		return chamberSenate
	default:
		return ""
	}
}

// passageMethod reads how a passage action passed: voice, uc, or "" for a roll call or
// anything else. Roll calls are the XML vote sync's to record.
func passageMethod(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "roll no.") || strings.Contains(lower, "record vote"):
		return ""
	case strings.Contains(lower, "voice vote"):
		return passageVoice
	case strings.Contains(lower, "unanimous consent") || strings.Contains(lower, "without objection"):
		return passageUC
	default:
		return ""
	}
}

// detectUnrecordedPassages finds the passages in a bill's actions that have no roll call.
// The second result counts passage actions it skipped (roll calls, other methods, bad dates).
func detectUnrecordedPassages(actions []congress.Action) ([]unrecordedPassage, int) {
	var (
		passages []unrecordedPassage
		skipped  int
	)
	for _, a := range actions {
		chamber := passageChamber(a.ActionCode)
		if chamber == "" {
			continue
		}
		method := passageMethod(a.Text)
		date, err := time.Parse(time.DateOnly, a.ActionDate)
		if method == "" || err != nil {
			skipped++
			continue
		}
		passages = append(passages, unrecordedPassage{
			chamber: chamber, method: method, date: date, action: a.Text,
		})
	}
	return passages, skipped
}

// syncVoiceVotes stores one congressional_votes row per voice-vote or unanimous-consent
// passage of the bill. It writes no member_votes: nobody's vote was recorded, so nobody's
// vote is stored.
func (s *Service) syncVoiceVotes(
	ctx context.Context, billID string, congressNum int, actions []congress.Action,
) {
	passages, skipped := detectUnrecordedPassages(actions)
	if skipped > 0 {
		s.logger.DebugContext(ctx, "passage actions skipped", "bill_id", billID, "skipped", skipped)
	}

	result := "Passed"
	for _, p := range passages {
		voteID := p.voteID(billID, congressNum)
		question := p.question()
		err := s.store.UpsertCongressionalVote(ctx, repository.CongressionalVoteRow{
			ID:       voteID,
			BillID:   &billID,
			Congress: congressNum,
			Chamber:  p.chamber,
			VoteDate: p.date,
			Question: &question,
			Result:   &result,
		})
		if err != nil {
			s.logger.WarnContext(ctx, "upsert unrecorded passage failed",
				"bill_id", billID, "vote_id", voteID, "error", err)
			continue
		}
		s.logger.InfoContext(ctx, "recorded unrecorded passage",
			"bill_id", billID, "vote_id", voteID, "chamber", p.chamber, "method", p.method)
	}
}
