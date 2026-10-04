package main

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/scheduler"
)

// timeoutEnvPrefix starts the variables that override a job's timeout, e.g.
// PIPELINE_JOB_TIMEOUT_SYNC_BILLS=4h (docs/design/80-pipeline-operability.md, open question 4).
const timeoutEnvPrefix = "PIPELINE_JOB_TIMEOUT_"

// timeoutEnv names the variable that overrides job's timeout: the job name uppercased, with
// dashes as underscores.
func timeoutEnv(job string) string {
	return timeoutEnvPrefix + strings.ToUpper(strings.ReplaceAll(job, "-", "_"))
}

// withTimeoutOverrides returns a copy of jobs with each timeout replaced by its
// PIPELINE_JOB_TIMEOUT_<JOB> variable in environ (KEY=value pairs, as from [os.Environ]), and
// the names of the jobs it changed. A value must be a positive [time.ParseDuration] shorter than
// the job's interval, so a run still ends before the next one is due. A variable that names no
// job is an error too, so a typo doesn't silently leave the constant in place.
func withTimeoutOverrides(jobs []scheduler.Job, environ []string) ([]scheduler.Job, []string, error) {
	byEnv := make(map[string]int, len(jobs))
	for i, j := range jobs {
		byEnv[timeoutEnv(j.Name)] = i
	}
	out := slices.Clone(jobs)
	var changed []string
	for _, kv := range environ {
		key, value, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(key, timeoutEnvPrefix) {
			continue
		}
		i, ok := byEnv[key]
		if !ok {
			return nil, nil, fmt.Errorf("%s names no job", key)
		}
		d, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", key, err)
		}
		if d <= 0 || d >= out[i].Interval {
			return nil, nil, fmt.Errorf("%s=%s must be positive and shorter than the %s interval of %s",
				key, value, out[i].Name, out[i].Interval)
		}
		out[i].Timeout = d
		changed = append(changed, out[i].Name)
	}
	return out, changed, nil
}

// summaryJobName is the summary job, whose interval AI_SUMMARY_INTERVAL sets.
const summaryJobName = "sync-summaries"

// summaryTimeoutShare is the part of the summary job's interval a run may take: 25 minutes of 30.
const summaryTimeoutShare = 5.0 / 6

// withSummaryInterval returns a copy of jobs with the summary job's interval set to value (a
// positive [time.ParseDuration], from AI_SUMMARY_INTERVAL) and its timeout to five sixths of it.
// An empty value keeps the table's interval. PIPELINE_JOB_TIMEOUT_SYNC_SUMMARIES still applies
// afterwards.
func withSummaryInterval(jobs []scheduler.Job, value string) ([]scheduler.Job, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return jobs, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return nil, fmt.Errorf("AI_SUMMARY_INTERVAL %q: want a positive duration such as 30m", value)
	}
	out := slices.Clone(jobs)
	for i := range out {
		if out[i].Name == summaryJobName {
			out[i].Interval = d
			out[i].Timeout = time.Duration(float64(d) * summaryTimeoutShare)
		}
	}
	return out, nil
}

// timeoutAttrs returns each job's timeout as a log attribute keyed by the job's name.
func timeoutAttrs(jobs []scheduler.Job) []any {
	attrs := make([]any, 0, len(jobs))
	for _, j := range jobs {
		attrs = append(attrs, slog.String(j.Name, j.Timeout.String()))
	}
	return attrs
}
