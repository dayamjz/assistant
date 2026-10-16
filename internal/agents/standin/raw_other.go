//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package standin

import (
	"errors"
	"runtime"
)

// makeRaw refuses, because this package has no raw mode for this platform's
// terminals, and an interactive worker reading in another mode would be
// reading a terminal the CLI it models does not.
func makeRaw(int) (func(), error) {
	return nil, errors.New("standin: no raw terminal mode on " + runtime.GOOS)
}
