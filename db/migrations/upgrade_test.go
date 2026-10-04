package migrations_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/testdb"
)

// baseVersionEnv carries main's highest migration version; scripts/db-upgrade-test.sh sets it.
const baseVersionEnv = "MIGRATIONS_BASE_VERSION"

// TestUpgradeFromBase applies a branch's new migrations to a database that already holds data,
// the way production will: the database is migrated to main's version, seeded with the test
// fixture, and then each new file runs on its own. It catches migrations that only fail on
// existing rows, such as a NOT NULL column without a default or a unique index over duplicates.
// Run it with `task db:upgrade-test`; without MIGRATIONS_BASE_VERSION it is skipped.
func TestUpgradeFromBase(t *testing.T) {
	raw := os.Getenv(baseVersionEnv)
	if raw == "" {
		t.Skip(baseVersionEnv + " not set; run scripts/db-upgrade-test.sh (task db:upgrade-test)")
	}
	base, err := strconv.Atoi(raw)
	if err != nil || base < 1 {
		t.Fatalf("%s = %q, want main's highest migration version", baseVersionEnv, raw)
	}
	pending := migrationsAbove(t, base)
	if len(pending) == 0 {
		t.Skipf("no migrations above version %d", base)
	}

	client, db := testdb.NewEmpty(t)
	w := newWrench(t, db)
	if out, runErr := w.run("migrate", "up", strconv.Itoa(base)); runErr != nil {
		t.Fatalf("migrating an empty database to main's version %d: %v\n%s", base, runErr, out)
	}
	testdb.SeedFixtureForSchema(t.Context(), t, client)

	for _, name := range pending {
		if out, runErr := w.run("migrate", "up", "1"); runErr != nil {
			t.Fatalf("%s fails on a database with data (main's version %d plus testdb's fixture): %v\n%s",
				name, base, runErr, out)
		}
		t.Logf("%s applied to the seeded database", name)
	}
}

// migrationsAbove returns the names of the migration files numbered above base, in order.
func migrationsAbove(t *testing.T, base int) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		m := fileName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if v, _ := strconv.Atoi(m[1]); v > base {
			names = append(names, e.Name())
		}
	}
	return names
}

// wrench runs the wrench CLI (WRENCH, default `go run` of the pinned version) against one
// test database, with this directory as its migrations.
type wrench struct {
	t      *testing.T
	cmd    []string
	target []string
}

func newWrench(t *testing.T, db testdb.Database) wrench {
	t.Helper()
	cmd := strings.Fields(os.Getenv("WRENCH"))
	if len(cmd) == 0 {
		cmd = []string{"go", "run", "github.com/cloudspannerecosystem/wrench@v1.13.5"}
	}
	migrations, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	// wrench reads <directory>/schema.sql and <directory>/migrations; the schema stays empty
	// because the migrations build everything from 000001.
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "schema.sql"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(migrations, filepath.Join(dir, "migrations")); err != nil {
		t.Fatal(err)
	}
	return wrench{t: t, cmd: cmd, target: []string{
		"--project", db.Project, "--instance", db.Instance, "--database", db.ID, "--directory", dir,
	}}
}

func (w wrench) run(args ...string) (string, error) {
	w.t.Helper()
	args = append(append(append([]string{}, w.cmd[1:]...), args...), w.target...)
	out, err := exec.CommandContext(w.t.Context(), w.cmd[0], args...).CombinedOutput()
	return string(out), err
}
