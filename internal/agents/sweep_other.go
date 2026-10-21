//go:build !linux && !darwin

package agents

import "context"

// listUnder has no listing on this platform, so it answers
// ErrSweepUnsupported and Sweeper.Sweep signals nothing. Linux and macOS
// are the platforms with one; Windows and the remaining unix platforms are
// here because no route to another process's working directory has been
// established and exercised on their CI by this package.
func listUnder(context.Context, string) (listing, error) { return listing{}, ErrSweepUnsupported }

// BlockerFor answers ErrSweepUnsupported here: with no listing there is no
// read a sweep makes of a refused process to repeat.
func BlockerFor(Identity) (Blocker, bool, error) { return Blocker{}, false, ErrSweepUnsupported }

// signalProcess is never reached here, because listUnder lists nothing.
func signalProcess(int, bool) error { return ErrSweepUnsupported }
