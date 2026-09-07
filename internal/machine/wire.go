package machine

import (
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
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
}

// Run is one run as a surface reports it: the record, where its execution
// stands, and what to do about it.
type Run struct {
	// Record is the authoritative run record, which internal/store owns.
	Record store.Run `json:"record"`
	// Outcome is what a driving agent reads to decide what to do next.
	Outcome Outcome `json:"outcome"`
	// NextAction is what to do about that outcome, which PRD section 9
	// requires of every outcome, terminal or not.
	NextAction string `json:"next_action"`
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
	// is OutcomeExecuting, which covers both a run in flight and a run
	// standing still, and NextActionFor is that outcome's action split at this
	// fact.
	Advancing bool `json:"advancing"`
	// Position is the node that has not run, empty exactly when the run
	// completed.
	Position string `json:"position,omitempty"`
	// Reason explains a parked run, and is empty otherwise.
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

// Runs is the answer to a request for recent runs, newest first.
type Runs struct {
	// Runs are the records, newest first.
	Runs []store.Run `json:"runs"`
}

// Task is one piece of fleet work with its resolved current state. PRD
// section 8 makes the resolved state a record of its own, distinct from the
// append-only event log, per P8; this carries both records rather than
// deriving one from the other.
type Task struct {
	// Record is the task record.
	Record store.Task `json:"record"`
	// State is the authoritative resolved current state.
	State store.TaskState `json:"state"`
}

// Tasks is the answer to a request for fleet work.
type Tasks struct {
	// Tasks are the records with their resolved states.
	Tasks []Task `json:"tasks"`
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
}

// Init is the answer to creating or repairing a gate.
type Init struct {
	// Gate is the gate as it now stands.
	Gate Gate `json:"gate"`
	// Reattached is whether an existing gate was adopted rather than created,
	// which internal/gate answers and which a person wants to know because it
	// says whether the run history under this working copy carried over.
	Reattached bool `json:"reattached"`
	// Repository is the repository record as it now stands.
	Repository store.Repository `json:"repository"`
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
}

// Plan is what a command that changes the working copy will do, printed before
// it does any of it.
//
// PRD section 9 makes assistant sync print the exact plan and require
// confirmation, which is what this is for: the plan is produced, shown, and
// only then carried out.
type Plan struct {
	// Steps are what would happen, in order.
	Steps []string `json:"steps"`
	// Applied is whether they were carried out, which is false for a plan that
	// was printed and not confirmed.
	Applied bool `json:"applied"`
	// Detail says why nothing was done, and is empty when something was.
	Detail string `json:"detail,omitempty"`
}
