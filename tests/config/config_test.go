package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	configpkg "venera_home_server/config"
)

func TestLoadConfigExample(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "server.example.toml")
	cfg, err := configpkg.LoadConfig(path)
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
	if cfg.Server.CacheMaxAgeHours != 168 || cfg.Server.CacheCleanupIntervalMinutes != 360 {
		t.Fatalf("unexpected cache cleanup config: %#v", cfg.Server)
	}
	if !cfg.EHBot.AutoRescan || cfg.EHBot.PollIntervalSeconds != 60 || cfg.EHBot.TargetLibraryID != "local-main" {
		t.Fatalf("unexpected ehbot config: %#v", cfg.EHBot)
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
	content := "\uFEFF[server]\nlisten = \"0.0.0.0:34123\"\ntoken = \"sk-buste\"\nmemory_cache_mb = 1024\n\n[ehbot]\nbase_url = \"https://ehbot.example.com\"\ntarget_library_id = \"local-main\"\n\n[[libraries]]\nid = \"local-main\"\nkind = \"local\"\nroot = \"Y:/comic\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := configpkg.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig with BOM: %v", err)
	}
	if cfg.Server.Token != "sk-buste" {
		t.Fatalf("unexpected token: %s", cfg.Server.Token)
	}
	if cfg.Server.MemoryCacheMB != 1024 {
		t.Fatalf("unexpected memory cache size: %d", cfg.Server.MemoryCacheMB)
	}
	if cfg.EHBot.BaseURL != "https://ehbot.example.com" || cfg.EHBot.TargetLibraryID != "local-main" {
		t.Fatalf("unexpected ehbot values: %#v", cfg.EHBot)
	}
	if len(cfg.Libraries) != 1 {
		t.Fatalf("expected 1 library, got %d", len(cfg.Libraries))
	}
	if cfg.Libraries[0].ID != "local-main" {
		t.Fatalf("unexpected library id: %s", cfg.Libraries[0].ID)
	}
}

func TestSaveConfigRoundTripEscapesWindowsPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.toml")
	cfg := &configpkg.Config{
		SourcePath: path,
		Server: configpkg.ServerConfig{
			Listen:        "127.0.0.1:34123",
			Token:         "test-token",
			DataDir:       `C:\venera\data`,
			CacheDir:      `D:\venera\cache`,
			MemoryCacheMB: 512,
			CacheMaxAgeHours: 72,
			CacheCleanupIntervalMinutes: 30,
			LogLevel:      "info",
		},
		Scan: configpkg.ScanConfig{
			Concurrency:           2,
			ExtractArchives:       true,
			WatchLocal:            false,
			RescanIntervalMinutes: 15,
		},
		Metadata: configpkg.MetadataConfig{
			ReadComicInfo:    true,
			ReadSidecar:      true,
			AllowRemoteFetch: false,
			DatabasePath:     `C:\venera\metadata\store.sqlite`,
		},
		EHBot: configpkg.EHBotConfig{
			Enabled:                true,
			BaseURL:                "https://ehbot.example.com",
			PullToken:              "secret-token",
			ConsumerID:             "home-consumer",
			TargetID:               "home-server",
			TargetLibraryID:        "local-main",
			TargetSubdir:           `EH\Inbox`,
			PollIntervalSeconds:    60,
			LeaseSeconds:           600,
			DownloadTimeoutSeconds: 900,
			AutoRescan:             true,
			MaxJobsPerPoll:         2,
		},
		Libraries: []configpkg.LibraryConfig{{
			ID:       "local-main",
			Name:     "Local",
			Kind:     "local",
			Root:     `Y:\Comics\Inbox`,
			ScanMode: "auto",
		}},
	}
	if err := configpkg.SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(raw)
	for _, needle := range []string{`C:\\venera\\data`, `D:\\venera\\cache`, `C:\\venera\\metadata\\store.sqlite`, `Y:\\Comics\\Inbox`, `EH\\Inbox`} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected saved config to contain %q, got %q", needle, text)
		}
	}
	loaded, err := configpkg.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.SourcePath != path {
		t.Fatalf("unexpected source path: %q", loaded.SourcePath)
	}
	if loaded.Server.DataDir != cfg.Server.DataDir || loaded.Server.CacheDir != cfg.Server.CacheDir {
		t.Fatalf("unexpected server paths: %#v", loaded.Server)
	}
	if loaded.Server.CacheMaxAgeHours != cfg.Server.CacheMaxAgeHours || loaded.Server.CacheCleanupIntervalMinutes != cfg.Server.CacheCleanupIntervalMinutes {
		t.Fatalf("unexpected cache cleanup config: %#v", loaded.Server)
	}
	if loaded.Metadata.DatabasePath != cfg.Metadata.DatabasePath {
		t.Fatalf("unexpected metadata path: %q", loaded.Metadata.DatabasePath)
	}
	if loaded.EHBot.TargetSubdir != cfg.EHBot.TargetSubdir || loaded.EHBot.PullToken != cfg.EHBot.PullToken {
		t.Fatalf("unexpected ehbot config: %#v", loaded.EHBot)
	}
	if len(loaded.Libraries) != 1 || loaded.Libraries[0].Root != cfg.Libraries[0].Root {
		t.Fatalf("unexpected libraries: %#v", loaded.Libraries)
	}
}
