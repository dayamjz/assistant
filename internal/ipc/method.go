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
	// MethodRunRespond answers a decision a run is holding on.
	MethodRunRespond Method = "run.respond"
	// MethodRunCancel ends a run.
	MethodRunCancel Method = "run.cancel"
	// MethodGateAdmit decides whether a push to a gate may proceed. It is
	// called from the gate's admission hook, before any reference in the gate
	// changes, and a refusal is what rejects the push.
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
	// no worker answers a hold, its own task's included.
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
	// MethodWakesList drains the durable wake queue for the coordinator: it
	// returns the queued wakes, oldest first, and acknowledges none, so a
	// row stays actionable until MethodWakesAcknowledge names it. It is the
	// coordinator's and refused for a task's worker, which the handler
	// decides from the caller's credentials against the worker group set.
	MethodWakesList Method = "wakes.list"
	// MethodWakesAcknowledge records that the coordinator handled the named
	// wakes. Who acknowledged is never in the request: it is derived from
	// this surface by Request.WakeAcknowledger, the way Request.HoldResolver
	// derives who resolved a hold. Refused for a task's worker on
	// MethodWakesList's terms.
	MethodWakesAcknowledge Method = "wakes.acknowledge"
	// MethodSupervisionAssert is PRD section 5's arm verification over the
	// socket: it answers whether the supervisor loop of the service that
	// holds this home is live, as the closed set internal/supervisor's Arm
	// carries. It is the coordinator's session start's and refused for a
	// task's worker on MethodWakesList's terms.
	MethodSupervisionAssert Method = "supervision.assert"
	// MethodServiceStop stops the service.
	MethodServiceStop Method = "service.stop"
	// MethodServiceRestart restarts the service.
	MethodServiceRestart Method = "service.restart"
	// MethodEventsSubscribe opens the event stream. It is the only streaming
	// method, and it answers with events rather than with a result.
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
// relation, whether the caller is a task's worker, is decided by the handler
// from the same Credentials and is not a third class here: the methods that
// refuse a worker, and the one that admits only a worker, each say so in
// their own doc, and internal/service's worker group set is the input. The
// table stays about containment because a class that meant "coordinator
// only" would need that set as an input of this package, which owns no
// process this service started.
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
	{MethodTaskMessage, KindRequest, AccessRestricted, "Send a message to a running task's worker, recorded first, for the coordinator."},
	{MethodTaskStop, KindRequest, AccessRestricted, "End a running task's worker, prove it dead, and hold the task, for the coordinator."},
	{MethodTaskInterrupt, KindRequest, AccessRestricted, "Interrupt a worker's turn; refused, since no adapter declares the capability."},
	{MethodWakesList, KindRequest, AccessRestricted, "Drain the wake queue: list the queued wakes, acknowledging none."},
	{MethodWakesAcknowledge, KindRequest, AccessRestricted, "Record that the coordinator handled the named wakes."},
	{MethodSupervisionAssert, KindRequest, AccessRestricted, "Verify that the supervisor loop of this home's service is live."},
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
// A streaming method that is restricted is refused too, and that one is about
// where a decision runs rather than about the table. Opening a stream is
// ordered against a cancel that may follow it on the same connection, so the
// server opens it on the goroutine that reads that connection, and deciding
// containment there would put a caller's Ancestry on the path that reads every
// other request on it. Serving such a method needs the stream path to carry its
// own authorization the way a request already does, so the table may not
// declare one until it can.
func indexSpecs(rows []Spec) (map[Method]Spec, error) {
	m := make(map[Method]Spec, len(rows))
	for _, s := range rows {
		if _, dup := m[s.Method]; dup {
			return nil, fmt.Errorf("ipc: method table declares %q twice", s.Method)
		}
		if s.Kind == KindStream && s.Access == AccessRestricted {
			return nil, fmt.Errorf("ipc: %q opens a stream and is restricted, which the server would have to decide on the goroutine reading the connection", s.Method)
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
