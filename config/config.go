package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	SourcePath string
	Server     ServerConfig
	Scan       ScanConfig
	Metadata   MetadataConfig
	EHBot      EHBotConfig
	Libraries  []LibraryConfig
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

type EHBotConfig struct {
	Enabled                bool
	BaseURL                string
	PullToken              string
	ConsumerID             string
	TargetID               string
	TargetLibraryID        string
	TargetSubdir           string
	PollIntervalSeconds    int
	LeaseSeconds           int
	DownloadTimeoutSeconds int
	AutoRescan             bool
	MaxJobsPerPoll         int
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
		SourcePath: path,
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
		EHBot: EHBotConfig{
			PollIntervalSeconds:    60,
			LeaseSeconds:           1800,
			DownloadTimeoutSeconds: 1800,
			AutoRescan:             true,
			MaxJobsPerPoll:         1,
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
		case "ehbot":
			assignEHBot(&cfg.EHBot, key, val)
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

func SaveConfig(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = strings.TrimSpace(cfg.SourcePath)
	}
	if path == "" {
		return fmt.Errorf("config source path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data := renderConfig(cfg)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	_ = os.Remove(path)
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	cfg.SourcePath = path
	return nil
}

func renderConfig(cfg *Config) []byte {
	var buf bytes.Buffer
	writeSectionHeader(&buf, "server")
	writeStringKV(&buf, "listen", cfg.Server.Listen)
	writeStringKV(&buf, "token", cfg.Server.Token)
	writeStringKV(&buf, "data_dir", cfg.Server.DataDir)
	writeStringKV(&buf, "cache_dir", cfg.Server.CacheDir)
	writeIntKV(&buf, "memory_cache_mb", cfg.Server.MemoryCacheMB)
	writeStringKV(&buf, "log_level", cfg.Server.LogLevel)
	buf.WriteString("\n")

	writeSectionHeader(&buf, "scan")
	writeIntKV(&buf, "concurrency", cfg.Scan.Concurrency)
	writeBoolKV(&buf, "extract_archives", cfg.Scan.ExtractArchives)
	writeBoolKV(&buf, "watch_local", cfg.Scan.WatchLocal)
	writeIntKV(&buf, "rescan_interval_minutes", cfg.Scan.RescanIntervalMinutes)
	buf.WriteString("\n")

	writeSectionHeader(&buf, "metadata")
	writeBoolKV(&buf, "read_comicinfo", cfg.Metadata.ReadComicInfo)
	writeBoolKV(&buf, "read_sidecar", cfg.Metadata.ReadSidecar)
	writeBoolKV(&buf, "allow_remote_fetch", cfg.Metadata.AllowRemoteFetch)
	writeStringKV(&buf, "database_path", cfg.Metadata.DatabasePath)
	buf.WriteString("\n")

	writeSectionHeader(&buf, "ehbot")
	writeBoolKV(&buf, "enabled", cfg.EHBot.Enabled)
	writeStringKV(&buf, "base_url", cfg.EHBot.BaseURL)
	writeStringKV(&buf, "pull_token", cfg.EHBot.PullToken)
	writeStringKV(&buf, "consumer_id", cfg.EHBot.ConsumerID)
	writeStringKV(&buf, "target_id", cfg.EHBot.TargetID)
	writeStringKV(&buf, "target_library_id", cfg.EHBot.TargetLibraryID)
	writeStringKV(&buf, "target_subdir", cfg.EHBot.TargetSubdir)
	writeIntKV(&buf, "poll_interval_seconds", cfg.EHBot.PollIntervalSeconds)
	writeIntKV(&buf, "lease_seconds", cfg.EHBot.LeaseSeconds)
	writeIntKV(&buf, "download_timeout_seconds", cfg.EHBot.DownloadTimeoutSeconds)
	writeBoolKV(&buf, "auto_rescan", cfg.EHBot.AutoRescan)
	writeIntKV(&buf, "max_jobs_per_poll", cfg.EHBot.MaxJobsPerPoll)
	buf.WriteString("\n")

	for _, lib := range cfg.Libraries {
		buf.WriteString("[[libraries]]\n")
		writeStringKV(&buf, "id", lib.ID)
		writeStringKV(&buf, "name", lib.Name)
		writeStringKV(&buf, "kind", lib.Kind)
		if lib.Root != "" {
			writeStringKV(&buf, "root", lib.Root)
		}
		if lib.Host != "" {
			writeStringKV(&buf, "host", lib.Host)
		}
		if lib.Share != "" {
			writeStringKV(&buf, "share", lib.Share)
		}
		if lib.Path != "" {
			writeStringKV(&buf, "path", lib.Path)
		}
		if lib.Username != "" {
			writeStringKV(&buf, "username", lib.Username)
		}
		if lib.PasswordEnv != "" {
			writeStringKV(&buf, "password_env", lib.PasswordEnv)
		}
		if lib.URL != "" {
			writeStringKV(&buf, "url", lib.URL)
		}
		writeStringKV(&buf, "scan_mode", lib.ScanMode)
		buf.WriteString("\n")
	}
	return buf.Bytes()
}

func writeSectionHeader(buf *bytes.Buffer, name string) {
	buf.WriteString("[")
	buf.WriteString(name)
	buf.WriteString("]\n")
}

func writeStringKV(buf *bytes.Buffer, key string, value string) {
	buf.WriteString(key)
	buf.WriteString(" = ")
	buf.WriteString(quoteValue(value))
	buf.WriteString("\n")
}

func writeIntKV(buf *bytes.Buffer, key string, value int) {
	buf.WriteString(key)
	buf.WriteString(" = ")
	buf.WriteString(strconv.Itoa(value))
	buf.WriteString("\n")
}

func writeBoolKV(buf *bytes.Buffer, key string, value bool) {
	buf.WriteString(key)
	buf.WriteString(" = ")
	if value {
		buf.WriteString("true")
	} else {
		buf.WriteString("false")
	}
	buf.WriteString("\n")
}

func quoteValue(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, `"`, `\"`)
	return "\"" + value + "\""
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
		if unquoted, err := strconv.Unquote(raw); err == nil {
			return unquoted
		}
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

func assignEHBot(cfg *EHBotConfig, key string, value any) {
	switch key {
	case "enabled":
		cfg.Enabled = asBool(value)
	case "base_url":
		cfg.BaseURL = asString(value)
	case "pull_token":
		cfg.PullToken = asString(value)
	case "consumer_id":
		cfg.ConsumerID = asString(value)
	case "target_id":
		cfg.TargetID = asString(value)
	case "target_library_id":
		cfg.TargetLibraryID = asString(value)
	case "target_subdir":
		cfg.TargetSubdir = asString(value)
	case "poll_interval_seconds":
		cfg.PollIntervalSeconds = asInt(value)
	case "lease_seconds":
		cfg.LeaseSeconds = asInt(value)
	case "download_timeout_seconds":
		cfg.DownloadTimeoutSeconds = asInt(value)
	case "auto_rescan":
		cfg.AutoRescan = asBool(value)
	case "max_jobs_per_poll":
		cfg.MaxJobsPerPoll = asInt(value)
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
