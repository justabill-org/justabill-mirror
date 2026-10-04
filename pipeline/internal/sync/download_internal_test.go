package sync

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

// httpGet goes through the upstream client, which returns a final 404 as an error rather than
// a response. It must still wrap errNotFound, which the vote sync reads as "no roll calls yet".
func TestHTTPGet_UpstreamClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/evs/2026/index.asp" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<a href="?rollnumber=7">7</a>`)
	}))
	defer srv.Close()

	budget, err := upstream.NewBudget("house-clerk", 1000, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	hc, err := upstream.NewClient(slog.New(slog.DiscardHandler), map[string]upstream.Host{
		srv.Listener.Addr().String(): {Budget: budget, AttemptTimeout: 5 * time.Second, MaxBodyBytes: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{http: hc, logger: slog.New(slog.DiscardHandler)}

	body, err := s.httpGet(t.Context(), srv.URL+"/evs/2026/index.asp")
	if err != nil || string(body) != `<a href="?rollnumber=7">7</a>` {
		t.Fatalf("httpGet = %q, %v", body, err)
	}

	_, err = s.httpGet(t.Context(), srv.URL+"/evs/2027/index.asp")
	if !errors.Is(err, errNotFound) {
		t.Errorf("404: err = %v, want errNotFound", err)
	}
	if !upstream.IsPermanent(err) {
		t.Errorf("404: err = %v, want it to stay a permanent upstream error", err)
	}

	_, err = s.httpGet(t.Context(), "https://example.com/evs/2026/index.asp")
	if !errors.Is(err, upstream.ErrUnknownHost) {
		t.Errorf("undeclared host: err = %v, want ErrUnknownHost", err)
	}
}
