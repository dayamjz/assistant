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
// holds it for the length of one segment and nowhere durable. Every answer
// this service builds carries it as machine.Run.Advancing, and the run's next
// action is machine.Outcome.NextActionFor of it, so a reader is told either to
// wait or to attach rather than a sentence covering both. PRD section 9
// requires that of the terminal interface, and says why: a stall that looks
// alive is worse than an error.
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
// It bounds that segment and no other, and what it leaves open is disclosed
// rather than implied. The segment already inside a node when the ending is
// recorded still has to return: cancel signals it and does not wait for it,
// which cancel's own documentation states, so execution can outlast the answer
// by as long as that node takes to notice. And a caller that read the run's
// record before the ending was recorded can still advance the run once the
// slot is given back; its read was made before there was anything to order it
// against, and a segment it strands is not one the ending marked, so carryOn
// may carry that one on. endRun holds the full list.
//
// A continuation cannot cause another, and that is structural too. It runs
// under this service's own context, so the only contexts that can end its
// segment are the service stopping and an ending through the protocol, and
// carryOn refuses both. What is left is one continuation per caller that
// walked away.
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
