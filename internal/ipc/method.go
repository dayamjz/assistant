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
