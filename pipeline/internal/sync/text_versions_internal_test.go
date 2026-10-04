package sync

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/repository"
)

// textVersionsStore records what syncBillTextVersions upserts and returns a canned result.
type textVersionsStore struct {
	repository.PipelineStore

	billID string
	rows   []repository.TextVersionRow
	result repository.TextVersionSyncResult
	err    error
}

func (f *textVersionsStore) UpsertBillTextVersions(
	_ context.Context, billID string, rows []repository.TextVersionRow,
) (repository.TextVersionSyncResult, error) {
	f.billID, f.rows = billID, rows
	return f.result, f.err
}

const textVersionsBody = `{"textVersions":[
	{"date":null,"type":"Enrolled Bill","formats":[]},
	{"date":"2025-02-04T05:00:00Z","type":"Introduced in House","formats":[
		{"type":"Formatted XML","url":"https://www.congress.gov/119/bills/hjres35/BILLS-119hjres35ih.xml"}]}
],"pagination":{"count":2}}`

func TestSyncBillTextVersions_UpsertsAndLogsPrunes(t *testing.T) {
	store := &textVersionsStore{result: repository.TextVersionSyncResult{
		Updated: 2, Pruned: 1, PrunedCodes: []string{"rh"}, Duplicates: 1, RefetchCodes: []string{"ih"},
	}}
	svc := serviceWithAPI(t, store, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(textVersionsBody))
	})
	var logs bytes.Buffer
	svc.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if err := svc.syncBillTextVersions(t.Context(), "hjres-119-35", 119, "hjres", 35); err != nil {
		t.Fatalf("syncBillTextVersions: %v", err)
	}

	if store.billID != "hjres-119-35" || len(store.rows) != 2 {
		t.Fatalf("upsert got bill %q with %d rows, want hjres-119-35 with 2", store.billID, len(store.rows))
	}
	want := []wantVersion{{"enr", "", 2}, {"ih", "2025-02-04", 1}}
	if got := gotVersions(store.rows); !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	if got := store.rows[1].VersionType; got != "Introduced in House" {
		t.Errorf("row 1 type = %q, want Introduced in House", got)
	}
	out := logs.String()
	for _, want := range []string{"pruned text versions no longer listed", "version_codes=[rh]",
		"dropped text versions with duplicate codes", "text versions synced", "inserted=0 updated=2 pruned=1",
		`msg="queued corrected text versions for a refetch" bill_id=hjres-119-35 version_codes=[ih]`, "refetch=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q:\n%s", want, out)
		}
	}
}

// An upsert error is returned, not just logged, so the bill isn't marked synced (#233).
func TestSyncBillTextVersions_UpsertErrorIsReturned(t *testing.T) {
	store := &textVersionsStore{err: errors.New("spanner down")}
	svc := serviceWithAPI(t, store, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(textVersionsBody))
	})
	var logs bytes.Buffer
	svc.logger = slog.New(slog.NewTextHandler(&logs, nil))

	err := svc.syncBillTextVersions(t.Context(), "hjres-119-35", 119, "hjres", 35)

	if err == nil || !strings.Contains(err.Error(), "upsert text versions: spanner down") {
		t.Errorf("err = %v, want the upsert failure", err)
	}
	if out := logs.String(); strings.Contains(out, "pruned") {
		t.Errorf("logs = %s, want no counts after a failed upsert", out)
	}
}
