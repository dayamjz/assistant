package safety

import (
	"errors"

	"github.com/dayamjz/assistant/internal/vcs"
)

// resolveCommitDetail states why a ResolveCommit failure stopped the work.
// A revision that does not resolve locally is named as such; any other git
// failure is named as unreadable, because a crashed or killed git is not the
// same fact as a missing revision.
func resolveCommitDetail(revPhrase, where string, err error, consequence string) string {
	if errors.Is(err, vcs.ErrRefNotFound) {
		return revPhrase + " does not resolve " + where + ", " + consequence
	}
	return revPhrase + " could not be read, " + consequence
}
