//go:build linux || darwin

package lintprobe

// Value reads platformValue.
func Value() int { return platformValue }
