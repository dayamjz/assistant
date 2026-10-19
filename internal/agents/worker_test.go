package agents_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/route"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/vcs"
)

// workerSpec is a valid spec in a directory the test owns.
func workerSpec(t *testing.T) agents.WorkerSpec {
	t.Helper()
	return agents.WorkerSpec{Dir: t.TempDir(), Instructions: "work on the task"}
}

// awaitWorkerOutput polls the handle until its capture carries want.
func awaitWorkerOutput(t *testing.T, w agents.Worker, want string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := w.Capture().Text
		if strings.Contains(got, want) {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker's output never carried %q; it is:\n%s", want, got)
		}
		time.Sleep(time.Millisecond)
	}
}

// OpenWorker reads the declaration before the type, so a shipped adapter
// that declares no worker sessions, the Cursor one, is refused by name and no
// process is started; and a stub that carries the mechanism without declaring
// it is refused the same way rather than served.
func TestOpenWorkerConsultsTheDeclarationBeforeTheMechanism(t *testing.T) {
	// Cursor, Codex, and Grok declare no worker. The refusal is typed and
	// names the capability; the mechanism is not reached.
	for _, build := range []struct {
		name string
		new  func() (agents.Runner, error)
	}{
		{agents.CursorName, func() (agents.Runner, error) {
			return agents.CursorFactory(agents.WithCursorBinary(helperBinary(t))).New(t.Context(), nil)
		}},
		{agents.CodexName, func() (agents.Runner, error) {
			return agents.CodexFactory(agents.WithCodexBinary(helperBinary(t))).New(t.Context(), nil)
		}},
		{agents.GrokName, func() (agents.Runner, error) {
			return agents.GrokFactory(agents.WithGrokBinary(helperBinary(t))).New(t.Context(), nil)
		}},
	} {
		runner, err := build.new()
		if err != nil {
			t.Fatalf("building the %s adapter: %v", build.name, err)
		}
		_, err = agents.OpenWorker(t.Context(), runner, workerSpec(t))
		var refusal *agents.CapabilityError
		if !errors.As(err, &refusal) {
			t.Fatalf("OpenWorker on the %s adapter answered %v, want a *CapabilityError", build.name, err)
		}
		if refusal.Capability != agents.CapabilityWorkerSessions || refusal.Agent != build.name {
			t.Errorf("the refusal names %q on %q, want %q on %q",
				refusal.Capability, refusal.Agent, agents.CapabilityWorkerSessions, build.name)
		}
	}

	undeclared := &workerStubRunner{stubRunner: &stubRunner{name: "undeclared"}}
	_, err := agents.OpenWorker(t.Context(), undeclared, workerSpec(t))
	if errors.Is(err, errStubWorkerReached) {
		t.Fatal("OpenWorker reached the worker mechanism of an adapter that declared nothing")
	}
	if !errors.Is(err, agents.ErrUndeclaredCapability) {
		t.Errorf("OpenWorker answered %v, want ErrUndeclaredCapability", err)
	}

	declared := (&stubFactory{
		name: "declared", available: true, workers: true,
		declares: []agents.Capability{agents.CapabilityWorkerSessions},
	}).runner()
	if _, err := agents.OpenWorker(t.Context(), declared, workerSpec(t)); !errors.Is(err, errStubWorkerReached) {
		t.Errorf("OpenWorker on a declared adapter answered %v, want the adapter's own mechanism", err)
	}

	boaster := &stubRunner{name: "boaster", declared: agents.Declare(agents.CapabilityWorkerSessions)}
	worker, err := agents.OpenWorker(t.Context(), boaster, workerSpec(t))
	if worker != nil {
		t.Errorf("OpenWorker returned %v for an adapter with no mechanism, want nothing", worker)
	}
	if !errors.Is(err, agents.ErrAdapterDeclaration) {
		t.Errorf("OpenWorker answered %v for an adapter with no mechanism, want ErrAdapterDeclaration", err)
	}
}

// A GIT_DIR set in the parent does not reach the worker, and neither does one
// the spec itself carries. The positive control is the run path on the same
// runner, with the same parent environment: there the variable arrives,
// which is the residual gap the package documentation names, and which is
// what shows the worker's absence of it is the builder's doing rather than
// the parent's.
//
// This is the "can be pointed at its own repository" half of PRD principle
// P11 and it does not claim the principle: P11 is a launch refused until the
// directory is proven an isolated copy, and nothing here asks about the
// directory, so the principle stays declared unclaimed in internal/principles.
func TestTheWorkerEnvironmentCarriesNoRedirectingGitVariable(t *testing.T) {
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	t.Setenv("GIT_DIR", elsewhere)
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", "/evil")
	t.Setenv("AGENTS_A_VARIABLE_THE_WORKER_KEEPS", "kept")

	runner, err := agents.ScriptedFactory(
		agents.WithBinary(helperBinary(t)),
		agents.WithTerminationGrace(300*time.Millisecond),
	).New(t.Context(), []string{helperModeFlag + "worker-environment"})
	if err != nil {
		t.Fatalf("building the scripted runner over the helper: %v", err)
	}

	read := func(t *testing.T, env []string) map[string]string {
		t.Helper()
		out := make(map[string]string, len(env))
		for _, entry := range env {
			name, value, _ := strings.Cut(entry, "=")
			out[name] = value
		}
		return out
	}

	// The control: the run path hands the parent's GIT_DIR through.
	result, err := runner.Run(t.Context(), agents.PurposeIntent, invocation(t, agents.ShapeText,
		map[string]string{helperModeVar: "environment"}))
	if err != nil {
		t.Fatalf("the control invocation failed: %v", err)
	}
	var runEnv []string
	if err := json.Unmarshal([]byte(result.Text), &runEnv); err != nil {
		t.Fatalf("the helper did not report the run's environment: %v (%q)", err, result.Text)
	}
	if got := read(t, runEnv)["GIT_DIR"]; got != elsewhere {
		t.Fatalf("the run path gave the agent GIT_DIR=%q, want the parent's %q; the hazard this "+
			"test is about is absent on the control, so the worker half below proves nothing",
			got, elsewhere)
	}

	spec := workerSpec(t)
	spec.Env = map[string]string{
		"GIT_DIR":                    filepath.Join(t.TempDir(), "from-the-spec.git"),
		"AGENTS_A_VARIABLE_THE_SPEC": "added",
	}
	worker, err := agents.OpenWorker(t.Context(), runner, spec)
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()
	text := awaitWorkerOutput(t, worker, "\n")
	line, _, _ := strings.Cut(text, "\n")
	var workerEnv []string
	if err := json.Unmarshal([]byte(line), &workerEnv); err != nil {
		t.Fatalf("the helper did not report the worker's environment: %v (%q)", err, line)
	}
	// The worker's environment is the parent's, as StripRedirecting leaves
	// it, plus the spec's own: nothing kept is lost and nothing removed
	// arrives. It is also exactly what WorkerEnvironment computes from the
	// same inputs, which is what lets a launcher assert the environment a
	// worker will get without a second builder.
	got := read(t, workerEnv)
	stripped := read(t, vcs.StripRedirecting(os.Environ()))
	for name := range stripped {
		if _, ok := got[name]; !ok {
			t.Errorf("the worker lost %s, which StripRedirecting keeps", name)
		}
	}
	for name := range got {
		if _, kept := stripped[name]; !kept && name != "AGENTS_A_VARIABLE_THE_SPEC" {
			t.Errorf("the worker was given %s, which the builder removes", name)
		}
	}
	if computed := read(t, agents.WorkerEnvironment(os.Environ(), spec.Env)); !reflect.DeepEqual(computed, got) {
		t.Errorf("WorkerEnvironment computes %v, and the worker was started with %v", computed, got)
	}
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
		if value, ok := got[name]; ok {
			t.Errorf("the worker was given %s=%q", name, value)
		}
	}
	if got["AGENTS_A_VARIABLE_THE_WORKER_KEEPS"] != "kept" {
		t.Error("the worker lost a variable that redirects nothing")
	}
	if got["AGENTS_A_VARIABLE_THE_SPEC"] != "added" {
		t.Error("the worker was not given the spec's own variable")
	}
}

// TestNoRouteFromAWorkerHandleToAFixerSession is P4 as the worker handle
// expresses it: what whoever started a worker holds is a handle over a
// process, and the type graph out of it reaches no Runner, no SessionRunner
// and no Fixer. The control is the walk finding the route out of
// agents.Resolution, and finding the route out of WorkerRunner itself, which
// embeds Runner and so is exactly what the handle must not be.
//
// The blind spot is the walk's own and route's documentation names it: a
// value held in an interface field is invisible to it. Worker holds none.
func TestNoRouteFromAWorkerHandleToAFixerSession(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)

	if found := route.ToFixerSession(reflect.TypeOf(agents.Resolution{})); len(found) == 0 {
		t.Fatal("the walk finds no route out of agents.Resolution, so it is inspecting nothing")
	}
	if found := route.ToFixerSession(reflect.TypeFor[agents.WorkerRunner]()); len(found) == 0 {
		t.Fatal("the walk finds no route out of agents.WorkerRunner, which embeds Runner, so it is inspecting nothing")
	}
	for _, root := range []reflect.Type{
		reflect.TypeFor[agents.Worker](),
		reflect.TypeOf(agents.WorkerSpec{}),
		reflect.TypeOf(agents.Capture{}),
	} {
		if routes := route.ToFixerSession(root); len(routes) != 0 {
			t.Errorf("a caller holding %s can reach a fixer session:\n  %s", root, strings.Join(routes, "\n  "))
		}
	}
}

// The worker entry point is on the fixer side of the split by its types: Start
// takes no purpose, and a WorkerSpec has no field a shape, a purpose or a
// session could be named in, so a worker cannot be told it is a review in
// either of the ways an Invocation can. The field scan is a naming check and
// nothing more; the route walk above is the evidence that the handle reaches
// no session, and this holds only the shape of the entry point's signature.
func TestAWorkerCannotBeGivenAPurposeOrAShape(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)

	start, ok := reflect.TypeFor[agents.WorkerRunner]().MethodByName("Start")
	if !ok {
		t.Fatal("WorkerRunner has no Start method")
	}
	wantIn := []reflect.Type{reflect.TypeFor[context.Context](), reflect.TypeFor[agents.WorkerSpec]()}
	if got := methodIn(start.Type); !reflect.DeepEqual(got, wantIn) {
		t.Errorf("WorkerRunner.Start takes %v, want exactly %v", got, wantIn)
	}
	spec := reflect.TypeFor[agents.WorkerSpec]()
	for i := range spec.NumField() {
		field := spec.Field(i)
		for _, word := range []string{"session", "shape", "purpose", "review"} {
			if strings.Contains(strings.ToLower(field.Name), word) ||
				strings.Contains(strings.ToLower(field.Type.String()), word) {
				t.Errorf("WorkerSpec.%s (%s) would let a caller say a worker is a %s", field.Name, field.Type, word)
			}
		}
	}
}

// A spec that cannot be started as written is refused before any process
// starts, with the class every pre-start refusal here belongs to.
func TestAnUnstartableWorkerSpecIsRefused(t *testing.T) {
	runner := newWorkerRunner(t)
	cases := []struct {
		name string
		spec agents.WorkerSpec
		want string
	}{
		{"no instructions", agents.WorkerSpec{Dir: t.TempDir()}, "Instructions"},
		{"no directory", agents.WorkerSpec{Instructions: "work"}, "Dir"},
		{"relative directory", agents.WorkerSpec{Dir: "relative", Instructions: "work"}, "Dir"},
		{"bad variable name", agents.WorkerSpec{Dir: t.TempDir(), Instructions: "work", Env: map[string]string{"A=B": "c"}}, "Env"},
		{"oversize instructions", agents.WorkerSpec{Dir: t.TempDir(), Instructions: strings.Repeat("x", agents.MaxPromptBytes+1)}, "Instructions"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			worker, err := agents.OpenWorker(t.Context(), runner, c.spec)
			if worker != nil {
				worker.Close()
				t.Fatal("an unstartable spec started a worker")
			}
			if !errors.Is(err, agents.ErrInvalidInvocation) {
				t.Fatalf("refused with %v, want ErrInvalidInvocation", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal reads %q, want it to name %s", err, c.want)
			}
		})
	}
}

// A worker that exits on its own closes Done without anyone closing it, and a
// send after that is refused rather than reported delivered.
func TestAWorkerThatExitsOnItsOwnIsDoneAndRefusesASend(t *testing.T) {
	runner, err := agents.ScriptedFactory(
		agents.WithBinary(helperBinary(t)),
		agents.WithTerminationGrace(300*time.Millisecond),
	).New(t.Context(), []string{helperModeFlag + "raw"})
	if err != nil {
		t.Fatalf("building the scripted runner over the helper: %v", err)
	}
	spec := workerSpec(t)
	spec.Env = map[string]string{helperStdoutVar: "goodbye", helperExitVar: "0"}
	worker, err := agents.OpenWorker(t.Context(), runner, spec)
	if err != nil {
		t.Fatalf("starting a worker that exits at once: %v", err)
	}
	defer worker.Close()
	select {
	case <-worker.Done():
	case <-time.After(promptly):
		t.Fatal("a worker that exited on its own never reported Done")
	}
	if got := worker.Capture().Text; got != "goodbye" {
		t.Errorf("the capture is %q, want what the worker wrote", got)
	}
	if err := worker.Send(t.Context(), "are you there"); !errors.Is(err, agents.ErrWorkerEnded) {
		t.Errorf("a send to an exited worker answered %v, want ErrWorkerEnded", err)
	}
}

// Capture keeps the most recent output within the runner's limit and counts
// what it let go, so a worker that talks for hours costs a bounded amount of
// memory and a reader is told that the beginning is missing.
func TestCaptureKeepsTheTailWithinTheLimitAndCountsTheRest(t *testing.T) {
	greeting := strings.Repeat("abcdefghij", 10)
	// Every line is answered, so the instructions draw a reply rather than
	// the stand-in's note about an unscripted line, and what the worker wrote
	// is known in full.
	agent := standin.NewWorker(t, standin.Script{Worker: &standin.WorkerScript{
		Greeting:  greeting,
		Exchanges: []standin.Exchange{{Reply: "klmnopqrst"}},
	}}, agents.WithMaxOutput(32), agents.WithTerminationGrace(300*time.Millisecond))
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), workerSpec(t))
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()

	full := greeting + "\nklmnopqrst\n"
	awaitWorkerOutput(t, worker, full[len(full)-32:])
	got := worker.Capture()
	if got.Text != full[len(full)-32:] {
		t.Errorf("the capture kept %q, want the last 32 bytes %q", got.Text, full[len(full)-32:])
	}
	if want := int64(len(full) - 32); got.Dropped != want {
		t.Errorf("the capture dropped %d bytes, want %d", got.Dropped, want)
	}
}

// Instructions reach the worker first and in full, on standard input, with a
// newline after them, so a worker reading lines sees every line of them
// before anything sent afterwards.
func TestInstructionsAreDeliveredBeforeAnythingSent(t *testing.T) {
	// Each line the test waits for is answered by the worker, so the wait is
	// on the worker's output and the record is read once afterwards; a test
	// that polled the record while the worker still rewrote it ended the
	// worker on windows-latest, which standin.Calls' doc explains.
	agent := standin.NewWorker(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{
			{Contains: "second", Reply: "saw the second line"},
			{Contains: "third", Reply: "saw the third line"},
		},
	}}, agents.WithTerminationGrace(300*time.Millisecond))
	spec := workerSpec(t)
	spec.Instructions = "first line of the instructions\nsecond line of the instructions\n"
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), spec)
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()
	awaitWorkerOutput(t, worker, "saw the second line")
	if err := worker.Send(t.Context(), "and a third"); err != nil {
		t.Fatalf("sending: %v", err)
	}
	awaitWorkerOutput(t, worker, "saw the third line")
	calls := agent.Calls()
	if len(calls) != 1 {
		t.Fatalf("the stand-in recorded %d calls, want one: %s", len(calls), calls)
	}
	want := []string{"first line of the instructions", "second line of the instructions", "and a third"}
	if !reflect.DeepEqual(calls[0].Lines, want) {
		t.Errorf("the worker read %q, want %q", calls[0].Lines, want)
	}
}

// A send its context cut off keeps its place: the bytes already in the pipe
// are followed by the rest of that write, a send made while it is pending
// waits behind it and writes nothing when its own context ends first, and a
// send made once it has drained follows it whole. The worker here reads
// nothing until told, so the first send fills the pipe and blocks, and what
// comes out afterwards is the order the pipe holds.
func TestASendCutOffByItsContextKeepsItsPlaceInTheOrder(t *testing.T) {
	runner, err := agents.ScriptedFactory(
		agents.WithBinary(helperBinary(t)),
		agents.WithTerminationGrace(300*time.Millisecond),
	).New(t.Context(), []string{helperModeFlag + "worker-echo-when"})
	if err != nil {
		t.Fatalf("building the scripted runner over the helper: %v", err)
	}
	proceed := filepath.Join(t.TempDir(), "go")
	spec := workerSpec(t)
	spec.Instructions = "instructions"
	spec.Env = map[string]string{helperGoVar: proceed}
	worker, err := agents.OpenWorker(t.Context(), runner, spec)
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()

	// Far more than a pipe buffers, so the write cannot finish until the
	// worker reads.
	first := strings.Repeat("a", 1<<20)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	err = worker.Send(ctx, first)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a send the worker does not read answered %v, want the context's deadline", err)
	}

	ctx, cancel = context.WithTimeout(t.Context(), 300*time.Millisecond)
	err = worker.Send(ctx, "second, which must not jump the queue")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a send behind a pending one answered %v, want the context's deadline", err)
	}

	if err := os.WriteFile(proceed, nil, 0o600); err != nil {
		t.Fatalf("telling the worker to read: %v", err)
	}
	if err := worker.Send(t.Context(), "third"); err != nil {
		t.Fatalf("a send once the pending write can drain failed: %v", err)
	}
	got := awaitWorkerOutput(t, worker, "third\n")
	if want := "instructions\n" + first + "\nthird\n"; got != want {
		t.Errorf("the worker read %d bytes in an order other than instructions, first, third; "+
			"it contains the second send: %t", len(got), strings.Contains(got, "second"))
	}
}

// Every sender waiting on a worker honours its own context, whether it is
// behind a pending write or behind another sender: with the pipe full and
// the worker reading nothing, two sends started together both return their
// own deadline promptly, and the worker is still alive afterwards.
func TestEveryQueuedSendHonoursItsOwnContext(t *testing.T) {
	runner, err := agents.ScriptedFactory(
		agents.WithBinary(helperBinary(t)),
		agents.WithTerminationGrace(300*time.Millisecond),
	).New(t.Context(), []string{helperModeFlag + "worker-echo-when"})
	if err != nil {
		t.Fatalf("building the scripted runner over the helper: %v", err)
	}
	spec := workerSpec(t)
	spec.Env = map[string]string{helperGoVar: filepath.Join(t.TempDir(), "never")}
	worker, err := agents.OpenWorker(t.Context(), runner, spec)
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	err = worker.Send(ctx, strings.Repeat("a", 1<<20))
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a send the worker does not read answered %v, want the context's deadline", err)
	}

	results := make(chan error, 2)
	for range 2 {
		go func() {
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			results <- worker.Send(ctx, "queued")
		}()
	}
	for i := range 2 {
		select {
		case err := <-results:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("queued send %d answered %v, want its own context's deadline", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("queued send %d did not return within five seconds of its 300ms deadline", i)
		}
	}
	select {
	case <-worker.Done():
		t.Fatal("the worker ended, and nothing here asked it to")
	default:
	}
}
