package main

import (
	"flag"
	"log"
	"net/http"
	"os"
)

const startupBuildMarker = "2026-03-07-async-fill-long2048-q82"

func main() {
	configPath := flag.String("config", "server.example.toml", "path to config file")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	app, err := NewApp(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
	logger := log.New(os.Stdout, "[venera-home] ", log.LstdFlags)
	exePath, _ := os.Executable()
	logger.Printf("startup build=%s exe=%s config=%s data_dir=%s cache_dir=%s libraries=%d features=metadata-grouping,media-cache,async-render-cache,visual-cache", startupBuildMarker, exePath, *configPath, cfg.Server.DataDir, cfg.Server.CacheDir, len(cfg.Libraries))
	logger.Printf("listening on %s", cfg.Server.Listen)
	if err := http.ListenAndServe(cfg.Server.Listen, newHTTPServer(app, logger)); err != nil {
		logger.Fatalf("server exited: %v", err)
	}
}
