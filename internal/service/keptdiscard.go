package service

import (
	"context"
	"errors"
	"fmt"
	"time"

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
// commits that anything the removal destroys names reaches (a reference, a
// root reference such as ORIG_HEAD, a reflog entry by either of its
// values, or HEAD; for a run's worktree only its own names and the stash
// entries it made, never the gate's shared references or reflogs, which
// stand after it) and that neither the default branch, read fresh, nor
// what the upstream holds now reaches, a remote-tracking reference and the
// gate's references holding nothing of themselves, so a commit a run's own
// names reach is shown even when the gate records it too, with every tag,
// tree or blob such a
// name or value records that none of those is, every path whose content
// or deletion nothing else holds, as git would store it, ignored files and
// deletions included, and the three indicators git reports, with a digest over exactly what is shown, each path by what it
// holds. A default branch that cannot be read now is refused rather than
// shown a list that may overstate or understate, naming why. A discard
// given a digest is compared against that read again: a copy in which
// anything shown changed since, a commit, an edit, a deletion, a file or a
// stash among it, digests differently and is refused, and the person is
// told to look again; a change to nothing shown, a branch made at a commit
// already shown among them, is no reason to refuse. Nothing is recorded for
// a discard shown or refused.
//
// # The order, and what a stop leaves
//
// A discard is recorded as two facts, so the entry is never answered over
// a copy that stands. The confirmation is recorded first, with the digest
// and the commits it was shown and who gave it
// (store.ConfirmKeptWorkDiscard), and leaves the entry open: status, the
// catch-up read and the supervisor loop list it, and the project's new
// tasks still wait on it. Then internal/tasks' Discard removes the copy,
// checking on the way that it holds exactly what the digest sealed and
// that what the default branch lacks of it now is among what was shown; a
// default branch that only moved since is no reason to refuse. Then the
// entry is answered discard and marked in one write
// (store.MarkKeptWorkDiscarded), which is when it closes and the project's
// tasks dispatch. A copy that no longer holds what the confirmation was
// shown has the confirmation withdrawn (store.WithdrawKeptWorkDiscard) and
// is left standing, with the entry open to be shown and confirmed afresh
// (discardWithdrawn). A service that stops between the confirmation and
// the mark leaves an open entry with a confirmation, which the next call
// on it or the next start carries out (completeDiscardsAtOpen): removed if
// the copy still holds exactly what was shown or marked if it is already
// gone, and withdrawn if it changed. Any other failure, an upstream that
// cannot be read among them, leaves the confirmation standing and the
// entry open and listed, and no other answer is taken on the copy until it
// is carried out or withdrawn (store.ErrKeptWorkCopyOwed); a promotion of
// the task is refused the same way. One call carries a copy's confirmation
// out at a time in this service (claimDiscard); a second is refused while
// it does.
//
// # Every entry of the copy
//
// A copy kept again at another state has an entry per state, and a
// discard removes the copy whole, which is what was shown and confirmed.
// So the mark answers and marks every open entry of the copy kept by the
// time the removal's read of the copy began, or by the confirmation when
// that is later, in its one write, none of them left holding the project's
// new tasks over a copy that is gone: the removal went ahead only because
// that read found nothing lost that was not shown, so it covers the state
// each of them was kept from; completeDiscard reads its
// entry again first, so a call that finds it marked reports it discarded.
// No answer and no confirmation is written while another entry of the copy
// holds one not yet carried out, a land whose run is not named, a carry on
// not marked or a discard confirmed (store.ErrKeptWorkCopyOwed, refused
// with refusedOwed): each still needs the copy where it stands.
//
// What is left: the moment between the removal's last read of the copy
// and the removal is not closed (internal/tasks' Discard names it).
// Anything written into the copy in that moment, a file git ignores, an
// edit or a deletion among it, is destroyed with the copy without having
// been shown, and the mark answers and marks the entries all the same, so
// nothing stays listed for it. A cleanup that keeps the copy then records
// no new entry for such a change: an entry is keyed by the copy's path and
// the commits its references and HEAD stand on (store.KeptWorkOf), which a
// file, an edit or a deletion leaves as they were, so the cleanup finds the
// entry already standing. Only a change to those commits in that moment
// has a cleanup record a new entry, kept after the removal's read began and
// so not marked: it is left open over a copy that is gone, and listed. One
// a cleanup records after the confirmation and before that read is marked
// with the rest when the read finds nothing lost unshown, a reference made
// or removed at a commit already shown or held upstream among it, and the
// removal is refused when it does.

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
	if entry.Answer.Or("") == store.KeptDiscard {
		return machine.KeptWorkAnswered{Entry: entry, Effect: machine.KeptWorkDiscarded}, nil
	}
	if !entry.Open() {
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	}
	if entry.DiscardConfirmed() {
		// A confirmation this answer finds standing, one a stopped service
		// left: it is carried out, or withdrawn and this answer taken
		// afresh.
		done, err := s.completeDiscard(ctx, entry)
		var withdrawn *discardWithdrawn
		switch {
		case errors.As(err, &withdrawn):
			entry = withdrawn.entry
		case err != nil:
			return machine.KeptWorkAnswered{}, err
		default:
			return done, nil
		}
	}
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
	confirmed, err := s.store.ConfirmKeptWorkDiscard(ctx, entry.ID, confirm, loss.Lacks, req.HoldResolver())
	switch {
	case errors.Is(err, store.ErrKeptWorkAnswered) || errors.Is(err, store.ErrKeptWorkClosed):
		again, readErr := s.store.KeptWorkEntry(ctx, entry.ID)
		if readErr != nil {
			return machine.KeptWorkAnswered{}, readErr
		}
		if again.Answer.Or("") == store.KeptDiscard {
			return machine.KeptWorkAnswered{Entry: again, Effect: machine.KeptWorkDiscarded}, nil
		}
		return machine.KeptWorkAnswered{}, refusedAnswered(again)
	case errors.Is(err, store.ErrKeptWorkCopyOwed):
		return machine.KeptWorkAnswered{}, refusedOwed(entry, err)
	case err != nil:
		return machine.KeptWorkAnswered{}, err
	}
	done, err := s.completeDiscard(ctx, confirmed)
	var withdrawn *discardWithdrawn
	if errors.As(err, &withdrawn) {
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d was not discarded: %w", ipc.ErrRefused, entry.ID, err)
	}
	return done, err
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
	files := make([]machine.KeptWorkLostPath, len(l.Files))
	for i, f := range l.Files {
		files[i] = machine.KeptWorkLostPath{Kind: string(f.Kind), Path: f.Path}
	}
	objects := make([]machine.KeptWorkLostObject, len(l.Objects))
	for i, o := range l.Objects {
		objects[i] = machine.KeptWorkLostObject{Object: o.Object, Type: o.Type, Names: o.Names}
	}
	return machine.KeptWorkLoss{Head: l.Head, Lacks: l.Lacks, Objects: objects, Files: files, Dirty: l.Dirty, Untracked: l.Untracked,
		Stash: l.Stash, Digest: l.Digest}
}

// discardWithdrawn is completeDiscard's account of a confirmation it took
// back because the copy no longer holds what it was shown: entry is the
// entry as the withdrawal left it, open with no confirmation.
type discardWithdrawn struct {
	entry store.KeptWork
	cause error
}

func (e *discardWithdrawn) Error() string {
	return fmt.Sprintf("the copy changed since the discard was confirmed (%v), so the confirmation was withdrawn, the copy left "+
		"standing and the entry is open again; answer discard without a confirmation to see what it holds now", e.cause)
}

func (e *discardWithdrawn) Unwrap() error { return e.cause }

// completeDiscard carries out the discard confirmation standing on the
// entry, on this file's terms: the copy is removed while it still holds
// exactly what the confirmation was shown, or is already gone, and the
// entry is then answered discard and marked in one write; a copy that no
// longer holds that has the confirmation withdrawn and is left standing,
// with the entry open (*discardWithdrawn). Anything else leaves the
// confirmation standing for the next call or the next start. One call
// carries out a copy's confirmation at a time in this service; another is
// refused while it does.
func (s *Service) completeDiscard(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if !s.claimDiscard(entry.CopyPath) {
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d: a discard of its copy is being carried out now; "+
			"answer again once it is done", ipc.ErrRefused, entry.ID)
	}
	defer s.releaseDiscard(entry.CopyPath)
	// Read again: another call may have carried the confirmation out, or a
	// sibling's mark answered this entry with it.
	again, err := s.store.KeptWorkEntry(ctx, entry.ID)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	entry = again
	if entry.Answer.Or("") == store.KeptDiscard {
		return machine.KeptWorkAnswered{Entry: entry, Effect: machine.KeptWorkDiscarded}, nil
	}
	if !entry.DiscardConfirmed() {
		if !entry.Open() {
			return machine.KeptWorkAnswered{}, refusedAnswered(entry)
		}
		return machine.KeptWorkAnswered{}, &discardWithdrawn{entry: entry, cause: errors.New("another call withdrew it")}
	}
	if s.betweenDiscardRecordAndRemoval != nil {
		if err := s.betweenDiscardRecordAndRemoval(); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
	}
	digest := entry.DiscardDigest.Or("")
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
	// The removal is held to a read of the copy that begins now, so every
	// entry of the copy kept by now was kept from a state that read covers.
	read := time.Now()
	lost, err := s.cleaner.Discard(ctx, entry, digest, entry.DiscardLacks, gatePath)
	if errors.Is(err, tasks.ErrLossChanged) {
		open, werr := s.store.WithdrawKeptWorkDiscard(ctx, entry.ID, digest)
		if werr != nil {
			return machine.KeptWorkAnswered{}, fmt.Errorf("service: kept work entry %d: the copy changed since its discard was "+
				"confirmed (%v), and the confirmation could not be withdrawn: %w", entry.ID, err, werr)
		}
		withdrawn := &discardWithdrawn{entry: open, cause: err}
		s.log.Printf("kept work entry %d: %v", entry.ID, withdrawn)
		return machine.KeptWorkAnswered{}, withdrawn
	} else if err != nil {
		return machine.KeptWorkAnswered{}, refusedLoss(entry, err)
	}
	marked, err := s.store.MarkKeptWorkDiscarded(ctx, entry.ID, read)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	s.log.Printf("kept work entry %d was discarded: the copy at %s is gone", entry.ID, entry.CopyPath)
	s.dispatchInBackground()
	gone := lossOf(lost)
	return machine.KeptWorkAnswered{Entry: marked, Effect: machine.KeptWorkDiscarded, Loss: &gone}, nil
}

// claimDiscard takes the copy's discard slot in this service, false when a
// call holds it.
func (s *Service) claimDiscard(copyPath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.discards == nil {
		s.discards = make(map[string]bool)
	}
	if s.discards[copyPath] {
		return false
	}
	s.discards[copyPath] = true
	return true
}

// releaseDiscard gives the copy's discard slot back.
func (s *Service) releaseDiscard(copyPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.discards, copyPath)
}

// completeDiscardsAtOpen carries out or withdraws every discard
// confirmation a stopped service recorded and did not carry out, on this
// file's terms, and logs what it could not; a withdrawal is logged by
// completeDiscard.
func (s *Service) completeDiscardsAtOpen(ctx context.Context) {
	owed, err := s.store.KeptWorkDiscardsOwed(ctx)
	if err != nil {
		s.log.Printf("the kept work discards owed could not be read at open: %v", err)
		return
	}
	for _, entry := range owed {
		var withdrawn *discardWithdrawn
		if _, err := s.completeDiscard(ctx, entry); err != nil && !errors.As(err, &withdrawn) {
			s.log.Printf("kept work entry %d holds a discard confirmation that could not be carried out, and stays open: %v", entry.ID, err)
		}
	}
}
