package tasks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CopyIdentity is what only one copy on disk has: the file identity, the
// device and the file number, of its root directory and of its .git, read
// without following a link. A rename within a filesystem keeps both, which
// is how a kept copy moved to a carrying task's path is still itself; a
// clone, a copy of the copy, or anything else put at that path has others,
// whatever commits, names or remotes it shares with the kept one. It is the
// identity os.SameFile compares, made to outlive the process that read it,
// since a carry on that stops is completed by the next start.
//
// What it does not cover: a kept copy moved to another filesystem is a new
// identity and refused as another copy, and a file number reused for a new
// directory after the original was removed would match; the second needs
// the kept copy removed and a directory created at the carrying task's path
// on the same device with the same numbers for its root and its .git.
type CopyIdentity struct {
	root, git fileIdentity
}

// fileIdentity is one file's device and file number.
type fileIdentity struct{ dev, ino uint64 }

// ErrCopyIdentityUnsupported is returned by IdentifyCopy where this build
// reads no file identity, so which copy is which cannot be told.
var ErrCopyIdentityUnsupported = errors.New("tasks: this platform's file identity is not read, so a copy cannot be told from another by what it is")

// IdentifyCopy reads the identity of the copy whose root is path.
func IdentifyCopy(path string) (CopyIdentity, error) {
	root, err := identityOf(path)
	if err != nil {
		return CopyIdentity{}, fmt.Errorf("tasks: reading the identity of the copy at %s: %w", path, err)
	}
	git, err := identityOf(filepath.Join(path, ".git"))
	if err != nil {
		return CopyIdentity{}, fmt.Errorf("tasks: reading the identity of the copy at %s: %w", path, err)
	}
	return CopyIdentity{root: root, git: git}, nil
}

// identityOf is one file's identity, not following a link.
func identityOf(path string) (fileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileIdentity{}, err
	}
	return fileIdentityOf(info)
}

// Same reports whether two identities are one copy's.
func (c CopyIdentity) Same(other CopyIdentity) bool { return c == other }

// String is the identity's one written form, which ParseCopyIdentity reads
// back: the root's device and file number, then the .git's.
func (c CopyIdentity) String() string {
	return strings.Join([]string{
		strconv.FormatUint(c.root.dev, 10), strconv.FormatUint(c.root.ino, 10),
		strconv.FormatUint(c.git.dev, 10), strconv.FormatUint(c.git.ino, 10),
	}, ":")
}

// ParseCopyIdentity reads what String wrote, and refuses anything else.
func ParseCopyIdentity(text string) (CopyIdentity, error) {
	fields := strings.Split(text, ":")
	if len(fields) != 4 {
		return CopyIdentity{}, fmt.Errorf("tasks: %q is not a copy's identity", text)
	}
	var n [4]uint64
	for i, f := range fields {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return CopyIdentity{}, fmt.Errorf("tasks: %q is not a copy's identity: %w", text, err)
		}
		n[i] = v
	}
	return CopyIdentity{root: fileIdentity{n[0], n[1]}, git: fileIdentity{n[2], n[3]}}, nil
}
