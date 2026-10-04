// Package handler implements the API's HTTP handlers: bills, members, votes, congresses,
// the civic-graph panels, and signed-in accounts. Handlers depend on the
// [repository] interfaces, so tests can pass fakes.
package handler

import (
	"log/slog"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/api/internal/district"
	"github.com/justabill-org/justabill/db/repository"
)

// Handler holds all dependencies for HTTP handlers.
type Handler struct {
	Bills      repository.BillRepo
	Members    repository.MemberRepo
	Users      repository.UserRepo
	Votes      repository.VoteRepo
	Congresses repository.CongressRepo
	Scorecard  repository.Scorecard
	Graph      repository.GraphRepo
	Law        repository.LawRepo
	// Aggregates reads the published aggregates; nil while AGGREGATES_PUBLIC is off, and then
	// the aggregate routes answer 404.
	Aggregates repository.AggregateReader
	District   district.Lookup
	// Accounts verifies tokens for, and deletes, Identity Platform users.
	Accounts auth.Client
	// forgetAuthUID drops a cached auth_uid → user_id mapping.
	forgetAuthUID func(uid string)
	rules         WriteRules
	log           *slog.Logger
	cache         *cache.Cache
	spanner       *spanner.Client
}

// SetLogger configures the handler's logger.
func (h *Handler) SetLogger(l *slog.Logger) { h.log = l }

// SetCache configures the handler's Redis cache.
func (h *Handler) SetCache(c *cache.Cache) { h.cache = c }

// New creates a Handler wired to the given Spanner client.
func New(sc *spanner.Client, opts ...Option) *Handler {
	h := &Handler{
		log:     slog.Default(),
		spanner: sc,
	}
	for _, o := range opts {
		o(h)
	}
	if h.District == nil {
		h.District = district.NewCensusLookup()
	}
	return h
}

// WriteRules are the per-account write limits of
// docs/design/89-aggregate-analytics.md. Zero values mean no limit.
type WriteRules struct {
	// DailyVoteCap is the most bills an account may vote on in any 24 hours
	// (AGG_DAILY_VOTE_CAP).
	DailyVoteCap int
	// DistrictChangeInterval is the least time between two changes to the
	// state or district (AGG_DISTRICT_CHANGE_DAYS).
	DistrictChangeInterval time.Duration
}

// Option configures a Handler.
type Option func(*Handler)

// WithBills sets the BillRepo.
func WithBills(r repository.BillRepo) Option { return func(h *Handler) { h.Bills = r } }

// WithMembers sets the MemberRepo.
func WithMembers(r repository.MemberRepo) Option { return func(h *Handler) { h.Members = r } }

// WithUsers sets the UserRepo.
func WithUsers(r repository.UserRepo) Option { return func(h *Handler) { h.Users = r } }

// WithVotes sets the VoteRepo.
func WithVotes(r repository.VoteRepo) Option { return func(h *Handler) { h.Votes = r } }

// WithCongresses sets the CongressRepo.
func WithCongresses(r repository.CongressRepo) Option { return func(h *Handler) { h.Congresses = r } }

// WithScorecard sets the Scorecard.
func WithScorecard(s repository.Scorecard) Option { return func(h *Handler) { h.Scorecard = s } }

// WithGraph sets the GraphRepo.
func WithGraph(g repository.GraphRepo) Option { return func(h *Handler) { h.Graph = g } }

// WithLaw sets the LawRepo.
func WithLaw(l repository.LawRepo) Option { return func(h *Handler) { h.Law = l } }

// WithAggregates sets the AggregateReader. Without it the aggregate routes answer 404.
func WithAggregates(a repository.AggregateReader) Option {
	return func(h *Handler) { h.Aggregates = a }
}

// WithDistrict sets the district lookup. Without it, New uses the Census
// Bureau geocoder.
func WithDistrict(d district.Lookup) Option { return func(h *Handler) { h.District = d } }

// WithWriteRules sets the per-account write limits.
func WithWriteRules(r WriteRules) Option { return func(h *Handler) { h.rules = r } }

// WithAccounts sets the Identity Platform client, and forget, which drops the
// auth middleware's cached user_id for a deleted account.
func WithAccounts(c auth.Client, forget func(uid string)) Option {
	return func(h *Handler) {
		h.Accounts = c
		h.forgetAuthUID = forget
	}
}
