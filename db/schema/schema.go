// Package schema reads db/schema.sql and splits it into the statements the
// Spanner database admin API takes. The test helpers (testdb), initdb and
// e2e-seed all build databases from it.
package schema

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Path returns the absolute path of db/schema.sql, located relative to this
// source file so it works from any working directory in the repository.
func Path() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("schema: cannot determine the source location")
	}
	return filepath.Join(filepath.Dir(file), "..", "schema.sql"), nil
}

// Read returns db/schema.sql's DDL statements, ready for CreateDatabase.
func Read() ([]string, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("schema: read %s: %w", path, err)
	}
	return Split(string(data)), nil
}

// Split splits a DDL script into individual statements. Blank lines and
// full-line "--" comments are dropped, and the trailing semicolon is removed
// because the Spanner admin API rejects it.
func Split(ddl string) []string {
	var stmts []string
	var current strings.Builder
	for line := range strings.SplitSeq(ddl, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") {
			stmts = appendStatement(stmts, current.String())
			current.Reset()
		}
	}
	return appendStatement(stmts, current.String())
}

func appendStatement(stmts []string, stmt string) []string {
	stmt = strings.TrimSuffix(strings.TrimSpace(stmt), ";")
	if stmt == "" {
		return stmts
	}
	return append(stmts, stmt)
}
