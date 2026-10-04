package main

import (
	"fmt"
	"io"
	"strings"
)

// maxListed caps how many IDs one report line lists.
const maxListed = 20

// report prints the checks' results as it goes and keeps count of failures and warnings.
// Failures make the command exit non-zero; warnings don't.
type report struct {
	w        io.Writer
	failures int
	warnings int
}

func (r *report) sectionf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.w, "\n== "+format+" ==\n", args...)
}

func (r *report) linef(status, format string, args ...any) {
	_, _ = fmt.Fprintf(r.w, "%-4s  %s\n", status, fmt.Sprintf(format, args...))
}

func (r *report) okf(format string, args ...any) { r.linef("ok", format, args...) }

func (r *report) infof(format string, args ...any) { r.linef("", format, args...) }

func (r *report) failf(format string, args ...any) {
	r.failures++
	r.linef("FAIL", format, args...)
}

func (r *report) warnf(format string, args ...any) {
	r.warnings++
	r.linef("WARN", format, args...)
}

// detail prints an indented line under the last one.
func (r *report) detail(s string) { _, _ = fmt.Fprintf(r.w, "        %s\n", s) }

// passed reports whether no check failed.
func (r *report) passed() bool { return r.failures == 0 }

// summary prints the result line.
func (r *report) summary() {
	result := "PASS"
	if !r.passed() {
		result = "FAIL"
	}
	_, _ = fmt.Fprintf(r.w, "\nRESULT: %s (%d failed, %d warnings)\n", result, r.failures, r.warnings)
}

// listIDs joins up to maxListed IDs and says how many more there are.
func listIDs(ids []string) string {
	if len(ids) <= maxListed {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(ids[:maxListed], ", "), len(ids)-maxListed)
}
