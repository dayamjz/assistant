package machine

import (
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// The request bodies. internal/ipc carries a method's parameters as written
// and owns no part of their shape, so they are owned here alongside the
// answers they ask for: one package holds both halves of the surface, and a
// command line and a service built from it cannot disagree about either.
//
// Every one of them is a struct rather than a bare value, so a field added
// later is a field an older peer ignores rather than a body that stops
// decoding.

// Working names the working copy a request is about. A run belongs to a
// repository, and which repository is a fact about where the caller is
// standing rather than something the service can infer, so every request that
// is about a repository carries one.
type Working struct {
	// WorkingPath is the absolute root of the working copy, which is what a
	// gate is filed under.
	WorkingPath string `json:"working_path"`
}

// StatusRequest asks what assistant status reports.
type StatusRequest struct {
	Working
}

// RunsRequest asks for recent runs of one repository, newest first.
type RunsRequest struct {
	Working
	// Limit bounds how many are returned, newest first. Zero means every run
	// the repository has.
	Limit int `json:"limit,omitempty"`
}

// RunRequest names one run.
type RunRequest struct {
	// Run is the run's identifier.
	Run string `json:"run"`
}

// StartRequest starts a run for the branch checked out in a working copy, and
// blocks while it advances that run, answering at its next decision point or a
// terminal outcome. A run the service is already advancing is reported where
// it stands rather than advanced twice, so the answer can be OutcomeExecuting.
type StartRequest struct {
	Working
	// Intent is what the change sets out to do, in the caller's own terms.
	//
	// PRD section 9 asks a driving agent for the goal, the decisions and
	// tradeoffs behind it, and the approaches ruled out. A one-line summary of
	// the diff makes the reviewer flag choices that were made deliberately,
	// which is the fastest way to make the gate feel like an obstacle.
	Intent string `json:"intent,omitempty"`
	// IntentSupplied says the intent above was supplied rather than inferred,
	// which makes it authoritative acceptance criteria downstream. A run may
	// not claim it with nothing behind it.
	IntentSupplied bool `json:"intent_supplied,omitempty"`
	// Skip names stages this one run does not take, by stage name. It is per
	// run on purpose: P2 lets a person skip a stage deliberately for one run
	// and never lets a standing configuration skip one on their behalf.
	Skip []string `json:"skip,omitempty"`
}

// RerunRequest starts a fresh run of the branch the working copy is standing
// on, from that branch's last known head, inheriting the intent recorded
// there, and blocks while it advances that run, answering at its next decision
// point or a terminal outcome. It has no attach to fall back on: a branch
// whose run is still in flight is refused rather than reported, so a rerun
// never answers with a run still executing.
//
// The branch is read from the working copy this names, the same way a start
// reads it, so a rerun never acts on a branch the caller is not on. A branch
// with no run of its own is refused by that branch's name rather than answered
// with another branch's run.
type RerunRequest struct {
	Working
}

// RespondRequest answers the decision a run is holding on, and blocks until
// the run reaches its next decision point or a terminal outcome.
type RespondRequest struct {
	// Run is the run holding.
	Run string `json:"run"`
	// Answer is one of the options the decision offered. An answer outside
	// them is refused by internal/graph rather than interpreted here.
	Answer string `json:"answer"`
	// Fix is what a fix round is to resolve, and it travels with the
	// pipeline.OutcomeFixRequested answer and no other: PRD section 5's
	// selection, notes, and the person's own findings. The record is
	// internal/pipeline's, so this composes it rather than restating it.
	//
	// It carries no field saying who asked. Who resolved a hold is derived
	// from the surface, never read from a request, which internal/ipc's
	// Request.HoldResolver states.
	Fix *pipeline.FixRequest `json:"fix,omitempty"`
}

// TaskRespondRequest answers a hold a task raised with no run behind it,
// which PRD section 9 has answered the same way a run's hold is, given the
// task rather than a run: a task past relaunch_attempts, a relaunch refused
// because the platform has no process sweep, or a decision an
// investigation's report registered. The hold answered is the oldest one
// open that names the task and no run; the answer names which.
//
// It carries no field saying who answered, on RespondRequest's terms: who
// resolved a hold is derived from the surface by internal/ipc's
// Request.HoldResolver and never read from a request.
type TaskRespondRequest struct {
	// Task is the task whose hold is answered.
	Task string `json:"task"`
	// Answer is what was decided. A relaunch hold, the one the relaunch
	// bound raised or the one a relaunch with no process sweep raised,
	// offers exactly AnswerRetry and AnswerStop and refuses anything else; a
	// local task's approval hold offers exactly AnswerApprove and AnswerStop;
	// a hold an investigation's report registered offers no closed set, and
	// the answer is recorded as given.
	Answer string `json:"answer"`
	// Note is what a retry on a stop's hold tells the relaunched worker,
	// after the relaunch note and before the text of every message the
	// stopped attempt never acknowledged, PRD section 6's "optionally with
	// a note". It is accepted only with a retry on a stop's hold, the one
	// answer that starts a worker with it, and refused as an invalid request
	// with any other answer or hold rather than dropped.
	Note string `json:"note,omitempty"`
}

// The answers a relaunch hold offers.
const (
	// AnswerRetry relaunches the task once more: past relaunch_attempts, or
	// with nothing proving its copy unowned where there is no process sweep.
	AnswerRetry = "retry"
	// AnswerStop leaves the task where it stopped; nothing relaunches it.
	// On a local task's approval hold it declines the change: nothing is
	// moved and the task stays where it is.
	AnswerStop = "stop"
	// AnswerApprove answers a local task's approval hold: the program
	// fast-forwards the local default branch to the commit the worker
	// pushed, anchored to the tip it read when it offered the change.
	AnswerApprove = "approve"
)

// TaskMessageRequest sends a message to a running task's worker, PRD
// section 6's steering. It carries no field saying who sent it: who sent a
// message is derived from the surface by internal/ipc's
// Request.MessageSender and never read from a request.
type TaskMessageRequest struct {
	// Task is the task whose current worker the message is for: the latest
	// attempt's, which the service reads and the caller does not name.
	Task string `json:"task"`
	// Text is what the worker is sent, at most MaxMessageBytes and refused
	// rather than truncated above it, and never empty.
	Text string `json:"text"`
}

// MaxMessageBytes is the fixed limit on a message's text, in bytes, PRD
// section 6's "bounded by a fixed limit in the program". It is not a
// setting. The same limit bounds a stop's retry note, which reaches the
// same worker input.
const MaxMessageBytes = 16 << 10

// CancelRequest ends a run.
type CancelRequest struct {
	// Run is the run to end.
	Run string `json:"run"`
}

// TaskRequest names one piece of fleet work.
type TaskRequest struct {
	// Task is the task's identifier.
	Task string `json:"task"`
}

// LifecycleRequest stops or restarts the service.
//
// PRD section 9 makes both refuse while runs are active, and requires an
// explicit flag rather than a general yes-to-everything one, which is why this
// field names the act rather than agreement in general.
type LifecycleRequest struct {
	// Force carries the caller past the refusal that active runs produce.
	Force bool `json:"force,omitempty"`
}

// SubscribeRequest opens the event stream. It carries nothing today and is a
// struct so that it can carry something later without a peer having to change.
type SubscribeRequest struct{}

// GateRequest is what a gate's hooks carry: which gate the push is arriving
// at, and the reference updates git is about to make or has just made.
//
// Both hooks carry the same two things, so they carry one shape. Which of them
// is asking is the method, and what that decides is on the service's side
// rather than in a field a caller writes.
//
// The updates are the caller's account of the push, and nothing establishes
// them. Admission runs before any reference in the gate changes, so at the
// moment the decision is made there is nothing in the gate to check them
// against, and after it there is no reading that would say what the push
// described rather than what it did.
//
// What that costs is bounded rather than closed, and the bound is worth
// stating exactly. The socket is one home's, so a caller reaching this is a
// caller that can already start a run through StartRequest; an invented
// account buys it a run it could have asked for. Both gate methods are
// declared restricted, which is where containment refuses a caller inside an
// active validation stage. A task's worker is the one pusher whose account
// is checked against something: its push is held to its own branch, forward,
// with the standing object read from the gate rather than taken from the
// account, which internal/service's admission owns.
type GateRequest struct {
	// Gate is the gate's identifier, which is what a hook is given and all it
	// knows. Which working copy it belongs to is the service's to resolve,
	// through internal/gate, so that a caller cannot name a repository.
	Gate string `json:"gate"`
	// Updates are the reference updates the push carries, as
	// [gate.ParseRefUpdates] read them. They are that package's shape rather
	// than a second one here, because it owns the hook protocol they arrive
	// on.
	Updates []gate.RefUpdate `json:"updates,omitempty"`
}

// StageReportRequest returns a stage's result from the agent running it. It is
// the one request a caller contained by an active validation stage may make,
// because returning its own stage is what such a caller is there to do.
type StageReportRequest struct {
	// Run is the run whose stage is being returned.
	Run string `json:"run"`
	// Stage is the stage being returned.
	Stage string `json:"stage"`
	// Report is the stage's result, as the stage produced it.
	Report string `json:"report"`
}

// TaskEventRequest appends one entry to a task's history from its worker.
//
// It names no task. The task is the one whose worker group the caller's
// credentials place it in, derived by the service from the kernel's answer
// and never from a field, so a worker cannot write to another task's history
// and a caller outside every worker group is refused. The kind is held to
// internal/taskstate's table: only a kind that table gives a worker as its
// writer is accepted, so "dispatched" and every other program-written kind
// is refused from this surface.
type TaskEventRequest struct {
	// Kind is what happened, in internal/taskstate's vocabulary.
	Kind string `json:"kind"`
	// Detail is what the worker says about it. A kind with a closed detail
	// vocabulary is checked against it before anything is appended.
	Detail string `json:"detail,omitempty"`
}

// TaskCreateRequest records a new task. It is internal/tasks' Request on
// the wire, and that type owns what each field means and which combinations
// intake refuses; the coordinator's judgment is the shape, the
// instructions and the overlap claim, and everything the program decides
// from them is intake's.
//
// It carries nothing about who is asking. The service decides that from the
// caller's credentials: a caller inside a task's worker group is refused
// before intake reads anything.
type TaskCreateRequest struct {
	// Shape is "delivery" or "investigation".
	Shape string `json:"shape"`
	// Project is what the task is for.
	Project string `json:"project"`
	// Instructions is what the worker is launched with.
	Instructions string `json:"instructions"`
	// Mode is how the worker is to be launched, recorded as given.
	Mode string `json:"mode,omitempty"`
	// Repository names the repository the task works in, by its record's
	// identifier.
	Repository string `json:"repository,omitempty"`
	// Branch is the branch a delivery task works on.
	Branch string `json:"branch,omitempty"`
	// Overlaps names a task this one overlaps, or is empty for a claim of
	// independence.
	Overlaps string `json:"overlaps,omitempty"`
	// Delivery is a delivery posture the coordinator asks to record in place
	// of the project's, "gated" or "local", or empty to record the project's
	// own. It may be stricter than the project's and never lighter, which
	// intake refuses (PRD section 6's "tighten, never loosen"). It is
	// refused on an investigation task, which has no posture.
	Delivery string `json:"delivery,omitempty"`
}

// TaskPromoteRequest promotes an investigation task to delivery in place.
// It is internal/tasks' Promotion on the wire less the environment, which
// is the service's: the environment a worker runs under is the one the
// service launches workers with, and a field for it would be the caller's
// claim about the assertion it is held to.
//
// It names the task to promote and nothing about who is asking; the service
// refuses a caller inside any task's worker group, so an investigation's own
// worker cannot promote it.
type TaskPromoteRequest struct {
	// Task is the investigation task to promote.
	Task string `json:"task"`
	// Repository names the repository the delivery works in, which must be
	// the one the task's copy was cut from.
	Repository string `json:"repository"`
	// Branch is the branch the delivery works on.
	Branch string `json:"branch"`
	// Intent is what the delivery is to do.
	Intent string `json:"intent"`
}

// WakesRequest drains the durable wake queue.
type WakesRequest struct {
	// Limit bounds how many queued wakes are returned, oldest first. Zero
	// means DefaultWakesLimit.
	Limit int `json:"limit,omitempty"`
}

// DefaultWakesLimit is how many queued wakes a drain returns when the caller
// names no bound. A drain is read before every coordinator turn, so the
// bound keeps one answer inside a frame rather than paging the queue.
const DefaultWakesLimit = 100

// AcknowledgeRequest records that the coordinator handled queued wakes.
//
// It carries no field saying who acknowledged. Who did is derived from the
// surface, never read from a request, which internal/ipc's
// Request.WakeAcknowledger states on the same terms as Request.HoldResolver.
type AcknowledgeRequest struct {
	// Wakes are the identifiers of the rows a drain returned and the caller
	// handled. A subset leaves the rest queued; an identifier no wake has,
	// or one already acknowledged, refuses the whole batch.
	Wakes []int64 `json:"wakes"`
}

// MarkSeenRequest moves the seen mark PRD section 9's catching up reads. It
// names the landed records a read showed, which CatchUp.Shown returns, and
// nothing else: the mark moves only on an explicit mark of what a read
// showed, so a record recorded between that read and this request is not
// in it and is still shown next time. A request naming a record that is not
// a landed one refuses the whole batch.
type MarkSeenRequest struct {
	// Records are the landed records the read showed.
	Records []store.SeenRecord `json:"records"`
}
