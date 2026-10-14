//go:build !linux

package agents

import "context"

// listUnder has no listing on this platform that this package has verified,
// so it answers ErrSweepUnsupported and Sweeper.Sweep signals nothing.
//
// macOS and Windows are here for want of evidence rather than by default.
// This package uses no route to another process's working directory on
// either, the module is built without cgo, and the one darwin process-table
// reading this package once relied on, for another question, did not answer
// on CI as it was assumed to (group_otherunix.go says how), so no route is
// claimed until one is established here and exercised on that platform's CI.
func listUnder(context.Context, string) (listing, error) { return listing{}, ErrSweepUnsupported }

// signalProcess is never reached here, because listUnder lists nothing.
func signalProcess(int, bool) error { return ErrSweepUnsupported }
