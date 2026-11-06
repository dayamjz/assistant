//go:build !linux && !darwin

package procgroup_test

// printGroup is the group stand-in where this package groups nothing, and
// no test runs it: it prints nothing.
func printGroup() {}
