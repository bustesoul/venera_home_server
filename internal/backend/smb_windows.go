//go:build windows

package backend

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "syscall"
    "unsafe"

    "venera_home_server/internal/config"
)

type netResource struct {
    Scope       uint32
    Type        uint32
    DisplayType uint32
    Usage       uint32
    LocalName   *uint16
    RemoteName  *uint16
    Comment     *uint16
    Provider    *uint16
}

const resourceTypeDisk = 0x00000001

func NewSMBBackend(lib config.LibraryConfig) (Backend, error) {
    shareRoot := `\\` + lib.Host + `\` + lib.Share
    if err := connectSMBShare(shareRoot, lib.Username, os.Getenv(lib.PasswordEnv)); err != nil {
        return nil, err
    }
    root := shareRoot
    trimmed := strings.Trim(strings.ReplaceAll(lib.Path, "/", `\`), `\`)
    if trimmed != "" {
        root = filepath.Join(root, trimmed)
    }
    return &localBackend{kind: "smb", root: root}, nil
}

func connectSMBShare(remote, username, password string) error {
    mpr := syscall.NewLazyDLL("mpr.dll")
    proc := mpr.NewProc("WNetAddConnection2W")

    remotePtr, err := syscall.UTF16PtrFromString(remote)
    if err != nil {
        return err
    }
    var usernamePtr *uint16
    if username != "" {
        usernamePtr, err = syscall.UTF16PtrFromString(username)
        if err != nil {
            return err
        }
    }
    var passwordPtr *uint16
    if password != "" {
        passwordPtr, err = syscall.UTF16PtrFromString(password)
        if err != nil {
            return err
        }
    }
    nr := netResource{Type: resourceTypeDisk, RemoteName: remotePtr}
    ret, _, callErr := proc.Call(
        uintptr(unsafe.Pointer(&nr)),
        uintptr(unsafe.Pointer(passwordPtr)),
        uintptr(unsafe.Pointer(usernamePtr)),
        0,
    )
    if ret == 0 || ret == 85 || ret == 1219 {
        return nil
    }
    if callErr != syscall.Errno(0) {
        return fmt.Errorf("connect smb share: %w", callErr)
    }
    return fmt.Errorf("connect smb share failed: %d", ret)
}
