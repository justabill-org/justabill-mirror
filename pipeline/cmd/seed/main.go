// Seed fetches real data from the Congress.gov API and inserts it into the local database: a
// few members and bills of the congress in progress by the calendar, or of --congress.
// Usage: CONGRESS_API_KEY=... SPANNER_PROJECT=... SPANNER_INSTANCE=... SPANNER_DATABASE=... go run ./cmd/seed
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
	"github.com/justabill-org/justabill/pipeline/internal/sync"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

const (
	// serviceName is the pipeline's service.name, shared with serve and backfill.
	serviceName    = "justabill-pipeline"
	maxBills       = 20
	maxMembers     = 50
	nameSplitParts = 2
	stateAbbrLen   = 2
)

// seeder holds what every seed step needs.
type seeder struct {
	store  repository.PipelineStore
	api    *congress.Client
	logger *slog.Logger
}

func main() {
	congressNum, err := parseCongress(os.Args[1:], os.Stderr, time.Now())
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "pipeline-seed:", err)
		os.Exit(1)
	}

	// obs.Main logs through the obs logger and flushes telemetry before the process exits.
	os.Exit(obs.Main(context.Background(), obs.Config{Service: serviceName, Environment: os.Getenv("APP_ENV")},
		func(ctx context.Context, logger *slog.Logger) error { return run(ctx, logger, congressNum) }))
}

// run seeds members and bills of congressNum.
func run(ctx context.Context, logger *slog.Logger, congressNum int) error {
	api, err := newAPIClient(ctx, logger)
	if err != nil {
		return err
	}

	sc, err := spannerdb.NewClient(ctx, os.Getenv("SPANNER_PROJECT"), os.Getenv("SPANNER_INSTANCE"),
		os.Getenv("SPANNER_DATABASE"), spannerdb.WithBatchPriority())
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer sc.Close()

	if err = sc.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	logger.InfoContext(ctx, "connected to database")

	s := &seeder{store: spannerdb.NewPipelineStore(sc), api: congress.NewClient(api), logger: logger}
	s.seedMembers(ctx, congressNum)
	s.seedBills(ctx, congressNum)

	logger.InfoContext(ctx, "seed complete")
	return nil
}

// parseCongress reads the flags and returns the congress to seed: --congress, or when it's 0 the
// congress in progress at now (rollcall.Current).
func parseCongress(args []string, stderr io.Writer, now time.Time) (int, error) {
	fs := flag.NewFlagSet("pipeline-seed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	congressNum := fs.Int("congress", 0, "congress to seed (default: the congress in progress, by the calendar)")
	if err := fs.Parse(args); err != nil {
		return 0, err
	}
	if fs.NArg() > 0 {
		return 0, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	switch {
	case *congressNum < 0:
		return 0, fmt.Errorf("--congress %d: must be a congress number, or 0 for the congress in progress",
			*congressNum)
	case *congressNum > 0:
		return *congressNum, nil
	}
	current, _ := rollcall.Current(now)
	return current, nil
}

// newAPIClient builds the pipeline's upstream client from the environment, the same way
// serve and backfill do, so seed is paced by the same budget and retried the same way.
func newAPIClient(ctx context.Context, logger *slog.Logger) (*http.Client, error) {
	cfg, err := upstream.ConfigFrom(func(key string) string { return os.Getenv(strings.ToUpper(key)) })
	if err != nil {
		return nil, err
	}
	if cfg.CongressAPIKey == "" {
		return nil, errors.New("CONGRESS_API_KEY or CONGRESS_API_KEY_FILE is required")
	}
	// Seed is a local tool and returns only the client, so nothing would flush an archive.
	cfg.ArchiveBucket = ""
	up, err := upstream.NewPipeline(ctx, logger, cfg)
	if err != nil {
		return nil, err
	}
	up.LogBudgets(ctx, logger)
	return up.Client, nil
}

// --- Members ---

func (s *seeder) seedMembers(ctx context.Context, congressNum int) {
	s.logger.InfoContext(ctx, "fetching members", "congress", congressNum)
	resp, err := s.api.ListMembers(ctx, congressNum, 0, maxMembers)
	if err != nil {
		s.logger.WarnContext(ctx, "fetch members failed", "error", err)
		return
	}

	for _, m := range resp.Members {
		s.insertMember(ctx, m, congressNum)
	}
	s.logger.InfoContext(ctx, "seeded members", "count", len(resp.Members))
}

func (s *seeder) insertMember(ctx context.Context, m congress.Member, congress int) {
	parts := strings.SplitN(m.Name, ", ", nameSplitParts)
	lastName := parts[0]
	firstName := ""
	if len(parts) > 1 {
		firstName = parts[1]
	}

	var photoURL *string
	if m.Depiction != nil && m.Depiction.ImageURL != "" {
		photoURL = &m.Depiction.ImageURL
	}

	err := s.store.UpsertMember(ctx, repository.MemberRow{
		BioguideID: m.BioguideID,
		FirstName:  firstName,
		LastName:   lastName,
		PhotoURL:   photoURL,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "insert member failed", "bioguide_id", m.BioguideID, "error", err)
		return
	}

	party := mapParty(m.PartyName)
	chamber := "Senate"
	if m.District != nil {
		chamber = "House"
	}

	stateCode := stateAbbrev(m.State)

	err = s.store.UpsertMemberTerm(ctx, repository.MemberTermRow{
		MemberID: m.BioguideID,
		Congress: congress,
		Chamber:  chamber,
		State:    stateCode,
		District: m.District,
		Party:    party,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "insert member term failed", "bioguide_id", m.BioguideID, "error", err)
	}
}

func mapParty(partyName string) string {
	switch {
	case strings.Contains(partyName, "Democrat"):
		return "D"
	case strings.Contains(partyName, "Republican"):
		return "R"
	case strings.Contains(partyName, "Independent"):
		return "I"
	default:
		return "O"
	}
}

// --- Bills ---

func (s *seeder) seedBills(ctx context.Context, congressNum int) {
	s.logger.InfoContext(ctx, "fetching bills", "congress", congressNum)
	resp, err := s.api.ListBills(ctx, congressNum, 0, maxBills)
	if err != nil {
		s.logger.WarnContext(ctx, "fetch bills failed", "error", err)
		return
	}

	for _, b := range resp.Bills {
		s.insertBill(ctx, b)
	}

	s.logger.InfoContext(ctx, "seeded bills", "count", len(resp.Bills))
}

// fetchBillDetail returns the bill row fields only the detail endpoint has: the introduced date,
// policy area, sponsors and laws. They stay empty when the detail can't be fetched.
func (s *seeder) fetchBillDetail(ctx context.Context, b congress.BillSummary, num int) repository.BillRow {
	var row repository.BillRow
	detail, err := s.api.GetBill(ctx, b.Congress, strings.ToLower(b.Type), num)
	if err != nil {
		return row
	}

	if detail.IntroducedDate != "" {
		if t, parseErr := time.Parse(time.DateOnly, detail.IntroducedDate); parseErr == nil {
			row.IntroducedDate = &t
		}
	}
	if detail.PolicyArea != nil {
		row.PolicyArea = &detail.PolicyArea.Name
	}
	if len(detail.Sponsors) > 0 {
		row.Sponsors, _ = json.Marshal(detail.Sponsors)
	}
	row.Laws = sync.BillLaws(detail)
	return row
}

func (s *seeder) insertBill(ctx context.Context, b congress.BillSummary) {
	num, _ := strconv.Atoi(b.Number)
	billType := strings.ToLower(b.Type)
	billID := fmt.Sprintf("%s-%d-%d", billType, b.Congress, num)

	row := s.fetchBillDetail(ctx, b, num)
	row.ID = billID
	row.Congress = b.Congress
	row.BillType = billType
	row.Number = num
	row.Title = b.Title
	row.OriginChamber = &b.OriginChamber
	if b.LatestAction != nil {
		row.LatestAction, _ = json.Marshal(b.LatestAction)
	}

	err := s.store.UpsertBill(ctx, row)
	if err != nil {
		s.logger.WarnContext(ctx, "insert bill failed", "bill_id", billID, "error", err)
		return
	}

	s.logger.InfoContext(ctx, "seeded bill", "bill_id", billID, "title", b.Title)

	s.seedBillActions(ctx, billID, b.Congress, billType, num)

	s.seedBillTextVersions(ctx, billID, b.Congress, billType, num)
}

func (s *seeder) seedBillActions(
	ctx context.Context, billID string, congressNum int, billType string, number int,
) {
	resp, err := s.api.GetBillActions(ctx, congressNum, billType, number)
	if err != nil {
		s.logger.WarnContext(ctx, "fetch actions failed", "bill_id", billID, "error", err)
		return
	}

	actions := make([]repository.BillActionRow, 0, len(resp.Actions))
	for i, a := range resp.Actions {
		actionDate, _ := time.Parse(time.DateOnly, a.ActionDate)
		var sourceSystem *string
		if a.SourceSystem != nil && a.SourceSystem.Name != "" {
			sourceSystem = &a.SourceSystem.Name
		}

		actions = append(actions, repository.BillActionRow{
			ActionDate:   actionDate,
			ActionText:   a.Text,
			ActionType:   nilIfEmpty(a.Type),
			ActionCode:   nilIfEmpty(a.ActionCode),
			SourceSystem: sourceSystem,
			SortOrder:    i + 1,
		})
	}

	if replaceErr := s.store.ReplaceBillActions(ctx, billID, actions); replaceErr != nil {
		s.logger.WarnContext(ctx, "replace actions failed", "bill_id", billID, "error", replaceErr)
	}
}

func (s *seeder) seedBillTextVersions(
	ctx context.Context, billID string, congressNum int, billType string, number int,
) {
	resp, err := s.api.GetBillTextVersions(ctx, congressNum, billType, number)
	if err != nil {
		s.logger.WarnContext(ctx, "fetch text versions failed", "bill_id", billID, "error", err)
		return
	}

	versions := sync.TextVersionRows(ctx, s.logger, billID, resp.TextVersions)
	if _, upsertErr := s.store.UpsertBillTextVersions(ctx, billID, versions); upsertErr != nil {
		s.logger.WarnContext(ctx, "upsert text versions failed", "bill_id", billID, "error", upsertErr)
	}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

//nolint:gochecknoglobals // lookup table used by stateAbbrev
var stateMap = map[string]string{
	"Alabama": "AL", "Alaska": "AK", "Arizona": "AZ", "Arkansas": "AR",
	"California": "CA", "Colorado": "CO", "Connecticut": "CT", "Delaware": "DE",
	"Florida": "FL", "Georgia": "GA", "Hawaii": "HI", "Idaho": "ID",
	"Illinois": "IL", "Indiana": "IN", "Iowa": "IA", "Kansas": "KS",
	"Kentucky": "KY", "Louisiana": "LA", "Maine": "ME", "Maryland": "MD",
	"Massachusetts": "MA", "Michigan": "MI", "Minnesota": "MN", "Mississippi": "MS",
	"Missouri": "MO", "Montana": "MT", "Nebraska": "NE", "Nevada": "NV",
	"New Hampshire": "NH", "New Jersey": "NJ", "New Mexico": "NM", "New York": "NY",
	"North Carolina": "NC", "North Dakota": "ND", "Ohio": "OH", "Oklahoma": "OK",
	"Oregon": "OR", "Pennsylvania": "PA", "Rhode Island": "RI", "South Carolina": "SC",
	"South Dakota": "SD", "Tennessee": "TN", "Texas": "TX", "Utah": "UT",
	"Vermont": "VT", "Virginia": "VA", "Washington": "WA", "West Virginia": "WV",
	"Wisconsin": "WI", "Wyoming": "WY", "District of Columbia": "DC",
	"American Samoa": "AS", "Guam": "GU", "Northern Mariana Islands": "MP",
	"Puerto Rico": "PR", "Virgin Islands": "VI",
}

func stateAbbrev(fullName string) string {
	if abbr, ok := stateMap[fullName]; ok {
		return abbr
	}
	// Already an abbreviation or unknown
	if len(fullName) == stateAbbrLen {
		return fullName
	}
	return fullName
}
