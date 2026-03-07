package main

import (
	"os"
	"path/filepath"
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
	if cfg.Server.MemoryCacheMB != 512 {
		t.Fatalf("unexpected memory cache size: %d", cfg.Server.MemoryCacheMB)
	}
	if len(cfg.Libraries) != 3 {
		t.Fatalf("expected 3 libraries, got %d", len(cfg.Libraries))
	}
	if cfg.Libraries[0].Kind != "local" || cfg.Libraries[1].Kind != "smb" || cfg.Libraries[2].Kind != "webdav" {
		t.Fatalf("unexpected library kinds: %#v", cfg.Libraries)
	}
}

func TestLoadConfigWithUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	content := "\uFEFF[server]\nlisten = \"0.0.0.0:34123\"\ntoken = \"sk-buste\"\nmemory_cache_mb = 1024\n\n[[libraries]]\nid = \"local-main\"\nkind = \"local\"\nroot = \"Y:/comic\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig with BOM: %v", err)
	}
	if cfg.Server.Token != "sk-buste" {
		t.Fatalf("unexpected token: %s", cfg.Server.Token)
	}
	if cfg.Server.MemoryCacheMB != 1024 {
		t.Fatalf("unexpected memory cache size: %d", cfg.Server.MemoryCacheMB)
	}
	if len(cfg.Libraries) != 1 {
		t.Fatalf("expected 1 library, got %d", len(cfg.Libraries))
	}
	if cfg.Libraries[0].ID != "local-main" {
		t.Fatalf("unexpected library id: %s", cfg.Libraries[0].ID)
	}
}
