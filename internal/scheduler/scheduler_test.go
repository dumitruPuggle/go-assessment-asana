package scheduler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dumitruPuggle/go-assessment-asana/internal/client"
)

// fakeClock drives the scheduler deterministically: sleep advances time
// instantly, and jobs can call advance to simulate how long they take.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func (f *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sleeps = append(f.sleeps, d)
	f.t = f.t.Add(d)
	return nil
}

func newFake(t *testing.T, interval time.Duration, job Job) (*Scheduler, *fakeClock) {
	t.Helper()
	s, err := New(interval, job)
	if err != nil {
		t.Fatal(err)
	}
	clk := newFakeClock()
	s.now, s.sleep = clk.now, clk.sleep
	return s, clk
}

// runWithDeadline runs s and fails the test (instead of hanging forever) if
// Run does not return within d. This is the backstop against infinite loops.
func runWithDeadline(t *testing.T, ctx context.Context, s *Scheduler, d time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(d):
		t.Fatalf("Run did not return within %v (infinite loop?)", d)
	}
}

func TestNewValidation(t *testing.T) {
	noop := func(context.Context) error { return nil }
	for _, d := range []time.Duration{0, -time.Second} {
		if _, err := New(d, noop); err == nil {
			t.Errorf("New(%v): expected error", d)
		}
	}
	if _, err := New(time.Second, nil); err == nil {
		t.Error("New(nil job): expected error")
	}
}

func TestRunsImmediatelyThenEveryInterval(t *testing.T) {
	for _, interval := range []time.Duration{30 * time.Second, 5 * time.Minute} {
		t.Run(interval.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var runs []time.Time
			var clk *fakeClock
			s, clk := newFake(t, interval, func(context.Context) error {
				runs = append(runs, clk.now())
				if len(runs) == 4 {
					cancel()
				}
				return nil
			})
			start := clk.now()
			runWithDeadline(t, ctx, s, time.Second)

			if len(runs) != 4 {
				t.Fatalf("runs = %d, want 4", len(runs))
			}
			for i, at := range runs {
				if want := start.Add(time.Duration(i) * interval); !at.Equal(want) {
					t.Errorf("run %d at +%v, want +%v", i+1, at.Sub(start), want.Sub(start))
				}
			}
		})
	}
}

func TestSkipsMissedRunsWhenJobOverruns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runs []time.Time
	var clk *fakeClock
	s, clk := newFake(t, 30*time.Second, func(context.Context) error {
		runs = append(runs, clk.now())
		if len(runs) == 1 {
			clk.advance(75 * time.Second) // overruns the 30s and 60s boundaries
		}
		if len(runs) == 2 {
			cancel()
		}
		return nil
	})
	start := clk.now()
	runWithDeadline(t, ctx, s, time.Second)

	// Run 2 lands on the next future boundary (+90s), not immediately at +75s
	// and not twice to "catch up".
	if got := runs[1].Sub(start); got != 90*time.Second {
		t.Fatalf("second run at +%v, want +90s", got)
	}
	if clk.sleeps[0] != 15*time.Second {
		t.Fatalf("first sleep = %v, want 15s", clk.sleeps[0])
	}
}

// The core infinite-loop guard: whatever the job duration (zero, exactly the
// interval, multiples of it, longer), every sleep is strictly positive and
// bounded by the interval, so the loop can never spin.
func TestNeverBusyLoops(t *testing.T) {
	const interval = 30 * time.Second
	durations := []time.Duration{0, time.Nanosecond, interval - 1, interval, interval + 1,
		2 * interval, 3*interval + 7*time.Second, 10 * time.Minute}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const totalRuns = 1000
	runs := 0
	var clk *fakeClock
	s, clk := newFake(t, interval, func(context.Context) error {
		clk.advance(durations[runs%len(durations)])
		runs++
		if runs == totalRuns {
			cancel()
		}
		return nil
	})
	runWithDeadline(t, ctx, s, 5*time.Second)

	if runs != totalRuns {
		t.Fatalf("runs = %d, want %d", runs, totalRuns)
	}
	for i, d := range clk.sleeps {
		if d <= 0 || d > interval {
			t.Fatalf("sleep %d = %v, want in (0, %v]", i, d, interval)
		}
	}
}

func TestContinuesAfterErrorAndPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var logs bytes.Buffer
	runs := 0
	s, _ := newFake(t, time.Second, func(context.Context) error {
		runs++
		switch runs {
		case 1:
			return errors.New("boom")
		case 2:
			panic("kaboom")
		}
		cancel()
		return nil
	})
	s.logger = slog.New(slog.NewTextHandler(&logs, nil))
	runWithDeadline(t, ctx, s, time.Second)

	if runs != 3 {
		t.Fatalf("runs = %d, want 3 (errors/panics must not stop the schedule)", runs)
	}
	out := logs.String()
	if !strings.Contains(out, "boom") || !strings.Contains(out, "job panicked: kaboom") {
		t.Fatalf("expected both failures logged, got:\n%s", out)
	}
}

func TestStopsPromptlyWhenCancelledWhileWaiting(t *testing.T) {
	ran := make(chan struct{}, 1)
	s, err := New(time.Hour, func(context.Context) error {
		ran <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-ran; cancel() }() // cancel during the 1h wait
	runWithDeadline(t, ctx, s, time.Second)
}

func TestStopsPromptlyWhenCancelledMidRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(time.Millisecond, func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	runWithDeadline(t, ctx, s, time.Second)
}

func TestAlreadyCancelledContextRunsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, _ := New(time.Second, func(context.Context) error {
		t.Error("job must not run on a cancelled context")
		return nil
	})
	runWithDeadline(t, ctx, s, time.Second)
}

// Real clock: a job slower than the interval must never run concurrently.
func TestRunsNeverOverlap(t *testing.T) {
	var inflight, maxInflight, runs atomic.Int32
	s, err := New(2*time.Millisecond, func(context.Context) error {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			m := maxInflight.Load()
			if n <= m || maxInflight.CompareAndSwap(m, n) {
				break
			}
		}
		runs.Add(1)
		time.Sleep(10 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	runWithDeadline(t, ctx, s, 2*time.Second)

	if maxInflight.Load() != 1 {
		t.Fatalf("max concurrent runs = %d, want 1", maxInflight.Load())
	}
	if runs.Load() < 2 {
		t.Fatalf("runs = %d, expected several", runs.Load())
	}
}

// End to end with the real client: a scheduler ticking far faster than the
// client's rate limit allows must still never exceed that limit, across runs.
func TestRespectsClientRateLimit(t *testing.T) {
	const (
		limit  = 4
		window = 300 * time.Millisecond
		slack  = 25 * time.Millisecond // network/scheduling jitter on timestamps
	)

	var mu sync.Mutex
	var hits []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, time.Now())
		mu.Unlock()
		// Two pages per run, so each extraction costs 2 requests.
		if r.URL.Query().Get("offset") == "" {
			fmt.Fprint(w, `{"data":[{"gid":"1"}],"next_page":{"offset":"p2"}}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"gid":"2"}]}`)
	}))
	defer srv.Close()

	c, err := client.New("tok", client.WithBaseURL(srv.URL), client.WithRateLimit(limit, window))
	if err != nil {
		t.Fatal(err)
	}

	var runs atomic.Int32
	s, err := New(5*time.Millisecond, func(ctx context.Context) error {
		runs.Add(1)
		_, err := c.ListWorkspaces(ctx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
	defer cancel()
	runWithDeadline(t, ctx, s, 3*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(hits) <= limit {
		t.Fatalf("only %d requests; test did not exercise the limit", len(hits))
	}
	for i := limit; i < len(hits); i++ {
		if span := hits[i].Sub(hits[i-limit]); span < window-slack {
			t.Fatalf("requests %d..%d within %v: more than %d per %v", i-limit, i, span, limit, window)
		}
	}
	// Unthrottled, a 5ms tick would run ~200 times in 1s; the limiter caps it
	// to roughly (1s / 300ms) * 4 requests / 2 per run ≈ 8 runs.
	if r := runs.Load(); r > 12 {
		t.Fatalf("runs = %d; scheduler was not held back by the rate limit", r)
	}
	t.Logf("%d runs, %d requests in 1s at %d req/%v", runs.Load(), len(hits), limit, window)
}

func TestLogsIntervalAtDebugLevel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var logs bytes.Buffer
	s, _ := newFake(t, 30*time.Second, func(context.Context) error { cancel(); return nil })
	s.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s.name = "extract"
	runWithDeadline(t, ctx, s, time.Second)

	out := logs.String()
	for _, want := range []string{`msg="scheduler started" scheduler=extract interval=30s`, `msg="run started"`} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q:\n%s", want, out)
		}
	}

	// Without -v (Info level) the debug lines are hidden.
	logs.Reset()
	ctx2, cancel2 := context.WithCancel(context.Background())
	s2, _ := newFake(t, 30*time.Second, func(context.Context) error { cancel2(); return nil })
	s2.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	runWithDeadline(t, ctx2, s2, time.Second)
	if logs.Len() != 0 {
		t.Errorf("expected no output at Info level, got:\n%s", logs.String())
	}
}
