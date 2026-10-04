package semconv_test

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/obs/semconv"
)

// TestGofmt keeps the templates honest: `weaver registry generate` doesn't run gofmt, and CI
// compares its output with the committed names.go byte for byte.
func TestGofmt(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("names.go")
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := format.Source(src)
	if err != nil {
		t.Fatalf("names.go doesn't parse: %v", err)
	}
	if string(formatted) != string(src) {
		t.Error("names.go isn't gofmt-clean; fix obs/semconv/templates/registry/go/names.go.j2 and run `task semconv`")
	}
}

// TestNamesAreOurs checks every generated name (the …Key, …Name and …Event constants) is under
// justabill.*: upstream names come from go.opentelemetry.io/otel/semconv, never from here.
func TestNamesAreOurs(t *testing.T) {
	t.Parallel()

	names := generatedNames(t)
	if len(names) == 0 {
		t.Fatal("found no names in names.go")
	}
	for ident, value := range names {
		if !strings.HasPrefix(value, "justabill.") {
			t.Errorf("%s = %q, want a justabill.* name", ident, value)
		}
	}
}

func TestConstants(t *testing.T) {
	t.Parallel()

	tests := []struct{ got, want string }{
		{string(semconv.JobNameKey), "justabill.job.name"},
		{semconv.JobOutcomeFailed, "failed"},
		{semconv.SummaryOutcomeSafetyBlocked, "safety_blocked"},
		{semconv.PipelineJobDurationName, "justabill.pipeline.job.duration"},
		{semconv.PipelineJobDurationUnit, "s"},
		{semconv.UpstreamQuotaRemainingUnit, "{request}"},
		{semconv.PipelineJobFinishedEvent, "justabill.pipeline.job.finished"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
}

// generatedNames maps each …Key, …Name and …Event constant in names.go to its string value.
func generatedNames(t *testing.T) map[string]string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "names.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		ident := spec.Names[0].Name
		if !strings.HasSuffix(ident, "Key") && !strings.HasSuffix(ident, "Name") &&
			!strings.HasSuffix(ident, "Event") {
			return true
		}
		value := spec.Values[0]
		if call, isCall := value.(*ast.CallExpr); isCall && len(call.Args) == 1 {
			value = call.Args[0] // attribute.Key("…")
		}
		lit, ok := value.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Errorf("%s isn't a string constant", ident)
			return true
		}
		s, unquoteErr := strconv.Unquote(lit.Value)
		if unquoteErr != nil {
			t.Errorf("%s: %v", ident, unquoteErr)
			return true
		}
		names[ident] = s
		return true
	})
	return names
}
