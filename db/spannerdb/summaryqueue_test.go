package spannerdb_test

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/testdb"
)

// oldSummaryDueSQL is the summary queue's statement before #869: one query that evaluated, for
// every bill of the congress, a correlated EXISTS over congressional_votes and a struct subquery
// for the latest fetched version. It's kept verbatim as the oracle the new queue must agree with.
const oldSummaryDueSQL = `WITH latest AS (
	SELECT b.bill_id, b.status_date,
		EXISTS(SELECT 1 FROM congressional_votes cv WHERE cv.bill_id = b.bill_id) AS voted,
		(SELECT AS STRUCT btv.version_id, btv.version_code, bt.content_hash
		 FROM bill_text_versions btv
		 JOIN bill_texts@{FORCE_INDEX=idx_bill_texts_version} bt ON bt.version_id = btv.version_id
		 WHERE btv.bill_id = b.bill_id
		 ORDER BY btv.sort_order DESC, btv.version_id LIMIT 1) AS v,
		(SELECT c.content_hash FROM bill_crs_summaries c WHERE c.bill_id = b.bill_id
		 ORDER BY c.action_date DESC, c.crs_updated_at DESC LIMIT 1) AS crs_hash,
		(SELECT r.context_hash FROM bill_cra_rules r WHERE r.bill_id = b.bill_id) AS rule_hash
	FROM bills b
	WHERE b.congress = @congress
		AND (NOT @passedChamber OR (EXISTS(SELECT 1 FROM bill_actions ba WHERE ba.bill_id = b.bill_id
		AND (ba.action_code IN UNNEST(@passedCodes)
			OR STARTS_WITH(ba.action_text, 'Passed/agreed to in ')
			OR STARTS_WITH(ba.action_text, 'Became Public Law')))
	OR EXISTS(SELECT 1 FROM bill_status_history h WHERE h.bill_id = b.bill_id AND h.status_rank >= @passedRank)))
), due AS (
	SELECT l.bill_id, l.status_date, l.v.version_id AS version_id, l.v.version_code AS version_code,
		l.v.content_hash AS content_hash,
		CASE
			WHEN bs.bill_id IS NULL OR bs.source_content_hash IS NULL
				OR bs.source_content_hash != l.v.content_hash THEN
				CASE WHEN l.voted THEN @tierVoted
					WHEN l.status_date >= @recentSince THEN @tierRecent
					ELSE @tierOther END
			WHEN ((@crsContext AND l.crs_hash IS NOT NULL AND IFNULL(bs.source_crs_hash, '') != l.crs_hash)
					OR (@ruleContext AND l.rule_hash IS NOT NULL AND IFNULL(bs.source_rule_hash, '') != l.rule_hash))
				AND NOT (sa.bill_id IS NOT NULL AND sa.outcome = @blocked AND sa.content_hash = l.v.content_hash) THEN
				CASE WHEN l.voted THEN @tierVoted
					WHEN l.status_date >= @recentSince THEN @tierRecent
					ELSE @tierOther END
			WHEN @onPromptChange AND IFNULL(bs.prompt_version, '') != @prompt THEN @tierPrompt
		END AS tier
	FROM latest l
	LEFT JOIN bill_summaries bs ON bs.bill_id = l.bill_id
	LEFT JOIN summary_attempts sa ON sa.bill_id = l.bill_id
	WHERE l.v.version_id IS NOT NULL
		AND NOT (sa.bill_id IS NOT NULL AND sa.outcome != @ok AND sa.content_hash = l.v.content_hash
			AND sa.prompt_version = @prompt AND sa.model = @model
			AND (sa.next_attempt_at IS NULL OR sa.next_attempt_at > @now))
)
`

func oldSummaryParams(q repository.SummaryQueueQuery) map[string]any {
	recentSince := q.Now.UTC().AddDate(0, 0, -q.RecentDays)
	return map[string]any{
		"congress": int64(q.Congress), "prompt": q.PromptVersion, "model": q.Model, "now": q.Now,
		"recentSince":    civil.Date{Year: recentSince.Year(), Month: recentSince.Month(), Day: recentSince.Day()},
		"onPromptChange": q.ResummarizeOnPromptChange, "crsContext": q.CRSContext, "ruleContext": q.RuleContext,
		"passedChamber": q.PassedChamber,
		"passedCodes":   []string{"8000", "17000", "28000", "E30000", "41000", "36000", "E40000"},
		"passedRank":    int64(4),
		"ok":            repository.SummaryOutcomeOK, "blocked": repository.SummaryOutcomeBlocked,
		"tierVoted": int64(repository.SummaryTierVoted), "tierRecent": int64(repository.SummaryTierRecent),
		"tierOther": int64(repository.SummaryTierOther), "tierPrompt": int64(repository.SummaryTierPromptChange),
		"lim": int64(q.Limit),
	}
}

// oldQueue runs the old queue statement and returns its rows as queue() formats them.
func oldQueue(t *testing.T, client *spanner.Client, q repository.SummaryQueueQuery) []string {
	t.Helper()
	rows := queryRowStrings(t, client, oldSummaryDueSQL+`SELECT bill_id, version_code, content_hash, tier FROM due
WHERE tier IS NOT NULL
ORDER BY tier, status_date DESC, bill_id
LIMIT @lim`, oldSummaryParams(q))
	return rows
}

// queryRowStrings returns each row's columns joined by "|".
func queryRowStrings(t *testing.T, client *spanner.Client, sql string, params map[string]any) []string {
	t.Helper()
	var out []string
	iter := client.Single().Query(t.Context(), spanner.Statement{SQL: sql, Params: params})
	if err := iter.Do(func(row *spanner.Row) error {
		cols := make([]string, row.Size())
		for i := range cols {
			var v spanner.GenericColumnValue
			if err := row.Column(i, &v); err != nil {
				return err
			}
			cols[i] = fmt.Sprint(v.Value.AsInterface())
		}
		out = append(out, strings.Join(cols, "|"))
		return nil
	}); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return out
}

// pick returns one of opts at random.
func pick[T any](r *rand.Rand, opts ...T) T { return opts[r.IntN(len(opts))] }

// corpus collects the rows of a random test corpus, drawn from a seeded generator.
type corpus struct {
	r    *rand.Rand
	now  time.Time
	muts []*spanner.Mutation
}

func (c *corpus) ins(table string, cols map[string]any) {
	c.muts = append(c.muts, spanner.InsertMap(table, cols))
}

func (c *corpus) apply(t *testing.T, client *spanner.Client) {
	t.Helper()
	if _, err := client.Apply(t.Context(), c.muts); err != nil {
		t.Fatalf("seed corpus: %v", err)
	}
}

// seedQueueCorpus writes n bills of the fixture congress, and a few of the one before, whose
// versions, texts, summaries, attempts, CRS summaries, rules, votes and actions are drawn at
// random from small pools, so hashes collide and every branch of the queue is taken.
func seedQueueCorpus(t *testing.T, client *spanner.Client, n int) {
	t.Helper()
	c := &corpus{r: rand.New(rand.NewPCG(869, 1)), now: summaryNow()}
	for i := range n + n/10 {
		congress := testdb.FixtureCongress
		if i >= n {
			congress = testdb.FixturePrevCongress
		}
		id := "hr-" + strconv.Itoa(congress) + "-" + strconv.Itoa(5000+i)
		c.queueBill(id, congress, i)
		c.queueSummary(id)
		c.queueContext(id, congress)
	}
	c.ins("congressional_votes", map[string]any{"vote_id": "vote-procedural", "congress": int64(testdb.FixtureCongress),
		"chamber": "Senate", "vote_date": c.now})
	c.apply(t, client)
}

// queueBill adds a bill with a random status date and up to three versions, some sharing a
// sort_order, about two in three of them fetched.
func (c *corpus) queueBill(id string, congress, i int) {
	bill := map[string]any{"bill_id": id, "congress": int64(congress), "bill_type": "hr",
		"number": int64(5000 + i), "title": "Bill " + id}
	switch c.r.IntN(4) {
	case 0:
		bill["status_date"] = civil.DateOf(c.now.AddDate(0, 0, -5))
	case 1:
		bill["status_date"] = civil.DateOf(c.now.AddDate(0, 0, -30)) // the first day of the recent tier
	case 2:
		bill["status_date"] = civil.DateOf(c.now.AddDate(0, 0, -200))
	}
	c.ins("bills", bill)
	sort := int64(0)
	for j := range c.r.IntN(4) {
		if j == 0 || c.r.IntN(5) > 0 {
			sort++ // otherwise a tie with the version before
		}
		vid := fmt.Sprintf("v%04d-%c%d", i, 'a'+rune(c.r.IntN(26)), j)
		c.ins("bill_text_versions", map[string]any{"bill_id": id, "version_id": vid,
			"version_type": "Version", "version_code": "c" + strconv.Itoa(j), "sort_order": sort})
		if c.r.IntN(3) > 0 {
			c.ins("bill_texts", map[string]any{"text_id": "t-" + vid, "version_id": vid,
				"format": "xml", "content": "x", "content_hash": pick(c.r, "h1", "h2", "h3")})
		}
	}
}

// queueSummary adds a random summary and attempt, or neither.
func (c *corpus) queueSummary(id string) {
	r := c.r
	if r.IntN(4) > 0 {
		c.ins("bill_summaries", map[string]any{"bill_id": id,
			"source_content_hash": pick[any](r, "h1", "h2", "h3", nil),
			"source_crs_hash":     pick[any](r, "c1", "c2", nil),
			"source_rule_hash":    pick[any](r, "r1", "r2", nil),
			"prompt_version":      pick[any](r, summaryPrompt, "bill-v1", nil)})
	}
	if r.IntN(2) > 0 {
		c.ins("summary_attempts", map[string]any{"bill_id": id, "content_hash": pick(r, "h1", "h2", "h3"),
			"prompt_version": pick(r, summaryPrompt, "bill-v1"), "model": pick(r, summaryModel, "other-model"),
			"outcome": pick(r, repository.SummaryOutcomeOK, repository.SummaryOutcomeError,
				repository.SummaryOutcomeBlocked, repository.SummaryOutcomeBatchPending),
			"attempts": int64(1), "attempted_at": c.now.Add(-time.Hour),
			"next_attempt_at": pick[any](r, nil, c.now.Add(-time.Minute), c.now.Add(time.Hour))})
	}
}

// queueContext adds random CRS summaries, a disapproved rule, a vote, and an action or status
// that may count as passing a chamber.
func (c *corpus) queueContext(id string, congress int) {
	r := c.r
	for j := range r.IntN(3) {
		c.ins("bill_crs_summaries", map[string]any{"bill_id": id, "version_code": "0" + strconv.Itoa(j),
			"action_date": pick(r, civil.Date{Year: 2025, Month: 3, Day: 1}, civil.Date{Year: 2025, Month: 6, Day: 1}),
			"action_desc": "Introduced", "text_html": "<p>x</p>", "text": "x", "content_hash": pick(r, "c1", "c2"),
			"crs_updated_at":    c.now.Add(-time.Duration(r.IntN(1000)+j) * time.Hour),
			"source_updated_at": c.now, "synced_at": c.now})
	}
	if r.IntN(4) == 0 {
		c.ins("bill_cra_rules", map[string]any{"bill_id": id, "rule_title": "Rule", "rule_agency": "Agency",
			"gao_opinion": false, "status": "found", "matcher_version": "1",
			"context_hash": pick(r, "r1", "r2"), "checked_at": c.now})
	}
	if r.IntN(5) == 0 {
		c.ins("congressional_votes", map[string]any{"vote_id": "vote-" + id, "bill_id": id,
			"congress": int64(congress), "chamber": "House", "vote_date": c.now})
	}
	action := map[string]any{"bill_id": id, "action_id": "a-" + id, "action_date": civil.DateOf(c.now),
		"sort_order": int64(1)}
	switch r.IntN(6) {
	case 0:
		action["action_text"], action["action_code"] = "Passed House", "8000"
	case 1:
		action["action_text"] = "Became Public Law No: 119-1."
	case 2:
		c.ins("bill_status_history", map[string]any{"bill_id": id, "status": "passed_senate",
			"status_date": civil.DateOf(c.now), "status_rank": int64(5)})
		return
	default:
		action["action_text"], action["action_code"] = "Referred to committee", "H11100"
	}
	c.ins("bill_actions", action)
}

// TestSummaryQueue_MatchesOldStatement checks the queue and its backlog count against the
// statement they replaced (#869), on a random corpus, for every combination of the query's
// switches, and the queue once more with a limit that cuts it.
func TestSummaryQueue_MatchesOldStatement(t *testing.T) {
	store, client := newSummaryStore(t)
	seedQueueCorpus(t, client, 100)

	tiers := map[int]bool{}
	for mask := range 16 {
		q := repository.SummaryQueueQuery{
			Congress: testdb.FixtureCongress, Limit: 1000, PromptVersion: summaryPrompt, Model: summaryModel,
			Now: summaryNow(), RecentDays: 30,
			CRSContext: mask&1 != 0, RuleContext: mask&2 != 0, ResummarizeOnPromptChange: mask&4 != 0,
			PassedChamber: mask&8 != 0,
		}
		name := fmt.Sprintf("crs=%v,rule=%v,prompt=%v,passed=%v",
			q.CRSContext, q.RuleContext, q.ResummarizeOnPromptChange, q.PassedChamber)
		want := oldQueue(t, client, q)
		if got := queue(t, store, func(qq *repository.SummaryQueueQuery) { *qq = q }); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: queue differs from the old statement\n got %q\nwant %q", name, got, want)
		}
		wantCounts := map[int]int{}
		for _, row := range want {
			tier, err := strconv.Atoi(row[strings.LastIndex(row, "|")+1:])
			if err != nil {
				t.Fatalf("tier of %q: %v", row, err)
			}
			wantCounts[tier]++
			tiers[tier] = true
		}
		gotCounts, err := store.CountBillsToSummarize(t.Context(), q)
		if err != nil {
			t.Fatalf("%s: count: %v", name, err)
		}
		if !reflect.DeepEqual(gotCounts, wantCounts) {
			t.Errorf("%s: backlog = %v, the old statement's = %v", name, gotCounts, wantCounts)
		}
		if mask == 15 {
			q.Limit = 25
			want = oldQueue(t, client, q)
			if got := queue(
				t,
				store,
				func(qq *repository.SummaryQueueQuery) { *qq = q },
			); !reflect.DeepEqual(
				got,
				want,
			) {
				t.Errorf("%s, limit 25: queue differs from the old statement\n got %q\nwant %q", name, got, want)
			}
		}
	}
	// The corpus must reach every tier, or the comparison proves less than it says.
	for _, tier := range []int{repository.SummaryTierVoted, repository.SummaryTierRecent,
		repository.SummaryTierOther, repository.SummaryTierPromptChange} {
		if !tiers[tier] {
			t.Errorf("no combination queued a bill in tier %d: widen the corpus", tier)
		}
	}
}
