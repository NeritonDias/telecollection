package config

import "testing"

func TestValidate_RejectsUnknownMode(t *testing.T) {
	c := Config{Mode: "bogus", DataDir: "/tmp"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestValidate_ServerRequiresDatabaseURL(t *testing.T) {
	c := Config{Mode: ModeServer, HTTPAddr: ":8550"}
	if err := c.Validate(); err == nil {
		t.Fatal("server mode without DatabaseURL must fail")
	}
}

func TestValidate_DesktopRequiresDataDir(t *testing.T) {
	c := Config{Mode: ModeDesktop}
	if err := c.Validate(); err == nil {
		t.Fatal("desktop mode without DataDir must fail")
	}
}

func TestValidate_AcceptsValidConfigs(t *testing.T) {
	desktop := Config{Mode: ModeDesktop, DataDir: "/data"}
	if err := desktop.Validate(); err != nil {
		t.Fatalf("valid desktop config rejected: %v", err)
	}
	server := Config{Mode: ModeServer, HTTPAddr: ":8550", DatabaseURL: "postgres://x"}
	if err := server.Validate(); err != nil {
		t.Fatalf("valid server config rejected: %v", err)
	}
}

func TestLoad_FromEnv(t *testing.T) {
	t.Setenv("TELECOL_MODE", "server")
	t.Setenv("TELECOL_HTTP_ADDR", ":9000")
	t.Setenv("TELECOL_DATABASE_URL", "postgres://localhost/tc")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Mode != ModeServer {
		t.Fatalf("Mode = %q, want %q", c.Mode, ModeServer)
	}
	if c.HTTPAddr != ":9000" {
		t.Fatalf("HTTPAddr = %q, want :9000", c.HTTPAddr)
	}
}
