package safety_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/dayamjz/assistant/internal/vcs"
)

// fakeGit is a git mechanism whose whole history is stated by the test that
// uses it. The policy under test is about reachability and about what a remote
// advertises, and both are stated here directly rather than built by running
// git, so a test says what it means and a failure points at the policy.
type fakeGit struct {
	// parents maps a commit to its parents. A commit absent from this map
	// does not exist locally.
	parents map[string][]string
	// advertised is what each remote answers with, in order. The first read
	// of a remote takes entry 0, the second entry 1, and so on; the last
	// entry answers every later read. This is how a test makes the remote
	// move between the run's observation and the decision.
	advertised map[string][][]vcs.Ref
	// remoteErr, when set for a remote, fails every read of it.
	remoteErr map[string]error
	// mergeBaseErr and commitsErr fail those comparisons when set.
	mergeBaseErr error
	commitsErr   error

	// reads counts the remote reads performed, so a test can assert that a
	// decision was taken against a read of its own and not against the
	// anchor it was handed.
	reads int
	// perRemote counts the reads of each remote, which is what selects the
	// round advertised is answered from.
	perRemote map[string]int
}

func (f *fakeGit) RemoteRefs(_ context.Context, remote string, patterns ...string) ([]vcs.Ref, error) {
	f.reads++
	if f.perRemote == nil {
		f.perRemote = map[string]int{}
	}
	f.perRemote[remote]++
	if err, ok := f.remoteErr[remote]; ok {
		return nil, err
	}
	rounds, ok := f.advertised[remote]
	if !ok {
		return nil, fmt.Errorf("fakegit: no remote %q", remote)
	}
	round := rounds[len(rounds)-1]
	if n := f.perRemote[remote]; n <= len(rounds) {
		round = rounds[n-1]
	}
	if len(patterns) == 0 {
		return round, nil
	}
	var out []vcs.Ref
	for _, ref := range round {
		for _, p := range patterns {
			if matchesTail(ref.Name, p) {
				out = append(out, ref)
				break
			}
		}
	}
	return out, nil
}

// matchesTail models how git ls-remote narrows its output: a pattern matches a
// reference whose name it equals, or whose name ends with it on a
// slash-separated component boundary. A read for refs/heads/feature therefore
// also carries back refs/tags/refs/heads/feature, which is why the policy has
// to pick the exact name out of what comes back.
func matchesTail(name, pattern string) bool {
	return name == pattern || strings.HasSuffix(name, "/"+pattern)
}

func (f *fakeGit) ResolveCommit(_ context.Context, rev string) (string, error) {
	if _, ok := f.parents[rev]; !ok {
		return "", fmt.Errorf("fakegit: %s: %w", rev, vcs.ErrRefNotFound)
	}
	return rev, nil
}

func (f *fakeGit) MergeBase(_ context.Context, a, b string) (string, error) {
	if f.mergeBaseErr != nil {
		return "", f.mergeBaseErr
	}
	ra, err := f.reachable(a)
	if err != nil {
		return "", err
	}
	rb, err := f.reachable(b)
	if err != nil {
		return "", err
	}
	var shared []string
	for c := range ra {
		if rb[c] {
			shared = append(shared, c)
		}
	}
	if len(shared) == 0 {
		return "", vcs.ErrNoMergeBase
	}
	sort.Strings(shared)
	return shared[0], nil
}

func (f *fakeGit) CommitsNotIn(_ context.Context, have, incorporated string) ([]string, error) {
	if f.commitsErr != nil {
		return nil, f.commitsErr
	}
	rh, err := f.reachable(have)
	if err != nil {
		return nil, err
	}
	ri, err := f.reachable(incorporated)
	if err != nil {
		return nil, err
	}
	var out []string
	for c := range rh {
		if !ri[c] {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, nil
}

// reachable returns the commit and everything it reaches.
func (f *fakeGit) reachable(commit string) (map[string]bool, error) {
	seen := map[string]bool{}
	queue := []string{commit}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if seen[c] {
			continue
		}
		parents, ok := f.parents[c]
		if !ok {
			return nil, fmt.Errorf("fakegit: unknown commit %q: %w", c, vcs.ErrRefNotFound)
		}
		seen[c] = true
		queue = append(queue, parents...)
	}
	return seen, nil
}

// errBroken is what a test uses when the only thing that matters is that the
// mechanism failed.
var errBroken = errors.New("fakegit: broken")

// branch is a convenience for one advertised branch.
func branch(name, commit string) vcs.Ref {
	return vcs.Ref{Name: name, Object: commit, Commit: commit}
}

// linear builds a parent map for a chain oldest first, so linear("a","b","c")
// makes c a descendant of b and b of a.
func linear(commits ...string) map[string][]string {
	parents := map[string][]string{}
	for i, c := range commits {
		if i == 0 {
			parents[c] = nil
			continue
		}
		parents[c] = []string{commits[i-1]}
	}
	return parents
}
