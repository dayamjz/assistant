package vcs

import "context"

// RootRefsByNameForTest is RootRefs' reading for a git older than
// rootRefsFloor, which this package's tests run under the git on PATH too,
// so that reading is held to what --include-root-refs lists even where no
// older git is at hand.
func RootRefsByNameForTest(ctx context.Context, r *Repository) ([]Ref, error) {
	return r.rootRefsByName(ctx)
}
