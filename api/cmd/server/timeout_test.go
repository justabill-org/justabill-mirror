package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// timeoutScale shrinks the production deadlines for the timeout tests and
// keeps their ratio: 25 s and 30 s become 500 ms and 600 ms.
const timeoutScale = 50

// stalledBills is a BillRepo whose List outlives the request's deadline, like
// a Spanner read that hangs. With failAfter it then fails with the context's
// error, as the Spanner client does; without it, it answers late but well.
type stalledBills struct {
	repository.BillRepo

	failAfter bool
}

func (s stalledBills) List(ctx context.Context, _ model.ListParams) (*model.ListResult[model.Bill], error) {
	<-ctx.Done()
	if s.failAfter {
		return nil, fmt.Errorf("listing bills: %w", ctx.Err())
	}
	return &model.ListResult[model.Bill]{Items: []model.Bill{}}, nil
}

// syncBuffer is a [bytes.Buffer] the server's goroutines can log into while the
// test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// serveStalled serves the API's router, with bills behind it, through
// newServer with the request and write deadlines scaled down by timeoutScale.
// It returns the base URL, the access log and a stop function that waits for
// in-flight handlers to finish (so their log lines are written).
func serveStalled(t *testing.T, bills repository.BillRepo) (string, *syncBuffer, func()) {
	t.Helper()
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	h := handler.New(nil, handler.WithBills(bills))
	h.SetLogger(log)
	e := devEdge(t, 0)
	e.requestTimeout = requestTimeout / timeoutScale
	srv := newServer("", buildRouter(h, log, nil, e), defaultIdleTimeout)
	srv.WriteTimeout = writeTimeout / timeoutScale

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if serr := srv.Shutdown(ctx); serr != nil {
			t.Errorf("shutdown: %v", serr)
		}
	}
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String(), logs, stop
}

// accessStatus returns the status the access log ("request") line recorded.
func accessStatus(t *testing.T, logs string) int {
	t.Helper()
	sc := bufio.NewScanner(bytes.NewBufferString(logs))
	for sc.Scan() {
		var entry struct {
			Msg    string `json:"msg"`
			Status int    `json:"status"`
		}
		if err := json.Unmarshal(sc.Bytes(), &entry); err == nil && entry.Msg == "request" {
			return entry.Status
		}
	}
	t.Fatalf("no access log line in %q", logs)
	return 0
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("the client got no response: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// A request whose handler is still waiting when the request timeout passes
// gets a 504 the client can read, and the access log says 504. With the
// request timeout equal to the server's WriteTimeout, the client got EOF and
// the log a 500 nobody saw (#930).
func TestRequestTimeoutAnswers504(t *testing.T) {
	base, logs, stop := serveStalled(t, stalledBills{failAfter: true})

	if got := getStatus(t, base+"/api/v1/bills"); got != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", got)
	}
	stop()
	if got := accessStatus(t, logs.String()); got != http.StatusGatewayTimeout {
		t.Errorf("access log status = %d, want 504", got)
	}
}

// A handler that answers after the deadline without checking it still sends
// its 200, and the access log keeps that 200, not the 504 the timeout
// middleware tries to write afterwards.
func TestRequestTimeoutLogsTheStatusSent(t *testing.T) {
	base, logs, stop := serveStalled(t, stalledBills{})

	if got := getStatus(t, base+"/api/v1/bills"); got != http.StatusOK {
		t.Errorf("status = %d, want 200", got)
	}
	stop()
	if got := accessStatus(t, logs.String()); got != http.StatusOK {
		t.Errorf("access log status = %d, want 200", got)
	}
}
