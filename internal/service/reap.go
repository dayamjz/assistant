package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
)

// reapGrace is how long recovery gives an orphaned group to leave after the
// polite step before the forceful one, on agents.EndGroup's terms. It is the
// adapter's own default rather than a bound of this package's, because the
// escalation is the adapter's and a recovering service applies it to a tree
// an earlier service of this binary started.
const reapGrace = agents.DefaultTerminationGrace

// processJournal is the durable half of the containment registry: the run
// process record internal/store keeps for every group containRunner
// registers, written where the group is registered and cleared once the
// adapter reports the invocation's tree terminated. The registry is what this
// service knows; the journal is what the next service of this home can know,
// and recovery reads it to reap what this service did not live to end.
//
// Neither write refuses the invocation. The adapter tells Started after the
// process exists and takes no answer, so a record that cannot be written is
// logged and the process runs unrecorded: it is contained by the registry
// for as long as this service lives, and a service that dies before it ends
// has no record for the next one to reap it by. The record understates
// rather than holding a launch over its own bookkeeping, which is the same
// stance invocation.go takes for the invocation history, and the gap is
// stated in the package documentation.
//
// A nil journal records nothing and clears nothing, which is a registry built
// without a store.
type processJournal struct {
	store *store.Store
	log   *home.Log
}

// record writes the record of a group just registered and reports whether
// it did, so the caller clears only a record it wrote: a second launch given
// the same group within one run is refused by the store, and clearing on its
// behalf would remove the first launch's record. An invocation the segment
// context names no run for is not recorded: the record references the run's
// row, and recovery decides from the run's status, so a record with no run is
// one recovery could never act on.
func (j *processJournal) record(run, stage string, pgid int) bool {
	if j == nil {
		return false
	}
	if run == "" {
		j.log.Printf("process group %d of a %s invocation outside any run is contained and not recorded, so a restart cannot reap it", pgid, stage)
		return false
	}
	r := store.ProcessRecord{RunID: run, Stage: stage, Group: pgid}
	// Identify is read after the adapter reported the start, so a process
	// that exited in between is recorded with no identity rather than with
	// a reading of whatever has its number now; recovery then reports it
	// unverified rather than touching anything.
	switch identity, found, err := agents.Identify(pgid); {
	case err != nil:
		j.log.Printf("the identity of process group %d of run %s could not be read, so its record carries none: %v", pgid, run, err)
	case !found:
		j.log.Printf("process group %d of run %s was not running when its identity was looked for, so its record carries none", pgid, run)
	default:
		r.Identity = store.Known(identity.Start)
	}
	if _, err := j.store.RecordProcess(context.Background(), r); err != nil {
		j.log.Printf("process group %d of run %s could not be recorded, so a restart cannot reap it: %v", pgid, run, err)
		return false
	}
	return true
}

// clear removes the record of a group whose tree the adapter reports
// terminated. A clear that fails is logged and leaves the record for the
// next recovery, which finds the identity's process gone and clears it then.
func (j *processJournal) clear(run string, pgid int) {
	if j == nil {
		return
	}
	if err := j.store.ClearProcess(context.Background(), run, pgid); err != nil {
		j.log.Printf("the record of process group %d of run %s could not be cleared after its tree ended, so a restart will look for it: %v", pgid, run, err)
	}
}

// reapOutcome is what recovery did about one process record. It is a closed
// set, and the Err beside it in a reaped says why wherever the outcome left a
// process alone for a reason that is not the run's status.
type reapOutcome string

const (
	// reapActive is a record whose run is unfinished, or whose group this
	// service registered itself. Nothing was looked at and nothing was
	// touched, decided from the run's reconciled status and this service's
	// own registry alone, and the record stands. A record of an earlier
	// service whose run is unfinished stands until a recovery finds the run
	// finished; nothing ends that group in between, which doc.go states.
	reapActive reapOutcome = "active"
	// reapEnded is a record whose identity was verified to name a running
	// process, whose group was then ended as that group through
	// agents.EndGroup, and whose record was cleared.
	reapEnded reapOutcome = "ended"
	// reapGone is a record whose leader is gone and whose group holds
	// nothing still running that the platform can see: on unix a group
	// signal zero no longer reaches or, on Linux alone, whose listed
	// members have all exited, on Windows the leader alone, which agents.GroupLeaderGone
	// states. Nothing was signalled, and the record was cleared.
	reapGone reapOutcome = "gone"
	// reapReplaced is a record whose identifier now names a process with
	// another start time, which is a reused identifier. Nothing was
	// signalled, and the record was cleared, because the process it named
	// is gone.
	reapReplaced reapOutcome = "replaced"
	// reapOutlivedLeader is a record whose leader is gone and whose group
	// still holds a running process, on a platform that can see one.
	// Nothing was signalled, because the recorded identity names the leader
	// and nothing recorded names the survivor, and the record stands so the
	// next recovery looks again.
	reapOutlivedLeader reapOutcome = "outlived-leader"
	// reapSurvived is a record whose leader was verified and whose group was
	// signalled, politely and then forcefully, and still held a process
	// afterwards: one this service may not signal, or one the platform
	// cannot tell from a running one. The record stands; Err is
	// agents.ErrGroupSurvived.
	reapSurvived reapOutcome = "survived"
	// reapUnverified is a record recovery could not verify: the run could
	// not be read or carries a status this build does not recognize, no
	// identity was recorded, the platform has no identity read, or the read
	// failed. Nothing was signalled and the record stands, which is P6's
	// refuse-and-report for a safety fact that could not be established;
	// Err says which. The residual gap is that such a process, if it is
	// there, may still be writing in the run's isolated copy, and nothing
	// here stops it.
	reapUnverified reapOutcome = "unverified"
)

// String renders the outcome.
func (o reapOutcome) String() string { return string(o) }

// leftAlone reports whether the outcome is a record recovery left standing
// over a process it did not end, which is what the summary line counts.
func (o reapOutcome) leftAlone() bool {
	switch o {
	case reapOutlivedLeader, reapSurvived, reapUnverified:
		return true
	default:
		return false
	}
}

// reaped is recovery's account of one process record.
type reaped struct {
	// Record is the record as it stood when recovery read it.
	Record store.ProcessRecord
	// Outcome is what was done.
	Outcome reapOutcome
	// Err is why the outcome left a process alone where that is not the
	// run's status, nil otherwise. A clear that failed after a decision is
	// reported here too, with the outcome the decision reached, because the
	// group's fate and the record's are two facts.
	Err error
}

// String renders the account for a log line.
func (r reaped) String() string {
	s := fmt.Sprintf("process group %d of run %s (%s, started %s): %s",
		r.Record.Group, r.Record.RunID, r.Record.Stage, r.Record.StartedAt.Format(time.RFC3339), r.Outcome)
	if r.Err != nil {
		s += ": " + r.Err.Error()
	}
	return s
}

// reapOrphans is PRD section 11's "reap orphaned agent processes" over the
// run process records, run inside recover after the runs have been
// reconciled and before anything is served. It walks every record this home
// holds and decides each from the run's reconciled status first and the
// process second:
//
//   - A record whose group this service registered itself, or whose run is
//     unfinished, is reapActive: nothing is looked at, because "never touch
//     one whose run is still active" is decided from the record and not
//     from the process.
//   - A record whose run cannot be read, or carries a status this build
//     does not recognize, is reapUnverified. internal/runs reads such a
//     status as finished so that nothing takes the run further; here that
//     same reading would end a process, which is the other direction, so it
//     is refused instead.
//   - Every other record is verified through agents.EndGroup, which signals
//     only a group whose leader still carries the identity recorded at
//     launch, and by group identifier alone, never by pattern. What it
//     answers maps onto the outcomes above one to one.
//
// Every account is logged, a summary line follows, and the list is returned
// so a caller reads what was decided rather than inferring it from the log.
// Nothing here refuses to serve: a process left alone is reported and the
// service comes up, which doc.go states with its gap.
func (s *Service) reapOrphans(ctx context.Context) []reaped {
	records, err := s.store.ProcessRecords(ctx)
	if err != nil {
		s.log.Printf("recovery could not list the run process records, so no orphaned agent process is reaped and none is reported: %v", err)
		return nil
	}
	if len(records) == 0 {
		return nil
	}
	out := make([]reaped, 0, len(records))
	counts := map[reapOutcome]int{}
	alone := 0
	for _, record := range records {
		account := s.reap(ctx, record)
		s.log.Printf("recovery: %s", account)
		out = append(out, account)
		counts[account.Outcome]++
		if account.Outcome.leftAlone() {
			alone++
		}
	}
	var parts []string
	for _, o := range []reapOutcome{reapEnded, reapGone, reapReplaced, reapActive, reapOutlivedLeader, reapSurvived, reapUnverified} {
		if counts[o] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[o], o))
		}
	}
	s.log.Printf("recovery: %d run process record(s): %s; %d process(es) left alone and reported above",
		len(records), strings.Join(parts, ", "), alone)
	return out
}

// reap decides one record on reapOrphans's terms.
func (s *Service) reap(ctx context.Context, record store.ProcessRecord) reaped {
	if held, ok := s.registry.lookup(record.Group); ok && held.run == record.RunID {
		return reaped{Record: record, Outcome: reapActive}
	}
	run, err := s.store.Run(ctx, record.RunID)
	if err != nil {
		return reaped{Record: record, Outcome: reapUnverified, Err: fmt.Errorf("reading the run: %w", err)}
	}
	if !run.Status.Recognized() {
		return reaped{Record: record, Outcome: reapUnverified,
			Err: fmt.Errorf("the run's status %q is not one this build recognizes, so whether it is still active is not known", run.Status)}
	}
	if !runs.Finished(run.Status) {
		return reaped{Record: record, Outcome: reapActive}
	}
	start, known := record.Identity.Get()
	if !known {
		return reaped{Record: record, Outcome: reapUnverified,
			Err: fmt.Errorf("%w: the record carries no identity to verify the process by", agents.ErrIdentityUnreadable)}
	}
	end, err := agents.EndGroup(agents.Identity{PID: record.Group, Start: start}, reapGrace)
	switch {
	case errors.Is(err, agents.ErrGroupSurvived):
		return reaped{Record: record, Outcome: reapSurvived, Err: err}
	case err != nil:
		return reaped{Record: record, Outcome: reapUnverified, Err: err}
	}
	account := reaped{Record: record}
	switch end {
	case agents.GroupEnded:
		account.Outcome = reapEnded
	case agents.GroupGone, agents.GroupLeaderGone:
		account.Outcome = reapGone
	case agents.GroupReplaced:
		account.Outcome = reapReplaced
	case agents.GroupOutlivedLeader:
		account.Outcome = reapOutlivedLeader
		return account
	default:
		account.Outcome = reapUnverified
		account.Err = fmt.Errorf("agents.EndGroup answered %q, which this service does not read", end)
		return account
	}
	if err := s.store.ClearProcess(ctx, record.RunID, record.Group); err != nil {
		account.Err = fmt.Errorf("clearing the record: %w", err)
	}
	return account
}
