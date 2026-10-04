// Audit prints row counts and sample data for every table.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/obs"
)

func main() {
	os.Exit(obs.Main(context.Background(), obs.Config{Service: "justabill-db", Environment: os.Getenv("APP_ENV")},
		func(ctx context.Context, _ *slog.Logger) error { return run(ctx, os.Stdout) }))
}

func run(ctx context.Context, w io.Writer) error {
	project := envOr("SPANNER_PROJECT", "justabill-local")
	instance := envOr("SPANNER_INSTANCE", "test-instance")
	database := envOr("SPANNER_DATABASE", "justabill")
	db := fmt.Sprintf("projects/%s/instances/%s/databases/%s", project, instance, database)

	client, err := spanner.NewClient(ctx, db)
	if err != nil {
		return err
	}
	defer client.Close()
	return audit(ctx, client, w)
}

// audit writes the whole report for the database behind client to w.
func audit(ctx context.Context, client *spanner.Client, w io.Writer) error {
	a := &auditor{ctx: ctx, client: client, w: w}
	a.tableCounts()
	a.distributions()
	a.samples()
	a.orphanEdges()
	a.textIntegrity()
	a.syncState()
	return a.err
}

// auditor runs the audit queries against one database and writes the report to w. The first
// write error is kept in err and stops further output; query errors are part of the report.
type auditor struct {
	ctx    context.Context
	client *spanner.Client
	w      io.Writer
	err    error
}

// printf writes to the report unless an earlier write failed.
func (a *auditor) printf(format string, args ...any) {
	if a.err != nil {
		return
	}
	_, a.err = fmt.Fprintf(a.w, format, args...)
}

// section starts a report section with a heading.
func (a *auditor) section(title string) {
	a.printf("\n=== %s ===\n", title)
}

// count prints a labeled single-number query.
func (a *auditor) count(label, sql string) {
	a.printf("  %-30s %d\n", label, queryInt(a.ctx, a.client, sql))
}

func (a *auditor) tableCounts() {
	tables := []string{
		"congresses", "members", "member_terms", "bills", "bill_actions",
		"bill_summaries", "bill_text_versions", "bill_texts", "bill_text_diffs",
		"bill_text_diff_summaries", "amendments", "congressional_votes",
		"member_votes", "users", "user_votes", "user_favorites", "sync_state", "sync_retry",
		"bill_sponsorships", "bill_committees", "bill_subjects", "bill_relations", "bill_crs_summaries",
	}

	a.printf("=== TABLE ROW COUNTS ===\n")
	for _, t := range tables {
		a.count(t, "SELECT COUNT(*) FROM "+t)
	}
}

func (a *auditor) distributions() {
	a.section("BILL STATUS DISTRIBUTION")
	a.rows("SELECT current_status, COUNT(*) AS cnt FROM bills GROUP BY current_status ORDER BY cnt DESC")

	a.section("BILL TYPES")
	a.rows("SELECT bill_type, COUNT(*) AS cnt FROM bills GROUP BY bill_type ORDER BY cnt DESC")

	a.section("MEMBER CHAMBERS")
	a.rows("SELECT chamber, COUNT(*) AS cnt FROM member_terms GROUP BY chamber")

	a.section("MEMBER PARTIES")
	a.rows("SELECT party, COUNT(*) AS cnt FROM member_terms GROUP BY party ORDER BY cnt DESC")

	a.section("TEXT VERSIONS PER BILL (top 5)")
	a.rows("SELECT bill_id, COUNT(*) AS cnt FROM bill_text_versions GROUP BY bill_id ORDER BY cnt DESC LIMIT 5")

	// Empty diffs (is_empty) only tell the diff sweep a pair had no section changes
	// (docs/design/401-remember-empty-diffs.md), so they're counted apart from the real ones.
	a.section("BILLS WITH DIFFS")
	a.count("empty diffs", "SELECT COUNT(*) FROM bill_text_diffs WHERE is_empty")
	a.rows(`SELECT bill_id, COUNT(*) AS cnt FROM bill_text_diffs WHERE NOT is_empty
		GROUP BY bill_id ORDER BY cnt DESC LIMIT 5`)

	a.section("BILLS WITH AMENDMENTS")
	a.rows("SELECT bill_id, COUNT(*) AS cnt FROM amendments GROUP BY bill_id ORDER BY cnt DESC LIMIT 5")

	a.section("CONGRESSIONAL VOTES BY CHAMBER")
	a.rows("SELECT chamber, COUNT(*) AS cnt FROM congressional_votes GROUP BY chamber")

	a.section("BILLS WITH CONGRESSIONAL VOTES")
	billsWithVotes := queryInt(a.ctx, a.client,
		"SELECT COUNT(DISTINCT bill_id) FROM congressional_votes WHERE bill_id IS NOT NULL")
	a.printf("  %d bills have at least one roll call vote\n", billsWithVotes)
}

func (a *auditor) samples() {
	a.section("SAMPLE BILL WITH ALL FIELDS")
	a.rows(`SELECT bill_id, congress, bill_type, number, title, introduced_date,
		origin_chamber, current_status, status_date, policy_area,
		CASE WHEN sponsors IS NOT NULL THEN 'YES' ELSE 'NO' END AS has_sponsors,
		CASE WHEN cosponsors IS NOT NULL THEN 'YES' ELSE 'NO' END AS has_cosponsors,
		CASE WHEN committees IS NOT NULL THEN 'YES' ELSE 'NO' END AS has_committees,
		CASE WHEN subjects IS NOT NULL THEN 'YES' ELSE 'NO' END AS has_subjects,
		CASE WHEN related_bills IS NOT NULL THEN 'YES' ELSE 'NO' END AS has_related_bills,
		CASE WHEN latest_action IS NOT NULL THEN 'YES' ELSE 'NO' END AS has_latest_action
		FROM bills LIMIT 3`)

	a.section("SAMPLE BILL TEXT (sections present?)")
	a.rows(`SELECT bt.version_id, bt.format, LENGTH(bt.content) AS content_len,
		bt.content_hash, CASE WHEN bt.sections IS NOT NULL THEN 'YES' ELSE 'NO' END AS has_sections
		FROM bill_texts bt LIMIT 5`)

	a.section("SAMPLE DIFF")
	a.rows(`SELECT diff_id, bill_id, from_version_id, to_version_id, diff_stats
		FROM bill_text_diffs WHERE NOT is_empty LIMIT 3`)
}

// orphanEdges counts link-table edges whose member or bill is missing. Link-table FKs to members
// and bills are NOT ENFORCED, so edges can point at rows the pipeline hasn't synced yet
// (docs/design/31-bill-ontology.md).
func (a *auditor) orphanEdges() {
	a.section("ORPHAN EDGES")
	a.count("sponsorships with no member", `SELECT COUNT(*) FROM bill_sponsorships s
		WHERE NOT EXISTS (SELECT 1 FROM members m WHERE m.bioguide_id = s.member_id)`)
	a.count("relations with no bill", `SELECT COUNT(*) FROM bill_relations r
		WHERE NOT EXISTS (SELECT 1 FROM bills b WHERE b.bill_id = r.related_bill_id)`)
	a.rows(`SELECT s.member_id, COUNT(*) AS cnt FROM bill_sponsorships s
		WHERE NOT EXISTS (SELECT 1 FROM members m WHERE m.bioguide_id = s.member_id)
		GROUP BY s.member_id ORDER BY cnt DESC LIMIT 5`)
}

// textIntegrity counts broken text rows; all four should be 0
// (docs/design/79-text-version-upsert.md; law refs: #530, db/scripts/530-law-refs-cleanup.sql).
func (a *auditor) textIntegrity() {
	a.section("TEXT INTEGRITY")
	a.count("texts with no version", `SELECT COUNT(*) FROM bill_texts t
		WHERE NOT EXISTS (SELECT 1 FROM bill_text_versions v WHERE v.version_id = t.version_id)`)
	a.count("diffs missing a version", `SELECT COUNT(*) FROM bill_text_diffs d
		WHERE NOT EXISTS (SELECT 1 FROM bill_text_versions v
			WHERE v.bill_id = d.bill_id AND v.version_id = d.from_version_id)
		OR NOT EXISTS (SELECT 1 FROM bill_text_versions v
			WHERE v.bill_id = d.bill_id AND v.version_id = d.to_version_id)`)
	a.count("law refs missing a version", `SELECT COUNT(*) FROM bill_law_refs r
		WHERE NOT EXISTS (SELECT 1 FROM bill_text_versions v
			WHERE v.bill_id = r.bill_id AND v.version_id = r.version_id)`)
	a.count("backwards diffs", `SELECT COUNT(*) FROM bill_text_diffs d
		JOIN bill_text_versions f ON f.bill_id = d.bill_id AND f.version_id = d.from_version_id
		JOIN bill_text_versions t ON t.bill_id = d.bill_id AND t.version_id = d.to_version_id
		WHERE f.sort_order >= t.sort_order`)
}

// syncState prints sync_state, then the sync_retry counts per step and the oldest failures: items
// that still failed after retries and wait for a later run (docs/design/67-upstream-quota-retries.md).
func (a *auditor) syncState() {
	a.section("SYNC STATE")
	a.rows(`SELECT step, congress, last_synced_at, last_attempt_at, items_synced,
		consecutive_failures, error_count, last_error_at FROM sync_state ORDER BY step, congress`)

	a.section("SYNC RETRY")
	a.rows(`SELECT step, congress,
		COUNTIF(next_attempt_at <= CURRENT_TIMESTAMP()) AS due,
		COUNTIF(next_attempt_at > CURRENT_TIMESTAMP()) AS waiting,
		COUNTIF(next_attempt_at IS NULL) AS given_up
		FROM sync_retry GROUP BY step, congress ORDER BY step, congress`)
	a.printf("  oldest failures:\n")
	a.rows(`SELECT step, congress, item_id, attempts, first_failed_at, next_attempt_at,
		SUBSTR(last_error, 1, 120) AS last_error
		FROM sync_retry ORDER BY first_failed_at LIMIT 10`)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func queryInt(ctx context.Context, client *spanner.Client, sql string) int64 {
	iter := client.Single().Query(ctx, spanner.NewStatement(sql))
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		return -1
	}
	var v int64
	_ = row.Columns(&v)
	return v
}

// rows prints each row of the query as a JSON object, or the query's error.
func (a *auditor) rows(sql string) {
	iter := a.client.Single().Query(a.ctx, spanner.NewStatement(sql))
	defer iter.Stop()
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			a.printf("  ERROR: %v\n", err)
			return
		}
		cols := row.ColumnNames()
		vals := make(map[string]any)
		for i, name := range cols {
			var v spanner.GenericColumnValue
			_ = row.Column(i, &v)
			vals[name] = v.Value.GetStringValue()
			if lv := v.Value.GetListValue(); lv != nil {
				vals[name] = fmt.Sprintf("[list:%d]", len(lv.GetValues()))
			}
			if nv := v.Value.GetNumberValue(); nv != 0 {
				vals[name] = nv
			}
		}
		b, _ := json.Marshal(vals)
		a.printf("  %s\n", string(b))
	}
}
