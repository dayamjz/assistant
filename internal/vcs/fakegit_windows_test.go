//go:build windows

package vcs_test

import "os/exec"

// leaveGroup has no counterpart on Windows, where this package starts no
// process group, so the grandchild stays as it was started.
func leaveGroup(*exec.Cmd) {}
