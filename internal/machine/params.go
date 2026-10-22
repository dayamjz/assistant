package machine

import (
	"fmt"
	"strconv"
	"time"

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

// SyncRequest asks assistant sync's question of the branch the working copy
// has checked out: classify it, and with Confirm carry out the steps its
// class allows.
type SyncRequest struct {
	Working
	// Confirm carries the plan out. Without it nothing is written anywhere,
	// and the answer is the plan a person confirms.
	Confirm bool `json:"confirm,omitempty"`
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
	// StandingConsent asks for the run this call starts or attaches to to be
	// put under the standing consent PRD section 9 describes, which the
	// service refuses as an invalid request when the repository's trusted
	// copy does not allow it. Once given it stays given for that run.
	//
	// It asks for consent and says nothing about who answers anything: every
	// answer the consent then gives is recorded as the program's, which
	// internal/service decides in source, so no value here can make a record
	// say a person or a caller decided.
	StandingConsent bool `json:"standing_consent,omitempty"`
	// Task selects the run through a task instead of a working copy, for
	// standing consent alone: the run selected is the task's current run, on
	// RespondRequest.Task's terms, and the call attaches to it with
	// StandingConsent and never starts one. It is how a coordinator puts a
	// task's run under consent without checking the task's branch out. A
	// request naming a task is refused as invalid unless it asks for
	// standing consent and carries no working copy and none of the inputs
	// that start a run, because a run of a task is started by its push.
	Task string `json:"task,omitempty"`
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
// with another branch's run. A rerun validates the branch's last known head:
// the commit its last run pushed, or the commit that run was submitted with
// when it pushed nothing. A working copy whose HEAD is another commit is
// refused with RerunMoved, and no run begins.
//
// Intent replaces the inherited intent when it is not empty. The fresh run
// then records Intent as supplied, the standing an intent given to a start
// with nothing said about it takes, so the intent stage reports it as
// acceptance criteria the way it reports any supplied intent. Nothing else a
// rerun inherits changes with it: the branch, the head it validates and the
// refusal when the working copy stands elsewhere are the same with or without
// it. An intent of whitespace alone is refused as an invalid request rather
// than read as no replacement, because a caller who wrote one asked for a
// replacement and nothing would be honouring that.
type RerunRequest struct {
	Working
	// Intent is the intent that replaces the one the last run recorded,
	// empty to inherit that one.
	Intent string `json:"intent,omitempty"`
}

// RerunMoved is a rerun refused because the working copy it names stands at a
// commit other than the one the rerun would validate: Local is the commit the
// working copy's HEAD resolved to, Recorded is the branch's last known head,
// which is what a rerun validates, Branch is the branch both are
// about, and Reason says which of the two remedies applies. A rerun refused
// this way began no run and recorded nothing, and reading HEAD changed nothing
// in the working copy.
//
// It is a refusal rather than an answer, so it travels as an error: its
// message is the row Phrase chooses, followed by both commits and the
// commands for the remedy. The commits and the commands are machine text a
// person copies, so they are beside the row rather than in it.
type RerunMoved struct {
	// Branch is the branch the working copy is on.
	Branch string `json:"branch"`
	// Local is the commit the working copy stands at.
	Local string `json:"local"`
	// Recorded is the commit a rerun of Branch would validate.
	Recorded string `json:"recorded"`
	// Reason is why the two differ, as far as the refusing side established
	// it.
	Reason RerunMovedReason `json:"reason"`
}

// RerunMovedReason is which way a refused rerun's two commits differ, and so
// which remedy its refusal names.
type RerunMovedReason string

const (
	// RerunMovedElsewhere is every difference not established as
	// RerunMovedBehind: Local is ahead of Recorded, the two have diverged,
	// Recorded is the commit that was submitted rather than the run's own
	// work, or the ancestry could not be read. The remedy validates Local:
	// a push to the gate's remote, or a start with an intent.
	RerunMovedElsewhere RerunMovedReason = "elsewhere"
	// RerunMovedBehind is Local established as an ancestor of Recorded where
	// Recorded is work the run pushed past the commit it was submitted.
	// The remedy is assistant sync, which reconciles the branch with what the
	// pipeline holds: it prints its plan and changes nothing until it is
	// repeated with --confirm.
	RerunMovedBehind RerunMovedReason = "behind"
)

// Phrase is the row for this refusal's reason. A reason outside the two is
// phrased as RerunMovedElsewhere, whose remedy validates the local commit and
// assumes nothing about how the two are related.
func (m RerunMoved) Phrase() Phrase {
	if m.Reason == RerunMovedBehind {
		return phraseRerunBehind
	}
	return phraseRerunMoved
}

// Error is the refusal as a person reads it: the row, then the commit they
// are on, the commit a rerun would validate, and the commands for the remedy
// the row names.
func (m RerunMoved) Error() string {
	head := fmt.Sprintf("%s %s %s. %s %s.", m.Phrase(), rerunLocalLabel, m.Local, rerunRecordedLabel, m.Recorded)
	if m.Reason == RerunMovedBehind {
		return fmt.Sprintf("%s %s assistant sync prints the plan, and assistant sync --confirm carries it out.",
			head, rerunCatchUpLabel)
	}
	return fmt.Sprintf("%s %s git push %s %s, or assistant --intent followed by what it sets out to do.",
		head, rerunRemedyLabel, gate.RemoteName, m.Branch)
}

// The labels RerunMoved puts before the machine text it carries beside its
// row.
const (
	rerunLocalLabel    = "The commit you are on:"
	rerunRecordedLabel = "The commit a fresh run would validate:"
	rerunRemedyLabel   = "To validate yours:"
	rerunCatchUpLabel  = "To catch up:"
)

// RespondRequest answers the decision a run is holding on, and blocks until
// the run reaches its next decision point or a terminal outcome.
//
// The run is named directly, or selected through the task that produced it,
// PRD section 9's answer given a task: exactly one of Run and Task is set.
type RespondRequest struct {
	// Run is the run holding, and empty when Task selects it.
	Run string `json:"run"`
	// Task selects the run through a task, and is empty when Run names it.
	// The run selected is the task's current run: the newest unfinished run
	// of the task's branch in the task's repository, which is the run
	// --answer reaches from a checkout of that branch. Without TargetRun, a
	// task with a hold of its own open is refused rather than having its
	// run answered past that hold; with TargetRun set, the run is answered
	// while those holds stay open. internal/service's taskrun.go owns both
	// rules.
	Task string `json:"task,omitempty"`
	// TargetRun, with Task set, answers the task's current run even while
	// the task has a hold of its own open. It is how a coordinator reaches
	// a promoted delivery run without first clearing every report hold.
	TargetRun bool `json:"target_run,omitempty"`
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
// investigation's report registered. Without Hold, the hold answered is the
// only one open that names the task and no run; when more than one is open
// the answer is refused and lists them. With Hold set, that hold is named
// (full key, or a finding id resolved under the task).
//
// It carries no field saying who answered, on RespondRequest's terms: who
// resolved a hold is derived from the surface by internal/ipc's
// Request.HoldResolver and never read from a request.
type TaskRespondRequest struct {
	// Task is the task whose hold is answered. It is empty when Domain is
	// set, and one of the two is required.
	Task string `json:"task,omitempty"`
	// Domain is the domain whose hold is answered instead of a task's: the
	// one the relaunch bound raises for a delegation whose subordinate
	// coordinator's terminal is gone, PRD section 11's "past the bound it
	// holds for you". It offers AnswerRetry, which begins the subordinate
	// once more, and AnswerStop, which ends the delegation; Hold is not
	// read with it, since a delegation holds on one key at a time.
	Domain string `json:"domain,omitempty"`
	// Hold names which open hold to close: the hold's full key, or a
	// finding id that resolves to task/<Task>/finding/<id>. Empty answers
	// the only open hold naming the task and no run, and is refused when
	// more than one is open.
	Hold string `json:"hold,omitempty"`
	// Answer is what was decided. A relaunch hold, the one the relaunch
	// bound raised or the one a relaunch with no process sweep raised,
	// offers exactly AnswerRetry and AnswerStop and refuses anything else; a
	// local task's approval hold offers exactly AnswerApprove and AnswerStop;
	// a hold an investigation's report registered offers no closed set, and
	// the answer is recorded as given.
	Answer string `json:"answer"`
	// Note is free text recorded with the resolution about what was
	// decided, and on a retry of a stop's hold it is also what the
	// relaunched worker is told, after the relaunch note and before the
	// text of every message the stopped attempt never acknowledged. Who
	// decided stays Request.HoldResolver; the note is not that claim.
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

// CancelRequest ends a run. A run no record names, and a run that has already
// ended, are refused with CancelRefused rather than answered, so a caller who
// named the wrong run is told so instead of being handed a run as it stands.
type CancelRequest struct {
	// Run is the run to end.
	Run string `json:"run"`
}

// CancelRefused is a cancellation refused because there was nothing to end:
// Run is the identifier the caller named, Reason says whether no record
// carries it or the run it names has already ended, and Status is the status
// an ended run was recorded in, empty when no record was found. A refused
// cancellation changed nothing.
//
// It is a refusal rather than an answer, so it travels as an error, and
// internal/service sends it as an invalid request: the caller named something
// that cannot be ended, which is the caller's to correct. Its message is the
// row Phrase chooses followed by the identifier and the status, which are
// machine text a person copies and so stand beside the row rather than in it.
type CancelRefused struct {
	// Run is the identifier the caller named.
	Run string `json:"run"`
	// Reason is why there was nothing to end.
	Reason CancelRefusedReason `json:"reason"`
	// Status is the status a run that had already ended was recorded in,
	// empty for a run no record carries.
	Status store.RunStatus `json:"status,omitempty"`
}

// CancelRefusedReason is why a cancellation found nothing to end.
type CancelRefusedReason string

const (
	// CancelUnknownRun is an identifier no run in the home carries.
	CancelUnknownRun CancelRefusedReason = "unknown"
	// CancelRunEnded is a run whose record already says it ended, whichever
	// way it ended.
	CancelRunEnded CancelRefusedReason = "ended"
)

// Phrase is the row for this refusal's reason. A reason outside the two is
// phrased as CancelUnknownRun, whose remedy is to check the identifier and
// which claims nothing about a run that may not exist.
func (c CancelRefused) Phrase() Phrase {
	if c.Reason == CancelRunEnded {
		return phraseCancelEnded
	}
	return phraseCancelUnknown
}

// Error is the refusal as a person reads it: the row, then the identifier
// named and, for a run that had ended, the status it was recorded in.
func (c CancelRefused) Error() string {
	text := fmt.Sprintf("%s %s %s.", c.Phrase(), cancelRunLabel, c.Run)
	if c.Status != "" {
		text = fmt.Sprintf("%s %s %s.", text, cancelStatusLabel, c.Status)
	}
	return text
}

// The labels CancelRefused puts before the machine text it carries beside its
// row.
const (
	cancelRunLabel    = "The run you named:"
	cancelStatusLabel = "Its recorded status:"
)

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
	// Role is the configured role the worker runs under. Empty means the
	// worker role. An unknown role, and review or fix, are refused and no
	// task is created.
	Role string `json:"role,omitempty"`
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
	// Delivery is a delivery posture the coordinator asks to record in place
	// of the project's, "gated" or "local", or empty to record the project's
	// own, read from its trusted copy as intake reads it. It may be stricter
	// than the project's and never lighter, which promotion refuses on
	// TaskCreateRequest.Delivery's terms.
	Delivery string `json:"delivery,omitempty"`
}

// WakesRequest drains the durable wake queue: the queued wakes on offer are
// handed out and stay queued until they are acknowledged, PRD section 8's
// "draining hands a row out; acknowledging closes it". It is also the
// request of a person's reading of the queue, which hands out nothing.
type WakesRequest struct {
	// Limit bounds how many queued wakes are returned, oldest first. Zero
	// means DefaultWakesLimit.
	Limit int `json:"limit,omitempty"`
}

// DefaultWakesLimit is how many queued wakes a drain returns when the caller
// names no bound. A drain is made before every coordinator turn, so the
// bound keeps one answer inside a frame; a row past it stays queued for the
// next drain.
const DefaultWakesLimit = 100

// WakeWaitRequest arms the blocking wait on the durable wake queue. The
// wait answers as soon as the queue holds at least one wake on offer, with a
// drain on WakesRequest's terms, so the rows it returns are handed out and
// stay queued until they are acknowledged, or once its bound elapses with
// none; the answer is Wakes either way, so an empty answer is the bound
// elapsing and nothing else.
type WakeWaitRequest struct {
	// TimeoutSeconds is how long the wait may block, in whole seconds,
	// from MinWakeWaitTimeout to MaxWakeWaitTimeout. Zero means
	// DefaultWakeWaitTimeout; anything else outside the range is refused
	// by Bounds, never clamped.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// Limit bounds how many queued wakes the answer carries, on
	// WakesRequest.Limit's terms: zero means DefaultWakesLimit, and a
	// negative limit is refused by Bounds.
	Limit int `json:"limit,omitempty"`
}

// The wait's bound. The range and the default are chosen, not measured.
// The lower end is the smallest bound worth a round trip, and the upper end
// is what keeps one call from holding a request slot on its connection
// without limit, which is the cost a wait has for the service. A caller
// whose host ends a shell command sooner than the default passes a smaller
// bound; nothing here knows any host's limit.
const (
	// MinWakeWaitTimeout is the shortest bound a wait accepts.
	MinWakeWaitTimeout = time.Second
	// MaxWakeWaitTimeout is the longest bound a wait accepts.
	MaxWakeWaitTimeout = time.Hour
	// DefaultWakeWaitTimeout is the bound of a wait that names none.
	DefaultWakeWaitTimeout = 5 * time.Minute
)

// Bounds is how long the wait blocks and how many wakes its answer may
// carry, with each zero replaced by its default, and the one owner of which
// requests are in range: the command line checks it before asking, and the
// service checks it again on arrival, so a request either side refuses is
// refused by the same rule. A bound outside the range, a bound too large to
// be a duration among them, and a negative limit are an error naming what
// was wrong; nothing is clamped.
func (r WakeWaitRequest) Bounds() (time.Duration, int, error) {
	if r.Limit < 0 {
		return 0, 0, fmt.Errorf("a wait's limit is %d, and a limit is never negative", r.Limit)
	}
	limit := r.Limit
	if limit == 0 {
		limit = DefaultWakesLimit
	}
	if r.TimeoutSeconds == 0 {
		return DefaultWakeWaitTimeout, limit, nil
	}
	// The seconds are compared before they are multiplied, so a count too
	// large for a duration is refused rather than wrapped into the range.
	if r.TimeoutSeconds < int(MinWakeWaitTimeout/time.Second) || r.TimeoutSeconds > int(MaxWakeWaitTimeout/time.Second) {
		return 0, 0, fmt.Errorf("a wait's bound is %d second(s), and it must be from %s to %s",
			r.TimeoutSeconds, MinWakeWaitTimeout, MaxWakeWaitTimeout)
	}
	return time.Duration(r.TimeoutSeconds) * time.Second, limit, nil
}

// AcknowledgeRequest records that the coordinator acted on queued wakes,
// which is what closes them: rows a drain handed it, or rows it learned of
// from the wake stream or a task's history.
//
// It carries no field saying who acknowledged. Who did is derived from the
// surface, never read from a request, which internal/ipc's
// Request.WakeAcknowledger states on the same terms as Request.HoldResolver.
type AcknowledgeRequest struct {
	// Wakes are the identifiers of the queued rows the caller acted on,
	// handed out or not. A subset leaves the rest queued; an identifier no
	// wake has, or one already acknowledged, refuses the whole batch.
	Wakes []int64 `json:"wakes"`
	// DomainWakes are the identifiers of the queued rows about a domain the
	// caller acted on, numbered apart from Wakes, on the same terms. A
	// subordinate coordinator may name none, since no such row is its.
	DomainWakes []int64 `json:"domain_wakes,omitempty"`
	// KeptWorkWakes are the identifiers of the queued rows about a kept
	// work entry the caller acted on, numbered apart from both, on the same
	// terms. A subordinate coordinator may name none, since no such row is
	// its. Acknowledging one closes the wake and answers nothing about the
	// entry.
	KeptWorkWakes []int64 `json:"kept_work_wakes,omitempty"`
}

// The prefixes a wake of the queue's second and third shapes is named with
// on the acknowledge command line, before its number. Each shape numbers its
// rows apart from the others, so a task wake, a domain wake and a kept-work
// wake can share a number: a bare number is always a task wake, one with
// DomainWakePrefix always a domain wake, and one with KeptWorkWakePrefix
// always a wake about a kept work entry no task names. A task's own wake
// about work kept in its copy is a task wake and takes no prefix.
// AcknowledgeNames writes them and internal/cli's acknowledge command reads
// them, so the two cannot drift.
const (
	DomainWakePrefix   = "d"
	KeptWorkWakePrefix = "k"
)

// AcknowledgeNames is every row of the drain w, named as the acknowledge
// command line takes it: the task wakes by their numbers, then the domain
// wakes and the kept-work wakes with their shape's prefix, each shape
// oldest first, as w holds them.
func AcknowledgeNames(w Wakes) []string {
	names := make([]string, 0, len(w.Wakes)+len(w.DomainWakes)+len(w.KeptWorkWakes))
	for _, row := range w.Wakes {
		names = append(names, strconv.FormatInt(row.ID, 10))
	}
	for _, row := range w.DomainWakes {
		names = append(names, DomainWakePrefix+strconv.FormatInt(row.ID, 10))
	}
	for _, row := range w.KeptWorkWakes {
		names = append(names, KeptWorkWakePrefix+strconv.FormatInt(row.ID, 10))
	}
	return names
}

// DomainDelegateRequest delegates a named domain of tasks to a subordinate
// coordinator the service starts, PRD section 9's domain.delegate. It
// carries nothing about who is asking: the service refuses a caller placed
// as a task's worker or as a domain's subordinate from the caller's
// credentials, before anything is read.
type DomainDelegateRequest struct {
	// Domain is the domain's name, as internal/home's ValidDomain accepts
	// it: one to thirty-two lower-case letters, digits and inner dashes.
	Domain string `json:"domain"`
	// Delivery is the delivery posture every task the subordinate creates
	// in the domain takes, "gated" or "local". It is refused empty, because
	// a subordinate records no posture of its own and the delegation is the
	// one place the domain's is written; at each task it is held to
	// intake's rule against that task's project, never lighter.
	Delivery string `json:"delivery"`
	// Instructions is what the subordinate coordinator is started with:
	// what the domain is for and what the coordinator wants of it. The
	// service appends what the subordinate may run and how it is placed.
	Instructions string `json:"instructions"`
}

// DomainEndRequest ends a domain's delegation, PRD section 9's domain.end:
// the delegation record's one revision, after which the domain's wakes
// reach the coordinator again and its subordinate's session is ended.
type DomainEndRequest struct {
	// Domain is the domain whose standing delegation ends.
	Domain string `json:"domain"`
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

// UsageRequest asks for PRD section 9's usage view over a window. Window is
// read by ParseUsageWindow, so empty is DefaultUsageWindow and a window it
// cannot read is refused as an invalid request rather than read as the
// default.
type UsageRequest struct {
	// Window is how far back the read reaches, such as 24h or 7d.
	Window string `json:"window,omitempty"`
}

// StageLogRequest names one stage of one run, whose per-stage log the
// run.log method answers with a bounded tail of. Stage is one of the nine
// stage names; a fix round is logged under the stage whose findings it fixes,
// so it has no name of its own here.
type StageLogRequest struct {
	// Run is the run's identifier.
	Run string `json:"run"`
	// Stage is the stage's name.
	Stage string `json:"stage"`
}
