package gcsblob_test

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"

	"github.com/justabill-org/justabill/pipeline/internal/gcsblob"
)

// fakeGCS answers the JSON API's multipart upload, media download and object list for one
// bucket, "b".
type fakeGCS struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (f *fakeGCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/upload/storage/v1/b/b/o":
		name, media, err := readUpload(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.objects[name] = media
		writeJSON(w, map[string]any{"bucket": "b", "name": name, "generation": "1"})
	case r.Method == http.MethodGet && r.URL.Path == "/storage/v1/b/b/o":
		var items []map[string]any
		for name := range f.objects {
			if strings.HasPrefix(name, r.URL.Query().Get("prefix")) {
				items = append(items, map[string]any{"bucket": "b", "name": name})
			}
		}
		slices.SortFunc(
			items,
			func(a, b map[string]any) int { return strings.Compare(a["name"].(string), b["name"].(string)) },
		)
		writeJSON(w, map[string]any{"kind": "storage#objects", "items": items})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/storage/v1/b/b/o/"):
		body, ok := f.objects[strings.TrimPrefix(r.URL.Path, "/storage/v1/b/b/o/")]
		if !ok {
			http.Error(w, `{"error":{"code":404,"message":"No such object"}}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func readUpload(r *http.Request) (string, []byte, error) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return "", nil, err
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	var meta struct {
		Name string `json:"name"`
	}
	part, err := mr.NextPart()
	if err != nil {
		return "", nil, err
	}
	if err = json.NewDecoder(part).Decode(&meta); err != nil {
		return "", nil, err
	}
	if part, err = mr.NextPart(); err != nil {
		return "", nil, err
	}
	media, err := io.ReadAll(part)
	return meta.Name, media, err
}

func newStore(t *testing.T) *gcsblob.Store {
	t.Helper()
	fake := &fakeGCS{objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	store, err := gcsblob.New(t.Context(), option.WithEndpoint(srv.URL+"/storage/v1/"),
		option.WithoutAuthentication(), storage.WithJSONReads())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStore_CreateOpenList(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()
	for name, body := range map[string]string{
		"summaries/x/input.jsonl":              "{\"request\":1}\n",
		"summaries/x/output/predictions.jsonl": "{\"response\":1}\n",
		"other/y.jsonl":                        "{}\n",
	} {
		w, err := store.Create(ctx, "gs://b/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
		if err = w.Close(); err != nil {
			t.Fatalf("close %s: %v", name, err)
		}
	}

	r, err := store.Open(ctx, "gs://b/summaries/x/input.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(got) != "{\"request\":1}\n" {
		t.Errorf("read = %q, %v", got, err)
	}

	uris, err := store.List(ctx, "gs://b/summaries/x/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gs://b/summaries/x/input.jsonl", "gs://b/summaries/x/output/predictions.jsonl"}
	if !slices.Equal(uris, want) {
		t.Errorf("List = %q, want %q", uris, want)
	}

	if _, err = store.Open(ctx, "gs://b/missing"); err == nil {
		t.Error("opening a missing object succeeded")
	}
}

func TestStore_BadURIs(t *testing.T) {
	store := newStore(t)
	if _, err := store.Create(t.Context(), "gs://b/"); !errors.Is(err, gcsblob.ErrBadURI) {
		t.Errorf("Create(gs://b/) = %v, want ErrBadURI", err)
	}
	if _, err := store.Open(t.Context(), "s3://b/x"); !errors.Is(err, gcsblob.ErrBadURI) {
		t.Errorf("Open(s3://b/x) = %v, want ErrBadURI", err)
	}
	if _, err := store.List(t.Context(), "gs:///x"); !errors.Is(err, gcsblob.ErrBadURI) {
		t.Errorf("List(gs:///x) = %v, want ErrBadURI", err)
	}
}

func TestParseURI(t *testing.T) {
	for uri, want := range map[string][2]string{
		"gs://bucket":           {"bucket", ""},
		"gs://bucket/":          {"bucket", ""},
		"gs://bucket/a/b.jsonl": {"bucket", "a/b.jsonl"},
	} {
		bucket, name, err := gcsblob.ParseURI(uri)
		if err != nil || bucket != want[0] || name != want[1] {
			t.Errorf("ParseURI(%q) = %q, %q, %v; want %q", uri, bucket, name, err, want)
		}
	}
}
