//go:build unix && !linux

package agents

// exitedOnly has no reading of a group's members on this platform that this
// package has verified to identify an exited, unreaped one, and answers no,
// so a group holding only a zombie never reads gone here and EndGroup
// reports ErrGroupSurvived or GroupOutlivedLeader for it rather than a group
// it cannot see into.
//
// macOS is here on evidence rather than by default. It once read the
// kernel's process table by group (kern.proc.pgrp) and counted the group
// exited when every entry carried the zombie state, and on darwin/arm64 CI
// that reading did not answer yes for a group whose only member was an
// exited, unreaped leader that signal zero still reached. Which half of the
// reading fell short was not established, so no darwin reading is trusted
// to tell this case apart, and darwin takes the over-cautious answer.
func exitedOnly(int) bool { return false }
