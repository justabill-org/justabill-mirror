package upstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Archive sizing. Writes happen off the request path, so a slow or failing bucket never
// delays the load: when the queue or its byte budget is full, the response is dropped from
// the archive (and counted), not waited for.
const (
	archiveWorkers      = 4
	archiveQueueLen     = 256
	archiveQueueBytes   = 256 * mib
	archiveWriteTimeout = 60 * time.Second
	// maxObjectName is GCS's limit on an object name, in bytes.
	maxObjectName = 1024
	// maxRequestBodyMeta caps the request body kept in a POST's metadata (GCS allows 8 KiB
	// of custom metadata in all).
	maxRequestBodyMeta = 1024
	// nameHashHex is how many hex digits of a SHA-256 go into a name, for a request body or
	// a name that's too long.
	nameHashHex = 16
)

// apiKeyParam is api.data.gov's query parameter for a key. The pipeline sends keys as a
// header, but a key in a URL must still never reach an object name or its metadata.
const apiKeyParam = "api_key"

// ErrArchiveExists means an object with that name is already in the archive. The first
// response of the day for a URL is the one kept: the pipeline may create objects, not
// overwrite them.
var ErrArchiveExists = errors.New("upstream: archive object already exists")

// ArchiveObject is one archived response: its body gzipped, and metadata without any key.
type ArchiveObject struct {
	Name        string
	Body        []byte
	ContentType string
	Metadata    map[string]string
}

// ArchiveStore writes archive objects. GCSStore is the production store.
type ArchiveStore interface {
	// Put writes obj unless an object with its name exists, in which case it returns
	// ErrArchiveExists.
	Put(ctx context.Context, obj ArchiveObject) error
}

// ArchiveStats counts what the archive did with the responses it was given.
type ArchiveStats struct {
	// Written objects, and Duplicates already in the bucket from an earlier fetch that day.
	Written, Duplicates int64
	// Failed writes, and Dropped responses the queue had no room for or that came after Close.
	Failed, Dropped int64
}

// Archive keeps a gzipped copy of every 2xx response body the upstream client reads, named
// <host>/<yyyy-mm-dd>/<path>?<query> (docs/design/28-production.md, "Raw archive"). It
// never fails or delays a request: writes run on background workers, and a write that
// fails is logged as upstream_archive_error and counted.
type Archive struct {
	log   *slog.Logger
	store ArchiveStore

	mu      sync.RWMutex // guards closed and sends on queue
	closed  bool
	queue   chan archiveJob
	pending atomic.Int64 // bytes queued
	done    sync.WaitGroup

	written, duplicates, failed, dropped atomic.Int64
}

type archiveJob struct {
	obj  ArchiveObject // Body not yet gzipped
	host string
}

// NewArchive starts an archive that writes to store. Close it to flush the queue.
func NewArchive(log *slog.Logger, store ArchiveStore) *Archive {
	a := &Archive{log: log, store: store, queue: make(chan archiveJob, archiveQueueLen)}
	a.done.Add(archiveWorkers)
	for range archiveWorkers {
		go a.work()
	}
	return a
}

// Stats returns the counts so far.
func (a *Archive) Stats() ArchiveStats {
	return ArchiveStats{
		Written: a.written.Load(), Duplicates: a.duplicates.Load(),
		Failed: a.failed.Load(), Dropped: a.dropped.Load(),
	}
}

// Close stops taking responses, waits for the queued writes until ctx is done, and logs an
// upstream_archive_summary line with the counts. Responses arriving after Close are
// dropped.
func (a *Archive) Close(ctx context.Context) error {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.queue)
	}
	a.mu.Unlock()

	flushed := make(chan struct{})
	go func() {
		a.done.Wait()
		close(flushed)
	}()
	var err error
	select {
	case <-flushed:
	case <-ctx.Done():
		err = fmt.Errorf("upstream archive: %d byte(s) still queued at close: %w", a.pending.Load(), ctx.Err())
	}
	s := a.Stats()
	a.log.InfoContext(ctx, "upstream_archive_summary", "written", s.Written, "duplicates", s.Duplicates,
		"failed", s.Failed, "dropped", s.Dropped)
	return err
}

// add queues a response for writing, or drops it if the archive is closed or its queue is
// full.
func (a *Archive) add(req *http.Request, resp *http.Response, body []byte, fetched time.Time) {
	job := archiveJob{obj: newArchiveObject(req, resp, body, fetched), host: req.URL.Host}
	size := int64(len(body))

	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		a.drop(req.Context(), job, "closed")
		return
	}
	if a.pending.Add(size) > archiveQueueBytes {
		a.pending.Add(-size)
		a.drop(req.Context(), job, "queue_full")
		return
	}
	select {
	case a.queue <- job:
	default:
		a.pending.Add(-size)
		a.drop(req.Context(), job, "queue_full")
	}
}

func (a *Archive) drop(ctx context.Context, job archiveJob, reason string) {
	a.dropped.Add(1)
	a.log.WarnContext(ctx, "upstream_archive_error", "host", job.host, "object", job.obj.Name,
		"reason", reason)
}

func (a *Archive) work() {
	defer a.done.Done()
	for job := range a.queue {
		a.write(job)
		a.pending.Add(-int64(len(job.obj.Body)))
	}
}

// write gzips and stores one object. It runs detached from the request, whose context may
// be gone by now.
func (a *Archive) write(job archiveJob) {
	ctx, cancel := context.WithTimeout(context.Background(), archiveWriteTimeout)
	defer cancel()

	obj := job.obj
	var err error
	if obj.Body, err = gzipBytes(obj.Body); err == nil {
		err = a.store.Put(ctx, obj)
	}
	switch {
	case err == nil:
		a.written.Add(1)
	case errors.Is(err, ErrArchiveExists):
		a.duplicates.Add(1)
	default:
		a.failed.Add(1)
		a.log.WarnContext(ctx, "upstream_archive_error", "host", job.host, "object", obj.Name,
			"reason", "write_failed", "error", err.Error())
	}
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// newArchiveObject names a response and builds its metadata. Neither carries the request's
// headers (so no X-Api-Key) or an api_key query parameter.
func newArchiveObject(req *http.Request, resp *http.Response, body []byte, fetched time.Time) ArchiveObject {
	u := stripKey(req.URL)
	meta := map[string]string{
		"url":        u.String(),
		"method":     req.Method,
		"status":     strconv.Itoa(resp.StatusCode),
		"fetched_at": fetched.UTC().Format(time.RFC3339),
	}
	reqBody := requestBody(req)
	if len(reqBody) > 0 && len(reqBody) <= maxRequestBodyMeta {
		meta["request_body"] = string(reqBody)
	}
	return ArchiveObject{
		Name:        ArchiveName(u, req.Method, reqBody, fetched),
		Body:        body,
		ContentType: resp.Header.Get("Content-Type"),
		Metadata:    meta,
	}
}

// ArchiveName is an archived response's object name: <host>/<yyyy-mm-dd>/<path>, then
// ?<query> with its parameters sorted and api_key removed. A request other than GET gets
// .<METHOD>-<hash of its body> after that, since its URL alone doesn't say what was asked.
// A name over GCS's 1,024 bytes is cut and ends with a hash of the whole name.
func ArchiveName(u *url.URL, method string, body []byte, fetched time.Time) string {
	u = stripKey(u)
	path := u.EscapedPath()
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var b strings.Builder
	b.WriteString(strings.ToLower(u.Host))
	b.WriteString("/")
	b.WriteString(fetched.UTC().Format(time.DateOnly))
	b.WriteString(path)
	if u.RawQuery != "" {
		b.WriteString("?")
		b.WriteString(u.RawQuery)
	}
	if method != http.MethodGet {
		b.WriteString("." + method + "-" + shortHash(body))
	}
	name := b.String()
	if len(name) > maxObjectName {
		suffix := "~" + shortHash([]byte(name))
		name = strings.ToValidUTF8(name[:maxObjectName-len(suffix)], "") + suffix
	}
	return name
}

// stripKey returns a copy of u without an api_key parameter (in any case), user info or
// fragment, and with the remaining parameters sorted.
func stripKey(u *url.URL) *url.URL {
	out := *u
	out.User = nil
	out.Fragment, out.RawFragment = "", ""
	q := u.Query()
	for k := range q {
		if strings.EqualFold(k, apiKeyParam) {
			delete(q, k)
		}
	}
	out.RawQuery = q.Encode()
	return &out
}

func requestBody(req *http.Request) []byte {
	if req.GetBody == nil {
		return nil
	}
	rc, err := req.GetBody()
	if err != nil {
		return nil
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, largeBodyCap))
	if err != nil {
		return nil
	}
	return b
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:nameHashHex]
}
