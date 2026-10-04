package main

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/scheduler"
)

// testSyncJobs is the serve job table with no service, following no congress.
func testSyncJobs() []scheduler.Job {
	return syncJobs(nil, newCongressTracker(&fakeCongresses{}, 0, slog.New(slog.DiscardHandler)), nil)
}

func TestTimeoutEnv(t *testing.T) {
	for job, want := range map[string]string{
		"sync-bills":     "PIPELINE_JOB_TIMEOUT_SYNC_BILLS",
		"sync-govinfo":   "PIPELINE_JOB_TIMEOUT_SYNC_GOVINFO",
		"sync-summaries": "PIPELINE_JOB_TIMEOUT_SYNC_SUMMARIES",
	} {
		if got := timeoutEnv(job); got != want {
			t.Errorf("timeoutEnv(%q) = %q, want %q", job, got, want)
		}
	}
}

func TestWithTimeoutOverrides_None(t *testing.T) {
	jobs := testSyncJobs()
	got, changed, err := withTimeoutOverrides(jobs, []string{"PATH=/usr/bin", "PIPELINE_HEALTH_ADDR=:8081"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("changed = %v, want none", changed)
	}
	for i := range jobs {
		if got[i].Name != jobs[i].Name || got[i].Timeout != jobs[i].Timeout || got[i].Interval != jobs[i].Interval {
			t.Errorf("job %d = %+v, want %+v", i, got[i], jobs[i])
		}
	}
}

func TestWithTimeoutOverrides_Applies(t *testing.T) {
	jobs := testSyncJobs()
	got, changed, err := withTimeoutOverrides(jobs, []string{
		"PIPELINE_JOB_TIMEOUT_SYNC_BILLS=210m",
		"PIPELINE_JOB_TIMEOUT_SYNC_GOVINFO= 29m ",
		"PIPELINE_JOB_TIMEOUT_SYNC_CRA_RULES=45m",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(changed, []string{"sync-bills", "sync-govinfo", "sync-cra-rules"}) {
		t.Errorf("changed = %v", changed)
	}
	want := map[string]time.Duration{
		"sync-bills":         210 * time.Minute,
		"sync-members":       memberSyncTimeout,
		"sync-votes":         voteSyncTimeout,
		"sync-texts":         textSyncTimeout,
		"sync-summaries":     summarySyncTimeout,
		"sync-law-changes":   lawChangeSyncTimeout,
		"sync-govinfo":       29 * time.Minute,
		"load-uscode":        uscodeLoadTimeout,
		"sync-gao":           gaoSyncTimeout,
		"sync-crs-summaries": crsSyncTimeout,
		"sync-cra-rules":     45 * time.Minute,
	}
	for _, j := range got {
		if j.Timeout != want[j.Name] {
			t.Errorf("%s: timeout %v, want %v", j.Name, j.Timeout, want[j.Name])
		}
		if j.Run == nil {
			t.Errorf("%s: Run lost", j.Name)
		}
	}
	if jobs[0].Timeout != billSyncTimeout {
		t.Errorf("input table changed: sync-bills timeout %v", jobs[0].Timeout)
	}
}

func TestWithTimeoutOverrides_Rejects(t *testing.T) {
	for name, tc := range map[string]struct{ env, wantErr string }{
		"unknown job":    {"PIPELINE_JOB_TIMEOUT_SYNC_BIL=4h", "PIPELINE_JOB_TIMEOUT_SYNC_BIL names no job"},
		"not a duration": {"PIPELINE_JOB_TIMEOUT_SYNC_BILLS=4", "PIPELINE_JOB_TIMEOUT_SYNC_BILLS: "},
		"empty":          {"PIPELINE_JOB_TIMEOUT_SYNC_BILLS=", "PIPELINE_JOB_TIMEOUT_SYNC_BILLS: "},
		"zero":           {"PIPELINE_JOB_TIMEOUT_SYNC_BILLS=0s", "must be positive and shorter than"},
		"negative":       {"PIPELINE_JOB_TIMEOUT_SYNC_BILLS=-1h", "must be positive"},
		"equals interval": {
			"PIPELINE_JOB_TIMEOUT_SYNC_BILLS=4h", "shorter than the sync-bills interval of 4h0m0s",
		},
		"over interval": {"PIPELINE_JOB_TIMEOUT_SYNC_GOVINFO=1h", "sync-govinfo interval of 30m0s"},
	} {
		t.Run(name, func(t *testing.T) {
			got, _, err := withTimeoutOverrides(testSyncJobs(), []string{tc.env})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if got != nil {
				t.Errorf("got jobs %v with an error", got)
			}
		})
	}
}

func TestTimeoutAttrs(t *testing.T) {
	jobs, _, err := withTimeoutOverrides(testSyncJobs(),
		[]string{"PIPELINE_JOB_TIMEOUT_SYNC_BILLS=210m"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var buf strings.Builder
	slog.New(slog.NewJSONHandler(&buf, nil)).InfoContext(t.Context(), "job timeouts",
		slog.Group("timeouts", timeoutAttrs(jobs)...))
	var line struct {
		Timeouts map[string]string `json:"timeouts"`
	}
	if err = json.Unmarshal([]byte(buf.String()), &line); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	if line.Timeouts["sync-bills"] != "3h30m0s" || line.Timeouts["sync-govinfo"] != "25m0s" ||
		len(line.Timeouts) != len(jobs) {
		t.Errorf("timeouts = %v", line.Timeouts)
	}
}

func TestWithSummaryInterval(t *testing.T) {
	jobs := testSyncJobs()
	same, err := withSummaryInterval(jobs, " ")
	if err != nil || !slices.EqualFunc(same, jobs, func(a, b scheduler.Job) bool {
		return a.Name == b.Name && a.Interval == b.Interval && a.Timeout == b.Timeout
	}) {
		t.Fatalf("unset AI_SUMMARY_INTERVAL changed the table (err %v)", err)
	}

	got, err := withSummaryInterval(jobs, "1h")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for i, j := range got {
		want := jobs[i]
		if j.Name == summaryJobName {
			want.Interval, want.Timeout = time.Hour, 50*time.Minute
		}
		if j.Interval != want.Interval || j.Timeout != want.Timeout {
			t.Errorf("%s: interval %v timeout %v, want %v and %v", j.Name, j.Interval, j.Timeout,
				want.Interval, want.Timeout)
		}
	}

	// The timeout override still applies, and is checked against the new interval.
	if _, _, err = withTimeoutOverrides(got, []string{"PIPELINE_JOB_TIMEOUT_SYNC_SUMMARIES=55m"}); err != nil {
		t.Errorf("55m override of a 1h interval: %v", err)
	}

	for _, bad := range []string{"30", "0s", "-5m", "soon"} {
		if _, err = withSummaryInterval(
			jobs,
			bad,
		); err == nil ||
			!strings.Contains(err.Error(), "AI_SUMMARY_INTERVAL") {
			t.Errorf("AI_SUMMARY_INTERVAL=%q: err = %v, want an AI_SUMMARY_INTERVAL error", bad, err)
		}
	}
}
