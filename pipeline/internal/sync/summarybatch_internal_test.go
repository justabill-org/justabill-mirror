package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/ai/batchjsonl"
)

const (
	testBucket  = "ai-batch"
	testJobName = "projects/test-project/locations/global/batchPredictionJobs/123"
)

// batchStore is the summary store plus summary_batches, holds and releases in memory.
type batchStore struct {
	*summaryStore

	bmu      gosync.Mutex
	batches  map[string]repository.SummaryBatch
	updates  []repository.SummaryBatchUpdate
	holds    []repository.SummaryBatchHolds
	releases []string
	// released is what ReleaseSummaryBatch reports per call.
	released int
	loads    atomic.Int32
	// fail fails a method with its error: "count", "query", "create", "open", "hold", "release",
	// "update:<state>" for an update to that state, or "record:<bill>" for a bill's attempt.
	fail map[string]error
}

func newBatchStore(items []repository.SummaryQueueItem) *batchStore {
	return &batchStore{summaryStore: &summaryStore{items: items}, batches: map[string]repository.SummaryBatch{}}
}

func (f *batchStore) LoadBillContext(
	ctx context.Context, billID, versionID string,
) (*repository.SummaryBillContext, error) {
	f.loads.Add(1)
	return f.summaryStore.LoadBillContext(ctx, billID, versionID)
}

func (f *batchStore) CountBillsToSummarize(
	ctx context.Context, q repository.SummaryQueueQuery,
) (map[int]int, error) {
	if err := f.fail["count"]; err != nil {
		return nil, err
	}
	return f.summaryStore.CountBillsToSummarize(ctx, q)
}

func (f *batchStore) QueryBillsToSummarize(
	ctx context.Context, q repository.SummaryQueueQuery,
) ([]repository.SummaryQueueItem, error) {
	if err := f.fail["query"]; err != nil {
		return nil, err
	}
	return f.summaryStore.QueryBillsToSummarize(ctx, q)
}

func (f *batchStore) CreateSummaryBatch(_ context.Context, b repository.SummaryBatch) error {
	f.bmu.Lock()
	defer f.bmu.Unlock()
	if err := f.fail["create"]; err != nil {
		return err
	}
	if _, ok := f.batches[b.BatchID]; ok {
		return errors.New("batch exists")
	}
	f.batches[b.BatchID] = b
	return nil
}

func (f *batchStore) UpdateSummaryBatch(_ context.Context, u repository.SummaryBatchUpdate) error {
	f.bmu.Lock()
	defer f.bmu.Unlock()
	if err := f.fail["update:"+u.State]; err != nil {
		return err
	}
	b, ok := f.batches[u.BatchID]
	if !ok {
		return repository.ErrNotFound
	}
	f.updates = append(f.updates, u)
	b.State = u.State
	if u.JobName != "" {
		b.JobName = u.JobName
	}
	if u.OutputURI != "" {
		b.OutputURI = u.OutputURI
	}
	b.OKCount, b.FailedCount = cmpOr(u.OKCount, b.OKCount), cmpOr(u.FailedCount, b.FailedCount)
	b.FinishedAt, b.ImportedAt = cmpOr(u.FinishedAt, b.FinishedAt), cmpOr(u.ImportedAt, b.ImportedAt)
	f.batches[u.BatchID] = b
	return nil
}

func cmpOr[T any](p, q *T) *T {
	if p != nil {
		return p
	}
	return q
}

func (f *batchStore) OpenSummaryBatches(context.Context) ([]repository.SummaryBatch, error) {
	f.bmu.Lock()
	defer f.bmu.Unlock()
	if err := f.fail["open"]; err != nil {
		return nil, err
	}
	var open []repository.SummaryBatch
	for _, b := range f.batches {
		if b.State != repository.SummaryBatchImported && b.State != repository.SummaryBatchReleased {
			open = append(open, b)
		}
	}
	slices.SortFunc(open, func(a, b repository.SummaryBatch) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return open, nil
}

func (f *batchStore) HoldSummaryBatch(_ context.Context, h repository.SummaryBatchHolds) error {
	f.bmu.Lock()
	defer f.bmu.Unlock()
	if err := f.fail["hold"]; err != nil {
		return err
	}
	f.holds = append(f.holds, h)
	return nil
}

func (f *batchStore) RecordSummaryAttempt(ctx context.Context, a repository.SummaryAttemptRow) error {
	if err := f.fail["record:"+a.BillID]; err != nil {
		return err
	}
	return f.summaryStore.RecordSummaryAttempt(ctx, a)
}

func (f *batchStore) ReleaseSummaryBatch(_ context.Context, batchID string, _ time.Time) (int, error) {
	f.bmu.Lock()
	defer f.bmu.Unlock()
	if err := f.fail["release"]; err != nil {
		return 0, err
	}
	f.releases = append(f.releases, batchID)
	return f.released, nil
}

func (f *batchStore) batch(t *testing.T, id string) repository.SummaryBatch {
	t.Helper()
	f.bmu.Lock()
	defer f.bmu.Unlock()
	b, ok := f.batches[id]
	if !ok {
		t.Fatalf("no batch %s in %v", id, slices.Collect(maps.Keys(f.batches)))
	}
	return b
}

// memFiles is a file store in memory. An object exists once its writer is closed.
type memFiles struct {
	mu      gosync.Mutex
	objects map[string][]byte
	// createErr fails Create, and writeErr every Write, for URIs ending with its key.
	createErr map[string]error
	writeErr  map[string]error
}

func newMemFiles() *memFiles { return &memFiles{objects: map[string][]byte{}} }

// memWriter stores its object on Close unless its context is done, as a GCS upload is aborted.
type memWriter struct {
	bytes.Buffer

	ctx      context.Context //nolint:containedctx // an upload's lifetime, as storage.Writer's
	f        *memFiles
	uri      string
	writeErr error
}

func (w *memWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.Buffer.Write(p)
}

func (w *memWriter) Close() error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	w.f.objects[w.uri] = w.Bytes()
	return nil
}

func (f *memFiles) Create(ctx context.Context, uri string) (io.WriteCloser, error) {
	for suffix, err := range f.createErr {
		if strings.HasSuffix(uri, suffix) {
			return nil, err
		}
	}
	w := &memWriter{ctx: ctx, f: f, uri: uri}
	for suffix, err := range f.writeErr {
		if strings.HasSuffix(uri, suffix) {
			w.writeErr = err
		}
	}
	return w, nil
}

func (f *memFiles) Open(_ context.Context, uri string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[uri]
	if !ok {
		return nil, errors.New("no object " + uri)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *memFiles) List(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var uris []string
	for uri := range f.objects {
		if strings.HasPrefix(uri, prefix) {
			uris = append(uris, uri)
		}
	}
	slices.Sort(uris)
	return uris, nil
}

func (f *memFiles) lines(t *testing.T, uri string) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[uri]
	if !ok {
		t.Fatalf("no object %s in %v", uri, slices.Collect(maps.Keys(f.objects)))
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// fakeBatchAPI is Vertex AI's batch prediction job API: create answers with testJobName, get with
// the job's state and output directory, and cancel with an empty body.
type fakeBatchAPI struct {
	mu        gosync.Mutex
	state     genai.JobState
	outputDir string
	createErr bool
	// noName answers create without a job name; cancelErr fails cancel.
	noName    bool
	cancelErr bool
	requests  []string
	created   map[string]any
}

func (f *fakeBatchAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/batchPredictionJobs"):
		if f.createErr {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		if f.noName {
			_, _ = io.WriteString(w, `{"state":"JOB_STATE_PENDING"}`)
			return
		}
		_, _ = io.WriteString(w, `{"name":"`+testJobName+`","state":"JOB_STATE_PENDING"}`)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":cancel"):
		if f.cancelErr {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/batchPredictionJobs/123"):
		job := map[string]any{"name": testJobName, "state": string(f.state)}
		if f.outputDir != "" {
			job["outputInfo"] = map[string]any{"gcsOutputDirectory": f.outputDir}
		}
		_ = json.NewEncoder(w).Encode(job)
	default:
		http.Error(w, `{"error":{"code":404,"message":"unexpected"}}`, http.StatusNotFound)
	}
}

func (f *fakeBatchAPI) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// newBatchJobs is a real genai batch client on api.
func newBatchJobs(t *testing.T, api *fakeBatchAPI) *genai.Batches {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	client, err := genai.NewClient(t.Context(), &genai.ClientConfig{
		Project: "test-project", Location: "global", Backend: genai.BackendVertexAI,
		HTTPClient: srv.Client(), HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client.Batches
}

// batchService is a Service on store with the batch path on (minimum minBills), the fake API and
// an in-memory file store.
func batchService(
	t *testing.T, store *batchStore, api *fakeBatchAPI, minBills int,
) (*Service, *memFiles, *bytes.Buffer) {
	t.Helper()
	s, logs := jobService(store.summaryStore, nil)
	s.store = store
	files := newMemFiles()
	s.SetSummaryBatches(SummaryBatchConfig{
		Bucket: testBucket, Location: "global", Hold: DefaultSummaryBatchHold, MinBills: minBills,
	}, ai.Config{Model: ai.DefaultModel}, newBatchJobs(t, api), files)
	return s, files, logs
}

// tieredItems is n bills in each tier, hr-119-<tier><i>.
func tieredItems(n map[int]int) []repository.SummaryQueueItem {
	var items []repository.SummaryQueueItem
	for tier := range 4 {
		for i := range n[tier] {
			id := "hr-119-" + strconv.Itoa(tier) + strconv.Itoa(i)
			items = append(items, repository.SummaryQueueItem{
				BillID: id, VersionID: "v-" + id, VersionCode: "ih", ContentHash: "queued-" + id, Tier: tier,
			})
		}
	}
	return items
}

func TestSummaryBatchConfigFrom(t *testing.T) {
	cfg, err := SummaryBatchConfigFrom(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	want := SummaryBatchConfig{Location: "global", Hold: 100 * time.Hour, MinBills: 1000}
	if cfg != want {
		t.Errorf("defaults = %+v, want %+v", cfg, want)
	}
	env := map[string]string{
		"ai_batch_bucket": " example-ai-batch ", "ai_batch_location": "us-central1",
		"ai_batch_hold": "30h", "ai_batch_min_bills": "50",
	}
	if cfg, err = SummaryBatchConfigFrom(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	want = SummaryBatchConfig{Bucket: "example-ai-batch", Location: "us-central1", Hold: 30 * time.Hour, MinBills: 50}
	if cfg != want {
		t.Errorf("config = %+v, want %+v", cfg, want)
	}
	for key, bad := range map[string]string{
		"ai_batch_bucket": "gs://example/x", "ai_batch_hold": "4 days", "ai_batch_min_bills": "0",
	} {
		_, err = SummaryBatchConfigFrom(func(k string) string {
			if k == key {
				return bad
			}
			return ""
		})
		if err == nil || !strings.Contains(err.Error(), strings.ToUpper(key)) {
			t.Errorf("%s=%q: err = %v, want one naming the variable", key, bad, err)
		}
	}
}

func TestParseSummaryTiers(t *testing.T) {
	tiers, err := ParseSummaryTiers("prompt, Other,prompt")
	if err != nil || !slices.Equal(tiers, []int{repository.SummaryTierOther, repository.SummaryTierPromptChange}) {
		t.Errorf("tiers = %v, %v", tiers, err)
	}
	if _, err = ParseSummaryTiers("other,old"); err == nil {
		t.Error("an unknown tier parsed")
	}
}

// A dry run counts the due bills of the chosen tiers, leaves the others out, and prices them from
// their requests, without writing a file, a batch or a hold, and needs no bucket.
func TestSubmitSummaryBatch_DryRun(t *testing.T) {
	store := newBatchStore(tieredItems(map[int]int{0: 2, 1: 1, 2: 3, 3: 1}))
	store.backlog = map[int]int{0: 2, 1: 1, 2: 3, 3: 1}
	s, _ := jobService(store.summaryStore, nil)
	s.store = store
	s.SetSummaryBatches(SummaryBatchConfig{}, ai.Config{Model: "gemini-test"}, nil, nil)

	plan, err := s.SubmitSummaryBatch(t.Context(), SummaryBatchRequest{
		Congress: 119, Tiers: []int{2, 3}, Max: 3, DryRun: true, InputPrice: 1000, OutputPrice: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(plan.Due, map[int]int{2: 3}) || plan.Bills != 3 || plan.BatchID != "" {
		t.Errorf("plan = %+v, want the 3 bills of tier 2 (max 3)", plan)
	}
	if plan.InputTokens < 1000 || plan.OutputTokens != 3*DefaultBatchOutputTokens {
		t.Errorf("tokens = %d in, %d out", plan.InputTokens, plan.OutputTokens)
	}
	wantCost := (float64(plan.InputTokens)*1000 + float64(plan.OutputTokens)*2000) / 1e6
	if plan.Cost != wantCost {
		t.Errorf("cost = %v, want %v", plan.Cost, wantCost)
	}
	q := store.queries[0]
	if q.Limit != 3+3 || !q.ResummarizeOnPromptChange || q.Model != "gemini-test" ||
		q.PromptVersion != ai.PromptVersionBill {
		t.Errorf("query = %+v, want limit 6 (the 3 tier 0-1 bills and max) with prompt changes", q)
	}
	if len(store.batches) != 0 || len(store.holds) != 0 {
		t.Errorf("a dry run wrote batches %v or holds %v", store.batches, store.holds)
	}
}

func TestSubmitSummaryBatch_RefusesWithoutABucket(t *testing.T) {
	s, _ := jobService(&summaryStore{}, nil)
	_, err := s.SubmitSummaryBatch(t.Context(), SummaryBatchRequest{Congress: 119, Tiers: []int{2}, Max: 10})
	if !errors.Is(err, ErrBatchNotConfigured) {
		t.Errorf("err = %v, want ErrBatchNotConfigured", err)
	}
	s.SetSummaryBatches(SummaryBatchConfig{}, ai.Config{}, nil, nil)
	if err = s.PollSummaryBatches(t.Context(), 0); err != nil {
		t.Errorf("poll without a bucket = %v, want nothing done", err)
	}
}

func TestSubmitSummaryBatch_RefusesTooFewBills(t *testing.T) {
	store := newBatchStore(tieredItems(map[int]int{2: 3}))
	api := &fakeBatchAPI{}
	s, files, _ := batchService(t, store, api, 4)

	_, err := s.SubmitSummaryBatch(t.Context(), SummaryBatchRequest{Congress: 119, Tiers: []int{2}, Max: 10})
	if !errors.Is(err, ErrBatchTooSmall) || !strings.Contains(err.Error(), "3 due") {
		t.Errorf("err = %v, want ErrBatchTooSmall with the count", err)
	}
	if len(files.objects) != 0 || len(store.batches) != 0 || len(api.calls()) != 0 {
		t.Error("a refused submit exported something")
	}
}

// Submit streams the input and manifest, records the batch, holds each exported bill for the hash
// its request was built from, and creates the job on the batch's files.
func TestSubmitSummaryBatch_Exports(t *testing.T) {
	store := newBatchStore(tieredItems(map[int]int{2: 3}))
	store.loadErr = map[string]error{"hr-119-21": errors.New("no text")}
	store.crs = map[string]*repository.SummaryCRSContext{
		"hr-119-20": {ActionDesc: "Introduced in House", Text: "CRS text of hr-119-20.", ContentHash: "crs-hr-119-20"},
	}
	store.rules = map[string]*repository.SummaryRuleContext{"hr-119-22": overdraftRule()}
	api := &fakeBatchAPI{}
	s, files, logs := batchService(t, store, api, 2)

	plan, err := s.SubmitSummaryBatch(t.Context(), SummaryBatchRequest{Congress: 119, Tiers: []int{2}, Max: 10})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Bills != 2 || plan.Skipped != 1 || plan.JobName != testJobName || len(plan.BatchID) != 26 {
		t.Fatalf("plan = %+v", plan)
	}
	prefix := "gs://" + testBucket + "/summaries/" + plan.BatchID + "/"
	if n := len(files.lines(t, prefix+"input.jsonl")); n != 2 {
		t.Errorf("input has %d lines, want 2", n)
	}
	manifestLines := files.lines(t, prefix+"manifest.jsonl")
	im, err := batchjsonl.ReadManifest(strings.NewReader(strings.Join(manifestLines, "\n")))
	if err != nil || im.Len() != 2 {
		t.Fatalf("manifest: %d items, %v", im.Len(), err)
	}
	checkBatchCRS(t, store, manifestLines, files.lines(t, prefix+"input.jsonl"))
	checkBatchRule(t, store, manifestLines, files.lines(t, prefix+"input.jsonl"))

	b := store.batch(t, plan.BatchID)
	if b.State != string(genai.JobStatePending) || b.JobName != testJobName || b.BillCount != 2 ||
		b.InputURI != prefix+"input.jsonl" || b.OutputURI != prefix+"output/" || b.Model != ai.DefaultModel {
		t.Errorf("batch = %+v", b)
	}
	if len(store.holds) != 1 {
		t.Fatalf("holds = %+v, want one", store.holds)
	}
	h := store.holds[0]
	if h.BatchID != plan.BatchID || !h.Until.Equal(jobNow().Add(DefaultSummaryBatchHold)) || len(h.Bills) != 2 {
		t.Errorf("hold = %+v", h)
	}
	for _, bill := range h.Bills {
		if bill.ContentHash != "hash-"+bill.BillID {
			t.Errorf("held %s for hash %q, want the loaded text's", bill.BillID, bill.ContentHash)
		}
	}

	created, _ := json.Marshal(api.created)
	for _, want := range []string{prefix + "input.jsonl", prefix + "output/", ai.DefaultModel, "jsonl"} {
		if !strings.Contains(string(created), want) {
			t.Errorf("create request %s lacks %q", created, want)
		}
	}
	states := []string{}
	for _, l := range logLines(t, logs, "summary_batch") {
		states = append(states, l["state"].(string))
	}
	if !slices.Equal(states, []string{"exporting", "JOB_STATE_PENDING"}) {
		t.Errorf("summary_batch states = %v", states)
	}
}

// checkBatchCRS checks that hr-119-20's request carries its CRS summary, its manifest line the
// summary's hash (which the import stores), and the queue was queried with the CRS context.
func checkBatchCRS(t *testing.T, store *batchStore, manifestLines, inputLines []string) {
	t.Helper()
	for _, l := range manifestLines {
		withCRS := strings.Contains(l, `"bill_id":"hr-119-20"`)
		if strings.Contains(l, `"crs_hash":"crs-hr-119-20"`) != withCRS {
			t.Errorf("manifest line %s: crs_hash wrong for CRS context %v", l, withCRS)
		}
	}
	if input := strings.Join(inputLines, "\n"); !strings.Contains(input, "CRS text of hr-119-20.") {
		t.Errorf("input lacks the CRS summary: %s", input)
	}
	if len(store.queries) == 0 {
		t.Error("no queue query")
	}
	for _, q := range store.queries {
		if !q.CRSContext {
			t.Errorf("queue query %+v without the CRS context", q)
		}
	}
}

// checkBatchRule checks that hr-119-22's request carries its disapproved rule, its manifest line
// the rule's context hash (which the import stores), and the queue was queried with the rule
// context.
func checkBatchRule(t *testing.T, store *batchStore, manifestLines, inputLines []string) {
	t.Helper()
	for _, l := range manifestLines {
		withRule := strings.Contains(l, `"bill_id":"hr-119-22"`)
		if strings.Contains(l, `"rule_hash":"rule-hash-1"`) != withRule {
			t.Errorf("manifest line %s: rule_hash wrong for rule context %v", l, withRule)
		}
	}
	if input := strings.Join(inputLines, "\n"); !strings.Contains(input, "The CFPB amends Regulation Z.") {
		t.Errorf("input lacks the rule's abstract: %s", input)
	}
	for _, q := range store.queries {
		if !q.RuleContext {
			t.Errorf("queue query %+v without the rule context", q)
		}
	}
}

// A job that can't be created releases the holds and closes the batch.
func TestSubmitSummaryBatch_CreateFailsReleases(t *testing.T) {
	store := newBatchStore(tieredItems(map[int]int{2: 2}))
	s, _, _ := batchService(t, store, &fakeBatchAPI{createErr: true}, 1)

	plan, err := s.SubmitSummaryBatch(t.Context(), SummaryBatchRequest{Congress: 119, Tiers: []int{2}, Max: 10})
	if err == nil || !strings.Contains(err.Error(), "create batch job") {
		t.Fatalf("err = %v, want the create error", err)
	}
	if plan != nil {
		t.Errorf("plan = %+v, want none", plan)
	}
	if len(store.releases) != 1 {
		t.Errorf("releases = %v, want the batch's", store.releases)
	}
	for _, b := range store.batches {
		if b.State != repository.SummaryBatchReleased {
			t.Errorf("batch state = %s, want released", b.State)
		}
	}
}

// A write that fails stops the export: the other bills aren't loaded, and neither file is stored.
func TestSubmitSummaryBatch_WriteFailureStopsTheExport(t *testing.T) {
	store := newBatchStore(tieredItems(map[int]int{2: 9}))
	store.items = append(store.items, tieredItems(map[int]int{3: 9})...)
	for range 4 {
		store.items = append(store.items, store.items...)
	}
	s, files, _ := batchService(t, store, &fakeBatchAPI{}, 1)
	files.writeErr = map[string]error{"input.jsonl": errors.New("upload failed")}

	_, err := s.SubmitSummaryBatch(t.Context(), SummaryBatchRequest{Congress: 119, Tiers: []int{2, 3}, Max: 1000})
	if err == nil || !strings.Contains(err.Error(), "upload failed") {
		t.Fatalf("err = %v, want the write error", err)
	}
	if n := store.loads.Load(); n > 2*DefaultSummaryWorkers {
		t.Errorf("%d of %d bills loaded after the first write failed", n, len(store.items))
	}
	if len(files.objects) != 0 || len(store.batches) != 0 || len(store.holds) != 0 {
		t.Errorf("a failed export stored files %v, batches or holds", slices.Collect(maps.Keys(files.objects)))
	}
}

func TestSubmitSummaryBatch_ExportFails(t *testing.T) {
	store := newBatchStore(tieredItems(map[int]int{2: 2}))
	s, files, _ := batchService(t, store, &fakeBatchAPI{}, 1)
	files.createErr = map[string]error{"manifest.jsonl": errors.New("denied")}

	if _, err := s.SubmitSummaryBatch(
		t.Context(),
		SummaryBatchRequest{Congress: 119, Tiers: []int{2}, Max: 5},
	); err == nil {
		t.Fatal("submit succeeded without a manifest")
	}
	if len(store.batches) != 0 || len(store.holds) != 0 {
		t.Error("a failed export recorded a batch or holds")
	}
}

// openBatch stores a batch created at created with job (none if empty), exported from three
// bills, and writes its manifest.
func openBatch(t *testing.T, store *batchStore, files *memFiles, job, state string, created time.Time) {
	t.Helper()
	prefix := "gs://" + testBucket + "/summaries/b1/"
	store.batches["b1"] = repository.SummaryBatch{
		BatchID: "b1", Congress: 119, Model: ai.DefaultModel, PromptVersion: ai.PromptVersionBill,
		JobName: job, State: state, BillCount: 3, InputURI: prefix + "input.jsonl",
		OutputURI: prefix + "output/", CreatedAt: created,
	}
	var input, manifest bytes.Buffer
	enc := batchjsonl.NewEncoder(&input, &manifest)
	for i := 1; i <= 3; i++ {
		id := "hr-119-" + strconv.Itoa(i)
		req, err := ai.Config{}.BillRequest(ai.BillContext{BillID: id, Title: "T", Text: "SEC. 1. Text."})
		if err != nil {
			t.Fatal(err)
		}
		bill := batchjsonl.Bill{
			BillID: id, VersionID: "v-" + id, VersionCode: "ih", ContentHash: "hash-" + id, CRSHash: "crs-" + id,
			RuleHash: "rule-" + id,
		}
		if _, err = enc.Encode(bill, req); err != nil {
			t.Fatal(err)
		}
	}
	files.objects[prefix+"input.jsonl"], files.objects[prefix+"manifest.jsonl"] = input.Bytes(), manifest.Bytes()
}

// outputLine is a batch output line for input line n with a response, or a status when it failed.
func outputLine(n int, response, status string) string {
	line := map[string]any{"request": map[string]any{"labels": map[string]string{"jab_line": strconv.Itoa(n)}}}
	if status != "" {
		line["status"] = status
	} else {
		line["response"] = json.RawMessage(response)
	}
	b, _ := json.Marshal(line)
	return string(b)
}

func TestPollSummaryBatches_StoresStateChanges(t *testing.T) {
	store := newBatchStore(nil)
	api := &fakeBatchAPI{state: genai.JobStateRunning}
	s, files, logs := batchService(t, store, api, 1)
	openBatch(t, store, files, testJobName, string(genai.JobStatePending), jobNow().Add(-time.Hour))

	for range 2 {
		if err := s.PollSummaryBatches(t.Context(), 119); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.updates) != 1 || store.updates[0].State != string(genai.JobStateRunning) ||
		store.updates[0].FinishedAt != nil {
		t.Errorf("updates = %+v, want one to running", store.updates)
	}
	lines := logLines(t, logs, "summary_batch")
	if len(lines) != 1 || lines[0]["age"] != "1h0m0s" || lines[0]["bill_count"] != float64(3) {
		t.Errorf("summary_batch lines = %v", lines)
	}
	// Another congress's run leaves the batch alone.
	if err := s.PollSummaryBatches(t.Context(), 118); err != nil || len(api.calls()) != 2 {
		t.Errorf("poll of the 118th = %v, calls %v", err, api.calls())
	}
}

// A finished job's lines go through the synchronous classification: an ok answer stores its
// summary, a line Vertex AI failed is an error attempt due now, and the bill with no line is
// released with the rest of the batch's holds.
func TestPollSummaryBatches_Imports(t *testing.T) {
	store := newBatchStore(nil)
	store.released = 1
	api := &fakeBatchAPI{
		state:     genai.JobStatePartiallySucceeded,
		outputDir: "gs://" + testBucket + "/summaries/b1/output/prediction-1",
	}
	s, files, logs := batchService(t, store, api, 1)
	openBatch(t, store, files, testJobName, string(genai.JobStateRunning), jobNow().Add(-20*time.Hour))
	ok := answer(`{"short_summary":"Short.","long_summary":"Long.","who_it_affects":"Farmers."}`, "STOP")
	files.objects[api.outputDir+"/predictions.jsonl"] = []byte(
		outputLine(2, "", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`) +
			"\n" + outputLine(1, ok, "") + "\n",
	)
	files.objects[api.outputDir+"/errors.txt"] = []byte("not jsonl")

	if err := s.PollSummaryBatches(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	got := store.attemptsByBill()
	if len(got) != 2 {
		t.Fatalf("attempts = %+v, want lines 1 and 2", store.attempts)
	}
	first := got["hr-119-1"]
	if first.Outcome != "ok" || first.Summary == nil || first.Summary.WhoItAffects != "Farmers." ||
		first.Summary.SourceVersionID != "v-hr-119-1" || first.Summary.SourceContentHash != "hash-hr-119-1" ||
		first.Summary.SourceCRSHash != "crs-hr-119-1" || first.Summary.SourceRuleHash != "rule-hr-119-1" ||
		first.RequestType != ai.RequestTypeBatch || first.BatchID != "b1" || first.RetryAt != nil {
		t.Errorf("line 1 attempt = %+v", first)
	}
	second := got["hr-119-2"]
	if second.Outcome != "error" || second.Summary != nil || second.RetryAt == nil || !second.RetryAt.Equal(jobNow()) ||
		second.Reason != "http 429 RESOURCE_EXHAUSTED" || second.ContentHash != "hash-hr-119-2" {
		t.Errorf("line 2 attempt = %+v", second)
	}
	b := store.batch(t, "b1")
	if b.State != repository.SummaryBatchImported || *b.OKCount != 1 || *b.FailedCount != 2 || b.ImportedAt == nil ||
		b.OutputURI != api.outputDir {
		t.Errorf("batch = %+v", b)
	}
	if !slices.Equal(store.releases, []string{"b1"}) {
		t.Errorf("releases = %v, want b1's missing bill", store.releases)
	}
	lines := logLines(t, logs, "summary_batch")
	if len(lines) != 2 || lines[1]["state"] != "imported" || lines[1]["released"] != float64(1) {
		t.Errorf("summary_batch lines = %v", lines)
	}
	if results := logLines(t, logs, "summary_result"); len(results) != 2 || results[0]["request_type"] != "batch" {
		t.Errorf("summary_result lines = %v", results)
	}
}

// A line for a bill the batch no longer holds for that text is skipped as stale: it counts as
// neither ok nor failed, and the import goes on (#650).
func TestPollSummaryBatches_SkipsStaleLines(t *testing.T) {
	store := newBatchStore(nil)
	store.fail = map[string]error{"record:hr-119-1": fmt.Errorf("hr-119-1: %w", repository.ErrStaleBatchAttempt)}
	api := &fakeBatchAPI{state: genai.JobStateSucceeded}
	s, files, logs := batchService(t, store, api, 1)
	openBatch(t, store, files, testJobName, string(genai.JobStateSucceeded), jobNow().Add(-time.Hour))
	ok := answer(`{"short_summary":"S.","long_summary":"L.","who_it_affects":"W."}`, "STOP")
	files.objects["gs://"+testBucket+"/summaries/b1/output/p/predictions.jsonl"] = []byte(
		outputLine(1, ok, "") + "\n" + outputLine(2, ok, "") + "\n" + outputLine(3, ok, "") + "\n",
	)

	if err := s.PollSummaryBatches(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if got := store.attemptsByBill(); len(got) != 2 || got["hr-119-1"].BillID != "" {
		t.Errorf("attempts = %+v, want H.R. 2 and 3 only", store.attempts)
	}
	b := store.batch(t, "b1")
	if b.State != repository.SummaryBatchImported || *b.OKCount != 2 || *b.FailedCount != 0 {
		t.Errorf("batch = %+v, want imported with 2 ok and none failed", b)
	}
	lines := logLines(t, logs, "summary_batch")
	if len(lines) != 1 || lines[0]["stale"] != float64(1) {
		t.Errorf("summary_batch lines = %v, want one with stale 1", lines)
	}
	stale := 0
	for _, r := range logLines(t, logs, "summary_result") {
		if r["stale"] == true {
			stale++
			if r["bill_id"] != "hr-119-1" {
				t.Errorf("stale summary_result = %v, want H.R. 1's", r)
			}
		}
	}
	if stale != 1 {
		t.Errorf("stale summary_result lines = %d, want 1", stale)
	}
}

// An import whose writes fail leaves the batch open, to be imported again.
func TestPollSummaryBatches_ImportStoreErrorKeepsTheBatchOpen(t *testing.T) {
	store := newBatchStore(nil)
	store.recordErr = errors.New("spanner down")
	api := &fakeBatchAPI{state: genai.JobStateSucceeded}
	s, files, _ := batchService(t, store, api, 1)
	openBatch(t, store, files, testJobName, string(genai.JobStateSucceeded), jobNow().Add(-time.Hour))
	ok := answer(`{"short_summary":"S.","long_summary":"L.","who_it_affects":"W."}`, "STOP")
	files.objects["gs://"+testBucket+"/summaries/b1/output/p/predictions.jsonl"] = []byte(outputLine(1, ok, ""))

	if err := s.PollSummaryBatches(
		t.Context(),
		0,
	); err == nil ||
		!strings.Contains(err.Error(), "imports the batch again") {
		t.Errorf("err = %v", err)
	}
	if b := store.batch(t, "b1"); b.State != string(genai.JobStateSucceeded) || len(store.releases) != 0 {
		t.Errorf("batch = %+v, releases %v; want it left to import again", b, store.releases)
	}
}

func TestPollSummaryBatches_ReleasesFailedJobs(t *testing.T) {
	for _, state := range []genai.JobState{genai.JobStateFailed, genai.JobStateCancelled, genai.JobStateExpired} {
		t.Run(string(state), func(t *testing.T) {
			store := newBatchStore(nil)
			s, files, logs := batchService(t, store, &fakeBatchAPI{state: state}, 1)
			openBatch(t, store, files, testJobName, string(genai.JobStateRunning), jobNow().Add(-time.Hour))

			if err := s.PollSummaryBatches(t.Context(), 0); err != nil {
				t.Fatal(err)
			}
			b := store.batch(t, "b1")
			if b.State != repository.SummaryBatchReleased || b.FinishedAt == nil || len(store.releases) != 1 {
				t.Errorf("batch = %+v, releases %v", b, store.releases)
			}
			if lines := logLines(t, logs, "summary_batch"); len(lines) != 2 || lines[1]["state"] != "released" {
				t.Errorf("summary_batch lines = %v", lines)
			}
		})
	}
}

// A job still open after AI_BATCH_HOLD is cancelled, then its bills are released.
func TestPollSummaryBatches_CancelsAfterTheHold(t *testing.T) {
	store := newBatchStore(nil)
	api := &fakeBatchAPI{state: genai.JobStateRunning}
	s, files, _ := batchService(t, store, api, 1)
	openBatch(t, store, files, testJobName, string(genai.JobStateRunning), jobNow().Add(-DefaultSummaryBatchHold))

	if err := s.PollSummaryBatches(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	calls := api.calls()
	if len(calls) != 2 || !strings.HasSuffix(calls[1], "/batchPredictionJobs/123:cancel") {
		t.Errorf("calls = %v, want a get then a cancel", calls)
	}
	if b := store.batch(t, "b1"); b.State != repository.SummaryBatchReleased {
		t.Errorf("state = %s, want released", b.State)
	}
}

// A submit that stopped before creating its job leaves an exporting batch, released after the
// export grace.
func TestPollSummaryBatches_ReleasesAnAbandonedExport(t *testing.T) {
	store := newBatchStore(nil)
	api := &fakeBatchAPI{}
	s, files, _ := batchService(t, store, api, 1)
	openBatch(t, store, files, "", repository.SummaryBatchExporting, jobNow().Add(-time.Hour))

	if err := s.PollSummaryBatches(t.Context(), 0); err != nil || len(store.releases) != 0 {
		t.Fatalf("young export: err %v, releases %v; want it left alone", err, store.releases)
	}
	store.batches["b1"] = func(b repository.SummaryBatch) repository.SummaryBatch {
		b.CreatedAt = jobNow().Add(-batchExportGrace)
		return b
	}(store.batches["b1"])
	if err := s.PollSummaryBatches(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if b := store.batch(t, "b1"); b.State != repository.SummaryBatchReleased || len(api.calls()) != 0 {
		t.Errorf("batch = %+v, calls %v", b, api.calls())
	}
}

// sync-summaries polls the congress's batches before its synchronous pass, and a failed poll
// fails the step without holding up the pass.
func TestSyncSummaries_PollsBatchesFirst(t *testing.T) {
	store := newBatchStore(queueItems(1))
	store.summaryStore.backlog = map[int]int{}
	api := &fakeBatchAPI{state: genai.JobStateRunning}
	s, files, _ := batchService(t, store, api, 1)
	s.summarizer = &fakeSummarizer{}
	openBatch(t, store, files, testJobName, string(genai.JobStateRunning), jobNow().Add(-time.Hour))
	store.batches["b2"] = repository.SummaryBatch{
		BatchID:   "b2",
		Congress:  119,
		JobName:   strings.Replace(testJobName, "123", "404", 1),
		State:     "JOB_STATE_RUNNING",
		CreatedAt: jobNow(),
	}

	n, err := s.syncSummariesStep(t.Context(), 119, 0)
	if err == nil || !strings.Contains(err.Error(), "summary batch b2") {
		t.Errorf("err = %v, want b2's failed get", err)
	}
	if n != 1 || len(store.attempts) != 1 {
		t.Errorf("summarized %d, attempts %d; want the synchronous pass to run", n, len(store.attempts))
	}
	if calls := api.calls(); len(calls) != 2 {
		t.Errorf("calls = %v, want both batches polled", calls)
	}
}

// An import that still fails after the hold gives up and releases the batch.
func TestPollSummaryBatches_ReleasesAnImportPastItsHold(t *testing.T) {
	store := newBatchStore(nil)
	s, files, _ := batchService(t, store, &fakeBatchAPI{state: genai.JobStateSucceeded}, 1)
	openBatch(t, store, files, testJobName, string(genai.JobStateSucceeded), jobNow().Add(-time.Hour))
	delete(files.objects, "gs://"+testBucket+"/summaries/b1/manifest.jsonl")

	if err := s.PollSummaryBatches(t.Context(), 0); err == nil {
		t.Fatal("import without a manifest succeeded")
	}
	if b := store.batch(t, "b1"); b.State != string(genai.JobStateSucceeded) {
		t.Errorf("state = %s, want it left to import again within the hold", b.State)
	}
	store.batches["b1"] = func(b repository.SummaryBatch) repository.SummaryBatch {
		b.CreatedAt = jobNow().Add(-DefaultSummaryBatchHold)
		return b
	}(store.batches["b1"])
	if err := s.PollSummaryBatches(t.Context(), 0); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Errorf("err = %v, want the manifest error", err)
	}
	if b := store.batch(t, "b1"); b.State != repository.SummaryBatchReleased {
		t.Errorf("state = %s, want released", b.State)
	}
}
