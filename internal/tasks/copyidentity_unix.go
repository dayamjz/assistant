//go:build unix

package tasks

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentityOf reads the device and file number the operating system
// keeps for the file.
func fileIdentityOf(info os.FileInfo) (fileIdentity, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, fmt.Errorf("%w: %s carries no stat", ErrCopyIdentityUnsupported, info.Name())
	}
	return fileIdentity{dev: uint64(st.Dev), ino: uint64(st.Ino)}, nil //nolint:unconvert // Dev and Ino differ in width by platform
}
