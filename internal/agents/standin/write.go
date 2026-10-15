package standin

import (
	"os"
	"path/filepath"
	"strings"
)

// writeFile writes the file a Write exchange's line names, on Exchange.Write's
// terms: the first line of rest, trimmed, is the path, and everything after
// that line is the content, written as it is. The path is resolved against
// this process's working directory, and one that is absolute, or whose
// cleaned form leaves that directory, is refused without writing. A path that
// leaves it through a symbolic link inside it is not caught: the check is
// over the text of the path, as the observation it models was over where a
// file was asked to go.
func writeFile(rest string) Wrote {
	first, content, _ := strings.Cut(rest, "\n")
	path := strings.TrimSpace(first)
	wrote := Wrote{Path: path}
	clean := filepath.Clean(path)
	switch {
	case path == "":
		wrote.Refused = "standin: a write names no path"
		return wrote
	case filepath.IsAbs(path) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)):
		wrote.Refused = "standin: writing outside the working directory needs an approval nobody was there to give (#267)"
		return wrote
	}
	if err := os.WriteFile(clean, []byte(content), 0o600); err != nil {
		wrote.Refused = "standin: writing the file: " + err.Error()
	}
	return wrote
}
