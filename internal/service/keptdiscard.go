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
// internal/tasks' Loss reads what would be lost: the commits the default
// branch lacks, from the entry's basis, and the three indicators of what no
// commit records, read from the copy now, with a digest over the copy's
// state. An entry whose basis is not established is refused rather than
// shown a list that may overstate or understate, naming why, until a later
// cleanup of the unchanged copy establishes it. A discard given a digest is
// compared against that read again: a copy that changed since, by a commit,
// an edit, a file or a stash, digests differently and is refused, and the
// person is told to look again. Nothing is recorded for a discard shown or
// refused.
//
// # The order, and what a stop leaves
//
// The answer is recorded first, with the digest it was shown
// (store.AnswerKeptWorkDiscard), which closes the entry so the project's
// new tasks no longer wait on it; then internal/tasks' Discard removes the
// copy, checking the digest again on the way; then the entry is marked
// discarded. A service that stops between the answer and the mark leaves an
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
// caller is the command surface. What the command surface enforces, by the
// owner's decision on #411: a confirmation is taken only from a person at
// a terminal (internal/cli's Environment.Interactive, never under --json),
// so a headless coordinator agent, which reaches the service over the
// socket from no terminal, cannot confirm one, while the preview stays
// scriptable. What neither enforces, on internal/store's Resolver terms: a
// person running an agent in a terminal's foreground can have it confirm,
// and a confirmation that reaches this method over the socket from such a
// process is indistinguishable here from the person's own, so the answer
// is recorded as the machine interface's (ipc.Request.HoldResolver) and
// never as a person's. The terminal check narrows who can confirm; it does
// not witness a person, and the record never claims one.

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
		if confirm != loss.Digest {
			return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d was not discarded: the digest given, %s, is not the "+
				"copy's as it stands, %s, so either the copy changed since it was shown or the digest is another's; answer discard "+
				"without a confirmation to see what it holds now", ipc.ErrRefused, entry.ID, confirm, loss.Digest)
		}
		answered, err := s.store.AnswerKeptWorkDiscard(ctx, entry.ID, confirm, req.HoldResolver())
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
	lost, err := s.cleaner.Discard(ctx, entry, digest, gatePath)
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
