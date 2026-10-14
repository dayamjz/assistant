//go:build unix && !linux && !darwin

package agents

// exitedOnly has no listing of a group's members on this platform and
// answers no, so a group holding only a zombie never reads gone here and
// EndGroup reports ErrGroupSurvived or GroupOutlivedLeader for it rather than
// a group it cannot see into.
func exitedOnly(int) bool { return false }
