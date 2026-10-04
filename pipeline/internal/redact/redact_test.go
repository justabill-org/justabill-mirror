package redact_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/justabill-org/justabill/pipeline/internal/redact"
)

const secret = "s3cr3tKEY123"

func TestString_RemovesKeys(t *testing.T) {
	cases := map[string]string{
		"query":        `Get "https://api.congress.gov/v3/bill/119?api_key=` + secret + `&format=json": EOF`,
		"query upper":  `https://api.govinfo.gov/x?API_KEY=` + secret,
		"header":       "X-Api-Key: " + secret,
		"header lower": "x-api-key:" + secret + " sent",
		"header equal": "X-API-KEY=" + secret,
		"json":         `{"X-Api-Key": "` + secret + `"}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := redact.String(in)
			if strings.Contains(got, secret) {
				t.Errorf("String(%q) = %q, still contains the key", in, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("String(%q) = %q, want a [REDACTED] placeholder", in, got)
			}
		})
	}
}

func TestString_KeepsSurroundingText(t *testing.T) {
	got := redact.String("https://h/p?api_key=" + secret + "&format=json")
	want := "https://h/p?api_key=[REDACTED]&format=json"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestString_Truncates(t *testing.T) {
	short := strings.Repeat("a", redact.MaxLen)
	if got := redact.String(short); got != short {
		t.Errorf("a %d-byte string was changed", redact.MaxLen)
	}

	// "é" is two bytes, so a cut at MaxLen would split a rune.
	long := "a" + strings.Repeat("é", redact.MaxLen)
	got := redact.String(long)
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Errorf("missing truncation suffix: %q", got[len(got)-20:])
	}
	body := strings.TrimSuffix(got, "…(truncated)")
	if len(body) > redact.MaxLen {
		t.Errorf("truncated body is %d bytes, want at most %d", len(body), redact.MaxLen)
	}
	if !utf8.ValidString(got) {
		t.Error("truncation split a UTF-8 sequence")
	}
}

func TestError(t *testing.T) {
	if got := redact.Error(nil); got != "" {
		t.Errorf("Error(nil) = %q, want empty", got)
	}
	err := fmt.Errorf("list bills: %w", errors.New("api_key="+secret))
	if got := redact.Error(err); got != "list bills: api_key=[REDACTED]" {
		t.Errorf("Error = %q", got)
	}
}

func TestURLError_StripsQuery(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // every request now fails in the transport

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, srv.URL+"/bill/119?api_key="+secret+"&offset=20#frag", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = http.DefaultClient.Do(req)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	msg := fmt.Errorf("wrapped: %w", redact.URLError(err)).Error()
	if strings.ContainsAny(msg, "?#") || strings.Contains(msg, secret) {
		t.Errorf("error still has the query: %q", msg)
	}
	if !strings.Contains(msg, srv.URL+"/bill/119") {
		t.Errorf("error lost the host and path: %q", msg)
	}
}

func TestURLError_OtherErrors(t *testing.T) {
	if redact.URLError(nil) != nil {
		t.Error("URLError(nil) != nil")
	}
	plain := errors.New("plain")
	if !errors.Is(redact.URLError(plain), plain) {
		t.Error("a non-url error was replaced")
	}
}

func TestStripQuery(t *testing.T) {
	cases := map[string]string{
		"https://h/p?a=1":    "https://h/p",
		"https://h/p#x":      "https://h/p",
		"https://h/p":        "https://h/p",
		"https://h/p?a=1#x?": "https://h/p",
	}
	for in, want := range cases {
		if got := redact.StripQuery(in); got != want {
			t.Errorf("StripQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
