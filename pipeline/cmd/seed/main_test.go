package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
	"time"
)

func TestParseCongress(t *testing.T) {
	lastDayOf119th := time.Date(2027, 1, 2, 23, 59, 0, 0, time.UTC)
	firstDayOf120th := time.Date(2027, 1, 3, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		args []string
		now  time.Time
		want int
	}{
		{"default before January 3, 2027", nil, lastDayOf119th, 119},
		{"default from January 3, 2027", nil, firstDayOf120th, 120},
		{"flag wins", []string{"--congress", "118"}, firstDayOf120th, 118},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCongress(tt.args, &bytes.Buffer{}, tt.now)
			if err != nil || got != tt.want {
				t.Fatalf("parseCongress(%v, %s) = %d, %v; want %d", tt.args, tt.now, got, err, tt.want)
			}
		})
	}
}

func TestParseCongressRejects(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	for _, args := range [][]string{{"--congress", "-1"}, {"extra"}, {"--congress", "x"}} {
		if _, err := parseCongress(args, &bytes.Buffer{}, now); err == nil {
			t.Errorf("parseCongress(%v): want an error", args)
		}
	}
}

func TestParseCongressHelp(t *testing.T) {
	var stderr bytes.Buffer
	_, err := parseCongress([]string{"-h"}, &stderr, time.Now())
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseCongress(-h) err = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(stderr.String(), "the congress in progress") {
		t.Errorf("help = %q, want it to say the default is the congress in progress", stderr.String())
	}
}
