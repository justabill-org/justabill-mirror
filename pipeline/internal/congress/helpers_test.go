package congress_test

import (
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

const testKey = "test-key"

// upstreamClient returns an upstream client, like the pipeline's, that knows only the host of
// the given httptest server URL and sends it testKey. Its budget is fast enough that tests
// don't wait on it.
func upstreamClient(t *testing.T, keyed string) *http.Client {
	t.Helper()
	budget, err := upstream.NewBudget("test", 1000, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(keyed)
	if err != nil {
		t.Fatal(err)
	}
	hosts := map[string]upstream.Host{u.Host: {
		Budget: budget, APIKey: testKey, AttemptTimeout: 5 * time.Second, MaxBodyBytes: 1 << 20,
	}}
	c, err := upstream.NewClient(slog.New(slog.DiscardHandler), hosts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
