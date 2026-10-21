//go:build !(linux || darwin)

package service

// HeldMain is held_unix_test.go's on the platforms that have it, and does
// nothing here, where no test holds a process.
func HeldMain() {}
