//go:build !linux && !darwin

package cli_test

// giveUp kills git push alone here. Every test that pushes into a gate skips
// on a platform with no local socket peer credentials, which is every
// platform this file builds for, so no push reaches a receive command or a
// hook here for it to list or end.
func (p *boundedPush) giveUp() string {
	_ = p.cmd.Process.Kill()
	<-p.done
	return "the push's processes are not listed on this platform; git push alone was killed\n"
}
