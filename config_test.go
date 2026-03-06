package main

import (
	"testing"
)

func TestLoadConfigExample(t *testing.T) {
	path := "server.example.toml"
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Server.Listen != "0.0.0.0:34123" {
		t.Fatalf("unexpected listen: %s", cfg.Server.Listen)
	}
	if cfg.Server.Token != "change-me" {
		t.Fatalf("unexpected token: %s", cfg.Server.Token)
	}
	if len(cfg.Libraries) != 3 {
		t.Fatalf("expected 3 libraries, got %d", len(cfg.Libraries))
	}
	if cfg.Libraries[0].Kind != "local" || cfg.Libraries[1].Kind != "smb" || cfg.Libraries[2].Kind != "webdav" {
		t.Fatalf("unexpected library kinds: %#v", cfg.Libraries)
	}
}
