package ipc

import "fmt"

// Method names one call the service serves. The set is closed: a name not in
// the table below is refused with ErrUnknownMethod rather than reaching a
// handler, so a client running a newer build learns that this service does not
// serve the call instead of having it silently do nothing.
type Method string

const (
	// MethodHealth answers whether the service is ready. Publishing a process
	// identifier is not proof of readiness, per PRD section 8; a reply to this
	// is.
	MethodHealth Method = "health"
	// MethodStatus reports repository, gate, service, and active run state.
	MethodStatus Method = "status"
	// MethodRunsList reports recent runs, newest first.
	MethodRunsList Method = "runs.list"
	// MethodRunGet reports one run in full, including the revision its state
	// is current as of, which is what a gapped consumer reconciles against.
	MethodRunGet Method = "run.get"
	// MethodRunStart starts a run for a branch.
	MethodRunStart Method = "run.start"
	// MethodRunRerun starts a fresh run from the last known head, inheriting
	// the recorded intent.
	MethodRunRerun Method = "run.rerun"
	// MethodRunRespond answers a decision a run is holding on, the run named
	// or selected through the task that produced it. Selecting through a
	// task is refused for a task's worker on MethodTaskRespond's terms.
	MethodRunRespond Method = "run.respond"
	// MethodRunCancel ends a run.
	MethodRunCancel Method = "run.cancel"
	// MethodBranchSync is PRD section 11's custody of a person's branch:
	// classify the branch a working copy has checked out and, when the
	// request confirms, carry out the steps its class allows. It is the one
	// method that moves a person's branch, so it is restricted, and the
	// handler refuses a task's worker as well, on MethodTaskCreate's terms:
	// a worker reconciles nobody's branch.
	MethodBranchSync Method = "branch.sync"
	// MethodGateAdmit decides whether a push to a gate may proceed. It is
	// called from the gate's admission hook, before any reference in the gate
	// changes, and a refusal is what rejects the push. Who is pushing is
	// read off the hook process's credentials and parent chain: a task's
	// worker is held to the service's worker rule, a peer with a terminal's
	// foreground on its chain and no worker above it is a person's shell,
	// and a peer placed as neither is refused ErrUnplaceable.
	MethodGateAdmit Method = "gate.admit"
	// MethodGateNotify reports a push a gate accepted and starts the runs it
	// calls for. It answers as soon as the runs are recorded rather than when
	// they finish, because PRD section 8 has the notification hand off and
	// exit while the service owns everything long-running.
	MethodGateNotify Method = "gate.notify"
	// MethodStageReport returns a stage's result from the agent running it.
	// It is open to a contained caller, because returning its own stage is
	// exactly what such a caller is there to do.
	MethodStageReport Method = "stage.report"
	// MethodTasksList reports fleet work with its resolved current state.
	MethodTasksList Method = "tasks.list"
	// MethodTaskGet reports one task with its resolved current state and the
	// revision that state is current as of.
	MethodTaskGet Method = "task.get"
	// MethodTaskEvent appends an event to the history of the task the caller
	// is the worker of. The task is never named in the request: it is the
	// one whose worker group the caller's credentials place it in, so a
	// caller outside every worker group is refused and no caller can write
	// to another task's history. It is restricted on the same terms as every
	// method that moves a record, and the kinds it accepts are the ones
	// internal/taskstate's table gives a worker as their writer.
	MethodTaskEvent Method = "task.event"
	// MethodTaskRespond answers a hold a task raised with no run behind it:
	// a task past relaunch_attempts, or a decision an investigation's report
	// registered. PRD section 9 has that hold answered the way a run's is,
	// given the task, and it is MethodRunRespond's sibling rather than that
	// method with a second shape, because a run's answer resumes the run and
	// blocks on it while a task's closes a hold record. Who resolved is
	// derived from this surface by Request.HoldResolver and never read from
	// the request. Refused for a task's worker on MethodWakesList's terms, so
	// no worker answers a hold, its own task's included. One hold is held to
	// a third relation as well: the local landing's approval hold, PRD
	// section 6's "You approve", is answered only from a peer in the
	// foreground of its controlling terminal, decided by the service from
	// the peer's lineage, and refused ErrNotAtTerminal to a host agent's tool
	// command and to anything else not there.
	MethodTaskRespond Method = "task.respond"
	// MethodTaskCreate records a new task through internal/tasks' intake,
	// PRD section 9's coordinator-only service method for creating one. It
	// is the coordinator's: the handler refuses a caller the worker group
	// set places inside a task's worker group, decided from the caller's
	// credentials, so no worker can create a task.
	MethodTaskCreate Method = "task.create"
	// MethodTaskPromote flips an investigation task to delivery through
	// internal/tasks' promotion. It is the coordinator's on
	// MethodTaskCreate's terms, which is what keeps an investigation's own
	// worker from promoting itself.
	MethodTaskPromote Method = "task.promote"
	// MethodTaskRelaunch relaunches a task whose terminal is gone, through
	// the one anchored relaunch PRD section 11's reconciliation uses. It is
	// the coordinator's on MethodTaskCreate's terms.
	MethodTaskRelaunch Method = "task.relaunch"
	// MethodTaskRelay records that the coordinator relayed a reported
	// investigation's findings, PRD section 6's "findings relayed", which
	// moves the task to Complete through the terminal tier's own record. It
	// is refused unless the records make the task a reported investigation:
	// an investigation task with a stored report, no open hold naming it,
	// and no relay already recorded. It is the coordinator's on
	// MethodTaskCreate's terms, so no worker records its own findings
	// relayed.
	MethodTaskRelay Method = "task.relay"
	// MethodTaskMessage sends a message to a running task's worker, PRD
	// section 6's steering: the program appends the message record, then
	// writes the text to the worker's input, and the record says which of
	// written or refused followed. Who sent it is derived from this surface
	// by Request.MessageSender and never read from the request. It is the
	// coordinator's on MethodTaskCreate's terms, so no worker steers itself
	// or a sibling.
	MethodTaskMessage Method = "task.message"
	// MethodTaskStop ends a running task's worker, leaves its copy as it
	// is, proves the end with the probe answering dead, and only then
	// registers a hold naming the task, which a retry answer relaunches
	// through the one anchored relaunch. It is the coordinator's on
	// MethodTaskCreate's terms.
	MethodTaskStop Method = "task.stop"
	// MethodTaskInterrupt asks to end a worker's turn while keeping its
	// process. No adapter declares the capability that needs, so it is
	// always refused naming that capability, PRD section 8's "undeclared
	// means unavailable", and section 12 defers it. It is the coordinator's
	// on MethodTaskCreate's terms, refused for a worker before the
	// capability is asked.
	MethodTaskInterrupt Method = "task.interrupt"
	// MethodTaskPeek reads a running task's worker's recent output, as the
	// session provider holding it captures it, redacted and bounded, with
	// the command a person attaches with where the provider has one. It
	// reads and writes nothing else. It is the coordinator's on
	// MethodTaskCreate's terms, so no worker reads its own or a sibling's
	// terminal through the service.
	MethodTaskPeek Method = "task.peek"
	// MethodWakesList drains the durable wake queue for the coordinator, PRD
	// section 8's drain as the owner amended it: it returns the queued wakes
	// on offer, oldest first, and records them as handed out to the
	// coordinator, closing none. A row handed out stays queued until
	// MethodWakesAcknowledge closes it, and is handed out again once the
	// coordinator it was handed to is no longer attached, which the service
	// decides and internal/service's wakewait.go states. Who it was handed to
	// is derived from this surface by Request.WakeAcknowledger. It is the
	// coordinator's and refused for a task's worker, which the handler
	// decides from the caller's credentials against the worker group set.
	MethodWakesList Method = "wakes.list"
	// MethodWakesPeek lists the queued wakes, handed out or not, oldest
	// first, and hands out and acknowledges nothing: it is the person's
	// reading of the queue, which leaves every row where it was for the
	// coordinator's drain. Refused for a task's worker on MethodWakesList's
	// terms.
	MethodWakesPeek Method = "wakes.peek"
	// MethodWakesAcknowledge records that the coordinator acted on the named
	// wakes, which is what closes a row: one a drain handed out, or one it
	// learned of from MethodWakesSubscribe or a task's history. A row
	// already acknowledged is refused. Who acknowledged is never in the
	// request: it is derived from this surface by Request.WakeAcknowledger,
	// the way Request.HoldResolver derives who resolved a hold. Refused for a
	// task's worker on MethodWakesList's terms.
	MethodWakesAcknowledge Method = "wakes.acknowledge"
	// MethodWakesWait is the blocking form of MethodWakesList: it answers
	// as soon as the queue holds a wake on offer, with the rows a drain
	// hands out, or once the bound the request names elapses with none. It
	// is a request rather than a stream, so it answers once and holds one
	// of its connection's in-flight slots while it waits. It keeps no state
	// across calls, so arming it again after a service restart reads the
	// durable queue as the first call did. Refused for a task's worker on
	// MethodWakesList's terms.
	MethodWakesWait Method = "wakes.wait"
	// MethodWakesSubscribe opens the coordinator's wake stream: the service
	// pushes each wake the supervisor queues, as a TypeWakeQueued event,
	// without being asked again, which is PRD section 5's "then wake the
	// coordinator". It hands out and acknowledges nothing, because pushing
	// is not draining. Holding it is also what the service reads as the
	// coordinator being attached, so a row a drain handed out while it is
	// held is not handed out again until it ends. It is restricted, and its
	// handler refuses a task's worker on MethodWakesList's terms before
	// anything is written, so neither a contained caller nor a worker is
	// sent a coordinator's wake.
	MethodWakesSubscribe Method = "wakes.subscribe"
	// MethodSupervisionAssert is PRD section 5's arm verification over the
	// socket: it answers whether the supervisor loop of the service that
	// holds this home is live, as the closed set internal/supervisor's Arm
	// carries. It is the coordinator's session start's and refused for a
	// task's worker on MethodWakesList's terms.
	MethodSupervisionAssert Method = "supervision.assert"
	// MethodCatchUpRead is PRD section 9's catching up: one read of what is
	// waiting on the person, what landed since they last marked what they
	// had seen, what is under way, and what stopped without a decision,
	// which assistant watch starts with. It is open, because it reads, and
	// it marks nothing: a read alone never moves the seen mark. The answer
	// carries the read's digest, its parts in counts, which machine.CatchUp
	// computes from those parts as it is encoded.
	MethodCatchUpRead Method = "catchup.read"
	// MethodCatchUpMark moves the seen mark: it records as seen exactly the
	// landed records the request names, which are the ones a read showed,
	// so a record recorded between that read and this mark is still shown
	// next time. It writes, so it is restricted, and it is the
	// coordinator's and the person's: refused for a task's worker on
	// MethodWakesList's terms, which the handler decides from the caller's
	// credentials against the worker group set.
	MethodCatchUpMark Method = "catchup.mark"
	// MethodUsageRead is PRD section 9's usage view: what the agent calls
	// recorded in a window cost, totalled from section 8's agent invocation
	// record by agent, model, purpose, role, provider, type, version and
	// effort, and by run and stage, which
	// assistant runs --usage prints. It is open, because it reads and
	// changes nothing, so a driving agent gets the same model a person does.
	MethodUsageRead Method = "usage.read"
	// MethodRunLog is PRD section 9's log view: the end of one stage's log in
	// one run, section 8's per-stage log, bounded and saying what it left
	// out, which assistant runs RUN --log=STAGE prints. It is open, because
	// it reads and changes nothing, on run.get's terms. It is a wider read
	// than run.get: a log holds each agent call's account and a fixer's whole
	// reply, where a run's answer and a fix commit's body carry bounded
	// summaries, so an agent inside a stage can read in full what an earlier
	// round of any run said. That is text it reads and not a session it
	// resumes, which is what P4 separates, but it is named here rather than
	// left for a reader to assume the two reads are the same.
	MethodRunLog Method = "run.log"
	// MethodDomainDelegate delegates a named domain of tasks to a
	// subordinate coordinator, PRD section 9's domain.delegate: it writes
	// section 8's delegation record and starts the domain's subordinate
	// coordinator through the session provider, placed from then on by the
	// same credentials and lineage as a worker. It is the coordinator's on
	// MethodTaskCreate's terms, and refused for a subordinate coordinator
	// as for a worker, with ErrSubordinate.
	MethodDomainDelegate Method = "domain.delegate"
	// MethodDomainEnd ends a delegation, PRD section 9's domain.end: the
	// delegation record's one revision, after which the domain's wakes
	// reach the first coordinator again. It is the coordinator's on
	// MethodDomainDelegate's terms.
	MethodDomainEnd Method = "domain.end"
	// MethodServiceStop stops the service.
	MethodServiceStop Method = "service.stop"
	// MethodServiceRestart restarts the service.
	MethodServiceRestart Method = "service.restart"
	// MethodEventsSubscribe opens the event stream, which answers with events
	// rather than with a result. It is open, so it carries nothing a
	// restricted caller alone may read: a coordinator's wakes travel on
	// MethodWakesSubscribe instead.
	MethodEventsSubscribe Method = "events.subscribe"
)

// Kind says whether a method answers once or opens a stream.
type Kind uint8

const (
	// KindRequest answers with one result or one error.
	KindRequest Kind = iota
	// KindStream answers with events until the consumer detaches or the
	// service ends the stream.
	KindStream
)

// Access says whether a method is available to a caller that is running inside
// an active validation stage.
//
// It is the whole of what the server decides about a caller. A second
// relation, whether the caller is a task's worker or a domain's subordinate
// coordinator, is decided by the handler from the same Credentials and is
// not a third class here: the methods that refuse a worker, the ones that
// admit a subordinate within its domain alone and the ones that refuse it,
// and the one that admits only a worker, each say so in their own doc, and
// internal/service's two held sets are the input. The table stays about
// containment because a class that meant "coordinator only" would need
// those sets as an input of this package, which owns no process this
// service started.
type Access uint8

const (
	// AccessOpen is served without consulting the peer's credentials at all.
	// It covers reading, and it covers a validating agent returning its own
	// stage, and identification buys nothing for either: the only decision
	// that rests on who is calling is containment, and a contained caller may
	// reach these anyway. That is also what lets a build on a platform with no
	// peer credentials serve every open method while every restricted one
	// refuses.
	AccessOpen Access = iota
	// AccessRestricted is refused for a caller contained by an active
	// validation stage. These are the calls that start, stop, respond to, or
	// otherwise drive a pipeline, which a process being validated must never
	// reach.
	AccessRestricted
)

// Spec is one row of the method table: the name, whether it streams, and
// whether a contained caller may reach it. A method is added by adding a row,
// never by adding a second list somewhere else.
type Spec struct {
	// Method is the name on the wire.
	Method Method
	// Kind is how the method answers.
	Kind Kind
	// Access is whether a contained caller may reach it.
	Access Access
	// Summary says what the method does, in one line.
	Summary string
}

// specs is the method table, and the only owner of what this service serves.
var specs = []Spec{
	{MethodHealth, KindRequest, AccessOpen, "Report readiness."},
	{MethodStatus, KindRequest, AccessOpen, "Report repository, gate, service, and active run state."},
	{MethodRunsList, KindRequest, AccessOpen, "List recent runs, newest first."},
	{MethodRunGet, KindRequest, AccessOpen, "Read one run and the revision it is current as of."},
	{MethodRunStart, KindRequest, AccessRestricted, "Start a run for a branch."},
	{MethodRunRerun, KindRequest, AccessRestricted, "Start a fresh run from the last known head."},
	{MethodRunRespond, KindRequest, AccessRestricted, "Answer a decision a run is holding on."},
	{MethodRunCancel, KindRequest, AccessRestricted, "End a run."},
	{MethodBranchSync, KindRequest, AccessRestricted, "Classify a person's branch and, when confirmed, reconcile it with what the pipeline holds."},
	{MethodGateAdmit, KindRequest, AccessRestricted, "Decide whether a push to a gate may proceed."},
	{MethodGateNotify, KindRequest, AccessRestricted, "Start the runs a push a gate accepted calls for."},
	{MethodStageReport, KindRequest, AccessOpen, "Return a stage result from the agent running it."},
	{MethodTasksList, KindRequest, AccessOpen, "List fleet work with its resolved current state."},
	{MethodTaskGet, KindRequest, AccessOpen, "Read one task and the revision it is current as of."},
	{MethodTaskEvent, KindRequest, AccessRestricted, "Append an event to the history of the task the caller is the worker of."},
	{MethodTaskRespond, KindRequest, AccessRestricted, "Answer a hold a task raised with no run behind it."},
	{MethodTaskCreate, KindRequest, AccessRestricted, "Record a new task through intake, for the coordinator."},
	{MethodTaskPromote, KindRequest, AccessRestricted, "Promote an investigation task to delivery in place, for the coordinator."},
	{MethodTaskRelaunch, KindRequest, AccessRestricted, "Relaunch a task whose terminal is gone, in its own copy, for the coordinator."},
	{MethodTaskRelay, KindRequest, AccessRestricted, "Record a reported investigation's findings relayed, for the coordinator."},
	{MethodTaskMessage, KindRequest, AccessRestricted, "Send a message to a running task's worker, recorded first, for the coordinator."},
	{MethodTaskStop, KindRequest, AccessRestricted, "End a running task's worker, prove it dead, and hold the task, for the coordinator."},
	{MethodTaskInterrupt, KindRequest, AccessRestricted, "Interrupt a worker's turn; refused, since no adapter declares the capability."},
	{MethodTaskPeek, KindRequest, AccessRestricted, "Read a running task's worker's recent output, redacted and bounded, for the coordinator."},
	{MethodWakesList, KindRequest, AccessRestricted, "Drain the wake queue: hand out the queued wakes on offer, closing none."},
	{MethodWakesPeek, KindRequest, AccessRestricted, "List the queued wakes without handing out or acknowledging any."},
	{MethodWakesAcknowledge, KindRequest, AccessRestricted, "Record that the coordinator acted on the named wakes, which closes them."},
	{MethodWakesWait, KindRequest, AccessRestricted, "Block until the wake queue holds a wake on offer or a bound elapses, then drain it."},
	{MethodWakesSubscribe, KindStream, AccessRestricted, "Open the coordinator's wake stream, which pushes each wake as it is queued."},
	{MethodSupervisionAssert, KindRequest, AccessRestricted, "Verify that the supervisor loop of this home's service is live."},
	{MethodCatchUpRead, KindRequest, AccessOpen, "Read what waits on you, what landed unseen, what is under way, and what stopped."},
	{MethodCatchUpMark, KindRequest, AccessRestricted, "Mark seen exactly the landed records a catch-up read showed."},
	{MethodUsageRead, KindRequest, AccessOpen, "Read what the agent calls recorded in a window cost."},
	{MethodRunLog, KindRequest, AccessOpen, "Read the end of one stage's log in one run."},
	{MethodDomainDelegate, KindRequest, AccessRestricted, "Delegate a domain of tasks to a subordinate coordinator this service starts, for the coordinator."},
	{MethodDomainEnd, KindRequest, AccessRestricted, "End a domain's delegation, after which its wakes reach the coordinator again."},
	{MethodServiceStop, KindRequest, AccessRestricted, "Stop the service."},
	{MethodServiceRestart, KindRequest, AccessRestricted, "Restart the service."},
	{MethodEventsSubscribe, KindStream, AccessOpen, "Open the event stream."},
}

// specByMethod indexes the table, refusing at build time a table this server
// could not serve as written.
var specByMethod = func() map[Method]Spec {
	m, err := indexSpecs(specs)
	if err != nil {
		panic(err.Error())
	}
	return m
}()

// indexSpecs indexes a method table, or reports what makes it unservable.
//
// A duplicate name would make one row unreachable and would make the table
// disagree with itself about a method's access, so it is refused rather than
// letting the last row quietly win.
//
// A streaming method may be restricted. Opening a stream is ordered against a
// cancel that may follow it on the same connection, so the server opens it on
// the goroutine that reads that connection; a restricted one is opened there
// and decided on a goroutine of its own, the access check and then the
// handler, before anything it carries is written, which server.go's
// startAdmittedStream owns.
func indexSpecs(rows []Spec) (map[Method]Spec, error) {
	m := make(map[Method]Spec, len(rows))
	for _, s := range rows {
		if _, dup := m[s.Method]; dup {
			return nil, fmt.Errorf("ipc: method table declares %q twice", s.Method)
		}
		m[s.Method] = s
	}
	return m, nil
}

// Methods returns every method the service serves, in the order the table
// declares them.
func Methods() []Spec {
	out := make([]Spec, len(specs))
	copy(out, specs)
	return out
}

// Lookup returns the specification of a method, and reports whether the method
// is served at all.
func Lookup(m Method) (Spec, bool) {
	s, ok := specByMethod[m]
	return s, ok
}
