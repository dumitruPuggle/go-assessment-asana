package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock lets limiter tests run instantly: sleep advances time.
type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	slept []time.Duration
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slept = append(f.slept, d)
	f.t = f.t.Add(d)
	return nil
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func newFakeLimiter(n int, window time.Duration) (*rateLimiter, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l := newRateLimiter(n, window)
	l.now, l.sleep = clk.now, clk.sleep
	return l, clk
}

func TestRateLimiterAllowsBurstUpToLimit(t *testing.T) {
	l, clk := newFakeLimiter(150, time.Minute)
	for i := 0; i < 150; i++ {
		if err := l.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(clk.slept) != 0 {
		t.Fatalf("slept %v within limit, want no waits", clk.slept)
	}
}

func TestRateLimiterBlocksRequestOverLimit(t *testing.T) {
	l, clk := newFakeLimiter(150, time.Minute)
	for i := 0; i < 150; i++ {
		_ = l.wait(context.Background())
	}
	clk.advance(10 * time.Second)

	if err := l.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The 151st request must wait until the 1st is a full minute old.
	if len(clk.slept) != 1 || clk.slept[0] != 50*time.Second {
		t.Fatalf("slept %v, want [50s]", clk.slept)
	}
}

func TestRateLimiterNeverExceedsLimitInAnyWindow(t *testing.T) {
	const n = 5
	window := time.Minute
	l, clk := newFakeLimiter(n, window)

	// Irregular arrivals, including bursts and idle gaps.
	gaps := []time.Duration{0, 0, 0, 1 * time.Second, 0, 0, 0, 30 * time.Second, 0, 0,
		2 * time.Minute, 0, 0, 0, 0, 0, 0, 5 * time.Second, 0, 0}
	var starts []time.Time
	for _, g := range gaps {
		clk.advance(g)
		if err := l.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		starts = append(starts, clk.now())
	}
	// Any n+1 consecutive starts must span at least one full window.
	for i := n; i < len(starts); i++ {
		if span := starts[i].Sub(starts[i-n]); span < window {
			t.Fatalf("requests %d..%d started within %v (< %v)", i-n, i, span, window)
		}
	}
}

func TestRateLimiterConcurrentCallers(t *testing.T) {
	const n = 10
	l, _ := newFakeLimiter(n, time.Minute)
	// Freeze time: all callers arrive at once, and sleeps are only recorded.
	var mu sync.Mutex
	waits := map[time.Duration]int{}
	l.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		waits[d]++
		return nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 3*n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.wait(context.Background())
		}()
	}
	wg.Wait()

	// 30 simultaneous requests at 10/min: 10 go now, 10 wait 1m, 10 wait 2m.
	if waits[time.Minute] != n || waits[2*time.Minute] != n || len(waits) != 2 {
		t.Fatalf("waits = %v, want %d×1m and %d×2m", waits, n, n)
	}
}

func TestRateLimiterRespectsContext(t *testing.T) {
	l := newRateLimiter(1, time.Hour)
	if err := l.wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := l.wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("wait did not return promptly on context cancellation")
	}
}

func TestWithRateLimitValidation(t *testing.T) {
	if _, err := New(testToken, WithRateLimit(0, time.Minute)); err == nil {
		t.Error("expected error for n=0")
	}
	if _, err := New(testToken, WithRateLimit(10, 0)); err == nil {
		t.Error("expected error for zero window")
	}
}

func TestDefaultRateLimitIs150PerMinute(t *testing.T) {
	c, err := New(testToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.limiter.slots) != 150 || c.limiter.window != time.Minute {
		t.Fatalf("limit = %d per %v, want 150 per 1m", len(c.limiter.slots), c.limiter.window)
	}
}

// End-to-end: a paginated call that exceeds the limit is throttled across pages.
func TestClientThrottlesPaginatedRequests(t *testing.T) {
	const window = 150 * time.Millisecond
	var hits atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			fmt.Fprintf(w, `{"data":[{"gid":"%d"}],"next_page":{"offset":"o%d"}}`, n, n)
			return
		}
		fmt.Fprint(w, `{"data":[{"gid":"3"}]}`)
	}, WithRateLimit(2, window))

	start := time.Now()
	ws, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 {
		t.Fatalf("got %d workspaces, want 3", len(ws))
	}
	if elapsed := time.Since(start); elapsed < window {
		t.Fatalf("3 requests at 2/%v took %v; limiter did not throttle", window, elapsed)
	}
}
