// Package retry provides network resilience primitives for Telegram (MTProto)
// calls: exponential backoff, injectable jitter, FLOOD_WAIT detection and a
// context-aware retry loop. Sleeps are cancellable via context so callers never
// block past a shutdown signal.
package retry

import (
	"context"
	"math/rand"
	"time"

	"github.com/gotd/td/tgerr"
)

// Policy configures the retry loop used by Do.
type Policy struct {
	// MaxAttempts is the total number of times fn may be called. Values < 1
	// are treated as 1 (a single attempt, no retry).
	MaxAttempts int
	// Base is the initial backoff delay (delay for attempt 0).
	Base time.Duration
	// Max caps the backoff delay.
	Max time.Duration
}

// Backoff returns the deterministic exponential backoff delay for the given
// attempt: base * 2^attempt, capped at max. attempt starts at 0, so attempt 0
// yields base, attempt 1 yields 2*base, and so on. It carries no jitter.
// Negative attempts are treated as 0.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := base
	for i := 0; i < attempt; i++ {
		// Cap early to avoid time.Duration (int64) overflow on large attempts.
		if d >= max {
			return max
		}
		d *= 2
	}
	if d > max {
		return max
	}
	return d
}

// Jitter applies full jitter to d, returning a random duration in [0, d] using
// the supplied *rand.Rand. Passing a seeded *rand.Rand makes the result
// reproducible, which keeps tests deterministic. A non-positive d returns 0.
func Jitter(d time.Duration, r *rand.Rand) time.Duration {
	if d <= 0 {
		return 0
	}
	// Int63n's argument is exclusive; +1 makes the upper bound d inclusive.
	return time.Duration(r.Int63n(int64(d) + 1))
}

// FloodWaitSeconds extracts the wait duration, in whole seconds, from a Telegram
// FLOOD_WAIT error (including wrapped errors). It returns (0, false) when err is
// nil or is not a flood-wait error. Detection uses gotd's tgerr.AsFloodWait,
// which unwraps the error chain and recognises FLOOD_WAIT / FLOOD_PREMIUM_WAIT.
func FloodWaitSeconds(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		return int(d / time.Second), true
	}
	return 0, false
}

// Do calls fn, retrying on error according to p. Between attempts it waits: for
// a FLOOD_WAIT error it honours the server-mandated delay, otherwise it uses the
// exponential Backoff for the current attempt. The wait is cancellable via ctx.
// Do returns nil on the first success, the last error once MaxAttempts is
// exhausted, or ctx.Err() if the context is done. fn is never called after the
// context is cancelled.
func Do(ctx context.Context, p Policy, fn func() error) error {
	attempts := p.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		err = fn()
		if err == nil {
			return nil
		}

		// No wait after the final attempt; return the last error.
		if attempt == attempts-1 {
			break
		}

		wait := Backoff(attempt, p.Base, p.Max)
		if n, ok := FloodWaitSeconds(err); ok {
			wait = time.Duration(n) * time.Second
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	return err
}
