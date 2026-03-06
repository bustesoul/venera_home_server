//go:build !windows

package main

import (
	"context"
	"fmt"
)

func openPDFArchive(_ context.Context, _ Backend, _ string, _ string) (Archive, error) {
	return nil, fmt.Errorf("pdf support requires Windows runtime")
}
