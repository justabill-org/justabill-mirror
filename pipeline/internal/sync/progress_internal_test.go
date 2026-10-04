package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/obstest"
	"github.com/justabill-org/justabill/obs/semconv"
)

func TestBillProgressLogsEvery250Bills(t *testing.T) {
	var buf bytes.Buffer
	requests := int64(100)
	p := newBillProgress(slog.New(slog.NewJSONHandler(&buf, nil)), func() int64 { return requests }, 600, 7)

	for i := range 520 {
		requests += 8
		p.add(t.Context(), i%10 != 0) // every tenth bill fails
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d progress lines for 520 bills, want 2:\n%s", len(lines), buf.String())
	}
	var last struct {
		Msg       string  `json:"msg"`
		Done      int     `json:"done"`
		Failed    int     `json:"failed"`
		Skipped   int     `json:"skipped"`
		Remaining int     `json:"remaining"`
		RPS       float64 `json:"requests_per_second"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &last); err != nil {
		t.Fatal(err)
	}
	if last.Msg != "bills progress" || last.Done != 450 || last.Failed != 50 || last.Skipped != 7 ||
		last.Remaining != 100 || last.RPS <= 0 {
		t.Errorf("second progress line = %+v, want done 450, failed 50, skipped 7, remaining 100, rps > 0", last)
	}
}

// TestItemsCountInsideAJob checks the per-item helpers feed justabill.pipeline.items for the
// running job, and the job's finished record adds them up.
func TestItemsCountInsideAJob(t *testing.T) {
	tel := obstest.New(t)
	p := newBillProgress(slog.New(slog.DiscardHandler), func() int64 { return 0 }, 4, 0)

	outcome, err := obs.Job(t.Context(), tel.Logger, "sync-bills", func(ctx context.Context) error {
		p.add(ctx, true)
		p.add(ctx, false)
		p.addUnresolved(ctx)
		countItem(ctx, nil)
		countItem(ctx, errors.New("fetch text: 404"))
		return nil
	})
	if err != nil || outcome != semconv.JobOutcomeOK {
		t.Fatalf("Job = %q, %v", outcome, err)
	}

	m, ok := tel.Metric(t, semconv.PipelineItemsName)
	if !ok {
		t.Fatalf("no %s metric", semconv.PipelineItemsName)
	}
	sum, _ := m.Data.(metricdata.Sum[int64])
	got := map[string]int64{}
	for _, dp := range sum.DataPoints {
		o, _ := dp.Attributes.Value(semconv.ItemOutcomeKey)
		got[o.AsString()] = dp.Value
	}
	if got["ok"] != 2 || got["failed"] != 2 || got["skipped"] != 1 || len(got) != 3 {
		t.Errorf("items = %v, want ok 2, failed 2, skipped 1", got)
	}
}
