package retry

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"
)

func TestBackoffGrowsAndSaturates(t *testing.T) {
	base := 1 * time.Second
	max := 10 * time.Second

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 10 * time.Second},  // 16s capped at 10s
		{5, 10 * time.Second},  // stays capped
		{40, 10 * time.Second}, // no overflow, stays capped
	}
	for _, c := range cases {
		if got := Backoff(c.attempt, base, max); got != c.want {
			t.Errorf("Backoff(%d, %v, %v) = %v, want %v", c.attempt, base, max, got, c.want)
		}
	}
}

func TestBackoffNegativeAttempt(t *testing.T) {
	base := 500 * time.Millisecond
	max := 5 * time.Second
	if got := Backoff(-3, base, max); got != base {
		t.Errorf("Backoff(-3, ...) = %v, want %v", got, base)
	}
}

func TestJitterReproducibleAndBounded(t *testing.T) {
	d := 100 * time.Millisecond

	// Same seed must reproduce the same sequence.
	r1 := rand.New(rand.NewSource(1))
	r2 := rand.New(rand.NewSource(1))
	for i := 0; i < 100; i++ {
		a := Jitter(d, r1)
		b := Jitter(d, r2)
		if a != b {
			t.Fatalf("Jitter not reproducible at i=%d: %v != %v", i, a, b)
		}
		if a < 0 || a > d {
			t.Fatalf("Jitter out of [0,d] range: got %v, d=%v", a, d)
		}
	}
}

func TestJitterZeroDuration(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	if got := Jitter(0, r); got != 0 {
		t.Errorf("Jitter(0, r) = %v, want 0", got)
	}
}

func TestFloodWaitSeconds(t *testing.T) {
	floodErr := tgerr.New(420, "FLOOD_WAIT_42")
	if n, ok := FloodWaitSeconds(floodErr); !ok || n != 42 {
		t.Errorf("FloodWaitSeconds(FLOOD_WAIT_42) = (%d, %t), want (42, true)", n, ok)
	}

	// Wrapped flood-wait error is still detected (unwrap chain).
	wrappedTyped := &wrapErr{msg: "outer", err: floodErr}
	if n, ok := FloodWaitSeconds(wrappedTyped); !ok || n != 42 {
		t.Errorf("FloodWaitSeconds(wrapped) = (%d, %t), want (42, true)", n, ok)
	}

	// Non-flood error → (0, false).
	if n, ok := FloodWaitSeconds(errors.New("boom")); ok || n != 0 {
		t.Errorf("FloodWaitSeconds(boom) = (%d, %t), want (0, false)", n, ok)
	}

	// A different RPC error → (0, false).
	other := tgerr.New(400, "BAD_REQUEST")
	if n, ok := FloodWaitSeconds(other); ok || n != 0 {
		t.Errorf("FloodWaitSeconds(BAD_REQUEST) = (%d, %t), want (0, false)", n, ok)
	}

	// nil error → (0, false).
	if n, ok := FloodWaitSeconds(nil); ok || n != 0 {
		t.Errorf("FloodWaitSeconds(nil) = (%d, %t), want (0, false)", n, ok)
	}
}

type wrapErr struct {
	msg string
	err error
}

func (w *wrapErr) Error() string { return w.msg + ": " + w.err.Error() }
func (w *wrapErr) Unwrap() error { return w.err }

func TestDoSucceedsAfterRetries(t *testing.T) {
	p := Policy{MaxAttempts: 5, Base: 1 * time.Millisecond, Max: 10 * time.Millisecond}
	calls := 0
	err := Do(context.Background(), p, func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("fn called %d times, want 3", calls)
	}
}

func TestDoExhaustsAttempts(t *testing.T) {
	p := Policy{MaxAttempts: 4, Base: 1 * time.Millisecond, Max: 5 * time.Millisecond}
	calls := 0
	sentinel := errors.New("always")
	err := Do(context.Background(), p, func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Do returned %v, want sentinel", err)
	}
	if calls != 4 {
		t.Fatalf("fn called %d times, want 4", calls)
	}
}

func TestDoRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before running

	p := Policy{MaxAttempts: 100, Base: 1 * time.Second, Max: 30 * time.Second}
	calls := 0
	err := Do(ctx, p, func() error {
		calls++
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do returned %v, want context.Canceled", err)
	}
	if calls > 1 {
		t.Fatalf("fn called %d times, want <= 1 (no attempt storm)", calls)
	}
}

func TestDoCancelDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := Policy{MaxAttempts: 100, Base: 500 * time.Millisecond, Max: 30 * time.Second}
	calls := 0
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := Do(ctx, p, func() error {
		calls++
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do returned %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Do did not abort during backoff, took %v", elapsed)
	}
}

func TestDoDefaultsMaxAttempts(t *testing.T) {
	p := Policy{MaxAttempts: 0, Base: 1 * time.Millisecond, Max: 5 * time.Millisecond}
	calls := 0
	err := Do(context.Background(), p, func() error {
		calls++
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("Do returned nil, want error")
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1 (MaxAttempts<1 defaults to 1)", calls)
	}
}
