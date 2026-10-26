package main

import (
	"strings"
	"testing"
	"time"
)

func TestReadRecordedVotesFlags(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		args []string
		want recordedVotesOptions
	}{
		{"defaults: the congress in progress, pages of 50", nil, recordedVotesOptions{congress: 119, pageSize: 50}},
		{
			"every flag",
			[]string{"--congress=118", "--page-size=25", "--wait-for-lease"},
			recordedVotesOptions{congress: 118, pageSize: 25, wait: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newRecordedVotesCmd()
			if err := cmd.ParseFlags(tt.args); err != nil {
				t.Fatalf("ParseFlags(%q): %v", tt.args, err)
			}
			got, err := readRecordedVotesFlags(cmd, now)
			if err != nil || got != tt.want {
				t.Errorf("readRecordedVotesFlags(%q) = %+v, %v; want %+v", tt.args, got, err, tt.want)
			}
		})
	}
}

func TestReadRecordedVotesFlagsRejects(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--congress=-1"}, "--congress -1"},
		{[]string{"--page-size=0"}, "--page-size 0"},
	}
	for _, tt := range tests {
		cmd := newRecordedVotesCmd()
		if err := cmd.ParseFlags(tt.args); err != nil {
			t.Fatalf("ParseFlags(%q): %v", tt.args, err)
		}
		if _, err := readRecordedVotesFlags(cmd, now); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("readRecordedVotesFlags(%q) = %v, want an error naming %q", tt.args, err, tt.want)
		}
	}
}
