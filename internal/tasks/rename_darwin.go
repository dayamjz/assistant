//go:build darwin

package tasks

import "golang.org/x/sys/unix"

// renameNoReplace renames from to to, refusing when to already names
// something rather than replacing it.
func renameNoReplace(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}
