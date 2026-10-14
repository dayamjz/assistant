package forge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/principles"
)

// mergedPullRequestJSON is the pr view answer for the pull request once the
// host has merged it.
const mergedPullRequestJSON = `{"baseRefName":"main","headRefName":"fm/work",` +
	`"headRefOid":"6f1a3c2b0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a","isDraft":false,` +
	`"mergeable":"UNKNOWN","number":12,"state":"MERGED","title":"feat: a change",` +
	`"url":"https://github.com/owner/name/pull/12"}`

// methodsJSON is the repo view answer for the merge method fields.
func methodsJSON(merge, squash, rebase bool) string {
	b := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}
	return `{"mergeCommitAllowed":` + b(merge) + `,"rebaseMergeAllowed":` + b(rebase) + `,"squashMergeAllowed":` + b(squash) + `}`
}

// A merge is bound to the head the caller observed through gh's
// --match-head-commit, preceded by the repository confirmation, made with a
// method the repository permits, and answered with the pull request as the
// provider then reports it.
func TestMergeIsBoundToTheObservedHead(t *testing.T) {
	principles.Cite(t, principles.P6)
	h := newWriteHarness(t, ghScript{
		"merge-methods": {{Stdout: methodsJSON(false, true, true)}},
		"merge":         {{Stdout: ""}},
		"view":          {{Stdout: mergedPullRequestJSON}},
	})
	pr, err := h.gh.Merge(context.Background(), 12, headCommit)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if pr.State != forge.PullRequestStateMerged || pr.Number != 12 {
		t.Fatalf("Merge answered %+v", pr)
	}
	merges := h.callsFor("merge")
	if len(merges) != 1 || !merges[0].hasArg("--match-head-commit="+headCommit) || !merges[0].hasArg("--squash") ||
		!merges[0].hasArg("12") {
		t.Fatalf("the merge was asked as %+v", merges)
	}
	if len(h.callsFor("repo")) != 1 {
		t.Fatal("the merge was not preceded by the repository confirmation")
	}
}

// The provider's refusal is reported as a rejection carrying its message and
// is not worked around: nothing is asked a second time, with another method
// or without the head binding. A repository that permits no method is
// refused before a merge is asked, a repository resolving elsewhere writes
// nothing, and a head that is not an identifier is refused before the
// provider runs.
func TestAMergeRefusalIsReportedAndNotWorkedAround(t *testing.T) {
	h := newWriteHarness(t, ghScript{
		"merge-methods": {{Stdout: methodsJSON(true, true, true)}},
		"merge":         {{Stderr: "GraphQL: Head branch was modified. Review and try the merge again.\n", Exit: 1}},
	})
	_, err := h.gh.Merge(context.Background(), 12, headCommit)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != forge.ReasonRejected || refusal.Detail == "" {
		t.Fatalf("a refused merge = %v, want a rejection carrying the provider's message", err)
	}
	if merges := h.callsFor("merge"); len(merges) != 1 || !merges[0].hasArg("--merge") {
		t.Fatalf("the refused merge was asked %d times (%+v)", len(merges), merges)
	}

	none := newWriteHarness(t, ghScript{"merge-methods": {{Stdout: methodsJSON(false, false, false)}}})
	if _, err := none.gh.Merge(context.Background(), 12, headCommit); !errors.As(err, &refusal) || refusal.Reason != forge.ReasonRejected {
		t.Fatalf("a repository permitting no method = %v", err)
	}
	if len(none.callsFor("merge")) != 0 {
		t.Fatal("a merge was asked of a repository that permits no method")
	}

	elsewhere := newWriteHarness(t, ghScript{"repo": {{Stdout: repoViewJSON("other/name")}}})
	if _, err := elsewhere.gh.Merge(context.Background(), 12, headCommit); !errors.As(err, &refusal) ||
		refusal.Reason != forge.ReasonWrongRepository {
		t.Fatalf("a merge on a specifier resolving elsewhere = %v", err)
	}
	if len(elsewhere.callsFor("merge")) != 0 {
		t.Fatal("a merge was asked of a repository that resolves elsewhere")
	}

	unbound := newWriteHarness(t, ghScript{})
	for _, head := range []string{"", "-x", "a b"} {
		if _, err := unbound.gh.Merge(context.Background(), 12, head); !errors.Is(err, forge.ErrInvalidArgument) {
			t.Fatalf("a merge bound to %q = %v, want an argument refusal", head, err)
		}
	}
	if len(unbound.calls()) != 0 {
		t.Fatalf("an unbound merge invoked the provider: %+v", unbound.calls())
	}
}
