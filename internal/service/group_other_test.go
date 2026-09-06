//go:build !unix

package service_test

import (
	"errors"
	"runtime"
)

// processGroupOfThisProcess has no answer here. The test that uses it needs an
// identified peer, which this platform does not report either, so it skips
// before reaching this.
func processGroupOfThisProcess() (int, error) {
	return 0, errors.New(runtime.GOOS + " reports no process group")
}
