//go:build windows

package service

import "os/exec"

// detach does nothing here: the one test that uses it skips on this
// platform before starting anything, because there is no sweep to end it.
func detach(*exec.Cmd) {}
