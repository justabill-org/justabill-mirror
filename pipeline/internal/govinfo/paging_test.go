package govinfo_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
)

// A real offsetMark from GovInfo (2026-09-26): base64 with characters that need escaping.
const secondMark = "AoJw+Pao7qADMkJJTExTLTExOXNyZXM5MTBpcw=="

// pageServer serves one collection page per offsetMark and records the marks it was asked for.
type pageServer struct {
	*httptest.Server

	marks []string
}

// newPageServer serves pages[mark] for each offsetMark. A page's nextPage may use {{base}}
// for the server's own URL and {{host}} for its host and port.
func newPageServer(t *testing.T, pages map[string]string) *pageServer {
	t.Helper()
	ps := &pageServer{}
	ps.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mark := r.URL.Query().Get("offsetMark")
		ps.marks = append(ps.marks, mark)
		if got := r.URL.Query().Get("pageSize"); got != "1000" {
			t.Errorf("pageSize = %q, want 1000", got)
		}
		page, ok := pages[mark]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, strings.NewReplacer("{{base}}", ps.URL, "{{host}}", r.Host).Replace(page))
	}))
	t.Cleanup(ps.Close)
	return ps
}

func nextPage(base, mark string) string {
	return base + "/collections/BILLS/2026-09-19T18:37:07Z?offsetMark=" + url.QueryEscape(mark) + "&pageSize=1000"
}

func packages(ids ...string) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = fmt.Sprintf(`{"packageId": %q}`, id)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func poll(t *testing.T, baseURL string) (*govinfo.CollectionResponse, error) {
	t.Helper()
	client := govinfo.NewClientWithBaseURL(&http.Client{}, baseURL)
	return client.PollChanges(context.Background(), "BILLS", time.Date(2026, 9, 19, 18, 37, 7, 0, time.UTC))
}

func packageIDs(resp *govinfo.CollectionResponse) []string {
	ids := make([]string, len(resp.Packages))
	for i, p := range resp.Packages {
		ids[i] = p.PackageID
	}
	return ids
}

func TestClient_PollChanges_FollowsOffsetMark(t *testing.T) {
	srv := newPageServer(t, map[string]string{
		"*": fmt.Sprintf(`{"count": 3, "packages": %s, "nextPage": %q}`,
			packages("BILLS-119hr1ih", "BILLS-119hr2ih"), nextPage("{{base}}", secondMark)),
		secondMark: fmt.Sprintf(`{"count": 7, "packages": %s, "nextPage": null}`, packages("BILLS-119s5is")),
	})

	resp, err := poll(t, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(packageIDs(resp), ","), "BILLS-119hr1ih,BILLS-119hr2ih,BILLS-119s5is"; got != want {
		t.Errorf("packages = %s, want %s", got, want)
	}
	if resp.Count != 3 || resp.NextPage != "" {
		t.Errorf("count = %d, nextPage = %q; want 3 and empty", resp.Count, resp.NextPage)
	}
	if got, want := strings.Join(srv.marks, " "), "* "+secondMark; got != want {
		t.Errorf("offsetMarks sent = %q, want %q", got, want)
	}
}

// An exactly full last page still has a nextPage, which returns no packages (seen on GovInfo
// with pageSize equal to count).
func TestClient_PollChanges_StopsOnEmptyPage(t *testing.T) {
	srv := newPageServer(t, map[string]string{
		"*": fmt.Sprintf(`{"count": 1, "packages": %s, "nextPage": %q}`,
			packages("BILLS-119hr1ih"), nextPage("{{base}}", secondMark)),
		secondMark: fmt.Sprintf(`{"count": 0, "packages": [], "nextPage": %q}`, nextPage("{{base}}", "third")),
	})

	resp, err := poll(t, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Packages) != 1 || len(srv.marks) != 2 {
		t.Errorf("got %d packages in %d requests, want 1 in 2", len(resp.Packages), len(srv.marks))
	}
	// Count covers every package returned, not the empty last page's count.
	if resp.Count != 1 {
		t.Errorf("count = %d, want 1", resp.Count)
	}
}

func TestClient_PollChanges_PageErrorReturnsNothing(t *testing.T) {
	srv := newPageServer(t, map[string]string{
		"*": fmt.Sprintf(`{"packages": %s, "nextPage": %q}`, packages("BILLS-119hr1ih"), nextPage("{{base}}", "gone")),
	})

	resp, err := poll(t, srv.URL)
	if err == nil || !strings.Contains(err.Error(), "collection page 2") {
		t.Fatalf("err = %v, want an error naming page 2", err)
	}
	if resp != nil {
		t.Errorf("resp = %+v, want nil", resp)
	}
}

func TestClient_PollChanges_NextPageOriginCheck(t *testing.T) {
	other := newPageServer(t, map[string]string{"x": `{"packages": []}`})
	cases := map[string]string{
		"another host":     nextPage(other.URL, "x"),
		"another scheme":   nextPage("https://{{host}}", "x"),
		"no offsetMark":    "{{base}}/collections/BILLS/2026-09-19T18:37:07Z?pageSize=1000",
		"unparseable link": "%zz",
	}
	for name, next := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newPageServer(t, map[string]string{
				"*": fmt.Sprintf(`{"packages": %s, "nextPage": %q}`, packages("BILLS-119hr1ih"), next),
			})

			resp, err := poll(t, srv.URL)
			if err == nil || resp != nil {
				t.Fatalf("resp = %+v, err = %v; want nil and an error", resp, err)
			}
			if len(srv.marks) != 1 {
				t.Errorf("page server saw %d requests, want 1", len(srv.marks))
			}
		})
	}
	if len(other.marks) != 0 {
		t.Errorf("the other host saw %d requests, want 0", len(other.marks))
	}
}

func TestClient_PollChanges_PageCap(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		fmt.Fprintf(w, `{"packages": %s, "nextPage": %q}`,
			packages(fmt.Sprintf("BILLS-119hr%dih", requests)), nextPage("http://"+r.Host, strconv.Itoa(requests)))
	}))
	defer srv.Close()

	resp, err := poll(t, srv.URL)
	if !errors.Is(err, govinfo.ErrPageCap) {
		t.Fatalf("err = %v, want ErrPageCap", err)
	}
	if resp != nil {
		t.Errorf("resp = %+v, want nil", resp)
	}
	if requests != 20 {
		t.Errorf("requests = %d, want 20", requests)
	}
}
