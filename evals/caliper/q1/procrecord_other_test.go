//go:build unix && !darwin && !linux

package q1_test

import (
	"errors"
	"runtime"
)

// startRecord is not read on this platform, so a check that needs it fails.
func startRecord(int) ([]byte, error) {
	return nil, errors.New("no start record reader on " + runtime.GOOS)
}
