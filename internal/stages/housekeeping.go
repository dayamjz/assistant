package stages

import (
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
// from the pushing task's record and never from the push. The scope is the
// run's configuration's HousekeepingPaths, a trusted-only key, so a pushed
// copy cannot widen it; an empty list, housekeeping turned off after the
// pass was created, leaves every touched path outside. The paths are the
// change the review read, unfiltered by the ignore patterns, which a pushed
// branch may set: a rename or a copy counts both its paths, and a deletion
// its path, so moving a file out of the scope is asked about too.
//
// The asks are built here and not parsed from the reviewer, so a reviewer
// that says nothing about a path does not make it disappear, and they reach
// both reviews reviewRead serves, the review stage's and the push stage's
// review of later commits. What this does not establish: whether a change
// inside the scope is factual drift rather than a new rule, which the
// worker's instructions and the reviewer's reading against the intent carry.

// outsideHousekeeping is every path the change touched outside scope, in the
// order git reported them, each once, for a pass's run; nil for any other
// run.
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

// housekeepingFindings is one ask per path a pass's change touched outside
// its scope.
func housekeepingFindings(paths []string) []findings.Finding {
	out := make([]findings.Finding, 0, len(paths))
	for _, path := range paths {
		out = append(out, findings.Finding{
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Location: findings.Location{Path: path},
			Description: fmt.Sprintf("This run was started by a housekeeping pass, which may change only the repository's agent "+
				"context, the files its trusted housekeeping.paths names, and this change touched %s, which is outside them. "+
				"Whether that change belongs in the pass is yours to decide; approving accepts it.", path),
		})
	}
	return out
}
