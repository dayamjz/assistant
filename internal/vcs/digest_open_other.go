//go:build !unix

package vcs

import "os"

// openForDigest opens full for reading. This platform's open cannot ask not
// to wait, so a path replaced by a blocking special file after the stat can
// still block the caller. The context does not end that wait.
func openForDigest(full string) (*os.File, error) {
	return os.Open(full)
}
