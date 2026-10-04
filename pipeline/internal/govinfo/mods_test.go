package govinfo_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
)

func TestFetchMODS(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.URL.Path != "/packages/BILLS-119hr1ih/mods" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("<mods/>"))
	}))
	t.Cleanup(srv.Close)
	c := govinfo.NewClientWithBaseURL(srv.Client(), srv.URL)

	body, err := c.FetchMODS(t.Context(), "BILLS-119hr1ih")
	if err != nil || string(body) != "<mods/>" {
		t.Fatalf("FetchMODS = %q, %v", body, err)
	}
	if path != "/packages/BILLS-119hr1ih/mods" {
		t.Errorf("request path %q", path)
	}
	if _, err = c.FetchMODS(t.Context(), "BILLS-119hr2ih"); err == nil {
		t.Error("FetchMODS of a missing package succeeded")
	}
}
