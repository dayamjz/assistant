package taskstate

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/dayamjz/assistant/internal/store"
)

// runStates is how a run's status reads in the task lifecycle. It is coarser
// than the run's own vocabulary on purpose: a failed and a terminated run are
// both a task that stopped, and nothing here tells a parked run from a running
// one. A status with no entry resolves to Unknown rather than to a guess.
var runStates = map[store.RunStatus]State{
	store.RunPending:    Validating,
	store.RunRunning:    Validating,
	store.RunHeld:       Held,
	store.RunPassed:     Ready,
	store.RunFailed:     Failed,
	store.RunTerminated: Failed,
}

// Resolve answers what is true of a task now, from the records that own the
// facts, in PRD section 6's fixed precedence and first match wins:
//
//  1. A terminal record: SourceRecord. A task is Landed when the merge
//     record of a run in Runs reads merged, or when a run in Runs has a
//     nothing-to-deliver record, which a run writes when its rebase leaves
//     nothing to deliver, PRD section 6's "nothing left to deliver", on the
//     merge record's terms; where more than one run has either, the newest
//     such run is the one named in Basis.LandedRun, and
//     Basis.NothingToDeliver says which record it was. A task is Landed
//     too when its own local landing record exists, which the approved
//     fast-forward of a task delivered under the local posture writes, PRD
//     section 6; Basis.LandedLocally names the commit it records, and where a
//     task has both records the merge record decides the detail. A delivery task
//     is Complete when it is Landed and its cleanup record carries a
//     removal, and Complete outranks Landed. Nothing else reaches this tier:
//     a cleanup record on a task that has not landed is a disk reclamation
//     and leaves the state to the tiers below, a record naming a run outside
//     Runs is not the task's, and a merge record reading closed is read by
//     the run tier through the run's own status. An investigation task is
//     Complete here when its findings relayed record exists, which the
//     coordinator's record of PRD section 6's "findings relayed" writes;
//     Basis.Relayed says so. It is read from the record and never from the
//     history entry written beside it (P8), and it is not inferred from the
//     stored report or from the cleanup record, so a reported investigation
//     whose copy was removed and whose findings nobody recorded relayed
//     stays Reported from the report tier. A delivery task, which a
//     promoted investigation is, is not made Complete by such a record.
//     A terminal record is final: a
//     newer run in Runs, attributed by branch or named by the task, does not
//     displace it, which is PRD section 6's "work that continues after
//     Landed is a new task".
//  2. An open hold naming the task: SourceHold, which resolves Held. A hold
//     names the task when its TaskID is Task's identifier, whether or not it
//     also names a run; the oldest open one, by opening time with the key
//     breaking a tie, is the one named in Basis.Hold and its subject is the
//     detail. It outranks every run, whatever the run's status, Validating
//     and Ready included, because PRD section 6 puts the tier above the
//     whole run tier: a task waiting on a person is waiting on them while a
//     run of its branch validates. A resolved hold is not open and reaches
//     nothing here, so an answered hold gives the task back to the tiers
//     below, and a hold naming only a run is the run's, read by the run
//     tier for its detail and never here.
//  3. The newest attributed run, whatever its status: SourceRun. A run that
//     is pending or running reads as Validating, held as Held, passed as
//     Ready, failed or terminated as Failed. A status outside the run
//     vocabulary reads as Unknown from SourceRun and does not fall through,
//     because a run exists and the log is exactly what it outranks.
//  4. A stored report: SourceReport, which resolves Reported, for an
//     investigation task whose ReportStoredAt is known, whatever the
//     terminal says. The report is a record the program wrote once, in the
//     transaction that registered its holds, so a worker still running
//     after it filed and one whose terminal has ended have both delivered;
//     PRD section 6 has the report outrank the terminal for that reason.
//     It is read from the report row and never from the history entry
//     written beside it (P8), so an entry the worker appended after its
//     report does not displace it either, and is listed in Superseded when
//     it claims a state of its own. A delivery task, which a promoted
//     investigation is, is not read by this tier: its row is evidence of
//     what it was asked before, not its deliverable. Reported is not
//     Complete: the tier says the report is in and nothing about cleanup.
//  5. The terminal: Live reads as Running, positive evidence, except where
//     the log's most recent event is a kind whose row is a DeclaredWait:
//     then the worker whose terminal runs has said it is waiting, and the
//     answer is Paused from SourceEvent, which is PRD section 5's "a
//     declared wait is not a stall" taking precedence over the live signal
//     by the owner's decision on #303. Dead with no run and no stored
//     report reads as Unknown from SourceProcess and does not fall through
//     to the log, declared wait or not, because a stale event is what a
//     dead terminal must not be trusted for. Unobserved skips the tier.
//  6. The log: only when its most recent event's kind has a row in this
//     package's table that claims a state, or says a decision is open (Held).
//     A kind that closes a decision, a note, an unrecognized kind, or an empty
//     log reads as Unknown from SourceNone.
//
// Resolve is a pure function of in: the same Inputs give the same Resolution,
// whatever order Runs, Holds and Events arrive in. It reads no clock, so no
// timestamp decides anything except which run is newest.
//
// Superseded is computed from the whole log, never from its tail alone, and
// only when a higher source decided. See Resolution.Superseded. Progress is
// derived last, from the answer and the two inputs its doc names.
func Resolve(in Inputs) Resolution {
	events := slices.Clone(in.Events)
	slices.SortFunc(events, func(a, b store.TaskEvent) int { return cmp.Compare(a.Sequence, b.Sequence) })

	out := Resolution{TaskID: in.Task.ID, Basis: Basis{Process: Unobserved, Advancing: in.Advancing}}
	if in.Process == Live || in.Process == Dead {
		out.Basis.Process = in.Process
	}
	if n := len(events); n > 0 {
		out.Basis.EventSequence, out.Basis.EventKind = events[n-1].Sequence, events[n-1].Kind
	}

	run, ok := newest(in.Runs)
	if ok {
		out.Basis.RunID, out.Basis.RunStatus, out.Basis.RunUpdatedAt = run.ID, run.Status, run.UpdatedAt
	}
	landed, isLanded := landedRun(in.Runs, in.MergeRecords, in.NothingToDeliver)
	nothing, isNothing := nothingOf(in.NothingToDeliver, landed.ID)
	isNothing = isLanded && isNothing
	if isLanded {
		out.Basis.LandedRun, out.Basis.NothingToDeliver = landed.ID, isNothing
	}
	local, isLocal := in.LocalLanding.Get()
	isLocal = isLocal && local.Task == in.Task.ID
	if isLocal {
		out.Basis.LandedLocally = local.Commit
	}
	if cleanup, known := in.Cleanup.Get(); known {
		out.Basis.CleanupRecorded, out.Basis.CleanupRemoved = true, cleanup.RemovedAt.IsKnown()
	}
	hold, isHeld := oldestOpenNaming(in.Holds, func(h store.Hold) bool {
		id, ok := h.TaskID.Get()
		return ok && id == in.Task.ID
	})
	if isHeld {
		out.Basis.Hold = hold.Key
	}
	relay, isRelayed := in.Relay.Get()
	isRelayed = isRelayed && relay.TaskID == in.Task.ID
	out.Basis.Relayed = isRelayed
	isRelayed = isRelayed && in.Task.Shape == store.TaskInvestigation
	reportedAt, isReported := in.ReportStoredAt.Get()
	out.Basis.ReportStored = isReported
	isReported = isReported && in.Task.Shape == store.TaskInvestigation

	switch {
	case isNothing:
		out.State, out.Source, out.Detail = fromNothingRecord(in.Task, nothing, in.Cleanup)
	case isLanded:
		out.State, out.Source, out.Detail = fromRecords(in.Task, landed, in.MergeRecords, in.Cleanup)
	case isLocal:
		out.State, out.Source, out.Detail = fromLocalRecord(in.Task, local, in.Cleanup)
	case isRelayed:
		out.State, out.Source = Complete, SourceRecord
		out.Detail = store.Known("the coordinator recorded the investigation's findings relayed at " +
			relay.RecordedAt.UTC().Format(time.RFC3339))
	case isHeld:
		out.State, out.Source, out.Detail = Held, SourceHold, store.Known(hold.Subject)
	case ok:
		out.State, out.Source, out.Detail = fromRun(run, in.Holds)
	case isReported:
		out.State, out.Source = Reported, SourceReport
		out.Detail = store.Known("the investigation's report was stored at " + reportedAt.UTC().Format(time.RFC3339))
	case in.Process == Live && declaredWait(events):
		out.State, out.Source = Paused, SourceEvent
		out.Detail = store.Known("the worker declared an external wait and its terminal is live")
	case in.Process == Live:
		out.State, out.Source = Running, SourceProcess
		out.Detail = store.Known("the task's terminal is live")
	case in.Process == Dead:
		out.State, out.Source = Unknown, SourceProcess
		out.Detail = store.Known("the task's terminal has stopped and no run is attributed to the task")
	default:
		out.State, out.Source, out.Detail = fromEvents(events)
	}

	if out.Source == SourceRecord || out.Source == SourceHold || out.Source == SourceRun || out.Source == SourceReport ||
		out.Source == SourceProcess {
		out.Superseded = supersededBy(events, out.State, out.Source)
	}
	out.Progress = (out.State == Validating && out.Source == SourceRun && in.Advancing) ||
		(out.State == Running && out.Source == SourceProcess && in.Process == Live)
	return out
}

// newest picks the newest run by creation time, with the identifier breaking a
// tie, so the choice does not depend on the order the runs were handed in.
func newest(runs []store.Run) (store.Run, bool) {
	var best store.Run
	for i, r := range runs {
		if i == 0 || r.CreatedAt.After(best.CreatedAt) || (r.CreatedAt.Equal(best.CreatedAt) && r.ID > best.ID) {
			best = r
		}
	}
	return best, len(runs) > 0
}

// landedRun picks, on newest's terms, the newest of runs whose merge record
// in records reads merged or that has a nothing-to-deliver record in
// nothing. A record naming a run outside runs is not consulted, and a merge
// record reading closed lands nothing.
func landedRun(runs []store.Run, records []store.MergeRecord, nothing []store.NothingToDeliver) (store.Run, bool) {
	var landed []store.Run
	for _, r := range runs {
		rec, merged := recordOf(records, r.ID)
		_, delivered := nothingOf(nothing, r.ID)
		if (merged && rec.End == store.PullRequestMerged) || delivered {
			landed = append(landed, r)
		}
	}
	return newest(landed)
}

// nothingOf finds the nothing-to-deliver record naming the run, if any.
func nothingOf(records []store.NothingToDeliver, runID string) (store.NothingToDeliver, bool) {
	for _, rec := range records {
		if runID != "" && rec.Run == runID {
			return rec, true
		}
	}
	return store.NothingToDeliver{}, false
}

// fromNothingRecord is the terminal tier for a task its run's
// nothing-to-deliver record landed, on fromRecords' terms: Complete when the
// task is a delivery task whose cleanup record carries a removal, otherwise
// Landed. The detail names the record and says the change was already on the
// default branch and nothing was delivered, never that anything merged or
// was validated.
func fromNothingRecord(task store.Task, rec store.NothingToDeliver,
	cleanup store.Optional[store.TaskCleanup]) (State, Source, store.Optional[string]) {
	said := fmt.Sprintf("run %s found its change already on the default branch at %s, so nothing was delivered",
		rec.Run, rec.DefaultCommit)
	if c, ok := cleanup.Get(); ok && task.Shape == store.TaskDelivery {
		if removedAt, removed := c.RemovedAt.Get(); removed {
			return Complete, SourceRecord, store.Known(said + ", and the isolated copy was removed at " +
				removedAt.UTC().Format(time.RFC3339))
		}
	}
	return Landed, SourceRecord, store.Known(said)
}

// recordOf finds the merge record naming the run, if any.
func recordOf(records []store.MergeRecord, runID string) (store.MergeRecord, bool) {
	for _, rec := range records {
		if rec.Run == runID {
			return rec, true
		}
	}
	return store.MergeRecord{}, false
}

// fromRecords is the terminal tier, entered only for a task that has landed:
// Complete when the task is a delivery task whose cleanup record carries a
// removal, otherwise Landed. The detail names the records and never a
// worker's text.
func fromRecords(task store.Task, landed store.Run, records []store.MergeRecord,
	cleanup store.Optional[store.TaskCleanup]) (State, Source, store.Optional[string]) {
	rec, _ := recordOf(records, landed.ID)
	if c, ok := cleanup.Get(); ok && task.Shape == store.TaskDelivery {
		if removedAt, removed := c.RemovedAt.Get(); removed {
			return Complete, SourceRecord, store.Known(fmt.Sprintf(
				"pull request %d of run %s was read as merged and the isolated copy was removed at %s",
				rec.PullRequest, landed.ID, removedAt.UTC().Format(time.RFC3339)))
		}
	}
	return Landed, SourceRecord, store.Known(fmt.Sprintf("pull request %d of run %s was read as merged", rec.PullRequest, landed.ID))
}

// fromLocalRecord is the terminal tier for a task its local landing record
// landed, on fromRecords' terms: Complete when the task's cleanup record
// carries a removal, otherwise Landed. The detail names the record and says
// the change was made on the person's machine, never that anything merged.
func fromLocalRecord(task store.Task, local store.LocalLanding,
	cleanup store.Optional[store.TaskCleanup]) (State, Source, store.Optional[string]) {
	said := fmt.Sprintf("%s was fast-forwarded from %s to %s on this machine when the change was approved",
		local.Branch, local.Before, local.After)
	if c, ok := cleanup.Get(); ok && task.Shape == store.TaskDelivery {
		if removedAt, removed := c.RemovedAt.Get(); removed {
			return Complete, SourceRecord, store.Known(said + " and the isolated copy was removed at " +
				removedAt.UTC().Format(time.RFC3339))
		}
	}
	return Landed, SourceRecord, store.Known(said)
}

func fromRun(run store.Run, holds []store.Hold) (State, Source, store.Optional[string]) {
	state, ok := runStates[run.Status]
	if !ok {
		return Unknown, SourceRun, store.Known(fmt.Sprintf("run %s is in status %q, which has no task state", run.ID, run.Status))
	}
	if state == Held {
		if h, ok := oldestOpen(holds, run.ID); ok {
			return state, SourceRun, store.Known(h.Subject)
		}
	}
	return state, SourceRun, store.Known(fmt.Sprintf("run %s is %s", run.ID, run.Status))
}

// oldestOpen picks the oldest open hold that names the run itself: a hold of
// another attributed run, or one naming only the task, is not this run's.
func oldestOpen(holds []store.Hold, runID string) (store.Hold, bool) {
	return oldestOpenNaming(holds, func(h store.Hold) bool {
		id, ok := h.RunID.Get()
		return ok && id == runID
	})
}

// oldestOpenNaming picks, of the open holds names accepts, the oldest by
// opening time with the key breaking a tie, so the choice does not depend on
// the order the holds were handed in.
func oldestOpenNaming(holds []store.Hold, names func(store.Hold) bool) (store.Hold, bool) {
	var best store.Hold
	found := false
	for _, h := range holds {
		if !h.Open() || !names(h) {
			continue
		}
		if !found || h.OpenedAt.Before(best.OpenedAt) || (h.OpenedAt.Equal(best.OpenedAt) && h.Key < best.Key) {
			best, found = h, true
		}
	}
	return best, found
}

// fromEvents is the log tier. events is in sequence order. The detail is a
// fixed sentence naming the kind's role and never the worker's text.
// declaredWait reports whether the most recent event in events, which is in
// sequence order, is a kind whose row is a DeclaredWait. It reads the
// event the log tier would answer from and nothing older, so a worker that
// declared a wait and then said it is working again is not read as waiting.
func declaredWait(events []store.TaskEvent) bool {
	if len(events) == 0 {
		return false
	}
	row, ok := Lookup(events[len(events)-1].Kind)
	return ok && row.DeclaredWait
}

func fromEvents(events []store.TaskEvent) (State, Source, store.Optional[string]) {
	if len(events) == 0 {
		return Unknown, SourceNone, store.Known("the task has no history")
	}
	row, ok := Lookup(events[len(events)-1].Kind)
	switch {
	case !ok:
		return Unknown, SourceNone, store.Known("the most recent event is of a kind with no defined meaning")
	case row.Role == RoleState || row.Role == RoleDecisionOpen:
		return row.State, SourceEvent, store.Known("from the most recent event, with no run or terminal to outrank it")
	default:
		return Unknown, SourceNone, store.Known("the most recent event claims no state")
	}
}

// supersededBy lists the events whose claim differs from state: every decision
// opened since the last decision was closed, and the newest event when it
// claims a state of its own. events is in sequence order. A close clears every
// open before it, because one decision is open at a time.
func supersededBy(events []store.TaskEvent, state State, by Source) []Superseded {
	var out []Superseded
	add := func(e store.TaskEvent, claimed State) {
		if claimed != state {
			out = append(out, Superseded{Sequence: e.Sequence, Kind: e.Kind, Claimed: claimed, By: by})
		}
	}
	var open []store.TaskEvent
	for _, e := range events {
		switch row, ok := Lookup(e.Kind); {
		case !ok:
		case row.Role == RoleDecisionOpen:
			open = append(open, e)
		case row.Role == RoleDecisionClosed:
			open = nil
		}
	}
	for _, e := range open {
		add(e, Held)
	}
	if n := len(events); n > 0 {
		tail := events[n-1]
		if row, ok := Lookup(tail.Kind); ok && row.Role == RoleState {
			add(tail, row.State)
		}
	}
	return out
}
