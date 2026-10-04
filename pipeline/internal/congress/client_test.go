package congress_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

func TestClient_ListBills(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"bills": [{"number": "1", "type": "HR", "title": "Test"}], "pagination": {"count": 1}}`)
	}))
	defer srv.Close()

	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)
	resp, err := client.ListBills(context.Background(), 119, 0, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Bills) != 1 {
		t.Errorf("expected 1 bill, got %d", len(resp.Bills))
	}
	if resp.Bills[0].Title != "Test" {
		t.Errorf("expected title 'Test', got %q", resp.Bills[0].Title)
	}
}

func TestClient_APIKeyInHeaderNotURL(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("X-Api-Key"); got != testKey {
			t.Errorf("%s: X-Api-Key = %q, want %s", r.URL.Path, got, testKey)
		}
		if strings.Contains(strings.ToLower(r.URL.RawQuery), "api_key") {
			t.Errorf("%s: key in the query string: %q", r.URL.Path, r.URL.RawQuery)
		}
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Errorf("%s: format = %q, want json", r.URL.Path, got)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	ctx := context.Background()
	client := congress.NewClientWithBaseURL(upstreamClient(t, srv.URL), srv.URL)
	calls := []func() error{
		func() error { _, err := client.ListBills(ctx, 119, 0, 10); return err },
		func() error { _, err := client.ListBillsUpdatedSince(ctx, 119, time.Now(), 0, 10); return err },
		func() error { _, err := client.GetBill(ctx, 119, "hr", 1); return err },
		func() error { _, err := client.ListMembers(ctx, 119, 0, 10); return err },
		func() error { _, err := client.GetMemberDetail(ctx, "A000001"); return err },
		func() error { _, err := client.GetBillActions(ctx, 119, "hr", 1); return err },
		func() error { _, err := client.GetBillAmendments(ctx, 119, "hr", 1); return err },
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
	if int(requests.Load()) != len(calls) {
		t.Errorf("server saw %d requests, want %d", requests.Load(), len(calls))
	}
}

func TestClient_TransportErrorHasNoQuery(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // every request now fails in the transport

	// Plain client: the upstream client would retry the refused connection with backoff.
	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)
	_, err := client.ListBills(context.Background(), 119, 20, 250)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if msg := err.Error(); strings.Contains(msg, "?") || strings.Contains(msg, testKey) {
		t.Errorf("error leaks the query: %q", msg)
	}
}

func TestClient_ListBills_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)
	_, err := client.ListBills(context.Background(), 119, 0, 10)
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestClient_GetBill(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"bill": {"number": "1", "type": "HR", "title": "Test Bill"}}`)
	}))
	defer srv.Close()

	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)
	bill, err := client.GetBill(context.Background(), 119, "hr", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bill.Title != "Test Bill" {
		t.Errorf("expected title 'Test Bill', got %q", bill.Title)
	}
}

func TestClient_GetBill_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	client := congress.NewClientWithBaseURL(upstreamClient(t, srv.URL), srv.URL)
	_, err := client.GetBill(t.Context(), 119, "hr", 99999)
	if !errors.Is(err, congress.ErrNotFound) {
		t.Errorf("GetBill of an unknown bill = %v, want congress.ErrNotFound", err)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer failing.Close()
	client = congress.NewClientWithBaseURL(upstreamClient(t, failing.URL), failing.URL)
	if _, err = client.GetBill(t.Context(), 119, "hr", 1); err == nil || errors.Is(err, congress.ErrNotFound) {
		t.Errorf("GetBill on a 400 = %v, want an error that isn't ErrNotFound", err)
	}
}

func TestClient_ListMembers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"members": [{"bioguideId": "A000001", "name": "Doe, John"}], "pagination": {"count": 1}}`)
	}))
	defer srv.Close()

	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)
	resp, err := client.ListMembers(context.Background(), 119, 0, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Members) != 1 {
		t.Errorf("expected 1 member, got %d", len(resp.Members))
	}
}

// Retries are the upstream client's job now: a 429 on any call (here a sub-resource, which the
// old client never retried) is retried after its Retry-After.
func TestClient_RetriedByUpstream(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if callCount.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprintf(w, `{"actions": [{"actionDate": "2025-01-03", "text": "Introduced"}]}`)
	}))
	defer srv.Close()

	client := congress.NewClientWithBaseURL(upstreamClient(t, srv.URL), srv.URL)
	resp, err := client.GetBillActions(context.Background(), 119, "hr", 1)
	if err != nil {
		t.Fatalf("expected the retry to succeed, got error: %v", err)
	}
	if len(resp.Actions) != 1 || callCount.Load() != 2 {
		t.Errorf("got %d actions after %d calls, want 1 after 2", len(resp.Actions), callCount.Load())
	}
}

// A final HTTP error from the upstream client comes back as a permanent *upstream.StatusError,
// so callers (and #185's retry list) can tell a 404 from a transient failure.
func TestClient_NotFoundIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	client := congress.NewClientWithBaseURL(upstreamClient(t, srv.URL), srv.URL)
	_, err := client.GetBill(context.Background(), 119, "hr", 99999)
	if !upstream.IsPermanent(err) {
		t.Errorf("err = %v, want a permanent upstream error", err)
	}
	if err != nil && strings.Contains(err.Error(), "?") {
		t.Errorf("error leaks the query: %q", err)
	}
}

func TestClient_GetBillActions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"actions": [{"actionDate": "2025-01-03", "text": "Introduced"}]}`)
	}))
	defer srv.Close()

	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)
	resp, err := client.GetBillActions(context.Background(), 119, "hr", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Actions) != 1 {
		t.Errorf("expected 1 action, got %d", len(resp.Actions))
	}
}
