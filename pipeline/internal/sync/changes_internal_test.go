package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/apicache"
	"github.com/justabill-org/justabill/pipeline/internal/revalidate"
)

// recordingRevalidator records each Flush and whether its context was still live.
type recordingRevalidator struct {
	mu      gosync.Mutex
	flushes [][]string
	ctxErrs []error
}

func (r *recordingRevalidator) Flush(ctx context.Context, billIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushes = append(r.flushes, slices.Clone(billIDs))
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
}

func (r *recordingRevalidator) calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.flushes)
}

// checkFlushes fails unless rv saw exactly want, one Flush per entry.
func checkFlushes(t *testing.T, rv *recordingRevalidator, want ...[]string) {
	t.Helper()
	got := rv.calls()
	if !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Errorf("flushes = %v, want %v", got, want)
	}
}

func TestRunStep_FlushesTheRunsBillsOnce(t *testing.T) {
	rv := &recordingRevalidator{}
	s := newBackfillService(&stepStore{})
	s.revalidator = rv

	err := s.runStep(t.Context(), stepBills, 119, false, func(ctx context.Context) (int, error) {
		markBill(ctx, "s-119-2")
		markBill(ctx, "hr-119-1")
		markBill(ctx, "s-119-2")
		return 2, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checkFlushes(t, rv, []string{"hr-119-1", "s-119-2"})
}

func TestRunStep_NoMarksNoFlush(t *testing.T) {
	rv := &recordingRevalidator{}
	s := newBackfillService(&stepStore{})
	s.revalidator = rv

	if err := s.runStep(t.Context(), stepBills, 119, false, func(context.Context) (int, error) {
		return 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	checkFlushes(t, rv)
}

// A failed or cancelled run stored the bills it marked, so it flushes them too, on a context
// that the cancellation doesn't reach.
func TestRunStep_FlushesAfterAFailedRun(t *testing.T) {
	tests := []struct {
		name string
		run  func(ctx context.Context, cancel context.CancelFunc) error
	}{
		{"the step fails", func(context.Context, context.CancelFunc) error { return errors.New("list bills: 503") }},
		{"the run is cancelled", func(ctx context.Context, cancel context.CancelFunc) error {
			cancel()
			return ctx.Err()
		}},
		{"cancelled but returns nil", func(_ context.Context, cancel context.CancelFunc) error {
			cancel()
			return nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rv := &recordingRevalidator{}
			store := &stepStore{}
			s := newBackfillService(store)
			s.revalidator = rv
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			err := s.runStep(ctx, stepVotes, 119, false, func(ctx context.Context) (int, error) {
				markBill(ctx, "hr-119-1")
				return 1, tt.run(ctx, cancel)
			})
			if err == nil {
				t.Fatal("runStep succeeded, want the failure")
			}
			checkFlushes(t, rv, []string{"hr-119-1"})
			if rv.ctxErrs[0] != nil {
				t.Errorf("Flush ctx err = %v, want a live context", rv.ctxErrs[0])
			}
			if len(store.failures) != 1 {
				t.Errorf("failures = %+v, want the run recorded as failed", store.failures)
			}
		})
	}
}

// serve runs steps concurrently: each run flushes only what it marked. Run with -race.
func TestRunStep_ConcurrentRunsFlushTheirOwnBills(t *testing.T) {
	rv := &recordingRevalidator{}
	s := newBackfillService(&stepStore{})
	s.revalidator = rv
	const perRun = 50

	var wg gosync.WaitGroup
	for _, prefix := range []string{"hr", "s"} {
		wg.Go(func() {
			_ = s.runStep(t.Context(), prefix, 119, false, func(ctx context.Context) (int, error) {
				var marks gosync.WaitGroup
				for n := range perRun {
					marks.Go(func() { markBill(ctx, fmt.Sprintf("%s-119-%d", prefix, n+1)) })
				}
				marks.Wait()
				return perRun, nil
			})
		})
	}
	wg.Wait()

	got := rv.calls()
	if len(got) != 2 {
		t.Fatalf("flushes = %d, want 2", len(got))
	}
	for _, ids := range got {
		if len(ids) != perRun {
			t.Errorf("flush of %d bills, want %d", len(ids), perRun)
		}
		prefix, _, _ := strings.Cut(ids[0], "-")
		for _, id := range ids {
			if !strings.HasPrefix(id, prefix+"-") {
				t.Errorf("flush %v mixes runs", ids)
				break
			}
		}
	}
}

func TestSetRevalidator_NilLeavesTheHookOff(t *testing.T) {
	s := newBackfillService(&stepStore{})
	s.SetRevalidator(nil)
	if s.revalidator != nil {
		t.Fatalf("revalidator = %#v, want nil", s.revalidator)
	}
	err := s.runStep(t.Context(), stepBills, 119, false, func(ctx context.Context) (int, error) {
		markBill(ctx, "hr-119-1")
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// orderLog records, in order, which hook saw which bills: "clear" or "flush".
type orderLog struct {
	mu     gosync.Mutex
	events []string
}

func (o *orderLog) add(hook string, billIDs []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, hook+" "+strings.Join(billIDs, ","))
}

type orderedClearer struct{ log *orderLog }

func (c orderedClearer) Clear(_ context.Context, billIDs []string) { c.log.add("clear", billIDs) }

type orderedRevalidator struct{ log *orderLog }

func (r orderedRevalidator) Flush(_ context.Context, billIDs []string) { r.log.add("flush", billIDs) }

// The API's cached copies go before the web app re-renders from the API (#400).
func TestRunStep_ClearsTheAPICacheBeforeRevalidating(t *testing.T) {
	tests := []struct {
		name        string
		revalidator bool
		want        []string
	}{
		{"with the web hook", true, []string{"clear hr-119-1,s-119-5", "flush hr-119-1,s-119-5"}},
		{"without the web hook", false, []string{"clear hr-119-1,s-119-5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &orderLog{}
			s := newBackfillService(&stepStore{})
			s.apiCache = orderedClearer{log}
			if tt.revalidator {
				s.revalidator = orderedRevalidator{log}
			}
			err := s.runStep(t.Context(), stepBills, 119, false, func(ctx context.Context) (int, error) {
				markBill(ctx, "s-119-5")
				markBill(ctx, "hr-119-1")
				return 2, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(log.events, tt.want) {
				t.Errorf("hooks = %q, want %q", log.events, tt.want)
			}
		})
	}
}

// A Redis that's down costs a warning, never the step or the revalidation.
func TestRunStep_UnreachableRedisStillRevalidates(t *testing.T) {
	ac, err := apicache.New("redis://127.0.0.1:1", apicache.WithTimeout(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	rv := &recordingRevalidator{}
	s := newBackfillService(&stepStore{})
	s.SetAPICache(ac)
	s.revalidator = rv

	err = s.runStep(t.Context(), stepBills, 119, false, func(ctx context.Context) (int, error) {
		markBill(ctx, "hr-119-1")
		return 1, nil
	})
	if err != nil {
		t.Fatalf("runStep = %v, want success", err)
	}
	checkFlushes(t, rv, []string{"hr-119-1"})
}

func TestSetAPICache_NilLeavesItOff(t *testing.T) {
	s := newBackfillService(&stepStore{})
	s.SetAPICache(nil)
	if s.apiCache != nil {
		t.Fatalf("apiCache = %#v, want nil", s.apiCache)
	}
}

func TestMarkBill_OutsideAStepDoesNothing(t *testing.T) {
	markBill(t.Context(), "hr-119-1") // no change set: no panic
	ctx, cs := withChangeSet(t.Context())
	markBill(ctx, "")
	if got := cs.billIDs(); len(got) != 0 {
		t.Errorf("billIDs = %v, want none for an empty ID", got)
	}
}

// End to end: the bills step's synced bills reach the web route as bill tags, with the
// secret. HR 2 fails a sub-resource, so it isn't marked.
func TestSyncBills_RevalidatesTheSyncedBills(t *testing.T) {
	var (
		mu    gosync.Mutex
		auth  []string
		calls [][]string
	)
	web := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var body struct {
			Tags []string `json:"tags"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		calls = append(calls, body.Tags)
		mu.Unlock()
	}))
	t.Cleanup(web.Close)

	store := &billSyncStore{}
	s := newBillSyncService(t, store, &billAPI{n: 3, failPaths: map[string]bool{"/bill/119/hr/2/subjects": true}})
	s.SetRevalidator(revalidate.New(web.URL, "s3cret", revalidate.WithHTTPClient(web.Client())))

	if err := s.SyncBills(t.Context(), 119, 0); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if want := [][]string{{"bill:hr-119-1", "bill:hr-119-3"}}; !slices.EqualFunc(calls, want, slices.Equal[[]string]) {
		t.Errorf("web calls = %v, want %v", calls, want)
	}
	if len(auth) != 1 || auth[0] != "Bearer s3cret" {
		t.Errorf("Authorization = %v, want the bearer secret", auth)
	}
}

// The House fixtures are quorum calls, which name no bill; the Senate's session 1 fixture
// is on S. 5.
func TestSyncVotes_RevalidatesTheVotedBills(t *testing.T) {
	store := newVoteStore()
	s, _ := voteService(t, store, the119th())
	rv := &recordingRevalidator{}
	s.revalidator = rv

	if err := s.SyncVotes(t.Context(), 119, []int{1}); err != nil {
		t.Fatal(err)
	}
	checkFlushes(t, rv, []string{"s-119-5"})
}

func TestSyncSummaries_MarksTheSummarizedBills(t *testing.T) {
	items := queueItems(3)
	store := &summaryStore{items: items}
	sum := &fakeSummarizer{outcomes: map[string]ai.Outcome{items[1].BillID: ai.OutcomeBlocked}}
	s, _ := jobService(store, sum)
	ctx, cs := withChangeSet(t.Context())

	if _, err := s.syncSummaries(ctx, 119, 0); err != nil {
		t.Fatal(err)
	}
	// The blocked bill got no summary, so its page didn't change.
	if got := cs.billIDs(); !slices.Equal(got, []string{items[0].BillID, items[2].BillID}) {
		t.Errorf("marked %v, want the two summarized bills", got)
	}
}

// govinfoTextStore has an unfetched version for every bill and records the texts stored.
type govinfoTextStore struct {
	syncStateStore

	mu     gosync.Mutex
	stored []string
}

func (f *govinfoTextStore) FindUnfetchedVersion(_ context.Context, billID, versionCode string) (string, error) {
	return billID + "/" + versionCode, nil
}

func (f *govinfoTextStore) InsertBillText(_ context.Context, t repository.BillTextRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stored = append(f.stored, t.TextVersionID)
	return nil
}

func (f *govinfoTextStore) ReplaceBillLawRefs(context.Context, string, string, []repository.BillLawRefRow) error {
	return nil
}

// HR 1's package has a text link and is stored; HR 2's summary has no download, so nothing
// is stored for it and it isn't marked.
func TestSyncGovInfoChanges_RevalidatesTheBillsWithNewText(t *testing.T) {
	store := &govinfoTextStore{since: time.Now().Add(-time.Hour)}
	s := serviceWithGovInfo(t, store, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/packages/BILLS-119hr1ih/summary":
			fmt.Fprintf(w, `{"packageId": "BILLS-119hr1ih", "download": {"txtLink": "http://%s/text/hr1"}}`, r.Host)
		case "/packages/BILLS-119hr2ih/summary":
			fmt.Fprint(w, `{"packageId": "BILLS-119hr2ih"}`)
		case "/text/hr1":
			fmt.Fprint(w, "SEC. 1. SHORT TITLE.\nThis Act may be cited as the Test Act.")
		default:
			fmt.Fprint(w, `{"packages": [{"packageId": "BILLS-119hr1ih"}, {"packageId": "BILLS-119hr2ih"}]}`)
		}
	})
	rv := &recordingRevalidator{}
	s.revalidator = rv

	if err := s.SyncGovInfoChanges(t.Context(), 119); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(store.stored, []string{"hr-119-1/ih"}) {
		t.Fatalf("stored texts = %v, want HR 1's", store.stored)
	}
	checkFlushes(t, rv, []string{"hr-119-1"})
}
