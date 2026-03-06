//go:build !windows

package main

import "fmt"

func newSMBBackend(lib LibraryConfig) (Backend, error) {
    return nil, fmt.Errorf("smb backend is only implemented on windows in this build")
}
