// Package revalidate tells the web app which bill pages a sync changed, so their cached copies
// are rendered fresh on the next visit instead of waiting out their revalidate time. It POSTs
// the bills' cache tags to the web app's /api/revalidate with a bearer secret
// (docs/design/297-revalidate-after-sync.md).
//
// The call is best effort: a failure is logged and never fails the sync, since the pages still
// refresh on their time-based schedule.
package revalidate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/secretfile"
)

const (
	// MaxTags is the most tags one call carries, the web route's limit. A run that changed more
	// bills sends [AllBillsTag] instead.
	MaxTags = 100
	// AllBillsTag is the tag on every cached bill read and list.
	AllBillsTag = "bills"
	// DefaultTimeout bounds one Flush, both attempts and the wait between them.
	DefaultTimeout = 10 * time.Second
	// DefaultRetryDelay is the wait before the one retry after a network error or a 5xx.
	DefaultRetryDelay = 2 * time.Second

	// billTagPrefix starts a single bill's tag: bill:hr-119-1.
	billTagPrefix = "bill:"
	// maxResponseBytes is how much of a response body is read, for the log line.
	maxResponseBytes = 512
)

// billIDPattern is the ID format the web route allows in a bill tag: type-congress-number. The
// route rejects the whole call if one tag doesn't match, so other IDs are dropped here.
var billIDPattern = regexp.MustCompile(`^[a-z]+-\d{1,3}-\d{1,5}$`)

// Client sends revalidation calls to the web app. A nil *Client is valid and does nothing, so
// callers don't branch on whether the hook is configured.
type Client struct {
	url        string
	secret     string
	http       *http.Client
	logger     *slog.Logger
	timeout    time.Duration
	retryDelay time.Duration
}

// Option configures a [Client].
type Option func(*Client)

// WithLogger sets the logger for the sent and failed lines. The default discards them.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.logger = l } }

// WithHTTPClient sets the [http.Client] the calls use. The default is a plain client; the
// pipeline's upstream client isn't used, since it only allows the data sources' hosts.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithTimeout sets how long one Flush may take in all. The default is [DefaultTimeout].
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// WithRetryDelay sets the wait before the retry. The default is [DefaultRetryDelay].
func WithRetryDelay(d time.Duration) Option { return func(c *Client) { c.retryDelay = d } }

// New returns a client that POSTs to endpoint with secret as its bearer token. With either one
// empty, the hook is off and New returns nil.
func New(endpoint, secret string, opts ...Option) *Client {
	if endpoint == "" || secret == "" {
		return nil
	}
	c := &Client{
		url:        endpoint,
		secret:     secret,
		http:       &http.Client{},
		logger:     slog.New(slog.DiscardHandler),
		timeout:    DefaultTimeout,
		retryDelay: DefaultRetryDelay,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// FromConfig builds a client from the pipeline's configuration, read through get (viper's
// GetString, with underscore keys):
//   - web_revalidate_url: the route, e.g. https://justabill.io/api/revalidate (http or https);
//   - web_revalidate_secret, or web_revalidate_secret_file naming a file that holds it.
//
// With the URL or the secret missing it returns nil, and the hook is off. Setting both forms of
// the secret, an unreadable file or a URL that isn't absolute http(s) is an error.
func FromConfig(get func(key string) string, opts ...Option) (*Client, error) {
	endpoint := strings.TrimSpace(get("web_revalidate_url"))
	secret, err := secretfile.Resolve(get, "web_revalidate_secret")
	if err != nil {
		return nil, err
	}
	if endpoint != "" {
		u, parseErr := url.Parse(endpoint)
		if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("WEB_REVALIDATE_URL %q: want an absolute http or https URL", endpoint)
		}
	}
	return New(endpoint, secret, opts...), nil
}

// Tags returns the cache tags for billIDs: bill:<id> for each distinct, well-formed ID in
// sorted order, or just [AllBillsTag] when there are more than [MaxTags]. IDs the web route
// wouldn't accept are left out.
func Tags(billIDs []string) []string {
	tags := make([]string, 0, len(billIDs))
	for _, id := range billIDs {
		if billIDPattern.MatchString(id) {
			tags = append(tags, billTagPrefix+id)
		}
	}
	slices.Sort(tags)
	tags = slices.Compact(tags)
	if len(tags) > MaxTags {
		return []string{AllBillsTag}
	}
	return tags
}

// Flush tells the web app that billIDs changed. It returns once the call succeeded, failed
// twice, failed with a 4xx, or ran out of time; it logs the outcome and never returns an error.
// It retries once, after the retry delay, on a network error or a 5xx. The client's timeout
// applies on top of ctx's deadline, so callers that must send even after a shutdown started
// pass [context.WithoutCancel].
func (c *Client) Flush(ctx context.Context, billIDs []string) {
	if c == nil || len(billIDs) == 0 {
		return
	}
	tags := Tags(billIDs)
	if len(tags) == 0 {
		c.logger.WarnContext(ctx, "web revalidation skipped: no valid bill IDs", "bills", len(billIDs))
		return
	}
	body, err := json.Marshal(struct {
		Tags []string `json:"tags"`
	}{tags})
	if err != nil {
		c.logger.WarnContext(ctx, "web revalidation failed", "bills", len(billIDs), "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	status, err := c.post(ctx, body)
	if retryable(status, err) {
		c.logger.InfoContext(ctx, "web revalidation retrying", "status", status, "error", err)
		select {
		case <-time.After(c.retryDelay):
			status, err = c.post(ctx, body)
		case <-ctx.Done():
			err = errors.Join(err, ctx.Err())
		}
	}
	if err != nil {
		c.logger.WarnContext(ctx, "web revalidation failed", "bills", len(billIDs), "tags", len(tags),
			"status", status, "error", err)
		return
	}
	c.logger.InfoContext(ctx, "web revalidation sent", "bills", len(billIDs), "tags", len(tags))
}

// statusError is a response outside 2xx.
type statusError struct {
	status int
	body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("revalidate: HTTP %d: %s", e.status, e.body)
}

// post sends one attempt and returns the response status (0 without a response) and an error
// for a failed call or a status outside 2xx.
func (c *Client) post(ctx context.Context, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("revalidate: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("revalidate: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return resp.StatusCode, &statusError{status: resp.StatusCode, body: strings.TrimSpace(string(snippet))}
	}
	return resp.StatusCode, nil
}

// retryable reports whether an attempt is worth one more try: a network error or a 5xx. A 4xx
// (bad secret, rejected tags, route off) won't change on a retry.
func retryable(status int, err error) bool {
	return err != nil && (status == 0 || status >= http.StatusInternalServerError)
}
