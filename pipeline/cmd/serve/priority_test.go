package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Every pipeline command opens its Spanner client with spannerdb.WithBatchPriority, so its
// batch reads yield the instance's CPU to the API's user reads (#867). The test reads each
// command's source, so a new command that forgets the option fails here.
func TestEveryCommandUsesBatchPriority(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "*", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, parseErr := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", file, parseErr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isSelector(call.Fun, "spannerdb", "NewClient") {
				return true
			}
			calls++
			if !hasBatchPriority(call.Args) {
				t.Errorf("%s: spannerdb.NewClient without spannerdb.WithBatchPriority()", file)
			}
			return true
		})
	}
	// serve, backfill (twice), backfill-law-refs, aggregates, load-uscode, seed, spotcheck and
	// summarize-batch: a glob that matched nothing would pass without checking anything.
	if calls < 9 {
		t.Errorf("found %d spannerdb.NewClient calls under cmd/, want at least 9", calls)
	}
}

func isSelector(expr ast.Expr, pkg, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

func hasBatchPriority(args []ast.Expr) bool {
	for _, arg := range args {
		if call, ok := arg.(*ast.CallExpr); ok && isSelector(call.Fun, "spannerdb", "WithBatchPriority") {
			return true
		}
	}
	return false
}
