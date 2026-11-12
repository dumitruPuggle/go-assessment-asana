// Package client is a minimal Asana REST API client built on net/http.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the Asana REST API root.
const DefaultBaseURL = "https://app.asana.com/api/1.0"

// defaultPageSize is the page size requested from paginated endpoints (Asana allows 1-100).
const defaultPageSize = 100

// Default rate limit: Asana allows 150 requests per minute per token on free plans.
const (
	DefaultRateLimit  = 150
	DefaultRateWindow = time.Minute
)

// Client talks to the Asana API using a personal access token.
type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
	pageSize   int
	limiter    *rateLimiter
}

// Option customizes a Client.
type Option func(*Client) error

// WithBaseURL overrides the API root (useful for tests).
func WithBaseURL(raw string) Option {
	return func(c *Client) error {
		u, err := url.Parse(strings.TrimRight(raw, "/"))
		if err != nil {
			return fmt.Errorf("parse base url: %w", err)
		}
		if u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("base url %q must be absolute", raw)
		}
		c.baseURL = u
		return nil
	}
}

// WithHTTPClient replaces the underlying *http.Client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) error {
		if hc == nil {
			return errors.New("http client must not be nil")
		}
		c.httpClient = hc
		return nil
	}
}

// WithPageSize sets the page size for paginated requests (1-100).
func WithPageSize(n int) Option {
	return func(c *Client) error {
		if n < 1 || n > 100 {
			return fmt.Errorf("page size %d out of range 1-100", n)
		}
		c.pageSize = n
		return nil
	}
}

// WithRateLimit caps outgoing requests to at most n per window, measured over
// any rolling window. The default is 150 per minute.
func WithRateLimit(n int, window time.Duration) Option {
	return func(c *Client) error {
		if n < 1 || window <= 0 {
			return fmt.Errorf("invalid rate limit %d per %v", n, window)
		}
		c.limiter = newRateLimiter(n, window)
		return nil
	}
}

// New builds a Client. token is an Asana personal access token.
func New(token string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("asana token must not be empty")
	}
	base, _ := url.Parse(DefaultBaseURL)
	c := &Client{
		baseURL:    base,
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		pageSize:   defaultPageSize,
		limiter:    newRateLimiter(DefaultRateLimit, DefaultRateWindow),
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// envelope is Asana's standard response wrapper.
type envelope[T any] struct {
	Data     T         `json:"data"`
	NextPage *nextPage `json:"next_page"`
}

type nextPage struct {
	Offset string `json:"offset"`
}

// get performs a GET against path (relative to the base URL) and decodes the
// Asana envelope into out. It returns the next page offset, if any.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) (string, error) {
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	if err := c.limiter.wait(ctx); err != nil {
		return "", fmt.Errorf("GET %s: rate limiter: %w", path, err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", newAPIError(resp)
	}

	env := envelope[json.RawMessage]{}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return "", fmt.Errorf("decode response from %s: %w", path, err)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return "", fmt.Errorf("decode data from %s: %w", path, err)
	}
	if env.NextPage != nil {
		return env.NextPage.Offset, nil
	}
	return "", nil
}

// eachPage follows Asana offset pagination until exhausted, calling fn with
// each page as it arrives: each response's next_page.offset is sent back as
// the offset query parameter, and a null or absent next_page marks the last
// page. Only one page is held in memory at a time. If fn returns an error,
// pagination stops and that error is returned.
func eachPage[T any](ctx context.Context, c *Client, path string, query url.Values, fn func([]T) error) error {
	if query == nil {
		query = url.Values{}
	}
	query.Set("limit", fmt.Sprint(c.pageSize))

	seen := map[string]bool{}
	for {
		var page []T
		offset, err := c.get(ctx, path, query, &page)
		if err != nil {
			return err
		}
		if err := fn(page); err != nil {
			return err
		}
		if offset == "" {
			return nil
		}
		// Guard against a server bug looping us forever on the same page.
		if seen[offset] {
			return fmt.Errorf("GET %s: pagination repeated offset %q", path, offset)
		}
		seen[offset] = true
		query.Set("offset", offset)
	}
}

// listAll collects every page into one slice. Prefer eachPage for large sets.
func listAll[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	var all []T
	err := eachPage(ctx, c, path, query, func(page []T) error {
		all = append(all, page...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// rateLimiter is a sliding-window limiter: it guarantees no more than n
// requests start within any rolling window. Unlike a token bucket with burst n,
// it can never let 2n requests through in a single window.
//
// It keeps the start times of the last n reserved slots in a ring buffer. A new
// request may start no earlier than window after the slot it replaces (the
// n-th most recent), which bounds every window to n requests. It is safe for
// concurrent use.
type rateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	slots  []time.Time // ring buffer of the last n reserved start times
	next   int         // index of the oldest slot

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

func newRateLimiter(n int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		window: window,
		slots:  make([]time.Time, n),
		now:    time.Now,
		sleep:  sleepCtx,
	}
}

// wait blocks until the caller may send a request, or ctx is done.
func (l *rateLimiter) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	l.mu.Lock()
	now := l.now()
	slot := l.slots[l.next].Add(l.window)
	if slot.Before(now) {
		slot = now
	}
	// Reserve the slot before sleeping so concurrent callers queue behind it.
	// A cancelled caller still uses up its slot; that only makes the limiter
	// stricter, never looser.
	l.slots[l.next] = slot
	l.next = (l.next + 1) % len(l.slots)
	l.mu.Unlock()

	if d := slot.Sub(now); d > 0 {
		return l.sleep(ctx, d)
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// APIError is returned for non-2xx responses.
type APIError struct {
	StatusCode int
	Messages   []string
	// RetryAfter is set when Asana rate-limits the request (HTTP 429).
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	msg := http.StatusText(e.StatusCode)
	if len(e.Messages) > 0 {
		msg = strings.Join(e.Messages, "; ")
	}
	return fmt.Sprintf("asana api error (%d): %s", e.StatusCode, msg)
}

func newAPIError(resp *http.Response) *APIError {
	apiErr := &APIError{StatusCode: resp.StatusCode}

	var body struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if json.Unmarshal(raw, &body) == nil {
		for _, e := range body.Errors {
			if e.Message != "" {
				apiErr.Messages = append(apiErr.Messages, e.Message)
			}
		}
	}

	if ra := resp.Header.Get("Retry-After"); ra != "" {
		var secs int
		if _, err := fmt.Sscan(ra, &secs); err == nil {
			apiErr.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return apiErr
}
