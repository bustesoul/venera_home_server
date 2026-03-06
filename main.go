package main

import (
	"flag"
	"log"
	"net/http"
	"os"
)

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
	logger.Printf("listening on %s", cfg.Server.Listen)
	if err := http.ListenAndServe(cfg.Server.Listen, newHTTPServer(app, logger)); err != nil {
		logger.Fatalf("server exited: %v", err)
	}
}
