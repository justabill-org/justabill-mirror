package main

import (
	"log/slog"
	"strings"
	"testing"
)

// The command takes its logger from obs.Main and rejects a bad --congress before it connects.
func TestNewCommandRejectsANegativeCongress(t *testing.T) {
	cmd := newCommand(slog.New(slog.DiscardHandler))
	cmd.SetArgs([]string{"--congress", "-1"})

	err := cmd.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "--congress -1") {
		t.Fatalf("err = %v, want --congress -1 rejected", err)
	}
}
