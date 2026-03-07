package main

import "venera_home_server/internal/cli"

const startupBuildMarker = "2026-03-07-async-fill-long2048-q82"

func main() {
	cli.RunCLI(startupBuildMarker)
}
