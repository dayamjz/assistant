package fixture

import (
	"fmt"
	"os"
	"path/filepath"
)

// GitHomeKey is the key in Scenario.Paths naming the directory the git
// configuration every invocation here runs under was written into. A deferred
// plant reads it so that it runs git the same way the build did, which is what
// keeps a plant applied later from picking up a developer's own configuration.
const GitHomeKey = "git-home"

// runnerFor rebuilds the runner the scenario was built with.
func runnerFor(s Scenario, opts ...Option) (*gitRunner, error) {
	home := s.Paths[GitHomeKey]
	if home == "" {
		return nil, fmt.Errorf("fixture: scenario %s carries no %s, so git cannot be run the way the "+
			"build ran it", s.Name, GitHomeKey)
	}
	b := &builder{}
	for _, opt := range opts {
		opt(b)
	}
	return newGitRunner(b.gitBinary, home)
}

// AdvanceRemoteOutOfBand lands a commit on the branch under validation from a
// second clone, which is the plant for refusal-remote-advanced-out-of-band. It
// returns the commit it landed.
//
// It has to be called after the run has observed the branch on the remote and
// before the run submits its update. Which point in the run that is belongs to
// the harness; what this states is only that an advance before the observation
// exercises nothing, because the run would then observe the advanced state and
// its anchor would describe the target correctly.
//
// The advance is a real push from a real second clone. A reference written
// directly into the bare repository would leave the same two identifiers in
// the same two places and would not have gone through the path a colleague's
// push goes through.
func AdvanceRemoteOutOfBand(s Scenario, opts ...Option) (string, error) {
	git, err := runnerFor(s, opts...)
	if err != nil {
		return "", err
	}
	colleague := s.Paths["colleague"]
	if colleague == "" {
		return "", fmt.Errorf("fixture: scenario %s carries no second clone, so there is nothing to "+
			"advance the remote from", s.Name)
	}
	before, err := git.run(colleague, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if err := writeFile(colleague, "docs/colleague.md", 0o644,
		"A paragraph somebody else landed on this branch while the run was working.\n"); err != nil {
		return "", err
	}
	commit, err := git.commitAll(colleague, "land a paragraph somebody else wrote")
	if err != nil {
		return "", err
	}
	if _, err := git.run(colleague, "push", "--quiet", "origin", s.Branch); err != nil {
		return "", err
	}
	// The plant is only a plant if the remote actually moved. Reporting
	// success over a push that changed nothing would leave a harness reporting
	// that P6 held when P6 was never asked.
	landed, err := git.run(s.Origin, "rev-parse", "refs/heads/"+s.Branch)
	if err != nil {
		return "", err
	}
	if landed != commit || landed == before {
		return "", fmt.Errorf("fixture: the out-of-band advance did not move %s on %s: it stands at %s "+
			"and the commit pushed was %s", s.Branch, s.Origin, landed, commit)
	}
	return commit, nil
}

// CopyGatedWorkingCopy copies the scenario's working copy to the destination
// the scenario names, which is the plant for the two copied-working-copy
// conditions. It returns the copy's path.
//
// It has to be called after a gate has been initialized against the original
// working copy. That ordering is the condition: what the copy carries is a
// remote it inherited from a gated original, and a remote written into a fresh
// directory instead is a state the product's own path never produces. It is
// also why the original is left standing rather than moved, because a working
// copy that moved leaves nothing behind and a working copy that was copied
// does, and those two are the opposite answers to the ownership question.
//
// It refuses when the original carries no assistant remote, rather than
// producing a copy that is missing the one thing the condition is about.
func CopyGatedWorkingCopy(s Scenario, opts ...Option) (string, error) {
	git, err := runnerFor(s, opts...)
	if err != nil {
		return "", err
	}
	destination := s.Paths["copy-destination"]
	if destination == "" {
		return "", fmt.Errorf("fixture: scenario %s names no copy destination", s.Name)
	}
	remote, err := git.run(s.WorkingCopy, "config", "--get", "remote."+gateRemoteName+".url")
	if err != nil || remote == "" {
		return "", fmt.Errorf("fixture: %s carries no %s remote, so a copy of it would not inherit one; "+
			"initialize a gate against it before copying", s.WorkingCopy, gateRemoteName)
	}
	if _, err := os.Stat(destination); err == nil {
		return "", fmt.Errorf("fixture: refusing to copy over %s, which already exists", destination)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", fmt.Errorf("fixture: creating the parent of %s: %w", destination, err)
	}
	if err := copyTree(s.WorkingCopy, destination); err != nil {
		return "", fmt.Errorf("fixture: copying %s to %s: %w", s.WorkingCopy, destination, err)
	}
	inherited, err := git.run(destination, "config", "--get", "remote."+gateRemoteName+".url")
	if err != nil || inherited != remote {
		return "", fmt.Errorf("fixture: the copy at %s did not inherit the %s remote of %s; it reads %q "+
			"where the original reads %q", destination, gateRemoteName, s.WorkingCopy, inherited, remote)
	}
	return destination, nil
}

// gateRemoteName is the remote a gate is reached by. It is gate.RemoteName,
// restated here rather than imported, because importing the package under
// validation to decide what a fixture plants would make the fixture agree with
// that package by construction. The fixture's own tests are what hold the two
// together.
const gateRemoteName = "assistant"
