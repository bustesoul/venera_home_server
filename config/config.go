package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Server    ServerConfig
	Scan      ScanConfig
	Metadata  MetadataConfig
	Libraries []LibraryConfig
}

type ServerConfig struct {
	Listen        string
	Token         string
	DataDir       string
	CacheDir      string
	MemoryCacheMB int
	LogLevel      string
}

type ScanConfig struct {
	Concurrency           int
	ExtractArchives       bool
	WatchLocal            bool
	RescanIntervalMinutes int
}

type MetadataConfig struct {
	ReadComicInfo    bool
	ReadSidecar      bool
	AllowRemoteFetch bool
	DatabasePath     string
}

type LibraryConfig struct {
	ID          string
	Name        string
	Kind        string
	Root        string
	Host        string
	Share       string
	Path        string
	Username    string
	PasswordEnv string
	URL         string
	ScanMode    string
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		Server: ServerConfig{
			Listen:        "127.0.0.1:34123",
			DataDir:       filepath.Join(filepath.Dir(path), "data"),
			CacheDir:      filepath.Join(filepath.Dir(path), "cache"),
			MemoryCacheMB: 512,
			LogLevel:      "info",
		},
		Scan: ScanConfig{
			Concurrency:           4,
			ExtractArchives:       true,
			RescanIntervalMinutes: 30,
		},
		Metadata: MetadataConfig{
			ReadComicInfo: true,
			ReadSidecar:   true,
		},
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	section := ""
	var currentLib *LibraryConfig
	lineNo := 0
	for scanner.Scan() {
		rawLine := scanner.Text()
		if lineNo == 0 {
			rawLine = strings.TrimPrefix(rawLine, "\uFEFF")
		}
		lineNo++
		line := strings.TrimSpace(stripComments(rawLine))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			name := strings.TrimSpace(line[2 : len(line)-2])
			if name != "libraries" {
				return nil, fmt.Errorf("unsupported array section: %s", name)
			}
			cfg.Libraries = append(cfg.Libraries, LibraryConfig{ScanMode: "auto"})
			currentLib = &cfg.Libraries[len(cfg.Libraries)-1]
			section = "libraries"
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			currentLib = nil
			continue
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			return nil, fmt.Errorf("invalid config line: %s", line)
		}
		key := strings.TrimSpace(line[:idx])
		raw := strings.TrimSpace(line[idx+1:])
		val := parseValue(raw)
		switch section {
		case "server":
			assignServer(&cfg.Server, key, val)
		case "scan":
			assignScan(&cfg.Scan, key, val)
		case "metadata":
			assignMetadata(&cfg.Metadata, key, val)
		case "libraries":
			if currentLib == nil {
				return nil, fmt.Errorf("library entry not initialized")
			}
			assignLibrary(currentLib, key, val)
		default:
			return nil, fmt.Errorf("unsupported section: %s", section)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if cfg.Server.DataDir == "" {
		cfg.Server.DataDir = filepath.Join(filepath.Dir(path), "data")
	}
	if cfg.Server.CacheDir == "" {
		cfg.Server.CacheDir = filepath.Join(filepath.Dir(path), "cache")
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "127.0.0.1:34123"
	}
	for i := range cfg.Libraries {
		if cfg.Libraries[i].ID == "" {
			return nil, fmt.Errorf("library id is required")
		}
		if cfg.Libraries[i].Name == "" {
			cfg.Libraries[i].Name = cfg.Libraries[i].ID
		}
		if cfg.Libraries[i].Kind == "" {
			cfg.Libraries[i].Kind = "local"
		}
	}
	return cfg, nil
}

func stripComments(line string) string {
	inQuote := false
	for i, r := range line {
		switch r {
		case '"':
			inQuote = !inQuote
		case '#':
			if !inQuote {
				return line[:i]
			}
		}
	}
	return line
}

func parseValue(raw string) any {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		return raw[1 : len(raw)-1]
	}
	if raw == "true" || raw == "false" {
		return raw == "true"
	}
	if i, err := strconv.Atoi(raw); err == nil {
		return i
	}
	return raw
}

func assignServer(cfg *ServerConfig, key string, value any) {
	switch key {
	case "listen":
		cfg.Listen = asString(value)
	case "token":
		cfg.Token = asString(value)
	case "data_dir":
		cfg.DataDir = asString(value)
	case "cache_dir":
		cfg.CacheDir = asString(value)
	case "memory_cache_mb":
		cfg.MemoryCacheMB = asInt(value)
	case "log_level":
		cfg.LogLevel = asString(value)
	}
}

func assignScan(cfg *ScanConfig, key string, value any) {
	switch key {
	case "concurrency":
		cfg.Concurrency = asInt(value)
	case "extract_archives":
		cfg.ExtractArchives = asBool(value)
	case "watch_local":
		cfg.WatchLocal = asBool(value)
	case "rescan_interval_minutes":
		cfg.RescanIntervalMinutes = asInt(value)
	}
}

func assignMetadata(cfg *MetadataConfig, key string, value any) {
	switch key {
	case "read_comicinfo":
		cfg.ReadComicInfo = asBool(value)
	case "read_sidecar":
		cfg.ReadSidecar = asBool(value)
	case "allow_remote_fetch":
		cfg.AllowRemoteFetch = asBool(value)
	case "database_path":
		cfg.DatabasePath = asString(value)
	}
}

func assignLibrary(cfg *LibraryConfig, key string, value any) {
	switch key {
	case "id":
		cfg.ID = asString(value)
	case "name":
		cfg.Name = asString(value)
	case "kind":
		cfg.Kind = strings.ToLower(asString(value))
	case "root":
		cfg.Root = asString(value)
	case "host":
		cfg.Host = asString(value)
	case "share":
		cfg.Share = asString(value)
	case "path":
		cfg.Path = asString(value)
	case "username":
		cfg.Username = asString(value)
	case "password_env":
		cfg.PasswordEnv = asString(value)
	case "url":
		cfg.URL = asString(value)
	case "scan_mode":
		cfg.ScanMode = asString(value)
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func asInt(v any) int {
	switch value := v.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(value))
		return i
	default:
		i, _ := strconv.Atoi(strings.TrimSpace(fmt.Sprint(value)))
		return i
	}
}

func asBool(v any) bool {
	switch value := v.(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}
