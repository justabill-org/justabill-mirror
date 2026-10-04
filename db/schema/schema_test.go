package schema_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/schema"
)

func TestSplit(t *testing.T) {
	ddl := `-- header comment
--

CREATE TABLE a (
  -- column comment
  id INT64 NOT NULL,
) PRIMARY KEY (id);

  -- indented comment
CREATE INDEX idx_a ON a(id);
CREATE TABLE b (id INT64 NOT NULL) PRIMARY KEY (id)
`
	want := []string{
		"CREATE TABLE a (\n  id INT64 NOT NULL,\n) PRIMARY KEY (id)",
		"CREATE INDEX idx_a ON a(id)",
		"CREATE TABLE b (id INT64 NOT NULL) PRIMARY KEY (id)",
	}
	if got := schema.Split(ddl); !reflect.DeepEqual(got, want) {
		t.Errorf("Split() = %q, want %q", got, want)
	}
}

func TestReadSchema(t *testing.T) {
	path, err := schema.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	stmts, err := schema.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := strings.Count(string(data), ";\n"); len(stmts) != want {
		t.Errorf("got %d statements, want %d", len(stmts), want)
	}
	for i, s := range stmts {
		if strings.HasSuffix(s, ";") || strings.Contains(s, "\n--") || strings.HasPrefix(s, "--") {
			t.Errorf("statement %d not clean: %q", i, s)
		}
	}
}
