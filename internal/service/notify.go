package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// How a wake reaches the operator's private channel: the one outbound
// notification this build has, what it sends, when, and what it does not
// cover.
//
// # What is sent, and to where
//
// The channel is config.KeyNotifyCommand, a command line in the operator's
// own file and nowhere else: the key is global-only, so a repository copy
// naming it is refused when it is parsed (P7). This service reads it once,
// at open, from the configuration it resolved there; a change reaches the
// next service of the home and not this one. Empty, the default, starts
// nothing: no goroutine, no command, no record.
//
// For each wake the supervisor loop appends, of any of the queue's three
// shapes, the command is run once with the wake's row on standard input:
// one line of the JSON store.Wake encodes to for a wake about a task, of
// the JSON store.DomainWake encodes to for a wake about a domain, or of the
// JSON store.KeptWorkWake encodes to for a wake about a kept work entry no
// task names, as the record holds it when it is offered, and then end of
// file. A task wake's line names the task in "task_id"; a domain wake's
// names the delegation in "delegation" and the domain in "domain"; a
// kept-work wake's names the entry in "kept_work" and its project and
// branch in "project" and "branch"; neither of the last two carries a "task_id", so the command
// tells the three apart by those fields. "kind" is subordinate-gone,
// subordinate-escalated or hold-opened for the second shape and kept-work
// for the third. That
// is the row bare, not the ipc.Event wakes.subscribe pushes: the stream
// carries the same encoding as an event's payload, beside its type and
// revision, and the command is given no such envelope. Every field of a
// task or domain wake's row is a closed word, a validated identifier, a
// time, or a marker the loop passed through internal/redact before writing
// it. A kept-work wake's row also names the entry's project and branch,
// which are a person's text read off the entry, so keptWorkNotice passes
// those two fields through internal/redact before the row is encoded. So
// no row carries a credential the redactor recognizes. The sender encodes
// the row as JSON and does not redact the encoded line: redaction on an
// encoded payload can corrupt the JSON and is not safe. The command is run
// through internal/stages' RunOperatorCommand, the runner every commands.*
// key runs through, in the home's root with this service's environment, so
// it carries the operator's credentials and that runner's gaps; no second
// process starter is written here. That runner ends the command's whole
// process group on unix when the bound or Close ends it, and leaves a
// descendant that left the group, or that a command exiting on its own left
// behind, running. Its output is not shown to anyone: the redacted end of it
// is kept on the record as evidence.
//
// Nothing the command does reaches the program. Its exit status and output
// are recorded and never read back as an answer, an acknowledgement, a
// hold's resolution or an instruction, so sending a notification authorizes
// nothing; a wake it was sent stays queued for the coordinator exactly as
// it was. A reply through the channel is not in this build: an answer to a
// hold has to arrive through a surface the service can place, and a command
// it started is not one.
//
// # Once per wake, and never on the loop
//
// The sender is one goroutine this service owns, registered under work, so
// Close ends it and waits for it. It is driven by wakeBell, the signal the
// loop's record rings after a write that appended a row, the same one an
// armed wakes.wait takes, and by a read at least once per wakeWaitRecheck,
// so the loop's only cost is the ring it already makes. The goroutine takes
// the bell before each read, so a row appended after the read is not
// missed. What it sends is decided from the record and never from the
// ring: store.PendingNotifications is the queued wakes with no notification
// record, oldest first, and store.PendingDomainNotifications and
// store.PendingKeptWorkNotifications the same for the other two shapes, so
// a wake appended before a restart and not yet offered is offered by the
// next service. A pass offers the task wakes first, then the domain wakes,
// then the kept-work wakes.
//
// Each wake is claimed before its command runs (store.ClaimNotification,
// or store.ClaimDomainNotification and store.ClaimKeptWorkNotification for
// the other shapes, each in a record of its own), and the claim is the
// whole of what keeps it from being sent twice:
// a wake with any record is never offered again, so delivery is at most
// once. The claim is settled once when the command ends
// (store.SettleNotification, store.SettleDomainNotification,
// store.SettleKeptWorkNotification):
// sent for a command that exited zero, failed for one that exited non-zero,
// could not start, ran past notifyBound, or was ended by the service
// closing, each with its exit status when it reported one and a detail
// saying which. A failure is also written to the service log, naming the
// wake and never the command line, which may carry the channel's own
// secret. A failed wake is not retried, because a retry is a second send of
// a wake the command may already have delivered.
//
// Commands run one at a time, so a channel that hangs delays the wakes
// behind it and never the loop. On unix running the command for one wake
// takes at most notifyBound plus notifyGrace, the claim and settlement
// writes aside: the command is ended with its process group at notifyBound,
// and what it printed is read for at most notifyGrace after it ended, so a
// descendant still holding its output cannot extend that. Elsewhere only
// notifyBound on the command is stated: whether the read ends at
// notifyGrace there is what internal/stages' commandGrace says that package
// does not establish.
//
// A failure to claim a wake, or to read the pending ones, ends the pass and
// is logged once; the sender then waits the recheck interval, not the bell,
// before it reads again, so a store refusing every write is tried once per
// interval rather than once per wake or per ring.
//
// # Residual gaps
//
//   - A claim whose settlement was never written, a service that crashed
//     while the command ran among them, stays claimed with no outcome, and
//     its wake is not offered again. Whether it was delivered is unknown,
//     and the record says unknown rather than guessing.
//   - A settlement the store refused after the command ran ends the same
//     way: the claim stands with no outcome, the failure is written to the
//     service log and the pass goes on, and the wake is not offered again.
//     The operator may have received it, and the record cannot say so. The
//     log line names the wake and the store's error and not the command's
//     outcome, so how the command ended is not kept anywhere.
//   - Sent means the command exited zero, not that a person read anything.
//   - A wake the coordinator acknowledged before the sender reached it is not
//     offered, because PendingNotifications offers queued wakes only; a
//     coordinator that acts within the same moment can close a wake before
//     its notification runs. That is also what keeps a home whose operator
//     names a command for the first time from being sent its whole history.
//   - The record is read by no surface in this build: a failure is visible
//     in the service log and the notification table, not in a command's
//     answer.
//   - The bound, the grace and the batch are chosen, not measured, and are
//     not settings.
//   - A pass ended by a claim failure leaves the wakes behind it unoffered
//     until the store accepts a claim again; with the failure lasting, they
//     are offered late or, if acknowledged meanwhile, never.

// notifyBound is how long the notify command may run for one wake before
// it is ended and the wake recorded failed. It is chosen, not measured.
const notifyBound = 30 * time.Second

// notifyGrace is how long the notify command's output is given to arrive
// once the command has ended, on its own or at notifyBound, so a delivery
// takes at most notifyBound plus notifyGrace on unix. It is chosen, not
// measured.
const notifyGrace = 2 * time.Second

// notifyBatch is how many pending wakes one read offers.
const notifyBatch = 16

// notifyTail is how much of the end of the command's redacted output the
// record keeps.
const notifyTail = 2 << 10

// notifySettleBound bounds the write that settles a claim. It runs on a
// context that outlives a closing service, so a claim made before Close is
// still settled before the store closes.
const notifySettleBound = 10 * time.Second

// startNotifier starts the sender on this file's terms, or nothing when the
// operator named no command.
func (s *Service) startNotifier(hooks supervisionHooks) {
	line := s.cfg.NotifyCommand
	if line == "" {
		return
	}
	bound := notifyBound
	if hooks.notifyTimeout > 0 {
		bound = hooks.notifyTimeout
	}
	recheck := wakeWaitRecheck
	if hooks.notifyRecheck > 0 {
		recheck = hooks.notifyRecheck
	}
	if !s.startWork() {
		return
	}
	go func() {
		defer s.work.Done()
		for {
			rung := s.wakeBell.armed()
			if !s.notifyPending(s.stopCtx, line, bound) {
				// The store refused; the bell is not listened to, because
				// every append rings it and the refusal is not the
				// append's to end.
				rung = nil
			}
			wait := time.NewTimer(recheck)
			select {
			case <-s.stopCtx.Done():
				wait.Stop()
				return
			case <-rung:
			case <-wait.C:
			}
			wait.Stop()
		}
	}()
}

// notifyPending offers every pending wake to the command, oldest first,
// task wakes, then domain wakes, then kept-work wakes, until a read of each
// finds none or ctx
// ends, and reports true. It reports false, having logged why once, when
// the store refused a read or a claim, which ends the pass at that wake.
func (s *Service) notifyPending(ctx context.Context, line string, bound time.Duration) bool {
	for ctx.Err() == nil {
		pending, err := s.store.PendingNotifications(ctx, notifyBatch)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Printf("could not read the wakes to notify: %v", err)
			}
			return false
		}
		pendingDomain, err := s.store.PendingDomainNotifications(ctx, notifyBatch)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Printf("could not read the domain wakes to notify: %v", err)
			}
			return false
		}
		pendingKept, err := s.store.PendingKeptWorkNotifications(ctx, notifyBatch)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Printf("could not read the kept work wakes to notify: %v", err)
			}
			return false
		}
		if len(pending)+len(pendingDomain)+len(pendingKept) == 0 {
			return true
		}
		for _, w := range pending {
			if ctx.Err() != nil {
				return true
			}
			if !s.notifyOne(ctx, line, bound, taskNotice(s.store, w)) {
				return false
			}
		}
		for _, w := range pendingDomain {
			if ctx.Err() != nil {
				return true
			}
			if !s.notifyOne(ctx, line, bound, domainNotice(s.store, w)) {
				return false
			}
		}
		for _, w := range pendingKept {
			if ctx.Err() != nil {
				return true
			}
			if !s.notifyOne(ctx, line, bound, keptWorkNotice(s.store, w)) {
				return false
			}
		}
	}
	return true
}

// notice is one wake of any shape as the sender offers it: what names
// it in the log, the row the command is given, and the claim and the
// settlement of its own record, so the once-only terms are written once.
type notice struct {
	label  string
	row    any
	claim  func(context.Context) error
	settle func(context.Context, store.NotificationSettlement) error
}

// taskNotice is a task wake's notice: its row as store.Wake encodes it,
// claimed and settled in the notification table.
func taskNotice(records *store.Store, w store.Wake) notice {
	return notice{
		label: fmt.Sprintf("wake %d of task %s", w.ID, w.TaskID),
		row:   w,
		claim: func(ctx context.Context) error { _, err := records.ClaimNotification(ctx, w.ID); return err },
		settle: func(ctx context.Context, n store.NotificationSettlement) error {
			_, err := records.SettleNotification(ctx, w.ID, n)
			return err
		},
	}
}

// domainNotice is a domain wake's notice: its row as store.DomainWake
// encodes it, which names the delegation and the domain where a task
// wake's names a task, claimed and settled in domain_notification.
func domainNotice(records *store.Store, w store.DomainWake) notice {
	return notice{
		label: fmt.Sprintf("domain wake %d of domain %s", w.ID, w.Domain),
		row:   w,
		claim: func(ctx context.Context) error { _, err := records.ClaimDomainNotification(ctx, w.ID); return err },
		settle: func(ctx context.Context, n store.NotificationSettlement) error {
			_, err := records.SettleDomainNotification(ctx, w.ID, n)
			return err
		},
	}
}

// keptWorkNotice is a kept-work wake's notice: its row as
// store.KeptWorkWake encodes it, which names the entry, its project and
// its branch where a task wake's names a task, claimed and settled in
// kept_work_notification.
func keptWorkNotice(records *store.Store, w store.KeptWorkWake) notice {
	return notice{
		label: fmt.Sprintf("kept work wake %d about kept work entry %d", w.ID, w.KeptWork),
		row:   redactedKeptWorkWake(w),
		claim: func(ctx context.Context) error { _, err := records.ClaimKeptWorkNotification(ctx, w.ID); return err },
		settle: func(ctx context.Context, n store.NotificationSettlement) error {
			_, err := records.SettleKeptWorkNotification(ctx, w.ID, n)
			return err
		},
	}
}

// redactedKeptWorkWake is a kept-work wake's row with the two fields that
// are a person's text, the entry's project and branch, passed through
// internal/redact, field by field before the row is encoded, so the JSON
// stays whole. Every other field is a closed word, a number, a time or the
// loop's redacted marker.
func redactedKeptWorkWake(w store.KeptWorkWake) store.KeptWorkWake {
	r := redact.New()
	if project, ok := w.Project.Get(); ok {
		w.Project = store.Known(r.Redact(project))
	}
	if branch, ok := w.Branch.Get(); ok {
		w.Branch = store.Known(r.Redact(branch))
	}
	return w
}

// notifyOne claims one wake, runs the command for it, and settles the claim.
// A wake already claimed or acknowledged since the read is passed over. It
// reports false, having logged why, only when the store refused the claim
// for another reason, so nothing was run.
func (s *Service) notifyOne(ctx context.Context, line string, bound time.Duration, n notice) bool {
	if err := n.claim(ctx); err != nil {
		if errors.Is(err, store.ErrNotificationClaimed) || errors.Is(err, store.ErrNotificationNotOffered) || ctx.Err() != nil {
			return true
		}
		s.log.Printf("could not claim the notification of %s, and the pass ends there: %v", n.label, err)
		return false
	}
	settlement := s.runNotify(ctx, line, bound, n.row)
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifySettleBound)
	defer cancel()
	if err := n.settle(settleCtx, settlement); err != nil {
		s.log.Printf("could not record how the notification of %s ended: %v", n.label, err)
		return true
	}
	if settlement.Outcome == store.NotificationFailed {
		s.log.Printf("the notify command failed for %s: %s", n.label, redact.New().Redact(settlement.Detail))
	}
	return true
}

// runNotify runs the command once with the wake's row on standard input and
// says how it ended.
func (s *Service) runNotify(ctx context.Context, line string, bound time.Duration, w any) store.NotificationSettlement {
	row, err := json.Marshal(w)
	if err != nil {
		return store.NotificationSettlement{Outcome: store.NotificationFailed,
			Detail: fmt.Sprintf("the wake could not be encoded, and the command was not run: %v", err)}
	}
	runCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	res := stages.RunOperatorCommand(runCtx, stages.OperatorCommand{
		Line: line, Dir: s.home.Root(), Stdin: string(row) + "\n", Tail: notifyTail, Grace: notifyGrace,
	})
	switch {
	case res.Exited && res.Code == 0:
		return store.NotificationSettlement{Outcome: store.NotificationSent, ExitCode: store.Known(0), Detail: res.Tail}
	case res.Exited:
		return store.NotificationSettlement{Outcome: store.NotificationFailed, ExitCode: store.Known(res.Code),
			Detail: fmt.Sprintf("the command exited with status %d: %s", res.Code, res.Tail)}
	case ctx.Err() != nil:
		return store.NotificationSettlement{Outcome: store.NotificationFailed,
			Detail: "the service closed while the command ran, and it was ended"}
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return store.NotificationSettlement{Outcome: store.NotificationFailed,
			Detail: fmt.Sprintf("the command did not exit within %s, and it was ended", bound)}
	default:
		return store.NotificationSettlement{Outcome: store.NotificationFailed,
			Detail: fmt.Sprintf("the command did not exit on its own: %v", res.Err)}
	}
}
