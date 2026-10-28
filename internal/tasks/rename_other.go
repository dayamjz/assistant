//go:build !linux && !darwin

package tasks

import "errors"

// renameNoReplace is refused where this build has no rename that refuses
// to replace what it would land on, so a discard there sets nothing aside
// and puts nothing back over something new: it refuses instead.
func renameNoReplace(from, to string) error {
	return errors.New("tasks: no rename that refuses to replace is available on this platform")
}
