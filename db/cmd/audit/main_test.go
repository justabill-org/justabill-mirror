package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/testdb"
)

// TestAudit runs every audit query against the current schema, so a renamed or dropped column
// shows up here as an ERROR line rather than on someone's terminal.
func TestAudit(t *testing.T) {
	ctx := context.Background()
	client := testdb.New(t)
	testdb.SeedFixture(ctx, t, client)

	var out bytes.Buffer
	if err := audit(ctx, client, &out); err != nil {
		t.Fatalf("audit: %v", err)
	}
	report := out.String()
	if strings.Contains(report, "ERROR:") {
		t.Errorf("a query failed:\n%s", report)
	}
	for _, want := range []string{
		"=== TABLE ROW COUNTS ===",
		"\n=== BILL STATUS DISTRIBUTION ===\n",
		"\n=== TEXT INTEGRITY ===\n",
		"  law refs missing a version ",
		"  empty diffs ",
		"\n=== SYNC RETRY ===\n",
		"  oldest failures:\n",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report is missing %q:\n%s", want, report)
		}
	}
	// queryInt reports a failed count as -1.
	if strings.Contains(report, " -1\n") {
		t.Errorf("a count query failed:\n%s", report)
	}
}

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errBrokenPipe }

var errBrokenPipe = errors.New("broken pipe")

func TestAuditReturnsWriteError(t *testing.T) {
	ctx := context.Background()
	client := testdb.New(t)

	if err := audit(ctx, client, failingWriter{}); !errors.Is(err, errBrokenPipe) {
		t.Errorf("audit error = %v, want %v", err, errBrokenPipe)
	}
}
