//go:build !linux && !darwin

package agents

import "context"

// listUnder has no listing on this platform, so it answers
// ErrSweepUnsupported and Sweeper.Sweep signals nothing. Linux and macOS
// are the platforms with one; Windows and the remaining unix platforms are
// here because no route to another process's working directory has been
// established and exercised on their CI by this package.
func listUnder(context.Context, string) (listing, error) { return listing{}, ErrSweepUnsupported }

// signalProcess is never reached here, because listUnder lists nothing.
func signalProcess(int, bool) error { return ErrSweepUnsupported }
