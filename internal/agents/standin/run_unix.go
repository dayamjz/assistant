//go:build linux || darwin

package standin

// groupRefusal is nil: internal/procgroup starts a command as the leader of a
// new process group on this platform, which is the shape runCommand models.
func groupRefusal() error { return nil }
