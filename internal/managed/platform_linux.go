//go:build linux

package managed

// platformKind is systemd's per-user instance on Linux. A Linux system that
// does not run systemd has no systemctl to answer, and Install refuses there
// with ReasonManagerMissing or ReasonManagerRefused.
func platformKind() (Kind, bool) { return KindSystemd, true }
