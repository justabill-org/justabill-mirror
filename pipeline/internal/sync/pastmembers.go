package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/legislators"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
)

// A congress starts on January 3 of its first session's year and lasts two years.
const (
	congressYears    = 2
	congressStartDay = 3
)

// SetLegislators sets the congress-legislators client SyncPastMemberTerms reads, to pin a
// commit of the dataset or to point at a test server.
func (s *Service) SetLegislators(c *legislators.Client) { s.legislators = c }

// SyncMemberRoster stores the names and photos of a congress's members from the Congress.gov
// list. It writes no terms and makes no senator detail calls: a past congress takes both from
// SyncPastMemberTerms, because the list shows members who switched chambers as senators only.
// Like SyncMembers, it records the members step and skips itself when that step succeeded in
// the last 24 hours.
func (s *Service) SyncMemberRoster(ctx context.Context, congressNum int) error {
	return s.syncMemberStep(ctx, congressNum, s.upsertMemberRoster, false)
}

// SyncPastMemberTerms loads a past congress's member terms and LIS IDs from congress-legislators
// (docs/design/78-118th-backfill.md). It writes one member_terms row per member and chamber,
// with the dates they held the seat in that congress, and sets members.lis_id only where it's
// NULL, logging each one it sets. Run SyncMemberRoster first: a term whose member isn't stored
// is logged, counted and skipped. If the dataset can't be fetched in full, nothing is written.
func (s *Service) SyncPastMemberTerms(ctx context.Context, congressNum int) error {
	return s.runStep(ctx, stepMemberTerms, congressNum, false, func(ctx context.Context) (int, error) {
		return s.syncPastMemberTerms(ctx, congressNum)
	})
}

// pastTerms is the state and tally of one SyncPastMemberTerms run.
type pastTerms struct {
	congress int
	roster   map[string]bool
	// lisOwner maps a stored LIS ID to its member, and memberLIS the other way round. Both are
	// updated as IDs are set, so a member with two terms is set once.
	lisOwner, memberLIS map[string]string

	written, notInRoster, invalid, failed, lisSet int
}

func (s *Service) syncPastMemberTerms(ctx context.Context, congressNum int) (int, error) {
	s.logger.InfoContext(ctx, "syncing past member terms from congress-legislators", "congress", congressNum)
	people, err := s.legislators.Fetch(ctx)
	if err != nil {
		return 0, err
	}
	start, end := congressDates(congressNum)
	codes := stateCodes()
	filter := legislators.Filter{Start: start, End: end, ValidState: func(code string) bool { return codes[code] }}
	terms, invalid := filter.Terms(ctx, s.logger, people)

	run, err := s.newPastTerms(ctx, congressNum)
	if err != nil {
		return 0, err
	}
	run.invalid = invalid
	for _, t := range terms {
		s.storePastTerm(ctx, run, t)
	}

	s.logger.InfoContext(ctx, "past member terms synced", "congress", congressNum,
		"terms", len(terms), "written", run.written, "not_in_roster", run.notInRoster,
		"invalid", run.invalid, "failed", run.failed, "lis_ids_set", run.lisSet)
	if run.failed > 0 {
		return run.written, fmt.Errorf("%d of %d member terms failed to write", run.failed, len(terms))
	}
	return run.written, nil
}

// newPastTerms reads the stored roster and LIS IDs.
func (s *Service) newPastTerms(ctx context.Context, congressNum int) (*pastTerms, error) {
	ids, err := s.store.ListMemberIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	lisOwner, err := s.store.LISLookup(ctx)
	if err != nil {
		return nil, fmt.Errorf("read lis ids: %w", err)
	}
	run := &pastTerms{
		congress:  congressNum,
		roster:    make(map[string]bool, len(ids)),
		lisOwner:  lisOwner,
		memberLIS: make(map[string]string, len(lisOwner)),
	}
	for _, id := range ids {
		run.roster[id] = true
	}
	for lis, member := range lisOwner {
		run.memberLIS[member] = lis
	}
	return run, nil
}

// storePastTerm writes one term and, if the member has none yet, their LIS ID.
func (s *Service) storePastTerm(ctx context.Context, run *pastTerms, t legislators.Term) {
	if !run.roster[t.Bioguide] {
		s.logger.WarnContext(ctx, "past member term skipped: member not in roster",
			"congress", run.congress, "bioguide_id", t.Bioguide, "chamber", t.Chamber)
		run.notInRoster++
		return
	}
	err := s.store.UpsertMemberTerm(ctx, repository.MemberTermRow{
		MemberID:  t.Bioguide,
		Congress:  run.congress,
		Chamber:   t.Chamber,
		State:     t.State,
		District:  t.District,
		Party:     mapParty(t.Party),
		StartDate: new(t.Start),
		EndDate:   new(t.End),
	})
	if err != nil {
		s.logger.WarnContext(ctx, "upsert past member term failed", "congress", run.congress,
			"bioguide_id", t.Bioguide, "chamber", t.Chamber, "error", err)
		run.failed++
		return
	}
	run.written++
	s.setPastLisID(ctx, run, t)
}

// setPastLisID sets a member's LIS ID when they have none and no other member holds it.
// UpdateMemberLisID itself never overwrites a stored ID.
func (s *Service) setPastLisID(ctx context.Context, run *pastTerms, t legislators.Term) {
	if t.LIS == "" {
		return
	}
	if stored := run.memberLIS[t.Bioguide]; stored != "" {
		if stored != t.LIS {
			s.logger.WarnContext(ctx, "stored lis_id differs from congress-legislators; keeping it",
				"bioguide_id", t.Bioguide, "stored_lis_id", stored, "lis_id", t.LIS)
		}
		return
	}
	if owner := run.lisOwner[t.LIS]; owner != "" {
		s.logger.WarnContext(ctx, "lis_id already belongs to another member; not set",
			"bioguide_id", t.Bioguide, "lis_id", t.LIS, "owner", owner)
		return
	}
	if err := s.store.UpdateMemberLisID(ctx, t.Bioguide, t.LIS); err != nil {
		s.logger.WarnContext(ctx, "update lis_id failed", "bioguide_id", t.Bioguide, "lis_id", t.LIS, "error", err)
		return
	}
	s.logger.InfoContext(ctx, "set lis_id from congress-legislators", "bioguide_id", t.Bioguide, "lis_id", t.LIS)
	run.memberLIS[t.Bioguide] = t.LIS
	run.lisOwner[t.LIS] = t.Bioguide
	run.lisSet++
}

// congressDates returns the day a congress began and the day it ended (the next one's first
// day): 2023-01-03 and 2025-01-03 for the 118th.
func congressDates(congressNum int) (time.Time, time.Time) {
	start := time.Date(rollcall.Year(congressNum, 1), time.January, congressStartDay, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(congressYears, 0, 0)
}

// stateCodes is the set of state and territory codes stateMap knows.
func stateCodes() map[string]bool {
	codes := make(map[string]bool, len(stateMap))
	for _, code := range stateMap {
		codes[code] = true
	}
	return codes
}
