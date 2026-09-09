package vcs_test

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// TestCreateRefAndDeleteRefNeverMoveAnExistingReference is the contract the
// gate's commit-keyed anchors rest on: creation is the only write CreateRef
// can make, and DeleteRef removes only a reference still holding what the
// caller says it holds.
//
// The refusing halves are driven against a reference that exists, because the
// accepting halves alone would pass against an unconditional update - which
// is exactly the write these operations exist to be incapable of. That was
// checked by making CreateRef's write unconditional and watching the refusal
// assertions here fail.
func TestCreateRefAndDeleteRefNeverMoveAnExistingReference(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, first, second := sourceRepo(t)
	repo, err := vcs.OpenWorktree(c, source)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}
	name := "refs/assistant/submitted/" + first

	if err := repo.CreateRef(c, name, first); err != nil {
		t.Fatalf("CreateRef on a fresh name: %v", err)
	}
	if got, err := repo.ResolveCommit(c, name); err != nil || got != first {
		t.Fatalf("the created reference resolves to %q, %v; want %q", got, err, first)
	}

	// The refusal is about existence, not disagreement: a name that exists is
	// never rewritten, not even back to the value it already holds.
	if err := repo.CreateRef(c, name, second); err == nil {
		t.Fatal("CreateRef rewrote an existing reference to a new commit")
	}
	if got, _ := repo.ResolveCommit(c, name); got != first {
		t.Fatalf("the refused creation moved the reference to %q; want it untouched at %q", got, first)
	}
	if err := repo.CreateRef(c, name, first); err == nil {
		t.Fatal("CreateRef reported success for a name that already exists")
	}

	if err := repo.CreateRef(c, "refs/assistant/submitted/none", "no-such-revision"); !errors.Is(err, vcs.ErrRefNotFound) {
		t.Fatalf("CreateRef at an unresolvable commit = %v; want ErrRefNotFound", err)
	}

	if err := repo.DeleteRef(c, name, second); err == nil {
		t.Fatal("DeleteRef removed a reference holding a commit other than the one named")
	}
	if got, _ := repo.ResolveCommit(c, name); got != first {
		t.Fatalf("the refused deletion left the reference at %q; want %q", got, first)
	}
	if err := repo.DeleteRef(c, name, first); err != nil {
		t.Fatalf("DeleteRef with the value the reference holds: %v", err)
	}
	if _, err := repo.ResolveCommit(c, name); !errors.Is(err, vcs.ErrRefNotFound) {
		t.Fatalf("the reference still resolves after deletion: %v", err)
	}
}
