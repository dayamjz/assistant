//go:build !linux && !darwin

package agents

// lineage has no read on this platform, so it answers ErrLineageUnreadable.
func lineage(int) (Lineage, bool, error) { return Lineage{}, false, ErrLineageUnreadable }
