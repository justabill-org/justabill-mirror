package migrations_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/schema"
)

// apiRole is the database role the API connects as (docs/design/609-api-database-role.md).
const apiRole = "api"

// wrenchTable is wrench's own bookkeeping table: it's in db/schema.sql, but no migration creates it.
const wrenchTable = "SchemaMigrations"

// The statements TestAPIRoleGrants follows. Statements are matched with their whitespace collapsed
// and their "--" comments removed.
var (
	createTableStmt = regexp.MustCompile(`(?i)^CREATE TABLE (?:IF NOT EXISTS )?(\w+)`)
	dropTableStmt   = regexp.MustCompile(`(?i)^DROP TABLE (?:IF EXISTS )?(\w+)$`)
	createRoleStmt  = regexp.MustCompile(`(?i)^CREATE ROLE (\w+)$`)
	dropRoleStmt    = regexp.MustCompile(`(?i)^DROP ROLE (\w+)$`)
	grantStmt       = regexp.MustCompile(`(?i)^(GRANT|REVOKE) (.+?) ON (.+?) (?:TO|FROM) ROLE (.+)$`)
	accessStmt      = regexp.MustCompile(`(?i)^(GRANT|REVOKE)\b`)
	lineComment     = regexp.MustCompile(`--[^\n]*`)
)

// apiClasses sorts every table by what the api role may do with it. Each map holds table names;
// apiNone's values say why the API never reads the table.
type apiClasses struct {
	writes map[string]bool
	reads  map[string]bool
	none   map[string]string
}

// apiTableClasses is the classification the migrations must grant. A migration that adds a table
// grants the api role access to it in the same file and lists it in writes or reads here, or lists
// it in none with the reason the API never reads it.
func apiTableClasses() apiClasses {
	return apiClasses{
		writes: set("users", "user_votes", "user_favorites"),
		reads: set(
			"congresses", "members", "member_terms", "bills", "committees", "subjects", "policy_areas",
			"bill_sponsorships", "bill_committees", "bill_subjects", "bill_relations", "bill_status_history",
			"bill_actions", "bill_summaries", "bill_text_versions", "bill_texts", "bill_text_diffs",
			"bill_text_diff_summaries", "amendments", "congressional_votes", "member_votes", "gao_reports",
			"bill_gao_reports", "vote_aggregates", "rep_alignment", "usc_release_points", "usc_sections",
			"bill_law_refs", "bill_law_changes", "bill_crs_summaries", "federal_register_documents", "bill_cra_rules",
		),
		none: map[string]string{
			wrenchTable:             "wrench's migration version",
			"sync_state":            "the pipeline's sync cursors and health",
			"sync_retry":            "the pipeline's retry queue",
			"pipeline_leases":       "the pipeline's leader leases",
			"summary_attempts":      "the pipeline's summary bookkeeping",
			"diff_summary_attempts": "the pipeline's diff summary bookkeeping",
			"summary_batches":       "the pipeline's batch summary jobs",
			"law_change_attempts":   "the pipeline's law change bookkeeping",
		},
	}
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// migration is one migration file's statements.
type migration struct {
	name  string
	stmts []string
}

// TestAPIRoleGrants keeps the api role's grants in step with the schema: every table is classified
// in apiTableClasses, and the migrations grant exactly that. The emulator accepts but ignores roles
// and grants, and leaves them out of db/schema.sql, so this static check is what catches a missing
// or wrong grant before production does.
func TestAPIRoleGrants(t *testing.T) {
	stmts, err := schema.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range checkAPIRole(readMigrations(t), schemaTables(stmts), apiTableClasses()) {
		t.Error(p)
	}
}

func readMigrations(t *testing.T) []migration {
	t.Helper()
	names, err := filepath.Glob("[0-9]*.sql")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	migs := make([]migration, 0, len(names))
	for _, name := range names {
		data, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		migs = append(migs, migration{name: name, stmts: schema.Split(string(data))})
	}
	return migs
}

// schemaTables returns the names of the tables the statements create.
func schemaTables(stmts []string) []string {
	var tables []string
	for _, s := range stmts {
		if m := createTableStmt.FindStringSubmatch(normalize(s)); m != nil {
			tables = append(tables, m[1])
		}
	}
	return tables
}

// normalize removes "--" comments and collapses whitespace, so a statement matches on one line.
func normalize(stmt string) string {
	return strings.Join(strings.Fields(lineComment.ReplaceAllString(stmt, "")), " ")
}

// checkAPIRole replays the migrations' tables, roles and grants and returns every problem: a
// statement production would reject or this test can't follow, a table classified wrongly, or a
// grant that doesn't match the table's class.
func checkAPIRole(migs []migration, tables []string, classes apiClasses) []string {
	r := newReplay()
	for _, m := range migs {
		for _, s := range m.stmts {
			if err := r.apply(normalize(s)); err != nil {
				r.problems = append(r.problems, fmt.Sprintf("%s: %v", m.name, err))
			}
		}
	}
	r.compareTables(tables)
	r.checkClasses(tables, classes)
	return r.problems
}

// replay is the state of the database after the migrations so far: its tables, roles and grants.
type replay struct {
	tables   map[string]bool
	roles    map[string]bool
	grants   map[string]map[string]map[string]bool // role → table → privilege
	problems []string
}

func newReplay() *replay {
	return &replay{tables: map[string]bool{}, roles: map[string]bool{}, grants: map[string]map[string]map[string]bool{}}
}

func (r *replay) apply(stmt string) error {
	if m := createTableStmt.FindStringSubmatch(stmt); m != nil {
		r.tables[m[1]] = true
		return nil
	}
	if m := dropTableStmt.FindStringSubmatch(stmt); m != nil {
		return r.dropTable(m[1])
	}
	if m := createRoleStmt.FindStringSubmatch(stmt); m != nil {
		if r.roles[m[1]] {
			return fmt.Errorf("CREATE ROLE %s: the role already exists", m[1])
		}
		r.roles[m[1]] = true
		return nil
	}
	if m := dropRoleStmt.FindStringSubmatch(stmt); m != nil {
		if len(r.held(m[1])) > 0 {
			return fmt.Errorf("DROP ROLE %s: REVOKE its privileges first", m[1])
		}
		delete(r.roles, m[1])
		return nil
	}
	if m := grantStmt.FindStringSubmatch(stmt); m != nil {
		return r.access(strings.EqualFold(m[1], "GRANT"), m[2], m[3], m[4])
	}
	if accessStmt.MatchString(stmt) {
		return fmt.Errorf("%q: only GRANT and REVOKE of table privileges to a role are allowed here;"+
			" extend TestAPIRoleGrants to follow anything else", stmt)
	}
	return nil
}

func (r *replay) dropTable(table string) error {
	for role := range r.grants {
		if len(r.grants[role][table]) > 0 {
			return fmt.Errorf("DROP TABLE %s: REVOKE role %s's privileges on it first", table, role)
		}
	}
	delete(r.tables, table)
	return nil
}

// held returns the tables a role holds any privilege on.
func (r *replay) held(role string) []string {
	var tables []string
	for table, privs := range r.grants[role] {
		if len(privs) > 0 {
			tables = append(tables, table)
		}
	}
	return tables
}

// access applies one GRANT (grant true) or REVOKE of table privileges to or from roles.
func (r *replay) access(grant bool, privList, object, roleList string) error {
	if !strings.HasPrefix(strings.ToUpper(object), "TABLE ") {
		return fmt.Errorf("ON %s: grant table by table (the emulator rejects ALL TABLES IN SCHEMA, and"+
			" this test follows only tables)", object)
	}
	privs, err := parsePrivileges(privList)
	if err != nil {
		return err
	}
	targets := object[len("TABLE "):]
	for role := range strings.SplitSeq(roleList, ",") {
		role = strings.TrimSpace(role)
		if !r.roles[role] {
			return fmt.Errorf("role %s doesn't exist (production rejects the statement)", role)
		}
		for table := range strings.SplitSeq(targets, ",") {
			table = strings.TrimSpace(table)
			if !r.tables[table] {
				return fmt.Errorf("table %s doesn't exist (production rejects the statement)", table)
			}
			r.setPrivileges(grant, role, table, privs)
		}
	}
	return nil
}

func (r *replay) setPrivileges(grant bool, role, table string, privs []string) {
	if r.grants[role] == nil {
		r.grants[role] = map[string]map[string]bool{}
	}
	if r.grants[role][table] == nil {
		r.grants[role][table] = map[string]bool{}
	}
	for _, p := range privs {
		if grant {
			r.grants[role][table][p] = true
		} else {
			delete(r.grants[role][table], p)
		}
	}
}

// parsePrivileges splits "SELECT, INSERT" into its privileges, refusing column-level ones.
func parsePrivileges(list string) ([]string, error) {
	var privs []string
	for p := range strings.SplitSeq(list, ",") {
		p = strings.ToUpper(strings.TrimSpace(p))
		switch {
		case strings.Contains(p, "("):
			return nil, fmt.Errorf("%s: column-level privileges aren't allowed (the emulator rejects them)", p)
		case slices.Contains([]string{"SELECT", "INSERT", "UPDATE", "DELETE"}, p):
			privs = append(privs, p)
		default:
			return nil, fmt.Errorf("%s: unknown privilege", p)
		}
	}
	return privs, nil
}

// compareTables checks the replay against db/schema.sql, so a statement this test misreads shows
// up as a disagreement instead of a silently wrong classification.
func (r *replay) compareTables(tables []string) {
	for _, t := range tables {
		if t != wrenchTable && !r.tables[t] {
			r.problems = append(r.problems, fmt.Sprintf("table %s is in db/schema.sql, but no migration"+
				" creates it (or this test misread one)", t))
		}
	}
	for t := range r.tables {
		if !slices.Contains(tables, t) {
			r.problems = append(r.problems, fmt.Sprintf("the migrations create table %s, but"+
				" db/schema.sql doesn't have it: run task db:schema", t))
		}
	}
}

// checkClasses checks that every table is in exactly one class and the api role holds exactly the
// privileges its class allows.
func (r *replay) checkClasses(tables []string, classes apiClasses) {
	for _, t := range tables {
		_, none := classes.none[t]
		n := btoi(classes.writes[t]) + btoi(classes.reads[t]) + btoi(none)
		switch {
		case n == 0:
			r.problems = append(r.problems, fmt.Sprintf("table %s isn't classified for the api role: grant"+
				" it in its migration and add it to apiTableClasses' writes or reads, or add it to none"+
				" with the reason the API never reads it", t))
			continue
		case n > 1:
			r.problems = append(r.problems, fmt.Sprintf("table %s is in more than one class in apiTableClasses", t))
			continue
		}
		want := wantPrivileges(classes, t)
		if got := r.privileges(apiRole, t); !slices.Equal(got, want) {
			r.problems = append(r.problems, fmt.Sprintf("the api role holds %v on table %s; its class"+
				" allows exactly %v", got, t, want))
		}
	}
	for t := range classes.writes {
		r.checkExists(t, tables)
	}
	for t := range classes.reads {
		r.checkExists(t, tables)
	}
	for t := range classes.none {
		r.checkExists(t, tables)
	}
}

func (r *replay) checkExists(table string, tables []string) {
	if !slices.Contains(tables, table) {
		r.problems = append(r.problems, fmt.Sprintf("apiTableClasses lists table %s, which isn't in"+
			" db/schema.sql", table))
	}
}

// privileges returns a role's privileges on a table, sorted.
func (r *replay) privileges(role, table string) []string {
	var privs []string
	for p, ok := range r.grants[role][table] {
		if ok {
			privs = append(privs, p)
		}
	}
	slices.Sort(privs)
	return privs
}

func wantPrivileges(classes apiClasses, table string) []string {
	switch {
	case classes.writes[table]:
		return []string{"DELETE", "INSERT", "SELECT", "UPDATE"}
	case classes.reads[table]:
		return []string{"SELECT"}
	}
	return nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestCheckAPIRole runs checkAPIRole on small migration sets, so the guard is known to fail where
// it should.
func TestCheckAPIRole(t *testing.T) {
	base := []string{
		"CREATE TABLE bills (bill_id STRING(64)) PRIMARY KEY (bill_id)",
		"CREATE TABLE users (id STRING(36)) PRIMARY KEY (id)",
		"CREATE TABLE sync_state (key STRING(64)) PRIMARY KEY (key)",
		"CREATE ROLE api",
		"GRANT SELECT ON TABLE bills TO ROLE api",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE users TO ROLE api",
	}
	tables := []string{"bills", "users", "sync_state"}
	classes := func() apiClasses {
		return apiClasses{
			writes: set("users"),
			reads:  set("bills"),
			none:   map[string]string{"sync_state": "bookkeeping"},
		}
	}
	tests := []struct {
		name    string
		stmts   []string // after base, in a second migration
		tables  []string // db/schema.sql's tables; nil means base's
		classes func(*apiClasses)
		want    string // a substring of the one problem expected; "" for none
	}{
		{name: "base passes"},
		{
			name:  "a new table needs a class",
			stmts: []string{"CREATE TABLE notes (\n  id STRING(36), -- the note\n) PRIMARY KEY (id)"},
			want:  "table notes isn't classified",
		},
		{
			name:    "a new read table needs its grant",
			stmts:   []string{"CREATE TABLE notes (id STRING(36)) PRIMARY KEY (id)"},
			classes: func(c *apiClasses) { c.reads["notes"] = true },
			want:    "holds [] on table notes",
		},
		{
			name: "a new read table with its grant passes",
			stmts: []string{
				"CREATE TABLE notes (id STRING(36)) PRIMARY KEY (id)",
				"GRANT SELECT ON TABLE notes TO ROLE api",
			},
			classes: func(c *apiClasses) { c.reads["notes"] = true },
		},
		{
			name:  "a write grant on a read table fails",
			stmts: []string{"GRANT UPDATE ON TABLE bills TO ROLE api"},
			want:  "holds [SELECT UPDATE] on table bills",
		},
		{
			name:  "a grant on a bookkeeping table fails",
			stmts: []string{"grant select on table sync_state to role api"},
			want:  "holds [SELECT] on table sync_state",
		},
		{
			name:  "a write table missing a privilege fails",
			stmts: []string{"REVOKE DELETE ON TABLE users FROM ROLE api"},
			want:  "holds [INSERT SELECT UPDATE] on table users",
		},
		{
			name:  "a grant on a missing table fails",
			stmts: []string{"GRANT SELECT ON TABLE nosuch TO ROLE api"},
			want:  "table nosuch doesn't exist",
		},
		{
			name: "a grant on a dropped table fails",
			stmts: []string{
				"CREATE TABLE old (id STRING(36)) PRIMARY KEY (id)",
				"DROP TABLE old",
				"GRANT SELECT ON TABLE old TO ROLE api",
			},
			tables: []string{"bills", "users", "sync_state"},
			want:   "table old doesn't exist",
		},
		{
			// Production rejects the DROP, so the table and its grant stay.
			name:  "dropping a granted table needs a REVOKE first",
			stmts: []string{"DROP TABLE bills"},
			want:  "DROP TABLE bills: REVOKE role api's privileges on it first",
		},
		{
			name:   "REVOKE then DROP TABLE passes",
			stmts:  []string{"REVOKE SELECT ON TABLE bills FROM ROLE api", "DROP TABLE bills"},
			tables: []string{"users", "sync_state"},
			classes: func(c *apiClasses) {
				delete(c.reads, "bills")
			},
		},
		{
			name:  "a column-level grant fails",
			stmts: []string{"GRANT SELECT(bill_id) ON TABLE bills TO ROLE api"},
			want:  "column-level privileges aren't allowed",
		},
		{
			name:  "ALL TABLES IN SCHEMA fails",
			stmts: []string{"GRANT SELECT ON ALL TABLES IN SCHEMA default TO ROLE api"},
			want:  "grant table by table",
		},
		{
			name:  "a grant to a missing role fails",
			stmts: []string{"GRANT SELECT ON TABLE bills TO ROLE apii"},
			want:  "role apii doesn't exist",
		},
		{
			name:  "a second CREATE ROLE fails",
			stmts: []string{"CREATE ROLE api"},
			want:  "the role already exists",
		},
		{
			name:  "role inheritance isn't followed",
			stmts: []string{"CREATE ROLE pipeline", "GRANT ROLE pipeline TO ROLE api"},
			want:  "extend TestAPIRoleGrants",
		},
		{
			name:  "dropping a role that holds privileges fails",
			stmts: []string{"DROP ROLE api"},
			want:  "DROP ROLE api: REVOKE its privileges first",
		},
		{
			name:    "a table in two classes fails",
			classes: func(c *apiClasses) { c.reads["users"] = true },
			want:    "table users is in more than one class",
		},
		{
			name:    "a class naming a missing table fails",
			classes: func(c *apiClasses) { c.none["gone"] = "dropped" },
			want:    "apiTableClasses lists table gone",
		},
		{
			name:   "a table the migrations don't create fails",
			tables: []string{"bills", "users", "sync_state", "extra"},
			classes: func(c *apiClasses) {
				c.none["extra"] = "test"
			},
			want: "table extra is in db/schema.sql, but no migration creates it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			migs := []migration{{name: "000001_base.sql", stmts: base}, {name: "000002_next.sql", stmts: tt.stmts}}
			c := classes()
			if tt.classes != nil {
				tt.classes(&c)
			}
			tbls := tt.tables
			if tbls == nil {
				tbls = slices.Concat(tables, schemaTables(tt.stmts))
			}
			wantProblem(t, checkAPIRole(migs, tbls, c), tt.want)
		})
	}
}

// wantProblem fails the test unless problems is exactly one problem containing want, or none when
// want is empty.
func wantProblem(t *testing.T, problems []string, want string) {
	t.Helper()
	if want == "" {
		if len(problems) > 0 {
			t.Fatalf("problems = %q, want none", problems)
		}
		return
	}
	if len(problems) != 1 || !strings.Contains(problems[0], want) {
		t.Fatalf("problems = %q, want one containing %q", problems, want)
	}
}
