package machine

import "github.com/dayamjz/assistant/internal/gate"

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
}

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
// declared restricted, which is where containment would refuse a caller inside
// an active validation stage, but internal/service/doc.go says that registry
// has no producer in this build, so today that declaration refuses nobody.
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
