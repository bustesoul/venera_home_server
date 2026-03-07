//go:build !windows

package backend

import (
    "fmt"

    "venera_home_server/internal/config"
)

func NewSMBBackend(lib config.LibraryConfig) (Backend, error) {
    return nil, fmt.Errorf("smb backend is only implemented on windows in this build")
}
