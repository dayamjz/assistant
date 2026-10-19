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
// One held run is looked at before serving, and it is PRD section 11's step
// 3: a run holding because checks_timeout elapsed has its pull request
// re-read by its recorded number, and a pull request positively read as
// merged or closed closes that hold by a resolution this service records as
// its own, with the state it read, and ends the run as PRD section 5's
// "Waiting on checks" says. The same re-read precedes an answer to that
// hold. checkshold.go owns which hold that is, read off the recorded report
// through stages.ChecksTimeoutHeld, and it is the one producer of
// store.ResolvedByProgram; a hold on a person's decision about a finding is
// never re-read, because the pull request's state must not answer a question
// a person was asked. A run that was waiting on checks when the service
// stopped is continued rather than re-read here: the first read of the
// resumed wait reads the pull request itself, which is step 1 and the checks
// body's. Recovery step 1 for a still-running wait is therefore a resume into
// the stage body, which makes its own read, and not a recover-time forge read.
//
// The record of such an ending is PRD section 8's merge record, written once:
// internal/runs' two pull request moves write it with the status in one
// transaction, and its RecordMergeAfterPass writes it beside a run that had
// already passed when its pull request was later read merged at the head the
// run observed, by autonomy.go after the program's own merge or by
// mergeread.go's pass for a person's; settle makes
// the same two moves for the live wait from what
// the checks body wrote under pipeline.KeyPullRequestEnd and its head key,
// and report reads that record to derive the run's passed, so a stage body
// decides nothing about a run's status, the run's own row stores the fact
// nowhere, and the record, not a checkpoint still standing at the checks
// stage, says what ended the run.
//
// # A person's merge is read on an interval, and mergeread.go owns it
//
// Nothing else reads a pull request after its run ends checks-passed, so
// mergeread.go's pass is what writes the merge record for a merge a person
// makes then: once as the service opens, on a goroutine after recovery so
// serving waits on no code host for it, and once every mergeReadInterval
// after, each pass reading each checks-passed run that recorded a
// repository on the code host and a pull request number at most once. It
// records only a pull request read merged at the head the run observed, and
// it is the same read autonomy.go records the program's own merge with.
// mergeread.go says why that trigger and not recovery alone, a read verb, or
// the supervisor loop's pass, and what the interval leaves open.
//
// # A restart reaps the agent processes an earlier service recorded
//
// PRD section 11's step 5 is "reap orphaned agent processes ... and never
// touch one whose run is still active", and reap.go is the run half of it.
// What makes an orphan findable is a record written while the process was
// not yet one: containRunner's Started value, beside the registry entry it
// adds, writes a run process record to internal/store naming the run, the
// invocation's purpose, the group the adapter reported and the leader's
// identity as agents.Identify read it then, and the forget the adapter calls
// once its termination of the tree has finished clears it. StageStarted
// writes no record: it registers whatever group its caller states, and a
// record of a group nothing here started is one recovery has no business
// acting on. A record says an agent was launched and its end was not
// recorded, never that anything runs now (P8), and nothing reads it as
// liveness: whether a process is there is asked of agents.Identity.Presence.
//
// recover reaps after the runs are reconciled and before anything is served.
// Each record is decided from the run's reconciled status first: a group
// this service registered itself, or a run internal/runs does not call
// finished, is left untouched without a process being looked at. A status
// this build does not recognize is refused as unverified rather than read as
// finished, because internal/runs' reading of it as finished is the safe
// direction for taking a run further and the unsafe one for ending a process.
// Every other record goes to agents.EndGroup, which signals a group only
// after verifying its leader still carries the recorded identity, and only
// by group identifier: this package holds no pattern kill and builds no
// command line to match on, so PRD section 5's "never kill watchers broadly"
// holds on this path by construction. A reused identifier is left alone and
// its record cleared; a gone leader is cleared without a signal; a group
// that outlived its leader, and every record that cannot be verified, are
// left alone with their records standing; and a group that still held a
// process after the forceful step keeps its record too, so the next
// recovery looks again.
//
// Two decisions the issue that built this asked are settled here. The record
// is a table in internal/store, because that package is the only one that
// writes SQL and the record has to outlive this process, while
// internal/agents owns the identity read and the ending, because it is the
// only package that starts or ends an agent process; this package composes
// the two and owns neither. And a process recovery cannot verify is a typed
// account, reported line by line in the service log with a summary line
// after, and the service comes up: refusing to serve until a person looks
// would hold every other run of the home on one process this service may not
// even be able to see, and nothing here offers a person a way to look. The
// accounts are kept on the service as well, so a test reads what was decided
// rather than parsing the log.
//
// What this does not reach is stated rather than implied. A process whose
// service died before the record was written, or whose record could not be
// written, was never recorded and is not reaped. A process of a run that is
// still unfinished is not ended by recovery, even when this service resumes
// that run in the same isolated copy; it is ended when this service ends the
// run, by the run's ending on runsweep.go's terms, or reaped by the first
// recovery that finds the run finished. A process left alone as unverified
// may still be writing in its run's isolated copy, and nothing here stops
// it. A descendant that moved itself to another process group is not in the
// group signalled, and nothing recorded names it; recovery does not ask
// agents.Sweeper for it, because one whose run is unfinished must not be
// touched. What finds it is the sweep runsweep.go asks of a run's copy when
// the run reaches a final state, on Linux and macOS and only while it is
// still in the copy; a service that stopped between a run's ending and that
// sweep leaves it until the next service's recovery, which sweeps the copy
// of every run its record shows finished while that copy stands, on
// runsweep.go's terms. What each sweep came to is recorded on the run and
// carried in its status answer, which runsweep.go also states. On
// unix the adapter's forget runs once its termination has finished whether
// or not the group emptied, so a member this service could not signal loses
// its record with the rest; a later recovery would have found its leader
// gone and left it alone in any case. Only on Linux does a group whose
// members have all exited and wait unreaped read as gone; on macOS and
// another unix such a group reads as outlived or survived, so its record
// stands and every later recovery looks again without signalling it, which
// internal/agents' EndGroup states. On Windows there is no process group:
// what is ended is the tree under the verified leader that taskkill's tree
// option ends, a leader that is gone answers agents.GroupLeaderGone without
// anything of its tree being looked for, and on a platform with no identity
// read every record is unverified and nothing is ended there at all.
//
// Two neighbouring jobs are not here. A task's terminal is a worker, its
// group is internal/session's set rather than this registry, and its
// survivors belong to the task terminal sweep; and removing an isolated copy
// no run owns is the cleanup proof's, which this path never touches.
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
// action, and nothing here can: the field is unexported in internal/machine,
// so no code here has an assignment site to reach, and a branch added to
// report cannot hand back an action that disagrees with the outcome beside it.
// PRD section 9 requires that of the terminal interface, and says why: a stall
// that looks alive is worse than an error.
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
// unfinished. Its record is not wrong - the run is unfinished - and the moves
// that would end it are answers to different questions: internal/runs' Fail
// is a verdict on the change rather than a failure of the service, Terminate
// is what a cancellation or a supersession leaves, and Park is what a spent
// bound leaves, which settle writes with the bound read off the parking
// checkpoint and logs by name. A run standing at a resumable position is none
// of them, and ending it would discard work its checkpoint history still
// holds, which P6 forbids.
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
// caller states. StageStarted is how a launcher records one, and on the
// agent's path nothing needs to call it: driverFor wraps the resolved runner
// in containRunner, which sets agents.Invocation.Started on every invocation
// - a stage's through the agents.StageAgent seam and a fix round's through
// the fixer the wrapped runner opens - so the adapter reports the group when
// the process starts and its end once the tree has been terminated, and the
// registration spans exactly the process's lifetime. The run it is filed
// under is the one the segment context names and the stage recorded is the
// invocation's purpose, both of which contain.go owns.
//
// Two things about that are worth being exact about, because a guard nobody
// can see fire is worth nothing. It fires: TestACallerInsideAnActiveStageIsRefused
// registers this test process's own group and then makes a restricted call,
// which is refused with ipc.ErrContained. And its producer covers the agent
// and only the agent: the configured commands the test, document and lint
// bodies run, and the provider command the pull request and checks bodies
// launch, are started outside internal/agents and register no group, so a
// push made from inside one of those is not contained. That is stated rather
// than implied, because a restricted method reads as a guarded one.
//
// What the relation deliberately excludes is a task's worker. A worker's push
// is the one the gate exists to admit, so containRunner carries no worker arm,
// declares none, and a worker's group is never in this registry: a worker started on the
// agent the wrapper wraps is answered not contained while a stage runs, and
// contain_worker_unix_test.go holds that against the control of the same
// process registered as a stage.
//
// The residual gap is the same one internal/agents names for its own sweep: a
// descendant that leaves its process group escapes the relation. Closing that
// needs a process-tree walk per platform, and the group is what this service
// already establishes.
//
// # A worker is placed by a second relation, and four methods turn on it
//
// The other thing the same credentials decide is which task a peer is the
// worker of. That is internal/session's worker group set, which the
// service's session provider fills for exactly each worker process's
// lifetime, whichever of session.Pipe and session.Tmux it is, and this service reads
// it through the driver that holds the provider; workers.go owns the
// derivation - credentials, then the peer's parent chain, which placement.go
// walks to the first process whose group is a worker group the set holds or
// that is itself a worker the set holds, then the set - and states it per
// platform, Windows included, where no peer can be placed because none is
// identified. It reads the chain rather than the peer's own group, which is
// what containment still reads, and a chain it cannot read is a refusal,
// never "not a worker"; placement.go names the one shape it places wrongly,
// a chain the kernel rewrote when an intermediate process exited, which
// places a worker's command as a person's. It is
// applied by handlers, not by internal/ipc's access class, and that table
// says why. task.event admits only a worker and appends to its own task's
// history, holding the kind to internal/taskstate's writer column so a
// worker cannot append what the program decides, and writing a message
// acknowledgement and an investigation's report through their own records;
// a launched worker reaches it over the route route.go gives it; wakes.list,
// wakes.acknowledge, wakes.wait, the wake stream wakes.subscribe and
// supervision.assert are the coordinator's and refuse a worker, and so is
// wakes.peek, a person's listing that hands out nothing, with who a drain
// handed rows to and who acknowledged derived by internal/ipc's
// Request.WakeAcknowledger and read from nothing; catchup.mark, which moves
// PRD section 9's seen mark and which catchup.go owns, refuses a worker on
// the same terms while catchup.read stays open; and gate.admit holds a
// worker's push to admit_workers.go's rule, only its task's branch, forward,
// never a deletion, with the reasoning for its being admission's decision
// rather than internal/safety's written there. A worker's group is in no
// stage, so containment passes its push and the rule is what admission asks
// of it; a person's push meets the rule nowhere.
//
// # A task is dispatched here, and launched here
//
// PRD section 6 moves a task from Dispatched to Running only when isolation
// is proven, and dispatch.go is the step that proves it: for a task that
// resolves Dispatched it claims the attempt through store.BeginTaskAttempt,
// records what the primary checkout looked like, cuts the copy PRD section 5
// gives the task's shape - a clone of the primary into home.TaskCopy, with
// the gate as its one remote and the task's branch for a delivery task, with
// no remote and a detached HEAD for an investigation task - observes it
// through internal/vcs, asserts it through internal/tasks under the
// environment internal/agents would start a worker with, and records the
// copy's own snapshot. A copy that fails the assertion is recorded as an
// isolation-refused entry carrying internal/taskstate's closed token, and
// the task resolves Failed from it. A copy that passes leaves the proof in
// this service's memory, in the slot dispatch.go owns, and the dispatch ends
// there. It runs at open, after the queued tasks are released, and after
// every release, once per task per service; dispatch.go says what a retry
// after a crash finds and what it may not do to it.
//
// launch.go is the launch half of the same edge, and it follows a proven
// dispatch in the same pass. It observes and asserts the copy again, under
// the same agents.WorkerEnvironment, because the slot's proof is as of the
// dispatch and the copy can have been retargeted since - internal/session
// names that gap and this is the hookup that closes it to the width of one
// step; a copy refused there is recorded exactly as a dispatch's refusal is.
// It then creates the terminal session through session.Provider.Create with
// the fresh proof and the task's instructions file, and records the
// session's reference on the attempt dispatch claimed, through
// store.SetAttemptSession, which is the one record of which terminal an
// attempt runs in. The provider is this service's, built in driverFor over
// the agent as resolved and before containRunner wraps it, because the
// wrapper carries no worker arm: a session.Pipe by default, or a
// session.Tmux when the operator's file sets config.KeySessionProvider to
// tmux, which terminals.go's sessionHost decides; the sessions it creates are
// held per task, one per attempt, and Close ends exactly those and no
// worker an earlier service of this home started.
//
// Four things about the launch are stated rather than implied, and launch.go
// owns each. Nothing here writes an event to say Running, and the task
// reads Running anyway: every task reader this service builds probes
// through its Terminals, which in production is this same provider, on
// terminals.go's terms, so internal/taskstate's process tier answers
// Running from the Live probe of the recorded reference while the worker
// runs and what precedence gives with Dead once it exits. One launch per
// attempt per service, and a restart dispatches and launches again as a new
// attempt over the standing copy, which is PRD section 11's relaunch in
// place by the record's lights; the earlier process, if alive and still in
// the copy, is ended by the sweep relaunch.go asks of the copy on Linux and
// macOS, or, where its working directory cannot be read by this service and
// neither of agents.Sweeper's carve-outs sets it aside, refuses the relaunch until a
// later start. Elsewhere there is no sweep, so the relaunch is refused and
// holds for a person, and on a retry answer goes ahead with that process,
// if any, an orphan this service holds no handle to and does not end. Worker
// routing (GAP-02) selects an adapter whose capabilities cover the worker
// need before Create: a pin or preference that covers nothing is recorded as
// route-refused and the task resolves Failed rather than staying Dispatched,
// and a selection records adapter and reason on the attempt before the
// process starts. Grants stay GrantWorker on the ordinary Create path. And
// between Create returning and the reference being written, a crash leaves a
// process no record names, which internal/session states; the dispatch that
// retries the task sweeps the standing copy before it claims, on sweep.go's
// terms, which ends that process on Linux and macOS while it is still in the
// copy. Elsewhere there is no sweep, so the retried dispatch is refused and
// holds for a person on a key of its own, and on a retry answer starts a
// worker beside that process, if any, which it neither finds nor ends.
//
// # A restart reconciles the fleet, and relaunches a dead terminal in place
//
// PRD section 11's fleet reconciliation runs inside recover, after the runs
// are reconciled and before anything is served: the queued wakes are read
// as input with store.QueuedWakes, which hands out nothing because a drain
// is the coordinator's and hands rows out to whoever makes it, every task of this home is read with its terminal probed under a
// bound, and a terminal gone with the work not landed is relaunched in the
// copy it stood in. reconcile.go owns the table, which is keyed on the
// resolved state, the source that decided it and the probed liveness, and
// the bound, which is what keeps Open from hanging with the lock held behind
// a provider that never answers; relaunch.go owns the one relaunch, which
// decides from a read of its own, re-runs internal/tasks' floor, re-probes
// before it claims the attempt through store.BeginTaskAttempt, appends the
// relaunched note and the dispatched entry, asserts the standing copy
// through the assertion dispatch and launch share, and acknowledges as the
// program the wakes about the session it replaced; it writes no wake and no
// marker itself, because both are supervise.go's loop's, under its fence,
// and the loop's terminal-gone row is what tells a person about a terminal
// this pass did not relaunch. Two cases are the exception to writing
// nothing, and both hold for a person through a hold record naming the
// task and an attempt, which the loop then surfaces like any open hold: a
// task past relaunch_attempts, whose hold taskholds.go registers for the
// attempt that reached the bound, and a relaunch on a platform with no
// process sweep, which relaunch.go refuses and holds for the attempt whose
// terminal is gone, because nothing proves the copy unowned there. The
// answer to either, given the task, is what decides whether it is retried,
// and taskholds.go says how a retry reaches the one relaunch and why it is
// good for one attempt. The launch of a claimed
// attempt goes to a goroutine under startWork, so "before serving anything"
// is a property of the claim. A launched task is never dispatched again,
// which dispatch.go states: a restarted service holds no session, so its
// provider answers every reference by identity, and a worker whose process
// is still present reads Unobserved and is left alone, on PRD section 11's
// ambiguous-liveness row, while one whose process is gone reads Dead and is
// relaunched. Both files state what the mechanism does not establish, a
// dead terminal not being proof that nothing still writes in the copy among
// it. An investigation with a stored report is not a terminal gone with the
// work not landed: internal/taskstate resolves it Reported from the report
// row whatever its terminal reads, and the table leaves Reported alone, so
// it is never relaunched, and reconcile.go says why the rule that read the
// resolver's newest history entry for the same purpose was removed.
//
// The one change the fleet work makes to the run path is beside it, in
// copy.go. A run's copy used to be cut after a fetch of the submitted commit
// from the working copy, on every run; a commit a task's worker pushes from
// its clone is not in the working copy, and the run such a push starts went
// on only because git writes a reference to an object the repository already
// holds without transferring it. The reference the copy is cut from is now
// written when the run is created, by create, from the gate's own object for
// a push and by a fetch from the working copy for a start or a rerun, and
// ensureCopy reads it from the gate and from nowhere else. copy.go's opening
// comment keeps the argument for one path that always runs and says where
// the fetch went and what it used to rest on.
//
// # An isolated copy is removed on a proof, or kept and said so
//
// PRD principle P12 covers every isolated copy, a run's and a task's, and
// the decision is internal/tasks' Cleaner: this service calls it and
// decides nothing of it. A run's copy goes through reclaimCopy when the run
// finishes, in settle and in the checks-hold closure, after the sweep
// runsweep.go asks of it from inside the move that finished the run, whose
// answer the decision does not read, so a sweep that did not clear does not
// keep the copy; a refusal there is
// recorded by the decision on the run's cleanup record and composed into
// the run's answer by report, so status says a local copy was kept rather
// than only the log. A delivery task's copy is cleaned up at the same two
// sites when the merge record landed it, and an investigation task's when
// its report is stored; both hand internal/tasks the terminal's liveness as
// the service's own task reader resolved it, so a copy whose worker is
// still running is kept with the live refusal and one whose worker is gone
// is examined on the proof, and cleanup.go owns the three sites and the
// window after the read. A landed task's worker this service holds is
// ended first, and the decision is asked again when its terminal is next
// observed Dead, from the supervisor's pass and from task.stop, which
// registers no relaunch hold for a task in the terminal tier; landed.go owns
// that, its cadence and its gaps. No verb or protocol method calls the
// decision except through that stop.
//
// # The supervisor loop is here, and supervise.go owns it
//
// PRD section 5's supervisor is a loop inside the service, and supervise.go
// is that loop: incarnations fenced by the store's epoch, a watchdog that
// replaces one whose beacon went stale and never writes the beacon itself,
// a pass that reads the records, then the slot, then the provider, and the
// bounded wait Close gives it. Its header owns the design and the residual
// gaps, Close's doc owns the one exception to the wait on work, and
// Options.Terminals is the seam the terminal reading arrives through, which
// the product leaves to this service's own session provider, on
// terminals.go's terms, so the loop absorbs an unchanged live worker on the
// handle's evidence and surfaces a dead one. The exceptions are a task that
// resolves Reported, Landed or Complete: internal/supervisor's
// TerminalWatched leaves its terminal unread, so an investigation whose
// worker ends after filing its report, or a task whose worker ends after it
// landed, raises no terminal-gone wake, while one that never reported or
// landed still does; supervise.go's header says why, and what landed.go
// still reads of such a terminal.
//
// # Four state events have a producer here, and publish.go owns two
//
// Subscribers learn that a run moved from runs.go's publishRunState, that a
// worker appended to its history from workers.go's publishTaskEvent, that a
// task's resolved state moved from the supervisor loop's pass, and that a
// hold opened or closed from the four sites that write one, and all four
// draw their revision from the one counter on this service, so a consumer
// orders them against each other. publish.go owns the second and third and
// says why the pass is the one reader that publishes a task: a task's state
// is resolved and never stored, so a change is a comparison between two
// resolutions, and the pass resolves every task anyway. The comparison is
// taskstate.Basis equality, kept per task in this service's memory, so a
// change is published once and not on every pass, within one beacon interval
// of being made, and a restart publishes every task again to subscribers who
// all attached fresh. A hold is published as the store returned it, at
// storeTaskReport for the holds a report registers, after the checks-hold
// closure for the one the program resolves, and at taskholds.go's
// registration of the two relaunch holds and its answer to a task's hold. What travels is the record, a
// store.Hold, which store.Resolver's design keeps from carrying who resolved
// it: the wire understates the resolver, as that package says the record
// itself does. The task payload is machine.Task, composed through taskView
// for the publisher and for the task surface alike, so a paused worker's
// declared wait reaches both the same way.
//
// # What this package does not do
//
// It does not implement a stage. The nine bodies are internal/stages', and
// this package takes a constructor for them rather than a value because a
// stage body's dependencies include the resolved agent and which agent
// resolves is not known until a run needs one. A build serves runs that hold
// at the stages stages.Holding names rather than runs that pass.
//
// What it does decide about them is which dependencies they get, since it is
// the one place holding the home, the resolved configuration, and the resolved
// agent at once. That agent is wrapped as an agents.StageAgent before it
// reaches a body, which is where P4 sits at this seam: agents.OpenFixer opens
// a fixer session from any Runner, so a body handed the Runner itself could
// reach the memory P4 keeps a reviewer out of, and a StageAgent has no Runner
// to hand it.
//
// It is also where PRD section 8's agent invocation history gets its feed.
// The default catalog's adapters are built with a recorder over this
// service's store, so every invocation a run makes through them, failed and
// cancelled ones included, lands one row of cost: purpose, agent, model,
// timing, failure category, token usage, and the run and stage it served,
// never a prompt or an output. The run and stage are stamped by containRunner
// from the context advance and the observe wrappers mark, so a call outside
// either records it as unknown; usage.go reads the history back as section
// 9's usage view, and ceiling.go reads it before every agent call
// containRunner starts, against the operator's global-only usage ceiling,
// holding the stage that needed a refused call for a person. ceiling.go owns
// what kind of bound that is and what it leaves open.
// invocation.go owns the translation and states the two edges plainly: a
// caller-supplied catalog built its factories already, so it records only
// what its own factories arranged, and a row that cannot be written is
// logged and dropped, so the history understates rather than failing an
// invocation over its own bookkeeping, and the ceiling compares a total that
// understates with it.
//
// It is also where two records beside a run's execution are written: when
// each stage execution started and ended, and what the run did next with a
// path a decision-model answer was about. topologyFor wraps every stage body
// and the fix body to write them, and they never enter graph state, because a
// time there would defeat the convergence bound. observe.go owns both and
// their residual gaps. The proposal to turn a decision-model check off is
// read from that record onto a run's answer, machine.Run.Proposals, and kept
// out of graph state for the same reason; decision.go owns the rule.
//
// It reads a repository's own configuration per run, on PRD section 10's
// terms, and layers.go owns the mechanism: the trusted copy from the default
// branch at a freshly fetched commit, the pushed copy at the run's submitted
// commit, both composed with the operator's layer by config.ResolveRun before
// the run's record moves to running. A trusted copy that cannot be fetched,
// read, or parsed stops the run there, before any stage body or agent
// launches, rather than the run guessing at defaults; an invalid pushed copy
// is refused on the same section's parse-time rule. A resume resolves again,
// fresh, and a resolution whose bounds moved is a different topology that
// internal/graph's identity check decides the resumability of.
//
// The one repository key the resolution admits and this wiring cannot honor
// is "agent": this build resolves one agent per service, from the operator's
// layer, so a run whose resolved agent list differs is refused with
// ErrRepositoryAgent rather than run under an agent its repository did not
// ask for. The refusal rather than a silent drop is the point; the operator
// aligns the two lists or unsets the repository's key.
//
// It does not push, open a pull request, or move any reference. Those are
// stage bodies' work, behind internal/vcs, internal/safety and internal/forge.
//
// It does not decide who resolved a hold that arrived on the socket, or who
// acknowledged a wake. internal/ipc derives both from the surface: every
// resolution arriving over this protocol is store.ResolvedByMachineInterface,
// including one a person typed, and every acknowledgement is
// store.AcknowledgedByCoordinator. taskholds.go records the resolver
// Request.HoldResolver returns and names none of its own. The resolutions it
// names itself are the ones it makes itself: store.ResolvedByProgram at the
// checks hold above, and store.ResolvedByStandingConsent for each answer
// standing consent gives, which consent.go records.
//
// # Standing consent answers inside the segment, and consent.go owns it
//
// PRD section 9's standing consent is a per-run flag on a start or an attach,
// refused unless the repository's trusted copy sets allow_standing_consent,
// and recorded on the run row once given. A segment that stops at a decision
// consent answers is answered in the same segment and the same slot, in
// advance, before the record moves, so a run under consent is never recorded
// held at a decision the program is about to answer. Which decisions it
// answers, the ones it never does, how each answer is recorded, and the gaps
// are consent.go's.
package service
