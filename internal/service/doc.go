// Package service is the background service PRD section 8's process model puts
// at the centre of a home: the one process that holds the home's exclusive
// lock, binds the local socket, owns every run that is executing, and answers
// the protocol internal/ipc defines.
//
// It is the wiring between packages that already own everything it does.
// internal/graph executes and bounds, internal/pipeline is the topology,
// internal/checkpoints makes a run's position durable, internal/runs owns the
// run's record and its fixer session, internal/store owns every record,
// internal/ipc owns the protocol, internal/machine owns the shapes an answer
// takes, and internal/home owns the layout and the lock. Nothing here decides
// whether a change is good, whether a branch may move, or what a finding
// means; a decision made here would be a second opinion on a question one of
// those packages already answers.
//
// # One service owns a home, and the lock is what makes that true
//
// Open takes the home's lock before it opens the database and before it binds
// the socket, per PRD section 8, so a second service on one home refuses
// rather than serving a second view of the same runs. The lock is released by
// the operating system when the process ends however it ends, so there is no
// stale state to reason about after a hard kill; internal/home says why that
// matters.
//
// A socket file left behind by a service that was killed is removed before
// binding. That is safe only because the lock is already held: nothing else
// can be serving on it, so the file names nothing.
//
// # Recovery is what a restart owes a run
//
// A run's position is durable, so a service that comes back has runs standing
// where the last one left them. Open reconciles every unfinished run's record
// against its latest checkpoint, and then continues the ones that were
// executing. Reconciling is not optional: a run whose record says running and
// whose checkpoint says halted is a run waiting on an answer nobody can give,
// because responding is refused for a run that is not held.
//
// # Blocking calls, and the one slot per run that makes them safe
//
// PRD section 9 has starting and responding block until the run reaches its
// next decision point or a terminal outcome. A call that advances the run does
// exactly that, and the answer is the run as it then stands.
//
// One run advances at a time here. A second request to advance a run already
// advancing is refused naming the first rather than queued, because
// internal/graph's anchor would refuse the loser's write anyway and refusing
// early is a better answer than a segment that ran a node before finding out.
// That is a bound on this service and not a distributed one: internal/graph's
// anchoring is what holds when two processes drive one run, and the home lock
// is what makes that not happen.
//
// The bare start is where that refusal becomes an answer instead. Attaching
// asks where a run stands, and a run this service is already advancing stands
// somewhere, so it is reported rather than refused and the call returns while
// the run is still moving. That is the one blocking call whose answer can be a
// run still executing; responding meets the refusal, because answering a
// decision is not a question about where a run stands.
//
// # Whether anything is advancing a run is this service's fact, and is reported
//
// A segment can stop without settling the record: the step failed, or the
// caller waiting on it gave up, which ends the segment on purpose so that a
// client that walked away does not leave a run walking nodes nobody is waiting
// for. What that leaves is a record saying running against a checkpoint saying
// running, at a position something can resume from - which is also exactly
// what a run being walked this instant looks like, and exactly what a service
// killed mid-segment leaves behind.
//
// The two are told apart by the slot, which is the only owner of the fact and
// holds it for the length of one segment and nowhere durable. It does not stay
// here: report hands it to machine.Run.Decide with the run's record and where
// its execution stands, and that one call decides the outcome, the advancing
// fact and the next action together. Nothing in this package chooses an
// action, and nothing here can: the field is unexported in internal/machine
// and Decide is the only writer, so a branch added to report cannot hand back
// an action that disagrees with the outcome beside it. PRD section 9 requires
// that of the terminal interface, and says why: a stall that looks alive is
// worse than an error.
//
// A run with no checkpoint goes through the same call and not a branch of its
// own. It has no position, which is an absence rather than a place execution
// stopped, so it travels as machine.ExecutionUnrecorded rather than as a
// status standing in for one - which is how a run between its start and its
// first checkpoint used to read as a run that failed while the same answer
// said a segment was inside it.
//
// What to do about a run that is genuinely stranded there - recorded
// unfinished, no position, nothing advancing it - is a separate question this
// package does not answer yet. The answer says so and tells a reader to end it
// and start again; deciding whether something should classify such a run at
// startup is work of its own, and it inherits this vocabulary rather than
// inventing a second one: machine.Standing is the three facts, and
// machine.Execution is where the absence lives.
//
// Whether that state persists is decided by what ended the segment, and
// carryOn is where. A context ended it means nothing about the run went wrong
// and the caller who would have been told is gone, so this service picks the
// run up itself - the same thing recovery does for a run a restart
// interrupted, done where the run would otherwise be stranded. A step that
// failed is a failure the caller was told about, in the error the call
// returned, and repeating it would be a loop rather than progress: that run
// stands where it is and the read above says what carries it on. The service
// stopping is a context ending that carries nothing, because recovery
// continues every unfinished run on the next open and starting work here would
// be starting work the service is giving up.
//
// No status is written over such a run, and that is deliberate rather than
// unfinished. Its record is not wrong - the run is unfinished - and the two
// statuses that would end it are answers to different questions:
// internal/runs' Fail is a verdict on the change rather than a failure of the
// service, and Terminate is what a cancellation or a supersession leaves. A
// run standing at a resumable position is neither, and ending it would discard
// work its checkpoint history still holds, which P6 forbids.
//
// # The record and the execution disagreeing, in both directions
//
// The stall above is a record saying running with nothing executing. Its
// mirror is a record saying terminated with something executing still, which
// is what a run ended through the protocol would leave if a continuation
// picked it up behind the caller who was told it was over. Ending a run
// cancels its segment, so that segment ends the way a lost caller's does and
// would otherwise be carried on for the same reason.
//
// What is structural and what is not are named apart here, because describing
// the second as the first is what let this ship. The segment an ending cancels
// is never followed by a continuation: the ending is recorded in the run's
// slot under the one mutex claim and release also take, so that segment's
// release reports it, and a run with no segment under its slot has the ending
// stand in that slot while it is written. That is a bound on the interleaving
// rather than a read that could be stale, and it is what carryOn's refusal
// rests on.
//
// It bounds that segment and no other, and what it leaves open is not nothing.
// endRun writes that down and is the one owner of it, so nothing here or at
// cancel restates any part of it: a reader who needs the list reads endRun.
//
// A continuation cannot cause another, and that is structural too. It runs
// under this service's own context, so the only contexts that can end its
// segment are the service stopping and an ending through the protocol, and
// carryOn refuses both. What is left is one continuation per caller that
// walked away.
//
// Whether a continuation begins at all is ordered against Close rather than
// decided beside it. carryOn reads the stop to classify an ending, and that
// read races nothing into place; the registration does, because startWork adds
// to the set Close waits for under the same mutex stopWork closes that set
// with. A continuation decided as a stop arrives either registers before Close
// stops taking work, and is waited for, or is refused - never registered after
// the wait, against a database Close has gone on to shut.
//
// # A push is the one caller that does not wait
//
// The gate's hooks reach this service through gate.admit and gate.notify, and
// they are the exception to the blocking calls above. PRD section 8 has a push
// return immediately, with the notification handing off and this service
// owning everything long-running, so notify records the runs a push calls for
// and walks each of them on a goroutine of this service's. It answers with
// what it started; where a run then gets to is read from the run.
//
// Both are answers about a gate rather than about a working copy, and neither
// takes a working copy from the caller. internal/gate resolves the identifier
// a hook carries to the working copy the gate belongs to, and this service
// pairs that with the repository record a run is created against - the same
// pairing assistant init writes. A caller therefore names a gate and nothing
// else, so nothing it writes can attach a push to another repository's runs.
//
// Admission establishes that the push has somewhere to go, and does not judge
// the change. That the gate resolves, that a repository record stands behind
// it, and that the reference updates can be read are what it answers of every
// push; whether the branch should be shared is what the nine stages are for. A
// push admitted without those is a push the gate takes and starts nothing for,
// which is the failure a sealed gate exists to prevent arriving through the
// front door.
//
// Of a push that would start a run it establishes one thing more: that a
// driver could be built for this home. That is asked during admission rather
// than when the run starts because of where the two hooks sit - admission runs
// before any reference changes and its refusal rejects the push, while the
// notification runs after every reference has moved and can only print - and
// it is asked only of a push carrying a branch update that is not a deletion,
// by the same predicate the notification starts runs by. A push of tags or
// deletions alone is admitted without one, because the gate is a repository
// git can push to normally and those start no run to need a driver for.
//
// What admission does not establish is why a driver could not be built -
// resolving an agent, opening the run service, and assembling the pipeline and
// its executor are all part of building one - so its refusal carries the reason
// it was given rather than naming a cause. The gap that leaves is the span
// between the two hooks. The notification asks for the driver again and can
// still fail, so a home that stops being able to build one after admission
// answered leaves a push accepted with no run started, and no failure in the
// notification can reject a push admission already took.
//
// A push to a branch that already has a run supersedes it, which PRD section 8
// asks for, and the guarantee is over the record: the branch's newest
// unfinished run is moved to terminated before the new one is created, both
// under the branch's own exclusion, so the record never shows two live runs
// for one branch. Execution is not covered. A run registers its cancellation
// with this service only once its own goroutine reaches the point that claims
// it, which is after its record already reads running, so a displaced run this
// service holds no cancellation for is signalled nothing; that is looked for
// twice and a run registering after the second look is signalled nothing at
// all. Nor is a displaced run that was signalled awaited, so nothing bounds how
// long two runs of one branch may execute at once - a missing wait rather than
// an interleaving. The mechanism that would bound it, a per-branch predecessor
// set with the wait as the arriving run's own first step, is specified outside
// this tree in internal/daemon's package documentation at tag
// pre-rebase-2-observation-edges, and is deliberately not implemented here.
//
// gate.admit is restricted, so containment is what refuses an agent inside an
// active validation stage that pushes at the gate - before any reference in
// the gate changes, which is PRD section 9's "push around a pipeline" landing
// on the surface a push actually arrives on. Everything the next section says
// about what containment covers today applies to it.
//
// # Containment is asked of the kernel, and answered from what this service
// started
//
// PRD section 9 contains an agent running inside a validation stage: it may
// inspect, fix, and return its own stage, and nothing else. internal/ipc reads
// the peer's credentials off the socket and asks an Ancestry, which is the
// seam this package fills.
//
// The relation it uses is the process group. internal/agents starts every
// agent as the leader of a new process group, so everything a stage's agent
// starts is in that group unless it deliberately leaves it, and a group this
// service started for a stage is a fact this service holds rather than one a
// caller states. StageStarted is how a stage launcher records one.
//
// Two things about that are worth being exact about, because a guard nobody
// can see fire is worth nothing. It fires: TestACallerInsideAnActiveStageIsRefused
// registers this test process's own group and then makes a restricted call,
// which is refused with ipc.ErrContained. And it has no producer in this
// build: nothing calls StageStarted, because no stage this build has a body
// for launches an agent - the written ones are functions of the run's state
// and start no process - so the registry is empty and nothing is contained
// today. What that costs is stated rather than implied: until a stage launcher
// calls StageStarted, containment protects nothing. The alternative, refusing
// every restricted call until then, is a service nobody can drive.
//
// The residual gap is the same one internal/agents names for its own sweep: a
// descendant that leaves its process group escapes the relation. Closing that
// needs a process-tree walk per platform, and the group is what this service
// already establishes.
//
// # What this package does not do
//
// It does not implement a stage. The nine bodies are internal/stages' and this
// package takes them as a value, so a build serves runs that hold at the first
// stage without a body rather than runs that pass.
//
// It does not read a repository's own configuration. PRD section 10 has the
// trusted layer read from the default branch at a freshly fetched commit, and
// nothing here fetches. What a run resolves is the operator's global layer and
// the schema defaults, so a repository's own settings are ignored rather than
// read from the wrong place. That is a gap against section 10 and not a hole
// in P7: the failure P7 exists to prevent is configuration that executes code
// being read from a pushed branch, and no branch's configuration is read here
// at all.
//
// It does not push, open a pull request, or move any reference. Those are
// stage bodies' work, behind internal/vcs, internal/safety and internal/forge.
//
// It does not decide who resolved a hold. internal/ipc derives that from the
// surface, and every resolution arriving over this protocol is
// store.ResolvedByMachineInterface, including one a person typed.
package service
