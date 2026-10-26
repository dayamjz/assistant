package taskstate

import (
	"slices"
	"time"

	"github.com/dayamjz/assistant/internal/store"
)

// State is where a task stands, in PRD section 6's lifecycle vocabulary. It is
// a closed set: a word outside States is not a State this package produces.
type State string

// The states a resolution can name. Landed and Complete are the two PRD
// section 6 resolves from the terminal records the program writes once each,
// in the first tier of its precedence: Landed from the merge record of a run
// attributed to the task, Complete from the task's cleanup record on top of
// that. The merge record has a producer in this build, internal/runs'
// PassOnMerge; the cleanup record has none yet (#108), and the rule that
// reads it is here so its writer inherits a consumer. Complete is resolved
// for a delivery task only: for an investigation task it is not reachable in
// this build, and the package comment says why. Reported is resolved by the
// report tier, from the stored report of an investigation task, and by the
// log only where no tier above it answered. Held is resolved by three
// tiers: the hold tier, from an open hold naming the task; the run tier,
// from a held run; and the log, from a decision the worker opened. Unknown
// is the resolver declining to say.
const (
	Queued     State = "queued"
	Dispatched State = "dispatched"
	Running    State = "running"
	Held       State = "held"
	Paused     State = "paused"
	Validating State = "validating"
	Ready      State = "ready"
	Landed     State = "landed"
	Reported   State = "reported"
	Complete   State = "complete"
	Failed     State = "failed"
	Unknown    State = "unknown"
)

var states = []State{
	Queued, Dispatched, Running, Held, Paused, Validating, Ready, Landed, Reported, Complete, Failed, Unknown,
}

// States returns the closed set in the order above. The result is a copy.
func States() []State { return slices.Clone(states) }

// Recognized reports whether s is one of States.
func (s State) Recognized() bool { return slices.Contains(states, s) }

// Source says which input decided the state. The values are PRD section 6's
// precedence tiers, in precedence order.
type Source string

// The sources. SourceNone accompanies Unknown when no tier had anything to say.
const (
	// SourceRecord is the terminal tier: a merge record of an attributed
	// run, or that and the task's cleanup record, the local landing record,
	// or an investigation's findings relayed record. It outranks every run,
	// because PRD section 6 makes a terminal record final for the task.
	SourceRecord Source = "record"
	// SourceHold is the hold tier: an open hold naming the task, which
	// resolves Held. It sits below the terminal tier and above the run,
	// because PRD section 6 has a task waiting on a person never resolve
	// Failed or Unknown, whatever its run says, while a terminal record
	// still outranks it.
	SourceHold Source = "hold"
	SourceRun  Source = "run"
	// SourceReport is the report tier: the stored report of an
	// investigation task, internal/store's task_report row, which resolves
	// Reported. It sits below the run tier and above the process tier,
	// because PRD section 6 has a filed report outrank the terminal's
	// liveness: a worker still running after it filed, and one that ended,
	// have both delivered, and neither is a crash.
	SourceReport  Source = "report"
	SourceProcess Source = "process"
	SourceEvent   Source = "event"
	SourceNone    Source = "none"
)

var sources = []Source{SourceRecord, SourceHold, SourceRun, SourceReport, SourceProcess, SourceEvent, SourceNone}

// Sources returns the closed set of sources in precedence order. The result
// is a copy. A table keyed on a Source derives its domain from here, so a
// tier added later fails that table until it is decided.
func Sources() []Source { return slices.Clone(sources) }

// Liveness is what a task's terminal looked like when it was asked about.
// Unobserved is not Dead: it means nobody asked, or nothing could answer, and
// the process tier then declines rather than reading absence as a stopped
// terminal.
type Liveness string

// The liveness answers. Any value other than Live or Dead is read as
// Unobserved.
const (
	Unobserved Liveness = "unobserved"
	// Live means the agent process is running in the session: a probe that
	// finds only a shell in the pane, with the agent gone, answers Dead. A
	// session whose shell survives its agent is the ordinary shape of a
	// worker that finished or crashed, and reading it as Live would be the
	// stale evidence the process tier exists to refuse.
	Live Liveness = "live"
	Dead Liveness = "dead"
)

var livenesses = []Liveness{Unobserved, Live, Dead}

// Livenesses returns the three answers, Unobserved first. The result is a
// copy, and a table keyed on a Liveness derives its domain from here.
func Livenesses() []Liveness { return slices.Clone(livenesses) }

// Inputs is everything Resolve reads. It is a value: Resolve takes no context,
// opens nothing and reads no clock.
type Inputs struct {
	// Task is the task record.
	Task store.Task
	// Runs are the runs attributed to the task, in any order and any status.
	Runs []store.Run
	// Holds are the holds naming the task or one of Runs. Only an open one is
	// read: one whose TaskID names Task decides the hold tier, and one naming
	// the newest run supplies the detail of a held run's answer. A hold
	// naming another task is not this task's and is not read.
	Holds []store.Hold
	// Events are the task's whole history, in any order; Resolve orders them
	// by sequence itself.
	Events []store.TaskEvent
	// MergeRecords are the merge records of Runs, in any order. A record
	// naming a run outside Runs is not the task's and is not read, so a
	// caller that hands the resolver another task's record changes nothing.
	MergeRecords []store.MergeRecord
	// Cleanup is the task's cleanup record, and unknown while none has been
	// written.
	Cleanup store.Optional[store.TaskCleanup]
	// LocalLanding is the task's local landing record, and unknown while no
	// approved fast-forward has delivered it. A record naming another task
	// is not this task's and is not read.
	LocalLanding store.Optional[store.LocalLanding]
	// ReportStoredAt is when the task's report row was written, and unknown
	// while the task has no stored report: store.TaskSnapshot's field of the
	// same name, read from the row and never from the history entry
	// appended beside it. It decides the report tier for an investigation
	// task only; a promoted task keeps its row and is no longer read by it.
	ReportStoredAt store.Optional[time.Time]
	// Relay is the task's findings relayed record, store.TaskSnapshot's
	// field of the same name, and unknown while none has been written. It
	// decides the terminal tier's Complete for an investigation task only,
	// and is read from the row and never from the history entry appended
	// beside it (P8). A record naming another task is not this task's and
	// is not read.
	Relay store.Optional[store.TaskRelay]
	// Process is what the terminal looked like, as of when it was asked.
	Process Liveness
	// Advancing is whether the service is executing the newest of Runs now:
	// its own fact about the slot a run advances in, which no record holds.
	// It is recorded in Basis as asked and used only for Progress, and there
	// only when the run tier answered Validating; it moves no state, so a
	// caller that cannot answer passes false and loses nothing but Progress.
	Advancing bool
}

// Resolution is the answer: one state, the source that decided it, and what was
// read to decide.
type Resolution struct {
	// TaskID names the task.
	TaskID string `json:"task_id"`
	// State is what is true now.
	State State `json:"state"`
	// Source says which tier decided it.
	Source Source `json:"source"`
	// Detail is what the source has to add, and it is unknown when it has
	// nothing. It never carries the text of an event: a worker's words are
	// history, not part of an answer.
	Detail store.Optional[string] `json:"detail"`
	// Progress is whether the answer rests on positive evidence that work is
	// proceeding, which PRD section 6's supervisor absorbs a wake on and
	// nothing else. It is derived here and nowhere else, and it is true in
	// exactly three cases: Validating from SourceRun with Inputs.Advancing,
	// Running from SourceProcess with a Live terminal, and a housekeeping
	// pass that resolves Held from SourceHold while its newest run has not
	// settled and Inputs.Advancing says the service is advancing it. The
	// third is PRD section 6's housekeeping: the hold tier sits above the
	// run tier, so a promoted pass with an open report decision resolves
	// Held while its run validates, and the run's stage is still positive
	// evidence for that pass's terminal. Every other answer is
	// no progress, including a run the service says it is advancing while
	// the record says it has settled: the record decided the state, and a
	// slot that disagrees with it is not evidence. A terminal record is
	// never progress: nothing is proceeding on a task that has landed.
	Progress bool `json:"progress"`
	// Superseded lists the events that claim something a higher source
	// overrode: a decision still open in the log, and the newest event when it
	// maps to a state of its own that differs from State. Empty when the event
	// log decided or nothing outranked it.
	Superseded []Superseded `json:"superseded"`
	// Basis is what was read. Equal Basis from equal records gives an equal
	// Resolution, so it is the cheap way to ask whether an answer could have
	// changed; it is not a revision and carries no order.
	Basis Basis `json:"basis"`
}

// Superseded is one event whose claim a higher source overrode.
type Superseded struct {
	// Sequence and Kind identify the event. Its text is not carried.
	Sequence int64  `json:"sequence"`
	Kind     string `json:"kind"`
	// Claimed is the state the event's kind maps to.
	Claimed State `json:"claimed"`
	// By is the tier whose answer overrode it.
	By Source `json:"by"`
}

// Basis is the facts a Resolution was computed from.
type Basis struct {
	// RunID and RunStatus name the attributed run that was read, and are empty
	// when none was.
	RunID        string          `json:"run_id"`
	RunStatus    store.RunStatus `json:"run_status"`
	RunUpdatedAt time.Time       `json:"run_updated_at"`
	// LandedRun names the attributed run whose merge record established
	// Landed, the newest attributed run with a record reading merged, and
	// is empty when no attributed run has one.
	LandedRun string `json:"landed_run"`
	// LandedLocally is the commit the task's local landing record says the
	// approved fast-forward moved the local default branch to, and empty
	// when the task has no such record. It is recorded whether or not it
	// decided, as LandedRun is; when both are set the merge record decided.
	LandedLocally string `json:"landed_locally"`
	// CleanupRecorded is whether the task has a cleanup record, and
	// CleanupRemoved whether that record carries a removal. Both are false
	// for a task cleanup has not reached.
	CleanupRecorded bool `json:"cleanup_recorded"`
	CleanupRemoved  bool `json:"cleanup_removed"`
	// ReportStored is whether the task has a stored report row. It is
	// recorded whether or not it decided, and for a delivery task too, which
	// a promoted investigation is: it decided exactly when Source is
	// SourceReport.
	ReportStored bool `json:"report_stored"`
	// Relayed is whether the task has a findings relayed record naming it.
	// It is recorded whether or not it decided, and for a delivery task
	// too: it decided exactly when Source is SourceRecord and State is
	// Complete for an investigation task.
	Relayed bool `json:"relayed"`
	// Hold is the key of the oldest open hold naming the task, read on
	// Resolve's terms, and empty when none is open. It is recorded whether
	// or not it decided, as LandedRun is: it decided exactly when Source is
	// SourceHold. It is what makes a hold opening or closing a change of
	// basis, so a reader comparing bases sees a task start or stop waiting
	// on a person.
	Hold string `json:"hold"`
	// EventSequence is the highest event sequence read, zero for an empty log.
	EventSequence int64 `json:"event_sequence"`
	// EventKind is the kind of the newest event read, and empty for an empty
	// log. It is what was read, recorded beside the sequence: when the event
	// tier decided, it is the kind whose row decided, so a reader that must
	// tell one event-tier Failed from another, such as a refusal the
	// isolation assertion recorded from a worker's own report, reads it here
	// rather than reading the log's tail itself (P8). Resolve is its one
	// writer, and fills it from the same Inputs.Events the state was
	// resolved from, inside the same call; nothing reads the newest event
	// on its own to fill it later.
	EventKind string `json:"event_kind"`
	// Process is the liveness that was read.
	Process Liveness `json:"process"`
	// Advancing is Inputs.Advancing as it was asked, whether or not the
	// answer used it: it is recorded so a reader can see what Progress
	// rested on, and it is used only for Validating from the run tier.
	Advancing bool `json:"advancing"`
}
