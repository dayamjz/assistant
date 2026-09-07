package machine

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
// blocks until that run reaches its next decision point or a terminal outcome.
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
// there, and blocks on the same terms as a start.
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
