package machine

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/custody"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Version is what --version answers. It is a shape of its own so that the
// machine interface's one-document-per-invocation contract holds for it too: a
// caller decoding standard output gets a document whatever it asked for.
type Version struct {
	// Version is the build this binary reports, exactly as the process was
	// given it. It says nothing about a service, which may be a different
	// build; Health carries that one.
	Version string `json:"version"`
}

// Help is what --help answers: the command list, or one verb's own flags. It
// travels as a document for the same reason Version does.
//
// The text is what a person reads, carried as written. Nothing here models the
// commands as data: the verb table is internal/cli's, and a second shape for
// it here would be a second owner of the surface.
type Help struct {
	// Usage is the text, with its line breaks as they were written.
	Usage string `json:"usage"`
}

// Health is the answer to a readiness check. PRD section 8 makes launch and
// readiness different states and says only a real answer to this proves the
// second, so a caller that wants to know whether the service is up asks for
// one of these rather than looking for a process.
type Health struct {
	// Ready is true in every Health a service returns. It is a field rather
	// than an implied true so that a caller decoding an answer into a zero
	// value does not read the zero as ready.
	Ready bool `json:"ready"`
	// Home is the home root the service owns.
	Home string `json:"home"`
	// Build is the software answering, which PRD section 8 requires every run
	// to be traceable to.
	Build store.Build `json:"build"`
	// Instance identifies the serving process, so a caller that asked one
	// service to make way for another can tell the successor from the service
	// it replaced. Two answers carrying the same value came from one process;
	// two carrying different values came from two.
	//
	// It says nothing else. It is minted when a service opens and is not
	// derived from the machine, the user, the build, or the operating system's
	// identifier for the process, so it names nothing outside the home it is
	// serving and does not survive that process.
	Instance string `json:"instance"`
}

// Service is what is known about the background service. It is reported by a
// command that ran whether or not the service answered, so the failure to
// reach one is a field rather than an error that replaces the whole report.
type Service struct {
	// Running is whether the service answered a readiness check.
	Running bool `json:"running"`
	// Socket is the endpoint that was tried.
	Socket string `json:"socket"`
	// Detail says why the service did not answer, and is empty when it did.
	Detail string `json:"detail,omitempty"`
	// Build is the software the service is running, known only when it
	// answered.
	Build *store.Build `json:"build,omitempty"`
	// Managed is the home's managed service, present when a definition for
	// it is set up and only on the answers that read one: assistant service
	// status and assistant service start.
	Managed *Managed `json:"managed,omitempty"`
}

// Gate is what is known about a working copy's gate: the local bare repository
// a push is validated through. internal/gate owns whether one exists and what
// it is; this carries that answer.
type Gate struct {
	// Present is whether the working copy is bound to a gate this home knows.
	Present bool `json:"present"`
	// ID is the gate's identifier, empty when there is none.
	ID string `json:"id,omitempty"`
	// Repository is the path of the bare repository, empty when there is none.
	Repository string `json:"repository,omitempty"`
	// Detail says what is wrong when a gate was expected and not found.
	Detail string `json:"detail,omitempty"`
}

// Branch is the state of the working copy the command was run in.
type Branch struct {
	// WorkingPath is the root of the working copy.
	WorkingPath string `json:"working_path"`
	// Name is the branch checked out there, empty on a detached head.
	Name string `json:"name,omitempty"`
	// Head is the commit that branch stands at.
	Head string `json:"head,omitempty"`
	// Detail says why the branch could not be read, and is empty when it was.
	Detail string `json:"detail,omitempty"`
}

// Status is what assistant status reports: repository, gate, service, active
// run, and local branch state, which is PRD section 9's list for that command.
//
// Every part of it is a pointer or carries its own detail, because a status
// that cannot report one part still reports the rest. A status that failed
// wholesale is an error rather than one of these.
type Status struct {
	// Home is the home root this command is acting on.
	Home string `json:"home"`
	// Service is what is known about the background service.
	Service Service `json:"service"`
	// Repository is the repository record, absent when this working copy has
	// none because it was never initialized.
	Repository *store.Repository `json:"repository,omitempty"`
	// Gate is what is known about this working copy's gate.
	Gate *Gate `json:"gate,omitempty"`
	// Branch is the local branch state.
	Branch *Branch `json:"branch,omitempty"`
	// ActiveRun is the run this branch has in flight, absent when it has none.
	ActiveRun *Run `json:"active_run,omitempty"`
	// ParkedRun is the branch's latest run when the branch has no run in
	// flight and that run's record names the bound that parked it, absent
	// otherwise. It is the whole answer for that run, so the stage reports it
	// stood on when it parked, the findings among them, stay readable after
	// its record has ended; which stage the park names is its record's Park.
	// A run parked before the park record existed is not reported here,
	// because nothing but its checkpoint says it parked.
	ParkedRun *Run `json:"parked_run,omitempty"`
	// Custody is PRD section 11's classification of the branch, read
	// without fetching and without mutating anything, absent when the
	// branch was not classified because there is no repository record or
	// no branch.
	Custody *Custody `json:"custody,omitempty"`
	// Supervision is the pull guard's finding, which a status computes from
	// the home's files and lock even when the service is down, because
	// reporting that the service is down is this command's job. It is absent
	// when the guard was not computed.
	Supervision *Supervision `json:"supervision,omitempty"`
	// Detail names what could not be reported and why, and is empty when
	// everything was.
	Detail string `json:"detail,omitempty"`
}

// Decision is a halted run's open question, with the findings that produced it
// carried verbatim.
//
// PRD section 9 requires a finding that needs a decision to be relayed with its
// full text, unsummarized and unjudged, so Findings holds what the stage
// reported and nothing here trims, ranks, or rewrites it.
type Decision struct {
	// Decision is the halt point's own declaration: the node, the question,
	// the options, and the state key the answer is written to.
	graph.Decision
	// Stage is the stage holding, when the halt point is one of the nine
	// stages' holds. It is empty for a halt point that is not a stage's.
	Stage string `json:"stage,omitempty"`
	// Findings is what that stage reported, exactly as it reported it.
	Findings []findings.Finding `json:"findings,omitempty"`
}

// Stage is what became of one stage of one run.
type Stage struct {
	// Stage is the stage's name.
	Stage string `json:"stage"`
	// Outcome is what became of it, which is pipeline's vocabulary.
	Outcome pipeline.Outcome `json:"outcome"`
	// Ran is whether the stage's body ran, which its outcome alone cannot say:
	// a skipped stage and one a person skipped past at its hold share an
	// outcome and differ here.
	Ran bool `json:"ran"`
	// Report is what the stage reported, absent for a stage that never ran.
	Report *findings.Report `json:"report,omitempty"`
	// Fix is the summary the last fix round of this stage wrote, empty when
	// there was none.
	Fix string `json:"fix,omitempty"`
	// Executions is when each execution of this stage's body started and
	// ended, in the order they ended, which is internal/store's record. A
	// stage taking fix rounds has one per execution; a stage that never ran a
	// body has none.
	Executions []store.StageExecution `json:"executions,omitempty"`
	// SkipReason is why a stage that did not run was passed over, when there
	// is a reason to name beyond the outcome alone. The pull request and
	// checks stages carry no-code-host when the run has no code host.
	SkipReason string `json:"skip_reason,omitempty"`
}

// Run is one run as a surface reports it: the record, where its execution
// stands, and what to do about it.
type Run struct {
	// Record is the authoritative run record, which internal/store owns.
	Record store.Run `json:"record"`
	// Outcome is what a driving agent reads to decide what to do next.
	Outcome Outcome `json:"outcome"`
	// nextAction is what to do about this run, which PRD section 9 requires
	// of every outcome, terminal or not. It is read through NextAction, and
	// this is where what may write it is written down.
	//
	// Two things do, and they answer different questions. Decide derives it
	// from a standing, so a surface building an answer cannot hand back an
	// action that disagrees with the outcome and the advancing fact beside it.
	// UnmarshalJSON writes whatever next_action a document carried, because a
	// decoded answer relays what the answering service decided rather than a
	// decision made here; an answer that arrived that way is as consistent as
	// the service that sent it and no more.
	//
	// Re-deriving the action on decode is the obvious alternative and it is
	// deliberately not done. An answer written by an older or a newer build
	// would then come back carrying this build's action rather than the one it
	// was sent with, so two builds genuinely disagreeing about what to do with
	// a run would be masked at the moment that mattered. Relaying the
	// producer's computation keeps such a disagreement legible; re-deriving
	// hides it behind a value that looks locally consistent.
	//
	// What the field being unexported buys is a package boundary rather than a
	// property of this package: no code outside internal/machine can assign
	// it, so a surface like internal/service's report has no assignment site
	// to forget.
	nextAction string
	// Progress is where the run's execution stood at its last checkpoint. It
	// is absent for a run that has not been executed yet, which is a different
	// state from a run that has and is standing still.
	Progress *graph.Status `json:"progress,omitempty"`
	// Advancing is whether a segment of this run was executing when this
	// answer was made.
	//
	// It is a fact about now, and the two records this answer is otherwise
	// built from do not hold it: the run's status says the run is unfinished
	// and its checkpoint says where its execution stopped, and both say the
	// same thing about a run being walked this instant and a run whose segment
	// stopped without settling and that nothing has picked up. Its owner is
	// the service, for the length of one segment and nowhere durable, which is
	// why a service that died mid-segment leaves a run this reports false for
	// until something carries it on.
	//
	// It is the answering service's own answer about itself. A home has one
	// service holding it, on the terms internal/home's lock states and with
	// the gaps that package's documentation names, so false means nothing here
	// is advancing the run rather than that nothing anywhere is.
	//
	// False on a run that has ended, or one waiting on an answer, says nothing
	// a reader did not already have from the outcome. Where it is load-bearing
	// is OutcomeExecuting, which covers a run in flight, a run standing still
	// at a position, and a run with no position at all; NextActionOf is where
	// this fact chooses between them, and Decide is what puts it and the
	// action into an answer together when a surface builds one.
	Advancing bool `json:"advancing"`
	// Position is the node that has not run, empty exactly when the run
	// completed.
	Position string `json:"position,omitempty"`
	// Activity is what the segment advancing this run is doing now: the stage
	// its position belongs to, that stage's fix round, and how long the node
	// at Position has been running. It is present only on an answer that
	// found a segment advancing the run, because it describes a node in
	// flight and a run nothing is advancing has none.
	Activity *Activity `json:"activity,omitempty"`
	// Reason explains a run that ended without a verdict on the change: which
	// bound parked it, or, for a run its pull request ended by closing
	// without a merge, the reason the record carries for that. It is empty
	// otherwise.
	Reason string `json:"reason,omitempty"`
	// Steps is how many node executions the run has spent.
	Steps int `json:"steps"`
	// Budget is the run-wide step budget it is being held to.
	Budget int `json:"budget"`
	// Decision is the open question, present exactly when the run is waiting
	// on an answer.
	Decision *Decision `json:"decision,omitempty"`
	// Stages is what became of each of the nine, in the order a run takes
	// them.
	Stages []Stage `json:"stages,omitempty"`
	// MergeRecord is PRD section 8's merge record for this run, present
	// exactly when a code-host read ended the run on its pull request's
	// state: the pull request, the head read, which end, and when. It is the
	// record composed rather than restated, and the passed outcome above is
	// derived from it.
	MergeRecord *store.MergeRecord `json:"merge_record,omitempty"`
	// Cleanup is PRD section 8's cleanup record for this run's isolated
	// copy, present exactly when the cleanup decision wrote one: the proof
	// the copy was removed on, or the reason it was kept, with what it
	// examined. It is the record composed rather than restated, on
	// MergeRecord's terms, and it is where a surface learns that a local
	// copy was kept because its work is not yet safely elsewhere.
	Cleanup *store.RunCleanup `json:"cleanup,omitempty"`
	// CopySweep is the record of the latest sweep of this run's isolated
	// copy, present exactly when one was recorded: what it came to, how many
	// processes it ended and how many kept it from clearing, whether the move
	// that finished the run or a later recovery asked it, and when. It is the
	// record composed rather than restated, on Cleanup's terms. It says what
	// a sweep found when it ran and not whether anything runs in the copy
	// now, and nothing decides from it: an uncleared sweep does not keep the
	// copy, whose removal is Cleanup's decision alone. CopySweepPhrase is the
	// line a person reads for the outcomes that leave them something to do.
	CopySweep *store.RunCopySweep `json:"copy_sweep,omitempty"`
	// Repairs is every repair a fix round of the checks stage sent back for
	// review after the push, in the order they were taken, as
	// pipeline.RepairsOf reads them off the run's state: the commit the run
	// had pushed, the repair, and whether the push stage has forwarded
	// another commit since. It is the state composed rather than restated,
	// and it is absent for a run that repaired nothing. RepairPhrase is the
	// line a person reads.
	Repairs []pipeline.Repair `json:"repairs,omitempty"`
	// AnswerNotApplied says, on the answer to a response, why the answer the
	// request carried was not applied to the run: the hold it answered was
	// closed by the service first, on a re-read of the run's pull request,
	// and the run reported here is the run as it then ended. It is empty on
	// every answer whose request carried no answer and on every response
	// whose answer was taken.
	AnswerNotApplied string `json:"answer_not_applied,omitempty"`
	// Proposals are notes raised about this run from records across runs of
	// its repository, read when this answer was built: today, PRD section 5's
	// proposal to turn a decision-model check off. They are not any stage's
	// findings and never part of the run's state, so they hold nothing and
	// the same run can carry one on one reading and not on a later one.
	Proposals []findings.Finding `json:"proposals,omitempty"`
	// NotApplied names the run-starting inputs a request carried that this
	// run was not built from, by the field of StartRequest each came in on.
	//
	// It is how the bare command stays attach-or-start without discarding
	// what a caller wrote. A start that finds the branch already has a run
	// answers with that run, which is the point of the command and is worth
	// doing twice; the intent and the skip list belong to the run a start
	// creates, and a run that already exists was built from neither. Naming
	// them here is the difference between input that was not applied and
	// input that was thrown away in silence.
	//
	// It is empty when nothing was dropped, which is every answer to a call
	// that created the run it reports and every answer to a call that carried
	// none of those inputs.
	NotApplied []string `json:"not_applied,omitempty"`
}

// NextAction is what to do about this run, which PRD section 9 requires of
// every outcome, terminal or not.
func (r Run) NextAction() string { return r.nextAction }

// Decide fills in what this answer says about the run: the outcome, whether
// anything is advancing it, and what to do next, all from one standing.
//
// It is how a surface that builds an answer gets a next action, and the three
// come from one input, so an action cannot be put behind an outcome it
// contradicts and a branch that forgot to derive one would leave the zero
// value rather than a stale sentence. What else writes the field, and what the
// unexported field does and does not buy, is on nextAction itself.
//
// What that does not make unrepresentable is the outcome and the advancing
// fact themselves. Both are exported, so a caller may write them after this
// returns; the action is the one it has no assignment site for.
func (r Run) Decide(s Standing) Run {
	r.Outcome = OutcomeOf(s)
	r.Advancing = s.Advancing
	r.nextAction = NextActionOf(s)
	return r
}

// MarshalJSON writes the run with its next action, which is unexported and so
// would not travel on its own. The shape is the exported fields plus that one,
// so what travels is what a caller decodes.
//
// It encodes with HTML escaping off for the reason Encoder gives: a run's
// answer carries the most agent-written text of any shape here - intents,
// reasons, stage reports, findings relayed verbatim - and rewriting three
// ordinary characters in them is worse than leaving them. Encoder's own
// setting cannot reach inside a Marshaler, which writes its own bytes, so the
// choice has to be made again here rather than inherited.
func (r Run) MarshalJSON() ([]byte, error) {
	type fields Run
	var document bytes.Buffer
	enc := json.NewEncoder(&document)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		fields
		NextAction string `json:"next_action"`
	}{fields(r), r.nextAction}); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(document.Bytes(), []byte("\n")), nil
}

// UnmarshalJSON reads a run back, including the next action the answering
// service decided. A decoded answer reports what that service said rather than
// a decision made here; nextAction says why that is deliberate.
func (r *Run) UnmarshalJSON(data []byte) error {
	type fields Run
	var read struct {
		fields
		NextAction string `json:"next_action"`
	}
	if err := json.Unmarshal(data, &read); err != nil {
		return err
	}
	*r = Run(read.fields)
	r.nextAction = read.NextAction
	return nil
}

// Runs is the answer to a request for recent runs, newest first.
type Runs struct {
	// Runs are the records, newest first.
	Runs []store.Run `json:"runs"`
	// Parked is, for each listed run whose record names the bound that
	// parked it, the report the stage that park names last recorded, in the
	// order Runs lists them. It is read off the checkpoint the park record
	// names, so it is what the stage reported as the run parked and not a
	// copy kept anywhere else. It is absent when no listed run was parked.
	Parked []ParkedReport `json:"parked,omitempty"`
}

// ParkedReport is the report the stage a run's park names last recorded,
// carried for a run listing so the findings a park stopped on can be read
// after the run has ended without asking for each run on its own.
type ParkedReport struct {
	// Run is the run's identifier.
	Run string `json:"run"`
	// Stage is the stage the park names, the record's own Park.Stage. It is
	// empty for a budget park that stood in front of no stage, which has no
	// report to carry.
	Stage string `json:"stage,omitempty"`
	// Report is what that stage recorded, absent when the stage never ran,
	// when the park names no stage, and when the report could not be read.
	Report *findings.Report `json:"report,omitempty"`
	// Detail says why a report that should be here is not: the checkpoint or
	// the report could not be read. It is empty when Report is present and
	// when there was nothing to read.
	Detail string `json:"detail,omitempty"`
}

// Task is one piece of fleet work with its resolved current state. The state
// is internal/taskstate's resolution of the records that own the facts, not a
// stored copy and not the event log, per P8 and P14; the resolution carries the
// source that decided it.
type Task struct {
	// Record is the task record.
	Record store.Task `json:"record"`
	// State is the resolved current state, with its source.
	State taskstate.Resolution `json:"state"`
	// Waiting is what the worker said it is waiting on, and is empty unless
	// State is Paused. PRD section 6 never shows that state as a word: it is
	// translated to the worker's own declared wait, which is the detail of
	// the history entry the resolution's basis names, so this is the one
	// worker's text an answer about a task carries. The resolution itself
	// carries no event text, on its own terms, and the service that answers
	// fills this from the same read the state was resolved from.
	Waiting string `json:"waiting,omitempty"`
	// Report is the investigation's stored report when one has been filed,
	// and is absent otherwise. It is the coordinator's and the operator's
	// structured read of what the worker delivered: summary, findings and
	// evidence paths from the task_report row, plus the on-disk evidence
	// copy's path. The person-facing rendering points at this field through
	// TaskPhrase and never relays these words (P13).
	Report *InvestigationReport `json:"report,omitempty"`
	// Policy is what the posture recorded on the task requires, publishes
	// and leaves to whom: config.Delivery.PolicyForNoCodeHost of that
	// posture and its noCodeHost argument, whether the task's repository
	// has no code host as codeHostOf decides for a run's start and taskPolicy
	// passes as host.none. It is
	// present exactly when both are known: the record holds a posture, and
	// the repository the task names can be read and is one a run can start
	// against. Otherwise it is absent. It is derived on every answer and
	// never stored.
	Policy *config.Policy `json:"policy,omitempty"`
}

// InvestigationReport is what task.get (and every other answer that carries
// a Task) exposes of an investigation's stored report. Summary, Findings and
// Evidence are the worker's filed report as internal/findings decoded it from
// the task_report row; EvidenceFile is the evidence copy under the task's
// directory for a person to open. The row is the source (P8); the file is
// beside it and may be missing after a crash between the two writes.
type InvestigationReport struct {
	// Summary is the report's account of what the investigation concluded.
	Summary string `json:"summary"`
	// Findings are what the investigation found, in the order filed.
	Findings []findings.Finding `json:"findings,omitempty"`
	// Evidence are the artifact paths the report named, as filed.
	Evidence []findings.Evidence `json:"evidence,omitempty"`
	// EvidenceFile is the path of the on-disk evidence copy, empty when the
	// home could not name one for the task.
	EvidenceFile string `json:"evidence_file,omitempty"`
	// RecordedAt is when the task_report row was written.
	RecordedAt time.Time `json:"recorded_at"`
}

// TaskAnswered is the answer to a task's hold. It carries the hold as the
// answer left it, what the answer did, and the task as it then stands.
type TaskAnswered struct {
	// Hold is the hold answered, with its resolution and when it was made.
	// Who made it is in the record and does not travel: a store.Resolver
	// has no encoded form, so this understates the resolver as the hold.state
	// event does.
	Hold store.Hold `json:"hold"`
	// Effect is what the answer did beyond closing the hold.
	Effect AnswerEffect `json:"effect"`
	// Relaunch is the attempt a retry began, and is nil for every effect
	// but AnswerRelaunched and AnswerRedispatched.
	Relaunch *RelaunchBegun `json:"relaunch,omitempty"`
	// NotRelaunched is why a retry began no work now, and is empty for
	// every effect RetryNotBegun does not report. The answer is recorded on
	// the hold either way; what it does after that is the effect's to say,
	// and only AnswerRetryDeferred leaves it to a later start to read. For
	// AnswerRetryUndispatchable it names what in the record was refused.
	NotRelaunched string `json:"not_relaunched,omitempty"`
	// Task is the task as it stands after the answer.
	Task Task `json:"task"`
}

// AnswerEffect is what answering a task's hold did. The set is closed and
// AnswerEffects lists it. Each effect says only what the answer itself did
// or what the mechanism behind it then does for certain: a retry that began
// work names which kind of start it was, and a retry that began none names
// the two refusals whose consequence is settled, AnswerRetryRefused and
// AnswerRetryUndispatchable, apart from the rest, AnswerRetryDeferred,
// whose consequence is not.
type AnswerEffect string

const (
	// AnswerRecorded is a hold whose answer is recorded and acts on nothing
	// further: a decision an investigation's report registered.
	AnswerRecorded AnswerEffect = "recorded"
	// AnswerRelaunched is a retry on a relaunch hold, the one past
	// relaunch_attempts or the one a relaunch with no process sweep raised,
	// that began one attempt through the anchored relaunch, in the copy as
	// the earlier worker left it and with the relaunch note telling the new
	// worker so. The launch follows the answer and is not part of it.
	AnswerRelaunched AnswerEffect = "relaunched"
	// AnswerRedispatched is a retry on the hold a dispatch over a standing
	// copy with no process sweep raised, that began one attempt in that
	// copy as it stands through the anchored dispatch. A dispatch carries
	// no note about an earlier attempt, so the new worker is started with
	// the task's instructions alone and is told nothing of what the crashed
	// attempt may have left in the copy. The launch follows the answer and
	// is not part of it.
	AnswerRedispatched AnswerEffect = "redispatched"
	// AnswerRetryDeferred is a retry recorded whose relaunch or dispatch was
	// refused now for a reason other than the three below; NotRelaunched says
	// why. Nothing about the next start is promised: the reasons it covers
	// range from a slot another caller holds to a refusal after the claim,
	// which spends the answer, and the one thing they share is that the
	// start-time passes read the task again, which may begin it, hold it
	// again, or leave it.
	AnswerRetryDeferred AnswerEffect = "retry-deferred"
	// AnswerRetryHeld is a retry recorded whose relaunch or dispatch was
	// refused because the platform has no process sweep, which raised a
	// further hold for the attempt it read; it begins nothing until that
	// hold is answered retry.
	// NotRelaunched says why and names the hold.
	AnswerRetryHeld AnswerEffect = "retry-held"
	// AnswerRetryAtBound is a retry on a stop or worker-fallback hold whose relaunch would
	// pass relaunch_attempts, which raised the bound's hold for the attempt;
	// it begins nothing until that hold is answered retry. NotRelaunched
	// says why and names the hold.
	AnswerRetryAtBound AnswerEffect = "retry-at-bound"
	// AnswerRetryRefused is a retry recorded whose relaunch or dispatch
	// began an attempt that the isolation assertion then refused, which
	// recorded that refusal on the attempt: the task resolves Failed from
	// it, reconciliation surfaces such a task rather than relaunching it,
	// and dispatch passes it over, so nothing begins the work again on its
	// own. NotRelaunched says what the assertion found.
	AnswerRetryRefused AnswerEffect = "retry-refused"
	// AnswerRetryUndispatchable is a retry recorded whose relaunch or
	// dispatch was refused because the task's record cannot be dispatched as
	// it stands: it names no repository, reserved its copy somewhere other
	// than the home puts it, has no instructions file, or carries values the
	// copy's expectation refuses. Every path that begins an attempt reads the
	// record that way before it claims one, so nothing begins the work until
	// what that read finds changes: the task's or its repository's record,
	// or the instructions file the home keeps for the task. NotRelaunched
	// says what was refused.
	AnswerRetryUndispatchable AnswerEffect = "retry-undispatchable"
	// AnswerStopped is a stop: the task is left where it stopped and
	// nothing relaunches it.
	AnswerStopped AnswerEffect = "stopped"
	// AnswerLanded is an approve on a local task's approval hold whose
	// fast-forward of the local default branch was made and recorded: the
	// local landing record exists and the task resolves Landed from it.
	// Nothing was validated and nothing left the machine. An approve whose
	// fast-forward is refused is not an effect: the answer is refused, the
	// hold stays open, and nothing is recorded.
	AnswerLanded AnswerEffect = "landed-locally"
)

// AnswerEffects is the closed set of AnswerEffect, in declaration order. The
// result is a copy.
func AnswerEffects() []AnswerEffect {
	return []AnswerEffect{
		AnswerRecorded, AnswerRelaunched, AnswerRedispatched,
		AnswerRetryDeferred, AnswerRetryHeld, AnswerRetryAtBound, AnswerRetryRefused, AnswerRetryUndispatchable, AnswerStopped, AnswerLanded,
	}
}

// RetryNotBegun reports whether the effect is a retry a person decided on
// that began no work: deferred, held on a further decision, held at the
// relaunch bound, refused by the isolation assertion, or refused on a record
// that cannot be dispatched. It is what a surface reads to tell a caller that
// the decision was taken and the work did not start, without the caller
// reading the document. A stop and an answer that only closed a hold asked
// for no work, and a started retry began some, so neither is reported.
func (e AnswerEffect) RetryNotBegun() bool {
	switch e {
	case AnswerRetryDeferred, AnswerRetryHeld, AnswerRetryAtBound, AnswerRetryRefused, AnswerRetryUndispatchable:
		return true
	}
	return false
}

// RelaunchBegun is the attempt a retry began: its position in the task's
// attempts and the session reference of the attempt it superseded.
type RelaunchBegun struct {
	// Attempt is the new attempt's sequence.
	Attempt int64 `json:"attempt"`
	// Superseded is the session reference of the attempt it replaced, and
	// empty when that attempt recorded none, which is a retried dispatch's.
	Superseded string `json:"superseded"`
}

// Tasks is the answer to a request for fleet work.
type Tasks struct {
	// Tasks are the records with their resolved states.
	Tasks []Task `json:"tasks"`
	// Supervision is the pull guard's finding over the fleet listed, computed
	// by the service that answered. A command that lists fleet work reads it
	// from the service, so with the service down there is no answer and no
	// warning in it; the status command is the one that warns then.
	Supervision *Supervision `json:"supervision,omitempty"`
}

// Lifecycle is the answer to a request that stops or restarts the service.
//
// PRD section 9 makes both refuse while runs are active, listing the affected
// runs and requiring an explicit force flag, so a refusal is one of these with
// Accepted false and Active naming them rather than an error with a sentence
// in it.
type Lifecycle struct {
	// Accepted is whether the service is acting on the request.
	Accepted bool `json:"accepted"`
	// Restarting is whether it will come back.
	Restarting bool `json:"restarting"`
	// Active are the runs that made this refuse, empty when it was accepted.
	Active []store.Run `json:"active,omitempty"`
	// Detail says why it refused, and is empty when it did not.
	Detail string `json:"detail,omitempty"`
	// ConfigurationRefused is whether a restart was refused because the
	// home's configuration file, which a replacement reads at start, could
	// not be read or parsed when the restart was asked for. Detail then holds
	// that refusal, and the running service was left serving.
	ConfigurationRefused bool `json:"configuration_refused,omitempty"`
}

// Init is the answer to creating or repairing a gate.
type Init struct {
	// Gate is the gate as it now stands.
	Gate Gate `json:"gate"`
	// Reattached is whether an existing gate was adopted rather than created,
	// which internal/gate answers and which a person wants to know because it
	// says whether the run history under this working copy carried over.
	Reattached bool `json:"reattached"`
	// Created is whether this init made the gate's repository, which
	// internal/gate answers. It is false for a checkout initialized again
	// over the gate it already had and for a reattachment, so the row a
	// person reads says a new gate only of one that is new.
	Created bool `json:"created"`
	// Repository is the repository record as it now stands.
	Repository store.Repository `json:"repository"`
	// Changed is every field of the repository record this init wrote a
	// value into that differs from the one the record held before, in
	// RecordFields' order, and is empty on a first init and on one that
	// recorded what was already there. It is this answer's: the record
	// keeps the value it now holds and not the one it replaced.
	Changed []RecordChange `json:"changed,omitempty"`
	// Upstream is how the upstream now recorded treats proposals, the same
	// check assistant doctor makes from the same record, so init says when
	// it recorded a host a run is refused on rather than leaving that to
	// the first run.
	Upstream *Check `json:"upstream,omitempty"`
	// DefaultBranchSource is where Repository.DefaultBranch came from, one
	// of DefaultBranchSources. It is this answer's and not the record's: the
	// record keeps the branch and not how it was learned.
	DefaultBranchSource DefaultBranchSource `json:"default_branch_source"`
	// Commands is what init found and did about the commands the checks
	// run: what it offered, what it wrote, and why it offered nothing for a
	// key. It is this answer's and not the record's: nothing here records
	// it, and a run reads its commands from the default branch's copy of
	// the configuration file rather than from anything init wrote.
	Commands CommandProposals `json:"commands"`
}

// Eject is the answer to removing a gate and its records.
type Eject struct {
	// Removed is whether the gate and the records were removed.
	Removed bool `json:"removed"`
	// Repository is the identifier of the repository that was forgotten.
	Repository string `json:"repository,omitempty"`
	// Runs is how many run records were removed with it.
	Runs int `json:"runs"`
}

// Admission is the answer to an admission check on a push to a gate: the push
// may proceed.
//
// There is no field saying so. Admission's answer reaches git as an exit
// status, and PRD section 5 puts the check before any reference changes so
// that a refusal happens instead of a change; everything that is not this
// answer - a refusal, a failure, a service that did not respond - is an error
// and a non-zero status. A boolean here would be a second way to say the same
// thing, and the one that could be read as permission while the call failed.
//
// The repository the push's runs will belong to is deliberately not a field.
// It is the gate's own identifier in every gate this build creates, and a
// field that always repeats another on the same shape is a second owner of one
// fact; a run names its own repository.
type Admission struct {
	// Gate is the gate the push was admitted to.
	Gate string `json:"gate"`
	// Refs are the reference names the push updates, in the order they
	// arrived.
	Refs []string `json:"refs,omitempty"`
}

// Started is one run a push started: which branch, at which commit, under
// which identifier.
type Started struct {
	// Branch is the branch that was pushed.
	Branch string `json:"branch"`
	// Head is the commit the push moved it to, which is the commit the run
	// validates.
	Head string `json:"head"`
	// Run is the run's identifier.
	Run string `json:"run"`
	// Superseded is the run of the same branch this one displaced in the
	// record, empty when the branch had none. PRD section 8 has a new push
	// supersede the run in progress, and naming it is the difference between a
	// run that was displaced and one that was moved aside without a word.
	//
	// It says the named run's record was moved to terminated, and no more than
	// that. The service signals that run's cancellation and does not await its
	// leaving supervision, so a caller reading this may not take it as the run
	// having stopped executing.
	Superseded string `json:"superseded,omitempty"`
}

// Ignored is one reference a push carried that started no run, and why.
//
// It is reported rather than dropped. A push of five references that started
// two runs and said nothing about the other three reads as a push that was
// fully acted on, which is the silent narrowing this repository keeps finding.
type Ignored struct {
	// Ref is the reference's full name.
	Ref string `json:"ref"`
	// Reason says why no run was started for it.
	Reason string `json:"reason"`
}

// Notification is the answer to a notification that a gate accepted a push:
// what it started and what it did not.
//
// It answers once the runs are recorded and their execution is handed to the
// service, not once they finish. PRD section 8 has a push return immediately
// and the notification hand off and exit, so a run named here is a run that
// exists and is the service's to advance; where it stands is read from the run
// itself.
type Notification struct {
	// Gate is the gate the push arrived at.
	Gate string `json:"gate"`
	// Started are the runs this push began, one per branch it updated.
	Started []Started `json:"started,omitempty"`
	// Ignored are the references it started nothing for, with the reason for
	// each.
	Ignored []Ignored `json:"ignored,omitempty"`
}

// Appended is the answer to a worker's task.event: the entry as the history
// now holds it, which is internal/store's record and not a second shape of
// it. The task it names is the one the caller's credentials placed it in.
type Appended struct {
	// Event is the entry, with the sequence the store assigned it.
	Event store.TaskEvent `json:"event"`
}

// TaskCreated is the answer to task.create: what internal/tasks' intake
// recorded and why, composed from that package's Created rather than
// restated. A task recorded Dispatched has its copy cut and its worker
// launched by the service after this is answered; where it stands is read
// from task.get.
type TaskCreated struct {
	// Record is the task as the store holds it. Its BlockerTask is known
	// exactly when the task was queued.
	Record store.Task `json:"record"`
	// Cause says whether the task was queued and by whose decision, in
	// internal/tasks' Cause vocabulary.
	Cause string `json:"cause"`
	// BlockerState is the blocker's state as intake read it, and empty for
	// an independent task.
	BlockerState taskstate.State `json:"blocker_state,omitempty"`
	// Instructions is the file the instructions were written to.
	Instructions string `json:"instructions"`
}

// TaskPromoted is the answer to task.promote: what internal/tasks'
// promotion did, composed from its Promoted.
type TaskPromoted struct {
	// Record is the task after the flip.
	Record store.Task `json:"record"`
	// Event is the promoted entry the task's history now carries.
	Event store.TaskEvent `json:"event"`
	// Head is the commit the task's branch was created at.
	Head string `json:"head"`
	// ReportRecordedAt is when the report the promotion carries as evidence
	// was stored, and absent when none was. The text is not carried.
	ReportRecordedAt *time.Time `json:"report_recorded_at,omitempty"`
	// Instructions is the file the intent and the second check were
	// appended to.
	Instructions string `json:"instructions"`
	// Brief is what became of the brief the promotion sends the task's
	// live worker: the same text that file was given.
	Brief Briefing `json:"brief"`
}

// Briefing is what became of a promotion's brief to the worker of the
// task's latest attempt. A brief is sent as task.message sends a message,
// recorded first and then written, so whether it reached the worker's input
// is read from Message, the record, and nowhere else (P8); whether the
// worker read it is the acknowledgement the worker appends to that record
// later. When no worker could be sent it, Message is absent and Unsent says
// why, and the brief is still in the instructions file a worker begun on the
// task later reads. The promotion stands either way.
type Briefing struct {
	// Message is the brief's message record, absent when none was appended.
	Message *store.Message `json:"message,omitempty"`
	// Unsent says why the brief is not known to have reached the worker's
	// input: why no record was appended, or why the one appended was
	// refused or could not be marked. Empty exactly when Message says
	// written.
	Unsent string `json:"unsent,omitempty"`
}

// Delivered reports whether the record says the brief was written to the
// worker's input.
func (b Briefing) Delivered() bool {
	return b.Message != nil && b.Message.Delivery() == store.DeliveryWritten
}

// TaskRelaunched is the answer to task.relaunch: the attempt the anchored
// relaunch began and what it superseded. It answers once the attempt is
// claimed and recorded, not once the worker runs: the launch is handed to
// the service, as a relaunch at service start hands it, so whether the
// worker started is read from the task's attempt afterwards.
type TaskRelaunched struct {
	// Task is the task relaunched.
	Task string `json:"task"`
	// Attempt is the attempt the relaunch began.
	Attempt int64 `json:"attempt"`
	// Superseded is the session reference of the attempt whose terminal was
	// found gone.
	Superseded string `json:"superseded"`
	// Swept is what the sweep of the standing copy did, in the service's
	// words.
	Swept string `json:"swept,omitempty"`
}

// TaskRelayed is the answer to task.relay: the findings relayed record the
// coordinator's statement wrote and the history entry written with it, both
// internal/store's records rather than a second shape of them. The task's
// Complete is resolved from Record and read from task.get.
type TaskRelayed struct {
	// Record is the findings relayed record as stored.
	Record store.TaskRelay `json:"record"`
	// Event is the findings-relayed entry the task's history now carries.
	Event store.TaskEvent `json:"event"`
}

// TaskMessaged is the answer to task.message: the message record as the
// send left it, which is the one place delivery is read from (P8). Delivery
// is written when the provider's Send returned and refused, with the
// record's reason, when it did not; whether the worker read it is the
// acknowledgement the worker appends later, read from the same record.
type TaskMessaged struct {
	// Message is the record.
	Message store.Message `json:"message"`
	// Delivery is the record's delivery fact.
	Delivery store.Delivery `json:"delivery"`
	// SentBy is who the record says sent it, which the surface derived and
	// not what the caller said.
	SentBy string `json:"sent_by"`
}

// MaxPeekBytes bounds what task.peek shows of a worker's output, in bytes
// of Output from the end: the most recent events whose rendering fits are
// shown, and the rest are counted in TaskPeek.Earlier. It bounds what is
// shown and not what is read, which is the session provider's capture,
// bounded by the agent runner's output limit. It is a fixed limit in the
// program and not a setting, on MaxMessageBytes' terms.
const MaxPeekBytes = 16 << 10

// TaskPeek is the answer to task.peek: the latest attempt's worker's recent
// output as the service's session provider holds it, read as a transcript
// by agents.ReadTranscript, redacted by internal/redact, abbreviated, and
// bounded by MaxPeekBytes, with how the terminal probed and, where the
// provider hosts the worker in a terminal, the command a person attaches
// with.
//
// The output is evidence for the coordinator, never text to relay to a
// person (P13): the person-facing rendering is PeekPhrase and carries none
// of it.
type TaskPeek struct {
	// Task is the task read.
	Task string `json:"task"`
	// Attempt is the attempt whose worker was read.
	Attempt int64 `json:"attempt"`
	// Session is that attempt's session reference.
	Session string `json:"session"`
	// Liveness is how the terminal probed, internal/taskstate's word.
	Liveness string `json:"liveness"`
	// Output is Events rendered, one agents.TranscriptEvent.Line each, in
	// order: what a person following the worker would read.
	Output string `json:"output"`
	// Events are the events Output renders, oldest first.
	Events []agents.TranscriptEvent `json:"events"`
	// Earlier is how many events were read from the capture and are not
	// shown, because showing them would take Output over MaxPeekBytes.
	Earlier int `json:"earlier,omitempty"`
	// Omitted is how many lines and blocks of the capture were left out as
	// noise, by agents' noise table: counters, status lines and thinking.
	Omitted int `json:"omitted,omitempty"`
	// Dropped is how many bytes of earlier output the provider's capture let
	// go before this read.
	Dropped int64 `json:"dropped"`
	// Uncounted is true when output may also have been let go before it was
	// read, by a terminal whose own history keeps no count of it.
	Uncounted bool `json:"uncounted,omitempty"`
	// Attach is the command line a person runs to watch the worker's
	// terminal and type into it, the binary first; empty for a worker with
	// no terminal, which every pipe-hosted worker is.
	Attach []string `json:"attach,omitempty"`
}

// TaskStopped is the answer to task.stop: the attempt whose worker was
// ended, how it was ended, and the hold registered once the probe read its
// terminal dead. It is answered only after that probe; a stop the probe did
// not prove is a refusal and no hold is registered.
type TaskStopped struct {
	// Task is the task stopped.
	Task string `json:"task"`
	// Attempt is the attempt whose worker was ended.
	Attempt int64 `json:"attempt"`
	// Session is that attempt's session reference.
	Session string `json:"session"`
	// Ended is how the worker was ended, in the service's words.
	Ended string `json:"ended"`
	// Hold is the hold the stop registered, open, which a retry answer
	// relaunches through the anchored relaunch and a stop answer leaves. It
	// is the zero hold when Finished is set.
	Hold store.Hold `json:"hold"`
	// Finished is the state the task resolved in from internal/taskstate's
	// terminal tier when it was read after the worker ended, landed or
	// complete, in which case no hold was registered because the work is
	// finished and nothing is owed a relaunch. It is empty when the stop
	// registered Hold.
	Finished taskstate.State `json:"finished,omitempty"`
}

// Wakes is the answer to a drain of the wake queue: the rows on offer,
// oldest first, as internal/store holds them after the drain. A drain hands
// out what it returns and closes nothing, so every row here is still queued
// until the caller acknowledges it after acting on it, and a row not
// acknowledged is handed out again once the caller it was handed to is no
// longer attached: delivery is at least once, so a caller may be handed a
// row it has seen, and Handouts counts how many times it has been. Act on a
// row keyed by its task, kind and session reference (in the marker for
// terminal-gone), never by Handouts; acknowledge every id acted on.
//
// Who the drain handed the rows to is in the record and not on this wire:
// store.Acknowledger has no text form outside internal/store, so
// HandedOutTo is kept off it. HandedOutAt and Handouts are the fields a
// reader can rely on.
type Wakes struct {
	// Wakes are the rows handed out, oldest first, at most the requested
	// limit.
	Wakes []store.Wake `json:"wakes"`
}

// QueuedWakes is the answer to a person's reading of the wake queue: every
// queued row, handed out or not, oldest first, each beside the project of
// the task it is about. The reading hands out and acknowledges nothing, so
// the rows are exactly where they were for the coordinator's drain.
type QueuedWakes struct {
	// Wakes are the queued rows, oldest first, at most the requested limit.
	Wakes []QueuedWake `json:"wakes"`
}

// QueuedWake is one queued row and the project of the task it is about,
// which is what a person's rendering names it by: a task's identifier and a
// wake's kind are words P13 keeps from a person, and the structured
// rendering carries both.
type QueuedWake struct {
	// Wake is the row as the store holds it.
	Wake store.Wake `json:"wake"`
	// Project is the project of the task the row names, as the task record
	// holds it.
	Project string `json:"project"`
}

// Acknowledged is the answer to an acknowledgement: which rows were closed
// and who the record says closed them, which is what the surface derived and
// not what the caller said.
type Acknowledged struct {
	// Wakes are the identifiers the batch closed, as the request named them.
	Wakes []int64 `json:"wakes"`
	// By is who the rows record as having acknowledged them, rendered the
	// way internal/store renders the closed set.
	By string `json:"by"`
}

// Check is one thing assistant doctor looked at.
type Check struct {
	// Name is what was checked.
	Name string `json:"name"`
	// OK is whether it is in a state a run could proceed from.
	OK bool `json:"ok"`
	// Detail is what was found, present whether or not it is in that state, so
	// a passing check still says what it found rather than only that it
	// passed.
	Detail string `json:"detail,omitempty"`
	// Blocking is whether this check being not OK is on its own enough to stop
	// a run from starting. A check that is not blocking is reported and does
	// not decide.
	Blocking bool `json:"blocking"`
	// Kind names a sub-result when one check has several readings. CheckUpstream
	// uses the UpstreamKind constants.
	Kind string `json:"kind,omitempty"`
}

// Doctor is what assistant doctor reports. PRD section 9 asks it to check
// every dependency and decide whether a run can start at all, so the decision
// is a field rather than something a reader infers from the list.
type Doctor struct {
	// Checks is everything that was looked at, in the order it was looked at.
	Checks []Check `json:"checks"`
	// CanStartRun is the decision: whether a run can start at all.
	CanStartRun bool `json:"can_start_run"`
	// Detail says what stops one, and is empty when nothing does.
	Detail string `json:"detail,omitempty"`
	// Change is where this checkout's change stands on the way to a run and
	// the next step it needs. It is read only once the checkout is set up
	// for validation, so it is absent before assistant init has run there,
	// where the gate row's own step comes first. It does not decide
	// CanStartRun: a run can start whatever the change's step is.
	Change *Change `json:"change,omitempty"`
}

// Plan is what assistant sync answers: PRD section 11's classification of
// the branch, the exact steps it allows, and whether they were carried out.
//
// PRD section 9 makes assistant sync print the exact plan and require
// confirmation, which is what this is for: the plan is produced, shown, and
// only then carried out, by the same command given --confirm. Every step
// and the classification are internal/custody's own values, carried as they
// are, so this shape is not a second owner of either.
type Plan struct {
	// Classification is the branch's class and the commits it was decided
	// on, absent when the branch could not be classified.
	Classification *custody.Classification `json:"classification,omitempty"`
	// Steps are what synchronizing does, in order, exactly as
	// internal/custody carries them out. A class that allows nothing has
	// none.
	Steps []custody.Step `json:"steps"`
	// Applied is whether the steps were carried out, which is false for a
	// plan that was printed and not confirmed, and for one refused.
	Applied bool `json:"applied"`
	// Done is the steps carried out, which on a refusal partway is what was
	// done before it.
	Done []custody.Step `json:"done,omitempty"`
	// Refusal is why custody refused to classify the branch or to carry the
	// plan out, empty when it did neither.
	Refusal custody.Reason `json:"refusal,omitempty"`
	// Detail says what happened or why nothing did, in custody's own
	// sentence.
	Detail string `json:"detail,omitempty"`
}

// Blocked reports a plan that stops the person: one refused, or a branch
// section 11 blocks as genuinely diverged. A plan waiting on confirmation, a
// branch the pipeline holds, and one with nothing to do are not blocked.
func (p Plan) Blocked() bool {
	return p.Refusal != "" || (p.Classification != nil && p.Classification.Class == custody.ClassDiverged)
}

// Custody is the classification assistant status reports for the branch,
// read without fetching and without writing anything in the working copy:
// the class, or why it could not be read.
type Custody struct {
	// Classification is the branch's class, absent when it was refused.
	Classification *custody.Classification `json:"classification,omitempty"`
	// Refusal is why the branch could not be classified.
	Refusal custody.Reason `json:"refusal,omitempty"`
	// Detail is custody's sentence for either.
	Detail string `json:"detail,omitempty"`
}
