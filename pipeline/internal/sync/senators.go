package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/xmlparse"
)

// senateMembersMaxBytes caps the Senate member feed read. The file is about 68 KB.
const senateMembersMaxBytes = 5 << 20

// lisIDRe and bioguideIDRe are the ID formats the Senate member feed must use for an entry to
// be stored.
var (
	lisIDRe      = regexp.MustCompile(`^S\d{3}$`)
	bioguideIDRe = regexp.MustCompile(`^[A-Z]\d{6}$`)
)

// lisSync is the tally of one syncSenateLISIDs run.
type lisSync struct {
	roster map[string]bool
	// owner maps each stored LIS ID to its member. It's updated as IDs are set.
	owner map[string]string

	set, unchanged, invalid, notStored, failed int
}

// syncSenateLISIDs stores every sitting senator's LIS ID from the Senate's own member feed
// (docs/design/66-honest-vote-data.md, "Senator IDs"). It is the only writer that overwrites
// members.lis_id: SetMemberLisID moves an ID another member holds, such as one the old name
// fallback guessed wrong. Entries with malformed IDs and senators not in members are skipped.
// Each change is logged at INFO with the old value. It returns an error when the feed can't be
// read or a write fails; callers log it and go on with the IDs already stored.
func (s *Service) syncSenateLISIDs(ctx context.Context) error {
	data, err := s.httpGetMax(ctx, s.feeds.senateMembersURL, senateMembersMaxBytes)
	if err != nil {
		return fmt.Errorf("fetch senate member feed: %w", err)
	}
	members, err := xmlparse.ParseSenateMembers(data)
	if err != nil {
		return fmt.Errorf("parse senate member feed: %w", err)
	}
	if len(members) == 0 {
		return errors.New("senate member feed lists no senators")
	}

	run, err := s.newLISSync(ctx)
	if err != nil {
		return err
	}
	for _, m := range members {
		s.storeSenateLISID(ctx, run, m)
	}

	s.logger.InfoContext(ctx, "senate lis ids synced", "listed", len(members), "set", run.set,
		"unchanged", run.unchanged, "invalid", run.invalid, "not_stored", run.notStored, "failed", run.failed)
	if run.failed > 0 {
		return fmt.Errorf("%d of %d senate lis ids failed to write", run.failed, len(members))
	}
	return nil
}

// newLISSync reads the stored members and LIS IDs.
func (s *Service) newLISSync(ctx context.Context) (*lisSync, error) {
	ids, err := s.store.ListMemberIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	owner, err := s.store.LISLookup(ctx)
	if err != nil {
		return nil, fmt.Errorf("read lis ids: %w", err)
	}
	run := &lisSync{roster: make(map[string]bool, len(ids)), owner: owner}
	for _, id := range ids {
		run.roster[id] = true
	}
	return run, nil
}

// storeSenateLISID stores one feed entry's LIS ID unless the member already has it.
func (s *Service) storeSenateLISID(ctx context.Context, run *lisSync, m xmlparse.SenateMember) {
	if !lisIDRe.MatchString(m.LISID) || !bioguideIDRe.MatchString(m.BioguideID) {
		s.logger.WarnContext(ctx, "senate member feed entry skipped: malformed id",
			"lis_id", m.LISID, "bioguide_id", m.BioguideID, "name", m.FirstName+" "+m.LastName)
		run.invalid++
		return
	}
	if !run.roster[m.BioguideID] {
		s.logger.DebugContext(ctx, "senate member feed entry skipped: member not stored",
			"lis_id", m.LISID, "bioguide_id", m.BioguideID)
		run.notStored++
		return
	}
	if run.owner[m.LISID] == m.BioguideID {
		run.unchanged++
		return
	}

	change, err := s.store.SetMemberLisID(ctx, m.BioguideID, m.LISID)
	if errors.Is(err, repository.ErrMemberNotFound) {
		run.notStored++
		return
	}
	if err != nil {
		s.logger.WarnContext(ctx, "set lis_id from senate feed failed",
			"bioguide_id", m.BioguideID, "lis_id", m.LISID, "error", err)
		run.failed++
		return
	}
	s.logger.InfoContext(ctx, "set lis_id from senate feed", "bioguide_id", m.BioguideID,
		"lis_id", m.LISID, "old_lis_id", change.Previous, "taken_from", change.TakenFrom)
	delete(run.owner, change.Previous)
	run.owner[m.LISID] = m.BioguideID
	run.set++
}

// senatorResolver places the senators named in one vote sync's Senate roll calls: by stored
// LIS ID, then by a strict name match over that congress's senators. Name matches are used
// for this run only and never written back. It's safe for concurrent use.
type senatorResolver struct {
	logger *slog.Logger
	// byLIS maps each stored LIS ID to its member. It's read-only after construction.
	byLIS    map[string]string
	senators []foldedSenator

	mu sync.Mutex
	// byName caches the fallback's answer per LIS ID for this run; "" is no match.
	byName map[string]string
}

// foldedSenator is a senator in the congress with names folded by foldName.
type foldedSenator struct {
	bioguideID, first, last, state string
}

// newSenatorResolver reads the stored LIS IDs and the congress's senators.
func (s *Service) newSenatorResolver(ctx context.Context, congressNum int) (*senatorResolver, error) {
	byLIS, err := s.store.LISLookup(ctx)
	if err != nil {
		return nil, fmt.Errorf("read lis ids: %w", err)
	}
	senators, err := s.store.ListSenators(ctx, congressNum)
	if err != nil {
		return nil, fmt.Errorf("list senators of congress %d: %w", congressNum, err)
	}
	return newResolver(s.logger, byLIS, senators), nil
}

func newResolver(logger *slog.Logger, byLIS map[string]string, senators []repository.SenatorName) *senatorResolver {
	folded := make([]foldedSenator, 0, len(senators))
	for _, sn := range senators {
		folded = append(folded, foldedSenator{
			bioguideID: sn.BioguideID,
			first:      foldName(sn.FirstName),
			last:       foldName(sn.LastName),
			state:      strings.ToUpper(strings.TrimSpace(sn.State)),
		})
	}
	return &senatorResolver{logger: logger, byLIS: byLIS, senators: folded, byName: map[string]string{}}
}

// resolve returns the bioguide ID of a senator in a roll call, or "" when neither the stored
// LIS ID nor the name fallback places them. The first fallback answer for each LIS ID in a
// run is logged: a match at INFO, a miss at WARN.
func (r *senatorResolver) resolve(ctx context.Context, v xmlparse.IndividualVote) string {
	if id := r.byLIS[v.MemberID]; id != "" {
		return id
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.byName[v.MemberID]; ok {
		return id
	}
	id := matchSenator(r.senators, v.FirstName, v.LastName, v.State)
	r.byName[v.MemberID] = id
	if id == "" {
		r.logger.WarnContext(ctx, "could not place senator", "lis_id", v.MemberID,
			"first_name", v.FirstName, "last_name", v.LastName, "state", v.State)
	} else {
		r.logger.InfoContext(ctx, "senator placed by name for this run", "lis_id", v.MemberID,
			"bioguide_id", id, "first_name", v.FirstName, "last_name", v.LastName, "state", v.State)
	}
	return id
}

// unmatched returns the LIS IDs the resolver couldn't place in this run, sorted.
func (r *senatorResolver) unmatched() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for lis, id := range r.byName {
		if id == "" {
			ids = append(ids, lis)
		}
	}
	slices.Sort(ids)
	return ids
}

// matchSenator finds the one senator with the roll call's last name and state whose first
// name starts with the roll call's ("Ben" matches "Ben Ray"), or failing that the one senator
// with that last name and state. Names are compared folded, so "Lujan" matches "Luján". Two
// or more candidates at the step that decides is no match: a guess could credit a vote to the
// wrong senator.
func matchSenator(senators []foldedSenator, first, last, state string) string {
	first, last, state = foldName(first), foldName(last), strings.ToUpper(strings.TrimSpace(state))
	if last == "" || state == "" {
		return ""
	}

	var byLast, byFirst []string
	for _, c := range senators {
		if c.last != last || c.state != state {
			continue
		}
		byLast = append(byLast, c.bioguideID)
		if first != "" && strings.HasPrefix(c.first, first) {
			byFirst = append(byFirst, c.bioguideID)
		}
	}

	switch {
	case len(byFirst) == 1:
		return byFirst[0]
	case len(byFirst) == 0 && len(byLast) == 1:
		return byLast[0]
	default:
		return ""
	}
}

// foldName lowercases a name, strips its accents ("Luján" becomes "lujan"), treats hyphens as
// spaces and collapses whitespace.
func foldName(name string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(t, name)
	if err != nil {
		folded = name
	}
	folded = strings.ReplaceAll(folded, "-", " ")
	return strings.ToLower(strings.Join(strings.Fields(folded), " "))
}
