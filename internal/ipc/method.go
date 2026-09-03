package ipc

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
	{MethodStageReport, KindRequest, AccessOpen, "Return a stage result from the agent running it."},
	{MethodTasksList, KindRequest, AccessOpen, "List fleet work with its resolved current state."},
	{MethodTaskGet, KindRequest, AccessOpen, "Read one task and the revision it is current as of."},
	{MethodServiceStop, KindRequest, AccessRestricted, "Stop the service."},
	{MethodServiceRestart, KindRequest, AccessRestricted, "Restart the service."},
	{MethodEventsSubscribe, KindStream, AccessOpen, "Open the event stream."},
}

// specByMethod indexes the table. A duplicate name would make one row
// unreachable and would make the table disagree with itself about a method's
// access, so building the index panics on one rather than letting the last row
// quietly win.
var specByMethod = func() map[Method]Spec {
	m := make(map[Method]Spec, len(specs))
	for _, s := range specs {
		if _, dup := m[s.Method]; dup {
			panic("ipc: method table declares " + string(s.Method) + " twice")
		}
		m[s.Method] = s
	}
	return m
}()

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
