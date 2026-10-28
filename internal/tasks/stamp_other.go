//go:build !linux && !darwin

package tasks

import "errors"

// stampOf is refused where this build reads no change time, so no copy's
// state is read there and a discard refuses, as renameNoReplace does.
func stampOf(string) (stamp, error) {
	return stamp{}, errors.New("tasks: this platform's change times are not read, so a copy's state is not read")
}
