//go:build !windows

package archive

import (
	"context"
	"fmt"

	backendpkg "venera_home_server/internal/backend"
)

func openPDFArchive(_ context.Context, _ backendpkg.Backend, _ string, _ string) (Archive, error) {
	return nil, fmt.Errorf("pdf support requires Windows runtime")
}
