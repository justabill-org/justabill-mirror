package govinfo_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

func TestClient_PollChanges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"packages": [{"packageId": "BILLS-119hr1ih"}], "count": 1}`)
	}))
	defer srv.Close()

	client := govinfo.NewClientWithBaseURL(srv.Client(), srv.URL)
	resp, err := client.PollChanges(context.Background(), "BILLS", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Packages) != 1 {
		t.Errorf("expected 1 package, got %d", len(resp.Packages))
	}
	if resp.Packages[0].PackageID != "BILLS-119hr1ih" {
		t.Errorf("expected packageId BILLS-119hr1ih, got %q", resp.Packages[0].PackageID)
	}
}

// keyServer answers every request with `{}` and records the key header and whether the
// query string had an api_key.
type keyServer struct {
	*httptest.Server

	keys    []string
	inQuery bool
}

func newKeyServer(t *testing.T) *keyServer {
	t.Helper()
	ks := &keyServer{}
	ks.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ks.keys = append(ks.keys, r.Header.Get("X-Api-Key"))
		if strings.Contains(strings.ToLower(r.URL.RawQuery), "api_key") {
			ks.inQuery = true
		}
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(ks.Close)
	return ks
}

// The key is the upstream client's job: every call, FetchText's download included (the old
// 401, U7 in the design), carries it as a header and never in the URL.
func TestClient_APIKeyInHeaderNotURL(t *testing.T) {
	srv := newKeyServer(t)
	ctx := context.Background()
	client := govinfo.NewClientWithBaseURL(upstreamClient(t, srv.URL), srv.URL)

	if _, err := client.PollChanges(ctx, "BILLS", time.Now()); err != nil {
		t.Errorf("PollChanges: %v", err)
	}
	if _, err := client.FetchPackageSummary(ctx, "BILLS-119hr1ih"); err != nil {
		t.Errorf("FetchPackageSummary: %v", err)
	}
	if _, err := client.SearchUSCode(ctx, 42, 1983); err != nil {
		t.Errorf("SearchUSCode (POST): %v", err)
	}
	if _, err := client.FetchText(ctx, srv.URL+"/packages/BILLS-119hr1ih/xml"); err != nil {
		t.Errorf("FetchText: %v", err)
	}

	const wantRequests = 4
	if len(srv.keys) != wantRequests {
		t.Fatalf("server saw %d requests, want %d", len(srv.keys), wantRequests)
	}
	for i, k := range srv.keys {
		if k != testKey {
			t.Errorf("request %d: X-Api-Key = %q, want %s", i, k, testKey)
		}
	}
	if srv.inQuery {
		t.Error("a request had api_key in its query string")
	}
}

func TestClient_PollChanges_Query(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		fmt.Fprint(w, `{"packages": []}`)
	}))
	defer srv.Close()

	client := govinfo.NewClientWithBaseURL(srv.Client(), srv.URL)
	if _, err := client.PollChanges(context.Background(), "BILLS", time.Now()); err != nil {
		t.Fatal(err)
	}
	if query.Get("pageSize") != "1000" || query.Get("offsetMark") != "*" {
		t.Errorf("query = %v, want pageSize=1000 and offsetMark=*", query)
	}
}

// FetchText takes URLs from GovInfo response bodies. The key goes only to the API's own host,
// and a host the upstream client doesn't know is refused before anything is sent.
func TestClient_FetchText_NoKeyForOtherHosts(t *testing.T) {
	api := newKeyServer(t)
	other := newKeyServer(t)
	undeclared := newKeyServer(t)
	ctx := context.Background()

	client := govinfo.NewClientWithBaseURL(upstreamClient(t, api.URL, other.URL), api.URL)
	if _, err := client.FetchText(ctx, other.URL+"/text"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchText(ctx, undeclared.URL+"/text"); !errors.Is(err, upstream.ErrUnknownHost) {
		t.Errorf("undeclared host: err = %v, want ErrUnknownHost", err)
	}

	if len(api.keys) != 0 || len(undeclared.keys) != 0 {
		t.Errorf("API server saw %d requests and the undeclared one %d, want 0 and 0",
			len(api.keys), len(undeclared.keys))
	}
	if len(other.keys) != 1 || other.keys[0] != "" {
		t.Errorf("other host saw keys %q, want one request without a key", other.keys)
	}
}

// FetchText and FetchGranuleHTM download large documents, so they use upstream's download
// timeout instead of the host's: a body that takes longer than the host's attempt timeout
// still arrives in one attempt.
func TestClient_Downloads_UseDownloadTimeout(t *testing.T) {
	const hostTimeout = 100 * time.Millisecond
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-time.After(3 * hostTimeout):
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "<bill/>")
	}))
	defer srv.Close()

	budget, err := upstream.NewBudget("test", 1000, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	hc, err := upstream.NewClient(slog.New(slog.DiscardHandler), map[string]upstream.Host{
		srv.Listener.Addr().String(): {Budget: budget, AttemptTimeout: hostTimeout, MaxBodyBytes: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := govinfo.NewClientWithBaseURL(hc, srv.URL)
	if _, err = client.FetchText(t.Context(), srv.URL+"/packages/BILLS-119hr1ih/xml"); err != nil {
		t.Fatalf("FetchText: %v", err)
	}
	if _, err = client.FetchGranuleHTM(t.Context(), "USCODE-2024-title42", "sec1983"); err != nil {
		t.Fatalf("FetchGranuleHTM: %v", err)
	}
	if requests.Load() != 2 {
		t.Errorf("server saw %d requests, want 2 (one attempt each)", requests.Load())
	}
}

// A search is a POST, which upstream retries only when marked idempotent.
func TestClient_Search_RetriedByUpstream(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "usctitlenum:42") {
			t.Errorf("attempt %d body = %q, want the query", requests.Load()+1, body)
		}
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"count": 1, "results": [{"packageId": "USCODE-2024-title42"}]}`)
	}))
	defer srv.Close()

	client := govinfo.NewClientWithBaseURL(upstreamClient(t, srv.URL), srv.URL)
	resp, err := client.SearchUSCode(context.Background(), 42, 1983)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || requests.Load() != 2 {
		t.Errorf("count %d after %d requests, want 1 after 2", resp.Count, requests.Load())
	}
}

func TestClient_TransportErrorHasNoQuery(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // every request now fails in the transport

	client := govinfo.NewClientWithBaseURL(srv.Client(), srv.URL)
	_, err := client.PollChanges(context.Background(), "BILLS", time.Now())
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if msg := err.Error(); strings.Contains(msg, "?") || strings.Contains(msg, testKey) {
		t.Errorf("error leaks the query: %q", msg)
	}
}

func TestClient_PollChanges_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := govinfo.NewClientWithBaseURL(srv.Client(), srv.URL)
	_, err := client.PollChanges(context.Background(), "BILLS", time.Now())
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestClient_FetchPackageSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"packageId": "BILLS-119hr1ih", "download": {"txtLink": "http://example.com/text"}}`)
	}))
	defer srv.Close()

	client := govinfo.NewClientWithBaseURL(srv.Client(), srv.URL)
	summary, err := client.FetchPackageSummary(context.Background(), "BILLS-119hr1ih")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.PackageID != "BILLS-119hr1ih" {
		t.Errorf("expected packageId BILLS-119hr1ih, got %q", summary.PackageID)
	}
	if summary.Download == nil || summary.Download.TxtLink != "http://example.com/text" {
		t.Error("expected download.txtLink")
	}
}

func TestClient_FetchPackageSummary_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := govinfo.NewClientWithBaseURL(srv.Client(), srv.URL)
	_, err := client.FetchPackageSummary(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for 404 response")
	}
}
