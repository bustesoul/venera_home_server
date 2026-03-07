package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"venera_home_server/exdbdryrun"
)

func main() {
	var cfg exdbdryrun.Config
	var outPath string
	flag.StringVar(&cfg.MetadataDBPath, "metadata", "", "path to local metadata.db")
	flag.StringVar(&cfg.ExDBPath, "exdb", "", "path to exdb sqlite file")
	flag.StringVar(&cfg.LibraryID, "library", "", "optional library id filter")
	flag.StringVar(&cfg.State, "state", "empty", "metadata state filter: empty|all|missing|error|stale")
	flag.IntVar(&cfg.Limit, "limit", 100, "max local metadata records to inspect")
	flag.Float64Var(&cfg.MinScore, "min-score", 0.72, "minimum accepted candidate score")
	flag.BoolVar(&cfg.InspectOnly, "inspect", false, "only inspect exdb schema, do not load local metadata")
	flag.StringVar(&cfg.Table, "table", "", "force a specific exdb table name")
	flag.StringVar(&outPath, "out", "", "optional path to write JSON report")
	flag.Parse()

	report, err := exdbdryrun.Run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exdb dry-run failed: %v\n", err)
		os.Exit(1)
	}

	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal report: %v\n", err)
		os.Exit(1)
	}
	if outPath != "" {
		if err := os.WriteFile(outPath, payload, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stdout, "report written to %s\n", outPath)
		return
	}
	_, _ = os.Stdout.Write(payload)
	_, _ = os.Stdout.Write([]byte("\n"))
}
