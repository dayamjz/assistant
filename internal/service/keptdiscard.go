package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// Discarding kept work: PRD section 6's "you name the copy, the program
// shows exactly what will be lost, and it removes the copy only on that
// answer, recording who discarded what", on the owner's decision for #411:
// a discard answer alone shows what would be lost with a digest of it and
// removes nothing, and the same answer given again with the digest removes
// the copy only if it still holds exactly what was shown.
//
// # What is shown, and what is confirmed
//
// internal/tasks' Loss reads what would be lost from the copy now: the
// commits the default branch, read fresh, lacks, and the three indicators of
// what no commit records, with a digest over the copy's state and those
// commits. A default branch that cannot be read now is refused rather than
// shown a list that may overstate or understate, naming why. A discard given a digest is
// compared against that read again: a copy that changed since, by a commit,
// an edit, a file or a stash, digests differently and is refused, and the
// person is told to look again. Nothing is recorded for a discard shown or
// refused.
//
// # The order, and what a stop leaves
//
// The answer is recorded first, with the digest it was shown and the
// commits it was shown as lost (store.AnswerKeptWorkDiscard), which closes
// the entry so the project's new tasks no longer wait on it; then
// internal/tasks' Discard removes the copy, checking on the way that it
// holds exactly what the digest sealed and that what the default branch
// lacks of it now is among what was shown; then the entry is marked
// discarded. A default branch that only moved since is no reason to refuse. A service that stops between the answer and the mark leaves an
// entry answered discard with a digest and no instant, which the next call
// or the next start completes (completeDiscardsAtOpen): the copy is removed
// if it still holds exactly what the digest was shown for, or is already
// gone, and the entry is marked. What is not recovered: an answered
// discard whose copy changed before it was removed, which is never removed
// by the program and is named in the service log at each start; the
// answer stands, so it cannot be shown and confirmed again.
//
// # What "person-only" is enforced by, and what it is not
//
// PRD section 6 has discard as the person's answer alone. What this file
// enforces: a task's worker is refused (refuseWorker, from the socket's
// credentials and the caller's lineage), a subordinate coordinator is
// refused (refuseDecision), and nothing in the program discards on its own,
// since the one producer of a discard answer is this method and its one
// caller is the command surface. What this file enforces for a
// confirmation, by the owner's decision on #411: foreground.go's relation,
// the one the local landing's approval hold is held to, so a confirmation
// is admitted only from a peer the kernel places in the foreground of its
// controlling terminal (agents.ReadLineage, Lineage.Foreground), decided
// here from the credentials and the lineage and never from anything the
// caller states; a peer with no controlling terminal, one in a group the
// terminal was not handed to, and one whose lineage cannot be read are
// refused, the first two with discardOffTerminal and the last as
// unavailable, before the answer is recorded. A headless coordinator agent,
// which reaches the service over the socket from no terminal, cannot
// confirm one, whether through the command line or the socket itself, while
// the preview stays scriptable. internal/cli refuses the same confirmation
// earlier, under --json or off a terminal (Environment.Interactive), with
// the same row, which is the early refusal and not the mechanism.
//
// What the relation buys and does not, on foreground.go's terms: a
// terminal's foreground is not a person. A program that allocates a
// pseudo-terminal of its own, a terminal multiplexer among them, puts its
// command in a terminal's foreground, and a person running an agent in a
// terminal's foreground can have it confirm; such a confirmation is
// indistinguishable here from the person's own, so the answer is recorded
// as the machine interface's (ipc.Request.HoldResolver) and never as a
// person's, on internal/store's Resolver terms. And the lineage is read when
// the request is decided, so a chain the kernel rewrote when a process
// between the peer and its terminal exited, which workers.go names for a
// worker's push, reads as the chain that stands then. The relation narrows
// who can confirm; it does not witness a person, and the record never
// claims one.

// discardKeptWork is the discard answer, on this file's terms.
func (s *Service) discardKeptWork(ctx context.Context, req ipc.Request, entry store.KeptWork, confirm string) (machine.KeptWorkAnswered, error) {
	if !entry.Open() && entry.Answer.Or("") != store.KeptDiscard {
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	}
	if entry.Open() {
		loss, err := s.cleaner.Loss(ctx, entry)
		if err != nil {
			return machine.KeptWorkAnswered{}, refusedLoss(entry, err)
		}
		shown := lossOf(loss)
		if confirm == "" {
			return machine.KeptWorkAnswered{Entry: entry, Effect: machine.KeptWorkDiscardShown, Loss: &shown}, nil
		}
		if err := s.refuseOffTerminal(req); err != nil {
			if errors.Is(err, ipc.ErrNotAtTerminal) {
				return machine.KeptWorkAnswered{}, discardOffTerminal{cause: err}
			}
			return machine.KeptWorkAnswered{}, err
		}
		if confirm != loss.Digest {
			return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d was not discarded: the digest given, %s, is not the "+
				"copy's as it stands, %s, so either the copy changed since it was shown or the digest is another's; answer discard "+
				"without a confirmation to see what it holds now", ipc.ErrRefused, entry.ID, confirm, loss.Digest)
		}
		answered, err := s.store.AnswerKeptWorkDiscard(ctx, entry.ID, confirm, loss.Lacks, req.HoldResolver())
		switch {
		case errors.Is(err, store.ErrKeptWorkAnswered) || errors.Is(err, store.ErrKeptWorkClosed):
			again, readErr := s.store.KeptWorkEntry(ctx, entry.ID)
			if readErr != nil {
				return machine.KeptWorkAnswered{}, readErr
			}
			if again.Answer.Or("") != store.KeptDiscard {
				return machine.KeptWorkAnswered{}, refusedAnswered(again)
			}
			entry = again
		case err != nil:
			return machine.KeptWorkAnswered{}, err
		default:
			entry = answered
			s.dispatchInBackground()
		}
	}
	return s.completeDiscard(ctx, entry)
}

// discardOffTerminal is the refusal of a discard confirmation from a peer
// not in the foreground of a terminal, foreground.go's relation applied to
// the one other answer PRD section 6 gives the person alone. Its text is
// the row a person reads, machine.PhraseDiscardNeedsTerminal, with what the
// relation read; it unwraps to ipc.ErrNotAtTerminal, which is the code that
// crosses the wire.
type discardOffTerminal struct{ cause error }

// Error is the row, then what was read of the peer.
func (e discardOffTerminal) Error() string {
	return machine.PhraseDiscardNeedsTerminal.String() + " (" + e.cause.Error() + ")"
}

// Unwrap resolves to the relation's refusal, so the wire code is its own.
func (e discardOffTerminal) Unwrap() error { return e.cause }

// refusedLoss is the refusal of a discard whose loss could not be read.
func refusedLoss(entry store.KeptWork, err error) error {
	switch {
	case errors.Is(err, tasks.ErrLossUnestablished):
		return fmt.Errorf("%w: kept work entry %d was not discarded: %v; the next cleanup of the unchanged copy establishes it, "+
			"and until then what a discard would lose cannot be shown exactly", ipc.ErrRefused, entry.ID, err)
	case errors.Is(err, tasks.ErrCopyGone):
		return fmt.Errorf("%w: kept work entry %d was not discarded: %w", ipc.ErrRefused, entry.ID, err)
	}
	return err
}

// lossOf is tasks.Loss as the answer carries it.
func lossOf(l tasks.Loss) machine.KeptWorkLoss {
	return machine.KeptWorkLoss{Head: l.Head, Lacks: l.Lacks, Dirty: l.Dirty, Untracked: l.Untracked, Stash: l.Stash, Digest: l.Digest}
}

// completeDiscard carries out a discard answer already recorded on the
// entry, on this file's terms: the copy is removed while it still holds
// exactly what the answer was shown, and the entry is marked.
func (s *Service) completeDiscard(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	digest, ok := entry.DiscardDigest.Get()
	if !ok {
		return machine.KeptWorkAnswered{}, fmt.Errorf("service: kept work entry %d is answered discard and records nothing it was shown", entry.ID)
	}
	if entry.DiscardedAt.IsKnown() {
		return machine.KeptWorkAnswered{Entry: entry, Effect: machine.KeptWorkDiscarded}, nil
	}
	gatePath := ""
	if runID, ok := entry.RunID.Get(); ok {
		owner, err := s.store.Run(ctx, runID)
		if err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		repository, err := s.store.Repository(ctx, owner.RepositoryID)
		if err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		if gatePath, err = s.gatePathOf(ctx, repository); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
	}
	lost, err := s.cleaner.Discard(ctx, entry, digest, entry.DiscardLacks, gatePath)
	if errors.Is(err, tasks.ErrLossChanged) {
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d is answered discard and its copy was not removed: %w",
			ipc.ErrRefused, entry.ID, err)
	} else if err != nil {
		return machine.KeptWorkAnswered{}, refusedLoss(entry, err)
	}
	marked, err := s.store.MarkKeptWorkDiscarded(ctx, entry.ID)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	s.log.Printf("kept work entry %d was discarded: the copy at %s is gone", entry.ID, entry.CopyPath)
	gone := lossOf(lost)
	return machine.KeptWorkAnswered{Entry: marked, Effect: machine.KeptWorkDiscarded, Loss: &gone}, nil
}

// completeDiscardsAtOpen completes every discard answer a stopped service
// recorded and did not carry out, on this file's terms, and logs what it
// could not.
func (s *Service) completeDiscardsAtOpen(ctx context.Context) {
	owed, err := s.store.KeptWorkDiscardsOwed(ctx)
	if err != nil {
		s.log.Printf("the kept work discards owed could not be read at open: %v", err)
		return
	}
	for _, entry := range owed {
		if _, err := s.completeDiscard(ctx, entry); err != nil {
			s.log.Printf("kept work entry %d was answered discard and its discard could not be completed: %v", entry.ID, err)
		}
	}
}
