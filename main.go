package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	app "venera_home_server/app"
	"venera_home_server/config"
	"venera_home_server/httpapi"
	"venera_home_server/shared"
)

const startupBuildMarker = "2026-03-07-async-fill-long2048-q82"

func main() {
	configPath := flag.String("config", "server.example.toml", "path to config file")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	application, err := app.NewApp(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}

	baseLogger := log.New(os.Stdout, "[venera-home] ", log.LstdFlags)
	logger := shared.NewLevelLogger(baseLogger, cfg.Server.LogLevel)
	exePath, _ := os.Executable()
	logger.Infof(
		"startup build=%s exe=%s config=%s data_dir=%s cache_dir=%s libraries=%d",
		startupBuildMarker,
		exePath,
		*configPath,
		cfg.Server.DataDir,
		cfg.Server.CacheDir,
		len(cfg.Libraries),
	)
	logger.Infof("listening on %s", cfg.Server.Listen)
	if err := http.ListenAndServe(cfg.Server.Listen, httpapi.NewHTTPServer(application, baseLogger)); err != nil {
		logger.Fatalf("server exited: %v", err)
	}
}
