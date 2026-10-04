package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// The command runs with the logger obs.Main gives it and returns its error for obs.Main to log.
func TestNewCommandNeedsAModel(t *testing.T) {
	var logs bytes.Buffer
	cmd := newCommand(slog.New(slog.NewJSONHandler(&logs, nil)))
	cmd.SetArgs([]string{"--models", " , "})

	err := cmd.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "name at least one model") {
		t.Fatalf("err = %v, want --models rejected", err)
	}
	if logs.Len() != 0 {
		t.Errorf("logged %s; obs.Main logs the error, not the command", logs.String())
	}
}
