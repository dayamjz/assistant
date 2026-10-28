//go:build linux

package tasks

import "golang.org/x/sys/unix"

// renameNoReplace renames from to to, refusing when to already names
// something rather than replacing it.
func renameNoReplace(from, to string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
}
