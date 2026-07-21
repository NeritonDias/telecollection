// Package config loads and validates TeleCollection runtime configuration.
//
// Precedence today: environment (TELECOL_*) over defaults. Layered file/flag
// config (koanf) will be introduced when the desktop/server settings UIs need it
// — env-only is sufficient for the foundation and keeps the dependency graph clean.
package config

import (
	"errors"
	"fmt"
	"os"
)

// Mode selects how the app runs. Desktop keeps everything local; server is the
// self-hosted daemon.
type Mode string

// Supported run modes.
const (
	ModeDesktop Mode = "desktop"
	ModeServer  Mode = "server"
)

// Config is the validated runtime configuration.
type Config struct {
	Mode        Mode   // desktop | server
	HTTPAddr    string // listen address (server mode)
	DataDir     string // local data dir for SQLite/session (desktop mode)
	DatabaseURL string // Postgres DSN (server mode)
	APIKeyHash  string // sha256 hex of the local API key (optional)
}

// Load reads configuration from the environment and validates it.
func Load() (Config, error) {
	c := Config{
		Mode:        Mode(getenv("TELECOL_MODE", string(ModeDesktop))),
		HTTPAddr:    getenv("TELECOL_HTTP_ADDR", ""),
		DataDir:     getenv("TELECOL_DATA_DIR", ""),
		DatabaseURL: getenv("TELECOL_DATABASE_URL", ""),
		APIKeyHash:  getenv("TELECOL_API_KEY_HASH", ""),
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate reports whether the configuration is internally consistent.
func (c Config) Validate() error {
	switch c.Mode {
	case ModeDesktop:
		if c.DataDir == "" {
			return errors.New("config: desktop mode requires DataDir")
		}
	case ModeServer:
		if c.DatabaseURL == "" {
			return errors.New("config: server mode requires DatabaseURL")
		}
		if c.HTTPAddr == "" {
			return errors.New("config: server mode requires HTTPAddr")
		}
	default:
		return fmt.Errorf("config: unknown mode %q", c.Mode)
	}
	return nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
