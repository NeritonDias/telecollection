// Package client wraps gotd's telegram.Client with the project's defaults:
// encrypted-at-rest session storage (internal/telegram/session), transparent
// FLOOD_WAIT handling at the transport layer (built on internal/telegram/retry)
// and an update dispatcher exposed for later phases to register handlers.
//
// New only constructs and configures the client; it performs no network I/O.
// The actual connection is established by Run. Secret material (APIHash, the
// session key and the session bytes) is never logged.
package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	tgauth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/telegram/retry"
	"github.com/telecollection/telecollection/internal/telegram/session"
)

// keyLen is the required length, in bytes, of the session encryption key
// (AES-256, as produced by crypto.DeriveKey).
const keyLen = 32

// defaultFloodRetries is how many times a single RPC call is retried after a
// server-mandated FLOOD_WAIT before giving up. Each retry waits the full
// duration the server asked for.
const defaultFloodRetries = 3

// Config holds the parameters required to build a Client. APIID/APIHash are the
// Telegram application credentials (my.telegram.org). Key must be a 32-byte key
// for the encrypted session storage. ProxyURL is optional; an empty string
// means a direct connection.
type Config struct {
	APIID       int
	APIHash     string
	SessionPath string
	Key         []byte // 32-byte key for encrypted session storage
	ProxyURL    string // optional; "" = direct
}

// Client is the project's Telegram client wrapper. It owns the configured
// gotd telegram.Client and the update dispatcher.
type Client struct {
	tg         *telegram.Client
	dispatcher tg.UpdateDispatcher

	// proxyURL is the validated SOCKS5 proxy address, if any. It is retained
	// for the dialer wiring described on socks5 below.
	proxyURL string
}

// New validates cfg and constructs a configured Client. It does not connect;
// call Run to establish the session. It returns an error for an invalid
// configuration or an unusable proxy URL.
func New(cfg Config) (*Client, error) {
	if cfg.APIID <= 0 {
		return nil, fmt.Errorf("client: APIID must be positive, got %d", cfg.APIID)
	}
	if cfg.APIHash == "" {
		return nil, errors.New("client: APIHash is required")
	}
	if cfg.SessionPath == "" {
		return nil, errors.New("client: SessionPath is required")
	}
	if len(cfg.Key) != keyLen {
		return nil, fmt.Errorf("client: Key must be %d bytes, got %d", keyLen, len(cfg.Key))
	}
	if cfg.ProxyURL != "" {
		if err := validateProxyURL(cfg.ProxyURL); err != nil {
			return nil, err
		}
	}

	// The dispatcher is passed to the client as the update handler and also
	// stored on Client. tg.UpdateDispatcher is a small struct backed by a map
	// allocated in NewUpdateDispatcher; copies share that map, so handlers
	// registered via Dispatcher() are seen by the client's copy.
	dispatcher := tg.NewUpdateDispatcher()

	opts := telegram.Options{
		SessionStorage: session.NewEncryptedStorage(cfg.SessionPath, cfg.Key),
		UpdateHandler:  dispatcher,
		Middlewares: []telegram.Middleware{
			floodWaitMiddleware{maxRetries: defaultFloodRetries},
		},
		// TODO(proxy): route DC dials through cfg.ProxyURL by setting
		// opts.Resolver = dcs.Plain(dcs.PlainOptions{Dial: <socks5 dialer>}).
		// Building the SOCKS5 dialer needs golang.org/x/net/proxy, which is
		// currently only an indirect module requirement; promoting it to a
		// direct dependency (a go.mod change) is out of scope for this task.
		// Until then the URL is validated and retained, and connections are
		// direct.
	}

	return &Client{
		tg:         telegram.NewClient(cfg.APIID, cfg.APIHash, opts),
		dispatcher: dispatcher,
		proxyURL:   cfg.ProxyURL,
	}, nil
}

// Run connects the client and invokes f once the session is ready, delegating
// to the underlying telegram.Client.Run. It blocks until f returns or ctx is
// cancelled.
func (c *Client) Run(ctx context.Context, f func(ctx context.Context) error) error {
	return c.tg.Run(ctx, f)
}

// Dispatcher returns the update dispatcher so later phases can register update
// handlers (e.g. OnNewMessage). The returned value shares state with the
// dispatcher the client invokes, so registrations take effect immediately.
func (c *Client) Dispatcher() tg.UpdateDispatcher {
	return c.dispatcher
}

// Auth exposes gotd's auth client for driving the login flow (code/2FA).
// It is only valid while the client is connected — i.e. called inside Run.
func (c *Client) Auth() *tgauth.Client {
	return c.tg.Auth()
}

// QR exposes gotd's QR login helper. Only valid inside Run.
func (c *Client) QR() qrlogin.QR {
	return c.tg.QR()
}

// floodWaitMiddleware is a telegram.Middleware that transparently honours
// Telegram FLOOD_WAIT responses at the transport layer. When the server asks
// the client to slow down, it waits the mandated duration and retries the same
// call, up to maxRetries times. Non-flood errors and successes pass straight
// through, so a call the server genuinely rejected is never blindly re-sent.
//
// FLOOD_WAIT detection reuses internal/telegram/retry (task 1.2), keeping a
// single source of truth for how flood-wait errors are recognised.
type floodWaitMiddleware struct {
	maxRetries int
}

// Handle implements telegram.Middleware.
func (m floodWaitMiddleware) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		var err error
		for attempt := 0; attempt <= m.maxRetries; attempt++ {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}

			err = next.Invoke(ctx, input, output)
			if err == nil {
				return nil
			}

			secs, ok := retry.FloodWaitSeconds(err)
			if !ok {
				return err
			}
			// No wait after the final permitted attempt; return the error.
			if attempt == m.maxRetries {
				break
			}

			// Add one second of margin, mirroring gotd's own flood waiter, to
			// avoid re-tripping the limit on the boundary.
			wait := time.Duration(secs)*time.Second + time.Second
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
}

// validateProxyURL parses proxyURL and confirms it names a supported proxy
// (SOCKS5). It returns an error for a malformed URL or an unsupported scheme so
// misconfiguration is caught at construction time rather than on first dial.
func validateProxyURL(proxyURL string) error {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return fmt.Errorf("client: parsing proxy URL: %w", err)
	}
	switch u.Scheme {
	case "socks5", "socks5h":
		return nil
	default:
		return fmt.Errorf("client: unsupported proxy scheme %q (only socks5 is supported)", u.Scheme)
	}
}
