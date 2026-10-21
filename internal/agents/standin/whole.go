package standin

import (
	"os"
	"path/filepath"
	"sync/atomic"
)

// writeWhole writes data to path, mode 0600, so that a reader of path finds
// either no file there or all of data, never a file created but not yet
// written: it writes a temporary file beside path and then renames it onto
// path, and POSIX specifies that rename puts the new name in place in one
// step. A reader that polls for path may therefore take the first content it
// reads there as complete; with os.WriteFile it could not, since that creates
// the file before writing it (#459). The temporary file's name is path's
// base name followed by ".partial-" and a random part, so it never ends in
// the suffix a reader selects files by, and one a failed write leaves behind
// keeps that name.
func writeWhole(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".partial-*")
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		// The close is what would report a failure to flush, and this path
		// already has a failure to report.
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if hook := beforeWholeRename.Load(); hook != nil {
		(*hook)(file.Name(), path)
	}
	return os.Rename(file.Name(), path)
}

// beforeWholeRename, when set, is called by writeWhole with the temporary
// file's name and the destination once the temporary file is written and
// closed and before the rename, so a test can observe the destination at
// the one moment a writer that wrote it in place would have left it partial.
// It is nil outside tests, and atomic so that a test setting it is not a
// data race with a writeWhole running on another test's goroutine.
var beforeWholeRename atomic.Pointer[func(temp, path string)]
