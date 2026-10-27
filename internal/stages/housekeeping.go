package stages

import (
	"context"
	"fmt"
	"slices"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/vcs"
)

// PRD section 6's housekeeping, the review stage's half of it: "the run that
// a pass's push starts holds at review with an ask naming each path the
// change touched outside housekeeping.paths. This is decided from the paths
// the commits touched, not from the worker's account, as the judge rule is,
// and it adds no stage."
//
// Whether the run is a pass's is pipeline.KeyHousekeepingPass, set at begin
// from the task records by the run's repository and branch, so every run of
// a pass's branch carries it, whoever pushed, and nothing the push says sets
// it. The scope is the
// run's configuration's HousekeepingPaths, a trusted-only key, so a pushed
// copy cannot widen it; an empty list, housekeeping turned off after the
// pass was created, leaves every touched path outside. The paths are what
// each commit the review covers touched, listed commit by commit against
// its first parent and with the net change (commitsChanges), unfiltered by
// the ignore patterns, which a pushed branch may set: a rename or a copy
// counts both its paths, and a deletion its path, so moving a file out of
// the scope is asked about too, and an edit a later commit reverted is
// asked about, since the rebase keeps both commits.
//
// The asks are built here and not parsed from the reviewer, so a reviewer
// that says nothing about a path does not make it disappear, and they reach
// both reviews reviewRead serves, the review stage's and the push stage's
// review of later commits. What this does not establish: whether a change
// inside the scope is factual drift rather than a new rule, which the
// worker's instructions and the reviewer's reading against the intent carry.

// outsideHousekeeping is every path the change touched outside scope, in the
// order git reported them, each once, for a pass's run; nil for any other
// run. changed is what every commit touched, commit by commit
// (commitsChanges), and not the net change alone: an edit outside the scope
// that a later commit reverted is absent from the net change, and the rebase
// keeps both commits, so it is asked about all the same.
func outsideHousekeeping(pass bool, scope config.PatternSet, changed []vcs.FileChange) []string {
	if !pass {
		return nil
	}
	var out []string
	for _, c := range changed {
		for _, path := range []string{c.OldPath, c.Path} {
			if path == "" || scope.Matches(path) || slices.Contains(out, path) {
				continue
			}
			out = append(out, path)
		}
	}
	return out
}

// commitsChanges is what the commits from from to head touched, each commit
// against its first parent, newest first, after the net change itself, so
// a path only a reverted commit touched is listed with the rest. A commit
// that cannot be listed is an error: a read that fails refuses the review
// rather than holding a pass's run to less than its commits.
func commitsChanges(ctx context.Context, repo *vcs.Repository, from, head string, net []vcs.FileChange) ([]vcs.FileChange, error) {
	commits, err := repo.CommitsNotIn(ctx, head, from)
	if err != nil {
		return nil, fmt.Errorf("stages: the review stage cannot list the commits of %s since %s: %w", head, from, err)
	}
	out := append([]vcs.FileChange(nil), net...)
	for _, c := range commits {
		changed, err := repo.ChangedFiles(ctx, c+"^", c)
		if err != nil {
			return nil, fmt.Errorf("stages: the review stage cannot list what commit %s changed: %w", c, err)
		}
		out = append(out, changed...)
	}
	return out, nil
}

// housekeepingFindings is one ask per path a pass's change touched outside
// its scope.
func housekeepingFindings(paths []string) []findings.Finding {
	out := make([]findings.Finding, 0, len(paths))
	for _, path := range paths {
		out = append(out, findings.Finding{
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Location: findings.Location{Path: path},
			Description: fmt.Sprintf("This run is on a housekeeping pass's branch, whoever pushed or started it, so it is held to "+
				"the pass's scope: a pass may change only the repository's agent context, the files its trusted "+
				"housekeeping.paths names, and this change touched %s, which is outside them. "+
				"Whether that change belongs in the pass is yours to decide; approving accepts it.", path),
		})
	}
	return out
}
