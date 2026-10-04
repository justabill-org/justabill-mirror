package upstream_test

import (
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

func TestNewBudgetValidates(t *testing.T) {
	for name, args := range map[string]struct {
		name              string
		rps               float64
		burst, reservePct int
	}{
		"no name":     {"", 1, 1, 5},
		"zero rate":   {"b", 0, 1, 5},
		"zero burst":  {"b", 1, 0, 5},
		"reserve 100": {"b", 1, 1, 100},
		"reserve < 0": {"b", 1, 1, -1},
	} {
		if _, err := upstream.NewBudget(args.name, args.rps, args.burst, args.reservePct); err == nil {
			t.Errorf("%s: NewBudget accepted it", name)
		}
	}
}

func TestRateIsEnforced(t *testing.T) {
	srv := newServer(t, statusThen())
	c := newClient(t, slog.New(slog.DiscardHandler),
		map[string]upstream.Host{srv.host(): host(newBudget(t, 20, 5, 0))}, upstream.Hooks{})

	start := time.Now()
	for range 50 {
		if _, err := get(t.Context(), c, srv.URL+"/", nil); err != nil {
			t.Fatal(err)
		}
	}
	// A burst of 5, then 45 more at 20/s.
	if elapsed := time.Since(start); elapsed < 2250*time.Millisecond {
		t.Errorf("50 requests at 20/s (burst 5) took %v, want at least 2.25s", elapsed)
	}
}

func TestRateLimitedCooldownDoublesAndCaps(t *testing.T) {
	b := newBudget(t, 1, 1, 0)
	upstream.SetCooldowns(b, 20*time.Millisecond, 50*time.Millisecond, time.Second)

	if d, started := upstream.RateLimited(b, 0, false); d != 20*time.Millisecond || !started {
		t.Errorf("first 429: %v, %v; want 20ms, started", d, started)
	}
	if d, started := upstream.RateLimited(b, 0, false); started || d > 20*time.Millisecond {
		t.Errorf("429 during a cooldown: %v, %v; want the running cooldown, not a new one", d, started)
	}
	time.Sleep(25 * time.Millisecond)
	if d, started := upstream.RateLimited(b, 0, false); d != 40*time.Millisecond || !started {
		t.Errorf("second cooldown: %v, %v; want 40ms, started", d, started)
	}
	time.Sleep(45 * time.Millisecond)
	if d, _ := upstream.RateLimited(b, 0, false); d != 50*time.Millisecond {
		t.Errorf("third cooldown: %v, want the 50ms cap", d)
	}
}

func TestSuccessResetsRateLimitedCooldown(t *testing.T) {
	srv := newServer(t, statusThen())
	b := fastBudget(t)
	upstream.SetCooldowns(b, 10*time.Millisecond, time.Second, time.Second)
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): host(b)}, upstream.Hooks{})

	upstream.RateLimited(b, 0, false) // next one would be 20ms
	time.Sleep(15 * time.Millisecond)
	if _, err := get(t.Context(), c, srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if d, _ := upstream.RateLimited(b, 0, false); d != 10*time.Millisecond {
		t.Errorf("cooldown after a success = %v, want it reset to 10ms", d)
	}
}

func TestQuotaAndReserveBrake(t *testing.T) {
	for name, tc := range map[string]struct {
		remaining    string
		wantCooldown bool
	}{
		"below the 5% reserve": {"40", true},
		"above the reserve":    {"100", false},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("X-Ratelimit-Limit", "1000")
				w.Header().Set("X-Ratelimit-Remaining", tc.remaining)
			})
			b := newBudget(t, 1000, 10, 5)
			upstream.SetCooldowns(b, time.Second, time.Second, 200*time.Millisecond)
			log, logs := newLogger()
			c := newClient(t, log, map[string]upstream.Host{srv.host(): host(b)}, upstream.Hooks{})

			if _, err := get(t.Context(), c, srv.URL+"/", nil); err != nil {
				t.Fatal(err)
			}
			q := b.Quota()
			if q.Limit != 1000 || q.ObservedAt.IsZero() {
				t.Errorf("quota = %+v, want limit 1000 observed", q)
			}
			if got := q.CooldownUntil.After(time.Now()); got != tc.wantCooldown {
				t.Errorf("cooldown running = %v, want %v", got, tc.wantCooldown)
			}
			if got := logs.count("quota_low") == 1; got != tc.wantCooldown {
				t.Errorf("quota_low logged = %v, want %v", got, tc.wantCooldown)
			}
		})
	}
}
