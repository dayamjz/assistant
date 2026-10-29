package service

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// launchSubject is a dispatchSubject whose hand-built service can resolve an
// agent, so launchTask has a driver to build its session provider from. It
// is built by hand for the reason newDispatchSubject is: what these tests
// reach is the launch below dispatchDue, with a step between the dispatch
// and the launch that no protocol method offers.
type launchSubject struct {
	*dispatchSubject
	agent *standin.Agent
}

// newLaunchSubject builds the subject over the given stand-in, which decides
// whether the resolved agent carries a worker: standin.NewWorker's does and
// standin.New's, the shipped adapter's stand-in, does not.
func newLaunchSubject(t *testing.T, agent *standin.Agent) *launchSubject {
	t.Helper()
	return newLaunchSubjectAs(t, agent, "")
}

// newLaunchSubjectAs is newLaunchSubject over a task recorded with role.
func newLaunchSubjectAs(t *testing.T, agent *standin.Agent, role string) *launchSubject {
	t.Helper()
	return launchSubjectOver(t, agent, newDispatchSubjectAs(t, role))
}

// launchSubjectOver is newLaunchSubject over a dispatch subject the caller
// built.
func launchSubjectOver(t *testing.T, agent *standin.Agent, sub *dispatchSubject) *launchSubject {
	t.Helper()
	sub.service.catalog = agents.NewCatalog(fixedRunner{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()})
	sub.service.cfg = config.Defaults()
	sub.service.registry = newRegistry()
	sub.service.starting = make(map[branchKey]*branchGate)
	t.Cleanup(func() {
		for _, err := range sub.service.closeSessions() {
			t.Errorf("closing the sessions: %v", err)
		}
	})
	return &launchSubject{dispatchSubject: sub, agent: agent}
}

// workerStandIn is the worker-capable stand-in with a greeting, so a launch
// that started a process shows it through Capture, and three exchanges: one
// that answers the program's relaunch note, so a test can wait on the
// worker's output for the note having been read rather than on its record,
// which standin.Agent.Calls says not to poll while a worker reads; one that
// answers a status line, so a test can wait until every line sent before it
// has been read; and one that ends the worker on its own, which is the
// shape a probe tells from a worker its caller ended.
func workerStandIn(t *testing.T) *standin.Agent {
	t.Helper()
	return standin.NewWorker(t, standin.Script{Worker: &standin.WorkerScript{
		Greeting: "worker ready",
		Exchanges: []standin.Exchange{
			{Contains: "This is attempt", Reply: noteReadReply},
			{Contains: "status", Reply: "still working on it"},
			{Contains: "finish", Reply: "worker finished", Ends: true},
		},
	}}, agents.WithTerminationGrace(2*time.Second))
}

// noteReadReply is what the stand-in worker prints once it has read the
// relaunch note's first line, which is the line the capture is waited on.
const noteReadReply = "the relaunch note was read"

// waitCaptured polls the provider's in-memory Capture until the session's
// output carries want.
func waitCaptured(t *testing.T, p session.Provider, s *session.Session, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := p.Capture(s)
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}
		if strings.Contains(got.Text, want) {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the capture never carried %q; it is:\n%s", want, got.Text)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitLiveness polls the provider until the reference probes want.
func waitLiveness(t *testing.T, p session.Provider, ref string, want taskstate.Liveness) session.Observation {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := p.Probe(t.Context(), ref)
		if got.Liveness == want {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the reference never probed %s; it probes %+v", want, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// PRD principle P11, at the launch edge: the copy is asserted again at
// launch, so a copy retargeted between dispatch's assertion and the launch
// is refused there and no worker starts in it. The control is the same
// subject untouched, which launches: the refusal below is the assertion
// refusing and not the launch failing for some other reason. The retarget is
// a second remote added to the copy with real git, which the shape's exact
// remote set refuses with the wrong-shape token; the refusal is recorded as
// the isolation-refused event the dispatch's own refusal would be, anchored
// to the same history, the task resolves Failed from it, the attempt keeps
// no session, and the stand-in recorded no worker. The time-of-check gap
// that remains is between this assertion and the process starting.
func TestACopyRetargetedBetweenDispatchAndLaunchIsRefusedAndNotLaunched(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P11)
	ctx := t.Context()

	// The control: untouched, the dispatch launches.
	control := newLaunchSubject(t, workerStandIn(t))
	d, err := control.service.dispatchTask(ctx, control.task)
	if err != nil {
		t.Fatalf("dispatching the control: %v", err)
	}
	launched, err := control.service.launchTask(ctx, d)
	if err != nil {
		t.Fatalf("launching the control, whose copy was not touched: %v", err)
	}
	provider, held, ok := control.service.sessionOf(control.task)
	if !ok || held != launched {
		t.Fatalf("the service holds %v, %v for the launched control, want the session launchTask returned", held, ok)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := provider.Capture(launched)
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}
		if strings.Contains(got.Text, "worker ready") {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the control's worker never greeted; its capture is %q", got.Text)
		}
		time.Sleep(time.Millisecond)
	}
	attempts, err := control.records.TaskAttempts(ctx, control.task)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.Or("") != launched.Reference().String() {
		t.Fatalf("the control has attempts %+v (%v), want one carrying %s", attempts, err, launched.Reference())
	}

	// The subject: retargeted after the dispatch proved it.
	subject := newLaunchSubject(t, workerStandIn(t))
	d, err = subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	_, upstream, _ := heldSubject(t)
	heldGit(t, subject.copy, "remote", "add", "elsewhere", upstream)

	_, err = subject.service.launchTask(ctx, d)
	var refused *isolationRefused
	if !errors.As(err, &refused) || !errors.Is(err, tasks.ErrNotIsolated) {
		t.Fatalf("the launch answered %v, want the typed isolation refusal", err)
	}
	if refused.Reason != taskstate.RefusedWrongShape || refused.Attempt != 1 {
		t.Fatalf("the refusal is %+v, want wrong-shape on attempt 1", refused)
	}
	if _, _, ok := subject.service.sessionOf(subject.task); ok {
		t.Fatal("the service holds a session for a task whose launch was refused")
	}
	resolved, err := subject.service.tasks().Resolved(ctx, subject.task)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State.State != taskstate.Failed || resolved.State.Source != taskstate.SourceEvent {
		t.Fatalf("the task resolves %s from %s, want Failed from the refusal", resolved.State.State, resolved.State.Source)
	}
	events, err := subject.records.TaskEvents(ctx, subject.task, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var refusals int
	for _, e := range events {
		if e.Kind == isolationRefusedKind {
			refusals++
			if reason, err := taskstate.ParseIsolationRefusal(e.Detail); err != nil || reason != taskstate.RefusedWrongShape {
				t.Fatalf("the refusal carries %q (%v), want the wrong-shape token", e.Detail, err)
			}
		}
	}
	if refusals != 1 {
		t.Fatalf("the task has %d refusals recorded, want one", refusals)
	}
	attempts, err = subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.IsKnown() {
		t.Fatalf("the task has attempts %+v (%v), want the dispatch's one with no session", attempts, err)
	}
	for _, call := range subject.agent.Calls() {
		if call.Worker {
			t.Fatalf("a worker was started for the refused task: %s", call)
		}
	}
	// The slot keeps the dispatch's proof: a refused launch is not retried
	// behind it, which is dispatchDue's one attempt per task per service.
	if held, ok := subject.service.isolationOf(subject.task); !ok || !held.Proven() {
		t.Fatalf("the slot holds %+v, %v after the refused launch", held, ok)
	}
}

// An OpenAI-only operator pin refuses worker launch with a recorded
// route-refused event: the pin is Override, missing Need is
// *agents.CapabilityError, the task resolves Failed rather than staying
// Dispatched, and the attempt keeps no session and no route. Grants are not
// rewritten on the refusal path. The adapter is built here over the
// stand-in's own arguments so nothing it could start is this test binary
// running its tests again.
func TestAnOpenAIPinRefusesWorkerLaunchWithRecordedReason(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	agent := standin.New(t, standin.Script{})
	subject := newLaunchSubject(t, agent)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	openai, err := agents.OpenAIFactory(agents.WithOpenAIBinary(self)).New(ctx, agent.Arguments())
	if err != nil {
		t.Fatalf("building the openai adapter over the stand-in: %v", err)
	}
	subject.service.catalog = agents.NewCatalog(fixedRunner{runner: openai}, cursorFactory{runner: openai})
	subject.service.cfg.Agent = []string{"openai"}
	d, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	_, err = subject.service.launchTask(ctx, d)
	var refused *routeRefused
	if !errors.As(err, &refused) {
		t.Fatalf("the launch answered %v, want a *routeRefused", err)
	}
	var undeclared *agents.CapabilityError
	if !errors.As(err, &undeclared) || undeclared.Capability != agents.CapabilityWorkerSessions {
		t.Fatalf("the cause is %v, want a *CapabilityError naming %s", err, agents.CapabilityWorkerSessions)
	}
	if errors.Is(err, tasks.ErrNotIsolated) {
		t.Fatal("an adapter without a worker read as an isolation refusal")
	}
	if _, _, ok := subject.service.sessionOf(subject.task); ok {
		t.Fatal("the service holds a session nothing could have started")
	}
	resolved, err := subject.service.tasks().Resolved(ctx, subject.task)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State.State != taskstate.Failed || resolved.State.Source != taskstate.SourceEvent ||
		resolved.State.Basis.EventKind != routeRefusedKind {
		t.Fatalf("the task resolves %+v, want Failed from route-refused", resolved.State)
	}
	events, err := subject.records.TaskEvents(ctx, subject.task, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawRoute bool
	for _, e := range events {
		if e.Kind == isolationRefusedKind {
			t.Fatalf("a missing capability was recorded as an isolation refusal: %+v", e)
		}
		if e.Kind == routeRefusedKind {
			sawRoute = true
			if !strings.Contains(e.Detail, string(agents.CapabilityWorkerSessions)) {
				t.Fatalf("route-refused detail %q does not name %s", e.Detail, agents.CapabilityWorkerSessions)
			}
		}
	}
	if !sawRoute {
		t.Fatalf("no route-refused event in %+v", events)
	}
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.IsKnown() ||
		attempts[0].Adapter.IsKnown() || attempts[0].RouteReason.IsKnown() {
		t.Fatalf("the task has attempts %+v (%v), want the dispatch's one with no session and no route", attempts, err)
	}
	if held, ok := subject.service.isolationOf(subject.task); !ok || !held.Proven() {
		t.Fatalf("the slot holds %+v, %v, want the dispatch's proof intact", held, ok)
	}
}

// A successful launch records the routed adapter and reason on the attempt
// before the session reference, and still starts the worker under the
// ordinary Create path (GrantWorker unchanged).
func TestLaunchRecordsTheRoutedAdapterAndReason(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	subject := newLaunchSubject(t, workerStandIn(t))
	name := subject.agent.Runner().Name()
	subject.service.cfg.Agent = []string{name}
	d, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	launched, err := subject.service.launchTask(ctx, d)
	if err != nil {
		t.Fatalf("launching: %v", err)
	}
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("TaskAttempts = %+v, %v", attempts, err)
	}
	adapter, ok := attempts[0].Adapter.Get()
	if !ok || adapter != name {
		t.Fatalf("adapter = %q (known=%v), want %s", adapter, ok, name)
	}
	reason, ok := attempts[0].RouteReason.Get()
	if !ok || reason != agents.ReasonOverride {
		t.Fatalf("route reason = %q (known=%v), want %s", reason, ok, agents.ReasonOverride)
	}
	if skipped, ok := attempts[0].RouteSkipped.Get(); !ok || len(skipped) != 0 {
		t.Fatalf("route skipped = %v (known=%v), want the known empty list of a pin", skipped, ok)
	}
	ref, ok := attempts[0].SessionRef.Get()
	if !ok || ref != launched.Reference().String() {
		t.Fatalf("session = %q (known=%v), want %s", ref, ok, launched.Reference())
	}
}

// closeSessions ends every session the service launched and forgets it: the
// reference probes Dead through the same provider afterwards, by identity
// alone, and the service holds no session for the task.
func TestCloseEndsTheSessionsTheServiceLaunched(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	subject := newLaunchSubject(t, workerStandIn(t))
	d, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	launched, err := subject.service.launchTask(ctx, d)
	if err != nil {
		t.Fatalf("launching: %v", err)
	}
	provider, _, _ := subject.service.sessionOf(subject.task)
	ref := launched.Reference().String()
	if got := provider.Probe(ctx, ref); got.Liveness != taskstate.Live {
		t.Fatalf("before Close the session probes %+v, want Live", got)
	}
	for _, err := range subject.service.closeSessions() {
		t.Fatalf("closing the sessions: %v", err)
	}
	if _, _, ok := subject.service.sessionOf(subject.task); ok {
		t.Fatal("the service still holds the session it closed")
	}
	waitLiveness(t, provider, ref, taskstate.Dead)
	if errs := subject.service.closeSessions(); len(errs) != 0 {
		t.Fatalf("a second close answered %v, want nothing to close", errs)
	}
}

// An ordered agent list whose first entry runs but cannot host a worker
// launches the second, and the attempt records the choice once with the
// reason ordered and the first entry passed over as a missing capability,
// naming which, and nothing of the entry's error text.
func TestAnOrderedSkipIsRecordedWithItsReasonOnTheAttempt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	subject := newLaunchSubject(t, workerStandIn(t))
	worker := subject.agent.Runner()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	openai, err := agents.OpenAIFactory(agents.WithOpenAIBinary(self)).New(ctx, subject.agent.Arguments())
	if err != nil {
		t.Fatalf("building the openai adapter over the stand-in: %v", err)
	}
	subject.service.catalog = agents.NewCatalog(fixedRunner{runner: openai}, fixedRunner{runner: worker}, cursorFactory{runner: worker})
	subject.service.cfg.Agent = []string{openai.Name(), worker.Name()}
	d, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	launched, err := subject.service.launchTask(ctx, d)
	if err != nil {
		t.Fatalf("launching: %v", err)
	}
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("TaskAttempts = %+v, %v", attempts, err)
	}
	got := attempts[0]
	if adapter, ok := got.Adapter.Get(); !ok || adapter != worker.Name() {
		t.Fatalf("adapter = %q (known=%v), want %s", adapter, ok, worker.Name())
	}
	if reason, ok := got.RouteReason.Get(); !ok || reason != agents.ReasonOrdered {
		t.Fatalf("route reason = %q (known=%v), want %s", reason, ok, agents.ReasonOrdered)
	}
	want := []store.SkippedAgent{{Agent: openai.Name(), Reason: agents.SkipMissingCapability, Capability: string(agents.CapabilityWorkerSessions)}}
	if skipped, ok := got.RouteSkipped.Get(); !ok || !slices.Equal(skipped, want) {
		t.Fatalf("route skipped = %+v (known=%v), want %+v", got.RouteSkipped, ok, want)
	}
	if ref, ok := got.SessionRef.Get(); !ok || ref != launched.Reference().String() {
		t.Fatalf("session = %q (known=%v), want %s", ref, ok, launched.Reference())
	}
}

// A worker role on Opus 5.5 at context 1m starts the worker with
// --model claude-opus-5-5[1m], read off the command line the stand-in
// recorded, and the attempt records that identifier as the model it asked
// for. The window comes from the role the operator named, not from a
// launcher outside the program.
func TestAWorkerRoleStartsTheContextWindowItNames(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	subject := newLaunchSubject(t, claudeWorkerStandIn(t))
	name := subject.agent.Runner().Name()
	subject.service.cfg.Roles = config.Roles{config.RoleWorker: {
		Agent: name, Provider: "anthropic", Type: "opus", Version: "5.5", Effort: "high", ContextWindow: config.Context1M,
	}}
	d, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	launched, err := subject.service.launchTask(ctx, d)
	if err != nil {
		t.Fatalf("launching: %v", err)
	}
	provider, _, _ := subject.service.sessionOf(subject.task)
	waitCaptured(t, provider, launched, claudeWorkerStarted)
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("TaskAttempts = %+v, %v", attempts, err)
	}
	if model, ok := attempts[0].Model.Get(); !ok || model != "claude-opus-5-5[1m]" {
		t.Fatalf("the attempt asked for model %q (known=%v), want claude-opus-5-5[1m]", model, ok)
	}
	var started []string
	for _, call := range subject.agent.Calls() {
		if call.Worker {
			started = append(started, call.Model())
		}
	}
	if len(started) != 1 || started[0] != "claude-opus-5-5[1m]" {
		t.Fatalf("the worker was started with --model %q, want claude-opus-5-5[1m] once", started)
	}
}

// A worker role whose context window the routed adapter has not declared on
// that model is refused before the worker starts: a recorded route-refused
// naming the role and the window, the task Failed from it, and no worker
// started at the standard window in its place. A task that names no role
// resolves the worker role, and one that names a role is refused under its
// own name.
func TestAnUndeclaredWorkerContextWindowIsRefusedBeforeLaunch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		taskRole string
		role     config.Role
	}{
		{"haiku at 1m", "", config.Role{Provider: "anthropic", Type: "haiku", Version: "4.5", ContextWindow: config.Context1M}},
		{"a window with no model", "", config.Role{ContextWindow: config.Context1M}},
		{"a named role", "pages", config.Role{Provider: "anthropic", Type: "haiku", Version: "4.5", ContextWindow: config.Context1M}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			undeclaredWorkerWindowIsRefused(t, tc.taskRole, tc.role)
		})
	}
}

func undeclaredWorkerWindowIsRefused(t *testing.T, taskRole string, role config.Role) {
	t.Helper()
	ctx := t.Context()
	subject := newLaunchSubjectAs(t, claudeWorkerStandIn(t), taskRole)
	role.Agent = subject.agent.Runner().Name()
	subject.service.cfg.Roles = config.Roles{config.TaskRoleName(taskRole): role}
	d, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	_, err = subject.service.launchTask(ctx, d)
	var refused *routeRefused
	if !errors.As(err, &refused) || !errors.Is(err, agents.ErrUndeclaredModel) {
		t.Fatalf("the launch answered %v, want a *routeRefused over ErrUndeclaredModel", err)
	}
	resolved, err := subject.service.tasks().Resolved(ctx, subject.task)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State.State != taskstate.Failed || resolved.State.Basis.EventKind != routeRefusedKind {
		t.Fatalf("the task resolves %+v, want Failed from route-refused", resolved.State)
	}
	detail := routeRefusedDetail(t, subject)
	want := "the " + config.TaskRoleName(taskRole) + " role"
	if !strings.Contains(detail, want) || !strings.Contains(detail, "context 1m") {
		t.Fatalf("route-refused detail %q does not name %s and context 1m", detail, want)
	}
	if _, _, ok := subject.service.sessionOf(subject.task); ok {
		t.Fatal("the service holds a session for the refused launch")
	}
	for _, call := range subject.agent.Calls() {
		if call.Worker {
			t.Fatalf("a worker was started for the refused launch: %s", call)
		}
	}
}

// routeRefusedDetail is the detail of the task's last route-refused event,
// empty when it has none.
func routeRefusedDetail(t *testing.T, subject *launchSubject) string {
	t.Helper()
	events, err := subject.records.TaskEvents(t.Context(), subject.task, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var detail string
	for _, e := range events {
		if e.Kind == routeRefusedKind {
			detail = e.Detail
		}
	}
	return detail
}

// claudeWorkerStandIn is a stand-in worker under the shipped Claude Code
// adapter, so the model identifier a launch translates is the one that
// adapter puts on the command line. It answers the subject's first turn,
// whose instructions are "work", with claudeWorkerStarted, which a test
// waits on before it reads the worker's record.
func claudeWorkerStandIn(t *testing.T) *standin.Agent {
	t.Helper()
	return standin.New(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "work", Reply: claudeWorkerStarted}},
	}}, agents.WithTerminationGrace(2*time.Second))
}

// claudeWorkerStarted is what claudeWorkerStandIn answers its first turn with.
const claudeWorkerStarted = "the first turn was read"
