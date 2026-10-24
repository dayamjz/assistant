//go:build !linux && !darwin

package tasks

import "errors"

// renameNoReplace is refused where this build has no rename that refuses
// to replace what it would land on, so an entry a discard's removal moved
// aside and found changed is left where it was moved rather than put back
// over something new.
func renameNoReplace(from, to string) error {
	return errors.New("tasks: no rename that refuses to replace is available on this platform")
}
