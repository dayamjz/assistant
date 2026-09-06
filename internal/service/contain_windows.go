//go:build windows

package service

import (
	"errors"
	"runtime"
)

// processGroup refuses, because this platform has no process group to read.
//
// It is unreachable in practice on this platform and is still a refusal rather
// than a permissive answer. internal/ipc reads the peer's credentials before it
// asks about containment and has no way to read them here, so a restricted
// request is already refused for an unidentified peer; if that ever changes,
// this refuses rather than answering a question it cannot.
func processGroup(int) (int, error) {
	return 0, errors.New(runtime.GOOS + " reports no process group for a peer")
}
