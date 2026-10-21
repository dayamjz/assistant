package standin

import (
	"os"
	"path/filepath"
)

// writeWhole writes data to path, mode 0600, so that a reader of path finds
// either no file there or all of data, never a file created but not yet
// written: it writes a temporary file beside path and then renames it onto
// path, and POSIX specifies that rename puts the new name in place in one
// step. A reader that polls for path may therefore take the first file it
// finds as complete; with os.WriteFile it could not, since that creates the
// file before writing it (#459). The temporary file is named after path with
// a ".partial-" suffix, so a reader that selects files by suffix never takes
// one for what it waits on, and one a failed write leaves behind keeps that
// name.
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
	return os.Rename(file.Name(), path)
}
