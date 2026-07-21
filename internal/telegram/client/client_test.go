package client

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

// validKey returns a 32-byte key of the length required by the encrypted
// session storage (AES-256).
func validKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func validConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		APIID:       123,
		APIHash:     "abc",
		SessionPath: filepath.Join(t.TempDir(), "session.bin"),
		Key:         validKey(),
	}
}

func TestNew_ValidConfig(t *testing.T) {
	c, err := New(validConfig(t))
	if err != nil {
		t.Fatalf("New returned unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil *Client with no error")
	}

	// The dispatcher must be usable: registering a handler must not panic and
	// the value must be a real, initialised UpdateDispatcher (its internal map
	// is allocated by tg.NewUpdateDispatcher).
	d := c.Dispatcher()
	d.OnNewMessage(func(_ context.Context, _ tg.Entities, _ *tg.UpdateNewMessage) error {
		return nil
	})
}

func TestNew_ValidConfigWithProxy(t *testing.T) {
	cfg := validConfig(t)
	cfg.ProxyURL = "socks5://user:pass@127.0.0.1:1080"

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New with valid socks5 proxy returned error: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil *Client with no error")
	}
}

func TestNew_Validation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero APIID", func(c *Config) { c.APIID = 0 }},
		{"negative APIID", func(c *Config) { c.APIID = -1 }},
		{"empty APIHash", func(c *Config) { c.APIHash = "" }},
		{"empty SessionPath", func(c *Config) { c.SessionPath = "" }},
		{"nil Key", func(c *Config) { c.Key = nil }},
		{"short Key", func(c *Config) { c.Key = make([]byte, 16) }},
		{"long Key", func(c *Config) { c.Key = make([]byte, 64) }},
		{"invalid proxy URL", func(c *Config) { c.ProxyURL = "://not a url" }},
		{"unsupported proxy scheme", func(c *Config) { c.ProxyURL = "http://127.0.0.1:8080" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig(t)
			tt.mutate(&cfg)
			c, err := New(cfg)
			if err == nil {
				t.Fatalf("New(%s) expected error, got nil", tt.name)
			}
			if c != nil {
				t.Fatalf("New(%s) returned non-nil *Client alongside error", tt.name)
			}
		})
	}
}

// TestRun_CancelledContext verifies that Run honours a context that is already
// cancelled without needing credentials or a live network: it returns fast and
// never invokes the callback. We deliberately do not assert on the returned
// error value: this gotd version tears the run loop down cleanly and returns
// nil on cancellation, so asserting a specific context error would be brittle.
// The load-bearing guarantees are (a) Run returns promptly (no hang) and (b)
// the callback is not run once the context is already done.
func TestRun_CancelledContext(t *testing.T) {
	c, err := New(validConfig(t))
	if err != nil {
		t.Fatalf("New returned unexpected error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var callbackRan atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, func(ctx context.Context) error {
			callbackRan.Store(true)
			return errors.New("callback must not run on a cancelled context")
		})
	}()

	select {
	case <-done:
		if callbackRan.Load() {
			t.Fatal("Run invoked the callback despite an already-cancelled context")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s on a cancelled context")
	}
}
