package safety_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// incorporationGit is a history where main has moved past the fork and the
// feature branch holds two commits, with names for both tips so a test can
// pass a reference and see the identifier come back.
func incorporationGit() *fakeGit {
	parents := linear("base", "f1", "f2")
	parents["main"] = []string{"base"}
	return &fakeGit{
		parents: parents,
		revs:    map[string]string{"feature": "f2", "refs/heads/main": "main"},
	}
}

// refusedIncorporation runs a check expected to fail closed and returns the
// refusal, asserting what every one has to be true of: the result it comes
// with is no proof, and it is not a branch update refusal.
func refusedIncorporation(t *testing.T, g *safety.Guard, commits []string, into string) *safety.IncorporationRefusal {
	t.Helper()
	result, err := g.Incorporation(context.Background(), commits, into)
	if result.Incorporated() {
		t.Fatalf("Incorporation reported %v beside its error", result)
	}
	var refusal *safety.IncorporationRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Incorporation: %v, want an *IncorporationRefusal", err)
	}
	if !errors.Is(err, safety.ErrNotEstablished) {
		t.Errorf("the refusal does not match ErrNotEstablished: %v", err)
	}
	if errors.Is(err, safety.ErrRefused) {
		t.Errorf("the refusal matches ErrRefused, so a caller would read a cleanup stop as a push refusal: %v", err)
	}
	return refusal
}

// A fact the check cannot read fails closed: a tip or a commit that does not
// resolve, a mechanism that could not answer, and an empty commit set are
// each the separate typed result, never a result reading false. That is the
// refusal half of PRD principle P12's content proof; nothing here removes a
// copy.
func TestIncorporationFailsClosedOnAnUnreadableFact(t *testing.T) {
	principles.Cite(t, principles.P12)
	t.Parallel()

	g := safety.New(incorporationGit())
	if r := refusedIncorporation(t, g, []string{"feature"}, "nowhere"); !errors.Is(r, vcs.ErrRefNotFound) {
		t.Errorf("an unresolvable tip: cause %v, want ErrRefNotFound", r.Cause)
	}
	if r := refusedIncorporation(t, g, []string{"f1", "ghost"}, "main"); !errors.Is(r, vcs.ErrRefNotFound) {
		t.Errorf("an unresolvable commit: cause %v, want ErrRefNotFound", r.Cause)
	}
	if r := refusedIncorporation(t, g, nil, "main"); r.Cause != nil {
		t.Errorf("an empty commit set: cause %v, want none", r.Cause)
	}

	broken := incorporationGit()
	broken.incorporatedErr = errBroken
	if r := refusedIncorporation(t, safety.New(broken), []string{"feature"}, "main"); !errors.Is(r, errBroken) || !errors.Is(r, vcs.ErrInconclusive) {
		t.Errorf("a mechanism that could not answer: cause %v, want the failure and ErrInconclusive", r.Cause)
	}
}

// The result names what was examined: the identifiers the named revisions
// resolved to, in the order named, and the identifier of the tip, which are
// exactly what the mechanism was asked about. A reference passed in comes
// back as the commit it named at the time.
func TestIncorporationNamesTheIdentifiersItExamined(t *testing.T) {
	t.Parallel()

	for _, answer := range []bool{true, false} {
		git := incorporationGit()
		git.incorporated = answer
		result, err := safety.New(git).Incorporation(context.Background(), []string{"feature", "f1"}, "refs/heads/main")
		if err != nil {
			t.Fatalf("Incorporation: %v", err)
		}
		if result.Incorporated() != answer {
			t.Errorf("Incorporated() = %v, want the mechanism's %v", result.Incorporated(), answer)
		}
		if want := []string{"f2", "f1"}; !slices.Equal(result.Commits(), want) {
			t.Errorf("Commits() = %v, want %v", result.Commits(), want)
		}
		if result.Into() != "main" {
			t.Errorf("Into() = %q, want the identifier main", result.Into())
		}
		if !slices.Equal(git.examined.commits, result.Commits()) || git.examined.into != result.Into() {
			t.Errorf("the mechanism was asked about %v into %q, and the result names %v into %q",
				git.examined.commits, git.examined.into, result.Commits(), result.Into())
		}
		// The record cannot be edited through the accessor.
		result.Commits()[0] = "edited"
		if result.Commits()[0] != "f2" {
			t.Errorf("Commits() handed out the record itself rather than a copy")
		}
	}
}

// The zero result is no proof, so a caller that dropped the error holds
// nothing.
func TestZeroIncorporationResultIsNoProof(t *testing.T) {
	t.Parallel()
	var zero safety.IncorporationResult
	if zero.Incorporated() || zero.Into() != "" || len(zero.Commits()) != 0 {
		t.Errorf("the zero result reads as a proof: %v", zero)
	}
}

// Positive control for the separation: a branch update refusal still matches
// ErrRefused and does not match ErrNotEstablished, so the two sentinels
// partition the two questions this package answers.
func TestUpdateRefusalsStillMatchErrRefusedAndNotErrNotEstablished(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("base", "tip"),
		advertised: map[string][][]advert{remote: {{branch(ref, "tip")}}},
	}
	g := safety.New(git)
	anchor := observe(t, g)
	git.remoteErr = map[string]error{remote: errBroken}
	_, err := g.Decide(context.Background(), safety.Update{Target: target, Proposed: "tip", Anchor: anchor})
	if !errors.Is(err, safety.ErrRefused) {
		t.Fatalf("Decide over an unreadable remote: %v, want ErrRefused", err)
	}
	if errors.Is(err, safety.ErrNotEstablished) {
		t.Errorf("a branch update refusal matches ErrNotEstablished: %v", err)
	}
}
