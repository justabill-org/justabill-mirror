package sync

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/ai/batchjsonl"
)

// Defaults for [SummaryBatchConfig] (docs/design/198-corpus-resummarization.md, "Flow"). A job
// may queue for 72 hours and run for 24, so a hold of 100 hours outlasts it with a margin.
const (
	DefaultSummaryBatchLocation = "global"
	DefaultSummaryBatchHold     = 100 * time.Hour
	DefaultSummaryBatchMinBills = 1000

	// DefaultBatchOutputTokens is the output a typical bill's summary takes, for --dry-run's
	// estimate: the #193 eval measured about 370.
	DefaultBatchOutputTokens = 400
)

const (
	// batchCallTimeout bounds each Vertex AI batch job call.
	batchCallTimeout = time.Minute
	// batchExportGrace is how long a batch may stay exporting with no job before the poller
	// takes it for a crashed submit and releases whatever it held.
	batchExportGrace = 6 * time.Hour
	// charsPerToken is the rough size of a token, for the dry run's estimate.
	charsPerToken = 4
	// perMillion turns a price per 1M tokens into a price per token.
	perMillion = 1_000_000

	batchInputFile    = "input.jsonl"
	batchManifestFile = "manifest.jsonl"
	batchOutputDir    = "output/"
)

// ErrBatchNotConfigured is returned by [Service.SubmitSummaryBatch] when AI_BATCH_BUCKET is unset.
var ErrBatchNotConfigured = errors.New("AI_BATCH_BUCKET is not set")

// ErrBatchTooSmall is returned by [Service.SubmitSummaryBatch] when fewer bills are due than
// AI_BATCH_MIN_BILLS: a small re-queue is as cheap and faster on the synchronous path.
var ErrBatchTooSmall = errors.New("too few bills due for a batch")

// BatchJobs is the part of [genai.Batches] the batch path uses.
type BatchJobs interface {
	Create(ctx context.Context, model string, src *genai.BatchJobSource,
		config *genai.CreateBatchJobConfig) (*genai.BatchJob, error)
	Get(ctx context.Context, name string, config *genai.GetBatchJobConfig) (*genai.BatchJob, error)
	Cancel(ctx context.Context, name string, config *genai.CancelBatchJobConfig) error
}

// BatchFiles holds a batch's input and output files, named by gs:// URIs (gcsblob.Store in
// production).
type BatchFiles interface {
	Create(ctx context.Context, uri string) (io.WriteCloser, error)
	Open(ctx context.Context, uri string) (io.ReadCloser, error)
	List(ctx context.Context, prefix string) ([]string, error)
}

// SummaryBatchConfig holds the batch path's settings.
type SummaryBatchConfig struct {
	// Bucket is the GCS bucket for batch files. Empty turns the batch path off.
	Bucket string
	// Location is the Vertex AI location batch jobs run in.
	Location string
	// Hold is how long an exported bill stays out of the synchronous queue, and how long a job
	// may stay open before the poller cancels it.
	Hold time.Duration
	// MinBills is the fewest due bills submit exports.
	MinBills int
}

// SummaryBatchConfigFrom reads the batch settings with get, which takes an env name in lower
// case as viper.GetString does: AI_BATCH_BUCKET, AI_BATCH_LOCATION, AI_BATCH_HOLD (a Go duration)
// and AI_BATCH_MIN_BILLS. Unset values get their defaults; a bad value is an error.
func SummaryBatchConfigFrom(get func(key string) string) (SummaryBatchConfig, error) {
	cfg := SummaryBatchConfig{
		Bucket:   strings.TrimSpace(get("ai_batch_bucket")),
		Location: strings.TrimSpace(get("ai_batch_location")),
		Hold:     DefaultSummaryBatchHold,
		MinBills: DefaultSummaryBatchMinBills,
	}
	if cfg.Location == "" {
		cfg.Location = DefaultSummaryBatchLocation
	}
	if strings.Contains(cfg.Bucket, "/") {
		return SummaryBatchConfig{}, fmt.Errorf("AI_BATCH_BUCKET %q: want a bucket name, not a path", cfg.Bucket)
	}
	if v := strings.TrimSpace(get("ai_batch_hold")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return SummaryBatchConfig{}, fmt.Errorf("AI_BATCH_HOLD %q: want a positive duration such as 100h", v)
		}
		cfg.Hold = d
	}
	if v := strings.TrimSpace(get("ai_batch_min_bills")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return SummaryBatchConfig{}, fmt.Errorf("AI_BATCH_MIN_BILLS %q: want a positive integer", v)
		}
		cfg.MinBills = n
	}
	return cfg, nil
}

// summaryBatches is the batch path's wiring: its settings, the summarizer settings its requests
// are built with, the job client and the file store.
type summaryBatches struct {
	cfg   SummaryBatchConfig
	ai    ai.Config
	jobs  BatchJobs
	files BatchFiles
}

// SetSummaryBatches turns on the batch path: submit exports to cfg.Bucket and creates jobs with
// jobs, and sync-summaries polls and imports open batches. aiCfg is the summarizer's settings,
// which every request is built with. A config without a bucket leaves the path off, and then only
// a dry run of submit uses aiCfg.
func (s *Service) SetSummaryBatches(cfg SummaryBatchConfig, aiCfg ai.Config, jobs BatchJobs, files BatchFiles) {
	s.batches = &summaryBatches{cfg: cfg, ai: aiCfg, jobs: jobs, files: files}
}

// batchesOn reports whether the batch path is on: a bucket, a job client and a file store.
func (s *Service) batchesOn() bool {
	return s.batches != nil && s.batches.cfg.Bucket != "" && s.batches.jobs != nil && s.batches.files != nil
}

// Summary tier names, as submit's --tiers takes them.
const (
	tierNameVoted  = "voted"
	tierNameRecent = "recent"
	tierNameOther  = "other"
	tierNamePrompt = "prompt"
)

// ParseSummaryTiers reads a comma-separated list of queue tiers (voted, recent, other, prompt)
// into [repository.SummaryTierVoted] and the other tier numbers, sorted.
func ParseSummaryTiers(list string) ([]int, error) {
	names := map[string]int{
		tierNameVoted: repository.SummaryTierVoted, tierNameRecent: repository.SummaryTierRecent,
		tierNameOther: repository.SummaryTierOther, tierNamePrompt: repository.SummaryTierPromptChange,
	}
	var tiers []int
	for name := range strings.SplitSeq(list, ",") {
		tier, ok := names[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			return nil, fmt.Errorf("tier %q: want voted, recent, other or prompt", name)
		}
		if !slices.Contains(tiers, tier) {
			tiers = append(tiers, tier)
		}
	}
	slices.Sort(tiers)
	return tiers, nil
}

// SummaryBatchRequest is what [Service.SubmitSummaryBatch] exports.
type SummaryBatchRequest struct {
	Congress int
	// Tiers are the queue tiers to take due bills from.
	Tiers []int
	// Max is the most bills exported.
	Max int
	// DryRun counts and prices the bills, and changes nothing.
	DryRun bool
	// InputPrice and OutputPrice are USD per 1M tokens at the batch price, for the estimate.
	InputPrice  float64
	OutputPrice float64
	// OutputTokens is the output assumed per bill; 0 means [DefaultBatchOutputTokens].
	OutputTokens int
}

// SummaryBatchPlan is what a submit exported, or would export on a dry run.
type SummaryBatchPlan struct {
	// BatchID is the new batch; empty on a dry run.
	BatchID string
	// JobName is the Vertex AI job's resource name; empty on a dry run.
	JobName string
	// Due is the bills taken per tier.
	Due map[int]int
	// Bills is how many bills have a request (the batch's size); Skipped had no text to load.
	Bills   int
	Skipped int
	// InputTokens and OutputTokens are estimates: characters ÷ 4, and OutputTokens per bill.
	InputTokens  int64
	OutputTokens int64
	// Cost is the estimate in USD at the request's prices.
	Cost float64
}

// SubmitSummaryBatch exports the due bills of req's tiers to a Vertex AI batch job
// (docs/design/198-corpus-resummarization.md, "Flow", step 1): it streams input.jsonl and
// manifest.jsonl to gs://<bucket>/summaries/<batch_id>/, inserts the summary_batches row, holds
// every exported bill out of the synchronous queue, and creates the job. A dry run only counts
// and prices them. Fewer due bills than AI_BATCH_MIN_BILLS is [ErrBatchTooSmall].
func (s *Service) SubmitSummaryBatch(ctx context.Context, req SummaryBatchRequest) (*SummaryBatchPlan, error) {
	if !s.batchesOn() && !req.DryRun {
		return nil, ErrBatchNotConfigured
	}
	if req.Max <= 0 || len(req.Tiers) == 0 {
		return nil, errors.New("submit summary batch: want a positive max and at least one tier")
	}
	if req.OutputTokens <= 0 {
		req.OutputTokens = DefaultBatchOutputTokens
	}
	aiCfg, minBills := s.batchAIConfig()
	now := s.now().UTC()
	items, err := s.batchItems(ctx, req, aiCfg, now)
	if err != nil {
		return nil, err
	}
	plan := &SummaryBatchPlan{Due: map[int]int{}}
	for _, it := range items {
		plan.Due[it.Tier]++
	}
	if req.DryRun {
		if err = s.priceBatch(ctx, aiCfg, items, req, plan); err != nil {
			return nil, err
		}
		return plan, nil
	}
	if len(items) < minBills {
		return plan, fmt.Errorf("%w: %d due, AI_BATCH_MIN_BILLS is %d", ErrBatchTooSmall, len(items), minBills)
	}
	return s.submitBatch(ctx, req, items, now, plan)
}

// batchAIConfig is the summarizer settings requests are built with, and the batch minimum.
func (s *Service) batchAIConfig() (ai.Config, int) {
	if s.batches != nil {
		minBills := s.batches.cfg.MinBills
		if minBills <= 0 {
			minBills = DefaultSummaryBatchMinBills
		}
		return s.batches.ai, minBills
	}
	if s.summarizer != nil {
		return s.summarizer.Config(), DefaultSummaryBatchMinBills
	}
	return ai.Config{Model: ai.DefaultModel}, DefaultSummaryBatchMinBills
}

// batchItems is up to req.Max due bills from req's tiers, in queue order. The queue returns tiers
// in order under one limit, so the limit also covers the due bills of the tiers left out.
func (s *Service) batchItems(
	ctx context.Context, req SummaryBatchRequest, aiCfg ai.Config, now time.Time,
) ([]repository.SummaryQueueItem, error) {
	q := repository.SummaryQueueQuery{
		Congress:                  req.Congress,
		PromptVersion:             ai.PromptVersionBill,
		Model:                     aiCfg.Model,
		Now:                       now,
		RecentDays:                s.summaryJob.RecentDays,
		ResummarizeOnPromptChange: slices.Contains(req.Tiers, repository.SummaryTierPromptChange),
		CRSContext:                aiCfg.CRSContext(),
		RuleContext:               aiCfg.RuleContext(),
	}
	counts, err := s.store.CountBillsToSummarize(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("count bills to summarize: %w", err)
	}
	q.Limit = req.Max
	for tier, n := range counts {
		if !slices.Contains(req.Tiers, tier) {
			q.Limit += n
		}
	}
	all, err := s.store.QueryBillsToSummarize(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query bills to summarize: %w", err)
	}
	items := make([]repository.SummaryQueueItem, 0, min(len(all), req.Max))
	for _, it := range all {
		if slices.Contains(req.Tiers, it.Tier) && len(items) < req.Max {
			items = append(items, it)
		}
	}
	return items, nil
}

// batchLine is one bill's request, built from its stored text.
type batchLine struct {
	bill batchjsonl.Bill
	req  *ai.Request
}

// eachBatchLine loads each item's context and builds its request with aiCfg, eight bills at a
// time, and calls fn with each, one call at a time. A bill whose context can't be loaded or has
// no text is logged and counted as skipped. An error from fn stops the loop and is returned.
func (s *Service) eachBatchLine(
	ctx context.Context, aiCfg ai.Config, items []repository.SummaryQueueItem, fn func(batchLine) error,
) (int, error) {
	var (
		mu      sync.Mutex
		skipped int
		fnErr   error
	)
	// An error from fn cancels the rest, so no more bills are loaded just to be dropped.
	poolCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	workerPool(poolCtx, items, DefaultSummaryWorkers, func(ctx context.Context, item repository.SummaryQueueItem) {
		if ctx.Err() != nil {
			return
		}
		line, err := s.loadBatchLine(ctx, aiCfg, item)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case fnErr != nil, ctx.Err() != nil:
		case err != nil:
			s.logger.WarnContext(ctx, "batch bill skipped", "bill_id", item.BillID, "error", err)
			skipped++
		default:
			if fnErr = fn(line); fnErr != nil {
				cancel()
			}
		}
	})
	if fnErr != nil {
		return skipped, fnErr
	}
	return skipped, ctx.Err()
}

func (s *Service) loadBatchLine(
	ctx context.Context,
	aiCfg ai.Config,
	item repository.SummaryQueueItem,
) (batchLine, error) {
	bc, err := s.store.LoadBillContext(ctx, item.BillID, item.VersionID)
	if err != nil {
		return batchLine{}, fmt.Errorf("load bill context: %w", err)
	}
	req, err := aiCfg.BillRequest(aiBillContext(bc, aiCfg))
	if err != nil {
		return batchLine{}, fmt.Errorf("build request: %w", err)
	}
	return batchLine{
		bill: summarySource(bc, aiCfg),
		req:  req,
	}, nil
}

// priceBatch fills plan's size and estimate from each bill's request.
func (s *Service) priceBatch(
	ctx context.Context, aiCfg ai.Config, items []repository.SummaryQueueItem, req SummaryBatchRequest,
	plan *SummaryBatchPlan,
) error {
	var chars int64
	skipped, err := s.eachBatchLine(ctx, aiCfg, items, func(l batchLine) error {
		plan.Bills++
		chars += requestChars(l.req)
		return nil
	})
	if err != nil {
		return err
	}
	plan.Skipped = skipped
	plan.InputTokens = chars / charsPerToken
	plan.OutputTokens = int64(plan.Bills) * int64(req.OutputTokens)
	plan.Cost = (float64(plan.InputTokens)*req.InputPrice + float64(plan.OutputTokens)*req.OutputPrice) / perMillion
	return nil
}

// requestChars is the text a request sends: the system instruction and the prompt.
func requestChars(req *ai.Request) int64 {
	n := int64(0)
	for _, c := range req.Contents {
		n += contentChars(c)
	}
	if req.Config != nil {
		n += contentChars(req.Config.SystemInstruction)
	}
	return n
}

func contentChars(c *genai.Content) int64 {
	var n int64
	if c == nil {
		return 0
	}
	for _, p := range c.Parts {
		n += int64(len(p.Text))
	}
	return n
}

// submitBatch exports items, records the batch, holds its bills and creates its job.
func (s *Service) submitBatch(
	ctx context.Context, req SummaryBatchRequest, items []repository.SummaryQueueItem, now time.Time,
	plan *SummaryBatchPlan,
) (*SummaryBatchPlan, error) {
	b := s.batches
	id := strings.ToLower(rand.Text())
	prefix := "gs://" + b.cfg.Bucket + "/summaries/" + id + "/"
	held, skipped, err := s.exportBatch(ctx, items, prefix)
	if err != nil {
		return nil, err
	}
	plan.BatchID, plan.Bills, plan.Skipped = id, len(held), skipped
	if len(held) == 0 {
		return plan, errors.New("submit summary batch: no due bill has text to summarize")
	}

	batch := repository.SummaryBatch{
		BatchID: id, Congress: req.Congress, Model: b.ai.Model, PromptVersion: ai.PromptVersionBill,
		State: repository.SummaryBatchExporting, BillCount: len(held), InputURI: prefix + batchInputFile,
		OutputURI: prefix + batchOutputDir, CreatedAt: now,
	}
	if err = s.store.CreateSummaryBatch(ctx, batch); err != nil {
		return nil, fmt.Errorf("record summary batch: %w", err)
	}
	s.logSummaryBatch(ctx, batch, now)
	// Hold the bills before the job exists, so it never runs while the synchronous job can take
	// them too. A crash before the job is created leaves an exporting batch the poller releases.
	err = s.store.HoldSummaryBatch(ctx, repository.SummaryBatchHolds{
		BatchID: id, PromptVersion: ai.PromptVersionBill, Model: b.ai.Model,
		At: now, Until: now.Add(b.cfg.Hold), Bills: held,
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("hold batch bills: %w", err), s.releaseBatch(ctx, batch, now))
	}

	job, err := s.createBatchJob(ctx, batch)
	if err != nil {
		return nil, errors.Join(err, s.releaseBatch(ctx, batch, now))
	}
	plan.JobName = job.Name
	batch.JobName, batch.State = job.Name, string(job.State)
	if err = s.store.UpdateSummaryBatch(ctx, repository.SummaryBatchUpdate{
		BatchID: id, State: batch.State, JobName: job.Name,
	}); err != nil {
		return plan, fmt.Errorf("record batch job %s: %w", job.Name, err)
	}
	s.logSummaryBatch(ctx, batch, now)
	return plan, nil
}

// exportBatch streams the items' requests and manifest under prefix, and returns the bills
// written, with the version and hash each request was built from, and how many were skipped.
func (s *Service) exportBatch(
	ctx context.Context, items []repository.SummaryQueueItem, prefix string,
) ([]repository.SummaryQueueItem, int, error) {
	files := s.batches.files
	// Cancelling the uploads' context before Close aborts them, so a failed export stores no
	// partial files.
	uploadCtx, abort := context.WithCancel(ctx)
	defer abort()
	input, err := files.Create(uploadCtx, prefix+batchInputFile)
	if err != nil {
		return nil, 0, fmt.Errorf("create batch input: %w", err)
	}
	manifest, err := files.Create(uploadCtx, prefix+batchManifestFile)
	if err != nil {
		abort()
		_ = input.Close()
		return nil, 0, fmt.Errorf("create batch manifest: %w", err)
	}
	enc := batchjsonl.NewEncoder(input, manifest)
	held := make([]repository.SummaryQueueItem, 0, len(items))
	skipped, err := s.eachBatchLine(ctx, s.batches.ai, items, func(l batchLine) error {
		if _, encErr := enc.Encode(l.bill, l.req); encErr != nil {
			return encErr
		}
		held = append(held, repository.SummaryQueueItem{
			BillID: l.bill.BillID, VersionID: l.bill.VersionID, VersionCode: l.bill.VersionCode,
			ContentHash: l.bill.ContentHash,
		})
		return nil
	})
	if err != nil {
		abort()
		_, _ = input.Close(), manifest.Close()
		return nil, skipped, fmt.Errorf("export batch: %w", err)
	}
	if err = errors.Join(input.Close(), manifest.Close()); err != nil {
		return nil, skipped, fmt.Errorf("export batch: %w", err)
	}
	return held, skipped, nil
}

// createBatchJob creates the batch's Vertex AI job on its input, writing under its output URI.
func (s *Service) createBatchJob(ctx context.Context, b repository.SummaryBatch) (*genai.BatchJob, error) {
	callCtx, cancel := context.WithTimeout(ctx, batchCallTimeout)
	defer cancel()
	job, err := s.batches.jobs.Create(callCtx, b.Model,
		&genai.BatchJobSource{Format: "jsonl", GCSURI: []string{b.InputURI}},
		&genai.CreateBatchJobConfig{
			DisplayName: "justabill-summaries-" + b.BatchID,
			Dest:        &genai.BatchJobDestination{Format: "jsonl", GCSURI: b.OutputURI},
		})
	if err != nil {
		return nil, fmt.Errorf("create batch job: %w", err)
	}
	if job.Name == "" {
		return nil, errors.New("create batch job: no job name in the response")
	}
	return job, nil
}

// logSummaryBatch logs one summary_batch line: a batch's state, size, counts and age.
func (s *Service) logSummaryBatch(ctx context.Context, b repository.SummaryBatch, now time.Time, extra ...slog.Attr) {
	attrs := []slog.Attr{
		slog.String("batch_id", b.BatchID), slog.Int("congress", b.Congress), slog.String("state", b.State),
		slog.Int("bill_count", b.BillCount), slog.Int("ok", derefInt(b.OKCount)),
		slog.Int("failed", derefInt(b.FailedCount)),
		slog.String("age", now.Sub(b.CreatedAt).Round(time.Second).String()),
		slog.String("job_name", b.JobName),
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "summary_batch", append(attrs, extra...)...)
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
