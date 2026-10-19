package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// changeStanding is where this checkout's change stands on the way to a run,
// which assistant doctor reports beside its checks so that a first run is the
// same loop as every other doctor answer: do what the line says, then run
// doctor again. It is asked only once the gate check passed, because before
// that the gate row's step comes first and there is no gate to compare with.
//
// The steps are taken in the order a first run meets them, and the first one
// that applies is the answer: a branch to stand on, a commit to make, and
// then what the gate holds for the branch, which decides between sending,
// replacing what was sent, and following what was already sent. The
// indicators are read first, so the structured answer carries them whatever
// the step.
//
// It reads and writes nothing in either repository: the working copy's
// status, HEAD and two references, and the gate's reference for the branch.
// What it does not establish is stated where each read is made.
func changeStanding(ctx context.Context, home string, records *store.Store, working string) *machine.Change {
	change := &machine.Change{}
	unreadable := func(err error) *machine.Change {
		return &machine.Change{Step: machine.ChangeUnreadable, Branch: change.Branch, DefaultBranch: change.DefaultBranch, Detail: err.Error()}
	}
	record, err := records.RepositoryAt(ctx, working)
	if err != nil {
		return unreadable(err)
	}
	change.DefaultBranch = record.DefaultBranch
	binding, err := records.GateBinding(ctx, working)
	if err != nil {
		return unreadable(err)
	}
	repo, err := vcs.OpenWorktree(ctx, working, vcs.WithRedactor(redact.New()))
	if err != nil {
		return unreadable(err)
	}
	changes, err := repo.Changes(ctx)
	if err != nil {
		return unreadable(err)
	}
	change.Untracked, change.Unstaged, change.Staged, change.Unmerged =
		changes.Untracked, changes.Unstaged, changes.Staged, changes.Unmerged

	branch, err := repo.HeadBranch(ctx)
	switch {
	case errors.Is(err, vcs.ErrDetachedHead):
		change.Step = machine.ChangeDetached
		return change
	case err != nil:
		return unreadable(err)
	}
	change.Branch = branch
	if branch == record.DefaultBranch {
		change.Step = machine.ChangeOnDefaultBranch
		return change
	}
	if !changes.Clean() {
		change.Step = machine.ChangeUncommitted
		return change
	}
	head, err := repo.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return unreadable(fmt.Errorf("reading the commit this branch is on: %w", err))
	}

	// What the branch holds beyond its default branch is read against
	// origin's copy of that branch as this checkout last fetched it, which
	// is where init read the default branch's name from, and against the
	// local branch of that name when the checkout has no such copy. Neither
	// is fetched, so a default branch that moved since is compared as it
	// was; with neither present the comparison is not made and the branch
	// is taken to hold something to send.
	for _, base := range []string{"refs/remotes/origin/" + record.DefaultBranch, "refs/heads/" + record.DefaultBranch} {
		if _, err := repo.ResolveCommit(ctx, base); errors.Is(err, vcs.ErrRefNotFound) {
			continue
		} else if err != nil {
			return unreadable(err)
		}
		ahead, err := repo.CommitsNotIn(ctx, head, base)
		if err != nil {
			return unreadable(err)
		}
		if len(ahead) == 0 {
			change.Step = machine.ChangeNothingNew
			return change
		}
		break
	}

	// What the gate holds for the branch is read from the gate itself,
	// located the way internal/service locates it, and never from this
	// checkout's record of its last send, which a send that the gate
	// refused would leave behind.
	path, err := gate.RepositoryFor(home, binding.GateID)
	if err != nil {
		return unreadable(err)
	}
	bare, err := vcs.OpenBare(ctx, path, vcs.WithRedactor(redact.New()))
	if err != nil {
		return unreadable(err)
	}
	sent, err := bare.ResolveCommit(ctx, "refs/heads/"+branch)
	switch {
	case errors.Is(err, vcs.ErrRefNotFound):
		change.Step = machine.ChangeUnsent
		change.Command = sendCommand(branch, false)
		return change
	case err != nil:
		return unreadable(err)
	case sent == head:
		change.Step = machine.ChangeSent
		return change
	}
	// A commit the gate holds that this checkout does not have is one the
	// branch does not hold either, so it reads as replaced, which is what a
	// send would do to it.
	if _, err := repo.ResolveCommit(ctx, sent); errors.Is(err, vcs.ErrRefNotFound) {
		change.Step = machine.ChangeDiverged
		change.Command = sendCommand(branch, true)
		return change
	} else if err != nil {
		return unreadable(err)
	}
	forward, err := repo.IsAncestor(ctx, sent, head)
	if err != nil {
		return unreadable(err)
	}
	if !forward {
		change.Step = machine.ChangeDiverged
		change.Command = sendCommand(branch, true)
		return change
	}
	change.Step = machine.ChangeUnsent
	change.Command = sendCommand(branch, false)
	return change
}

// sendCommand is the command line that hands a branch to the gate through
// its remote, quoted for a POSIX shell so a person can copy it exactly. A
// send that replaces what the gate holds carries --force-with-lease rather
// than --force, so the replacement the person chooses asks git for its lease
// check; whatever that check does is git's, and nothing here verifies it.
func sendCommand(branch string, replace bool) string {
	if replace {
		return "git push --force-with-lease " + gate.RemoteName + " " + shellQuote(branch)
	}
	return "git push " + gate.RemoteName + " " + shellQuote(branch)
}

// The labels doctor prints its change's machine text after, named so a test
// can tell the labelled lines from the rows.
const (
	changeDefaultBranchLabel = "Default branch"
	changeCommandLabel       = "Command:"
)

// readOutChange prints the rows for where the change stands, and beside them
// the default branch's name for the row that names it and the command for
// the rows that point at one, both as machine text.
func readOutChange(w io.Writer, change machine.Change) {
	for _, row := range machine.ChangePhrases(change) {
		writeln(w, row)
	}
	if change.Step == machine.ChangeOnDefaultBranch && change.DefaultBranch != "" {
		writef(w, "%s %s\n", changeDefaultBranchLabel, printable(change.DefaultBranch))
	}
	if change.Command != "" {
		writef(w, "%s %s\n", changeCommandLabel, printable(change.Command))
	}
}
