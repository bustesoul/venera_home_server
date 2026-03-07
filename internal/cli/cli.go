package cli

import (
	"flag"
	"log"
	"net/http"
	"os"

	app "venera_home_server/internal/app"
	"venera_home_server/internal/config"
	"venera_home_server/internal/httpapi"
)

// RunCLI starts the server process using the standard command-line flags.
func RunCLI(startupBuildMarker string) {
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

	logger := log.New(os.Stdout, "[venera-home] ", log.LstdFlags)
	exePath, _ := os.Executable()
	logger.Printf(
		"startup build=%s exe=%s config=%s data_dir=%s cache_dir=%s libraries=%d features=metadata-grouping,media-cache,async-render-cache,visual-cache",
		startupBuildMarker,
		exePath,
		*configPath,
		cfg.Server.DataDir,
		cfg.Server.CacheDir,
		len(cfg.Libraries),
	)
	logger.Printf("listening on %s", cfg.Server.Listen)
	if err := http.ListenAndServe(cfg.Server.Listen, httpapi.NewHTTPServer(application, logger)); err != nil {
		logger.Fatalf("server exited: %v", err)
	}
}
