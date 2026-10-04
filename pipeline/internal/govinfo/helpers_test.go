package govinfo_test

import (
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

const testKey = "test-key"

// upstreamClient returns an upstream client, like the pipeline's, that knows only the hosts
// of the given httptest server URLs. It sends testKey to the first one only, and its budget
// is fast enough that tests don't wait on it.
func upstreamClient(t *testing.T, keyed string, others ...string) *http.Client {
	t.Helper()
	budget, err := upstream.NewBudget("test", 1000, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	host := func(raw string) string {
		u, parseErr := url.Parse(raw)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return u.Host
	}
	h := upstream.Host{Budget: budget, AttemptTimeout: 5 * time.Second, MaxBodyBytes: 1 << 20}
	hosts := map[string]upstream.Host{}
	for _, o := range others {
		hosts[host(o)] = h
	}
	h.APIKey = testKey
	hosts[host(keyed)] = h
	c, err := upstream.NewClient(slog.New(slog.DiscardHandler), hosts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
