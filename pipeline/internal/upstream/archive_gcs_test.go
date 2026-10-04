package upstream_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/option"

	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

// fakeGCS answers the JSON API's multipart upload the way GCS does, refusing a name it has
// seen when the upload asks ifGenerationMatch=0.
type fakeGCS struct {
	mu      sync.Mutex
	objects map[string]gcsUpload
	queries []string
}

type gcsUpload struct {
	meta  map[string]any
	media []byte
}

const preconditionFailed = `{"error":{"code":412,` +
	`"message":"At least one of the pre-conditions you specified did not hold."}}`

func (f *fakeGCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/archive-bucket/o") {
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
		return
	}
	up, err := readUpload(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name, _ := up.meta["name"].(string)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, r.URL.RawQuery)
	if _, exists := f.objects[name]; exists && r.URL.Query().Get("ifGenerationMatch") == "0" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = io.WriteString(w, preconditionFailed)
		return
	}
	f.objects[name] = up
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"bucket": "archive-bucket", "name": name, "generation": "1"})
}

func readUpload(r *http.Request) (gcsUpload, error) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return gcsUpload{}, err
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	var up gcsUpload
	for i := range 2 {
		part, partErr := mr.NextPart()
		if partErr != nil {
			return gcsUpload{}, partErr
		}
		b, readErr := io.ReadAll(part)
		if readErr != nil {
			return gcsUpload{}, readErr
		}
		if i == 0 {
			if err = json.Unmarshal(b, &up.meta); err != nil {
				return gcsUpload{}, err
			}
		} else {
			up.media = b
		}
	}
	return up, nil
}

func newFakeGCS(t *testing.T) (*fakeGCS, *upstream.GCSStore) {
	t.Helper()
	fake := &fakeGCS{objects: map[string]gcsUpload{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	store, err := upstream.NewGCSStore(t.Context(), "archive-bucket",
		option.WithEndpoint(srv.URL+"/storage/v1/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return fake, store
}

func TestGCSStorePut(t *testing.T) {
	fake, store := newFakeGCS(t)

	obj := upstream.ArchiveObject{
		Name:        "api.congress.gov/2026-10-03/v3/bill?format=json&offset=0",
		Body:        []byte("\x1f\x8bgzipped"),
		ContentType: "application/json",
		Metadata:    map[string]string{"status": "200"},
	}
	if err := store.Put(t.Context(), obj); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(t.Context(), obj); !errors.Is(err, upstream.ErrArchiveExists) {
		t.Errorf("second Put = %v, want ErrArchiveExists", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	up, ok := fake.objects[obj.Name]
	if !ok {
		t.Fatalf("no object %q in %v", obj.Name, fake.objects)
	}
	if string(up.media) != string(obj.Body) {
		t.Errorf("media = %q", up.media)
	}
	if up.meta["contentEncoding"] != "gzip" || up.meta["contentType"] != "application/json" {
		t.Errorf("object resource = %v", up.meta)
	}
	if m, _ := up.meta["metadata"].(map[string]any); m["status"] != "200" {
		t.Errorf("custom metadata = %v", up.meta["metadata"])
	}
	for _, q := range fake.queries {
		if !strings.Contains(q, "ifGenerationMatch=0") {
			t.Errorf("upload %q doesn't require a new name", q)
		}
	}
}

// A body larger than 256 KiB, and not a multiple of it, still goes up in one multipart
// request: the storage client rounds ChunkSize up to a multiple of 256 KiB rather than
// rejecting it, and a body that fits in one chunk isn't sent as a resumable upload.
func TestGCSStorePutLargeBody(t *testing.T) {
	fake, store := newFakeGCS(t)

	body := make([]byte, 300_001)
	for i := range body {
		body[i] = byte(i)
	}
	obj := upstream.ArchiveObject{Name: "api.govinfo.gov/2026-10-03/packages/BILLS/htm", Body: body}
	if err := store.Put(t.Context(), obj); err != nil {
		t.Fatal(err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if up := fake.objects[obj.Name]; string(up.media) != string(body) {
		t.Errorf("media is %d bytes, want %d", len(up.media), len(body))
	}
	if len(fake.queries) != 1 || !strings.Contains(fake.queries[0], "uploadType=multipart") {
		t.Errorf("uploads = %q, want one multipart request", fake.queries)
	}
}

// A response goes from the upstream client through the archive to the bucket, gzipped.
func TestArchiveToGCS(t *testing.T) {
	fake, store := newFakeGCS(t)
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, "<rollcall-vote/>")
	})
	c, a := archivingClient(t, slog.New(slog.DiscardHandler), srv, store)

	if _, err := get(t.Context(), c, srv.URL+"/evs/2026/roll001.xml", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	up, ok := fake.objects[srv.host()+"/2026-10-03/evs/2026/roll001.xml"]
	if !ok {
		t.Fatalf("objects = %v", fake.objects)
	}
	if got := gunzip(t, up.media); got != "<rollcall-vote/>" {
		t.Errorf("stored body = %q", got)
	}
	if up.meta["contentType"] != "application/xml" {
		t.Errorf("object resource = %v", up.meta)
	}
}

// With a bucket configured, NewPipeline starts the archive and Close flushes it.
func TestNewPipelineWithArchiveBucket(t *testing.T) {
	srv := httptest.NewServer(&fakeGCS{objects: map[string]gcsUpload{}})
	t.Cleanup(srv.Close)
	t.Setenv("STORAGE_EMULATOR_HOST", strings.TrimPrefix(srv.URL, "http://"))
	log, lb := newLogger()
	p, err := upstream.NewPipeline(t.Context(), log, upstream.Config{
		CongressRPS: 1, CongressBurst: 1, GovInfoRPS: 1, ArchiveBucket: "archive-bucket",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Archive == nil {
		t.Fatal("no archive with a bucket")
	}
	if err = p.Close(t.Context()); err != nil {
		t.Errorf("Close = %v", err)
	}
	for _, msg := range []string{"upstream_archive_enabled", "upstream_archive_summary"} {
		if lb.count(msg) != 1 {
			t.Errorf("want one %s line in %s", msg, lb.String())
		}
	}
}
