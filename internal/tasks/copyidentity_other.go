//go:build !unix

package tasks

import "os"

// fileIdentityOf reads no file identity here: a copy is refused as one
// whose identity cannot be told, never taken as the kept one.
func fileIdentityOf(os.FileInfo) (fileIdentity, error) {
	return fileIdentity{}, ErrCopyIdentityUnsupported
}
