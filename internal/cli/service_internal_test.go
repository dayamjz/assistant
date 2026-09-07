package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// A restart is not over until the replacement is serving. The outgoing service
// is still bound to the home's socket when it accepts the request, so a
// readiness check that only asks whether something answers is answered by the
// service on its way out, and the command reports readiness before the
// successor exists.
func TestReadinessIsNotSatisfiedByTheServiceItWasToldToReplace(t *testing.T) {
	h := newTestHome(t)
	in := &invocation{
		env:  Environment{Stdout: io.Discard, Stderr: io.Discard, Getenv: func(string) string { return "" }},
		home: h,
	}

	outgoing := serveForTest(t, h)
	first, err := in.serviceHealth(t.Context())
	if err != nil {
		t.Fatalf("asking the service for a readiness answer: %v", err)
	}
	if first.Instance == "" {
		t.Fatal("a readiness answer names no serving process, so no successor could be told from it")
	}

	// A wait for a replacement is not satisfied by the service being replaced.
	bounded, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if state, err := in.waitReady(bounded, first.Instance); err == nil {
		t.Fatalf("the wait for a replacement was satisfied by the outgoing service: %+v", state)
	}

	// The successor is a different process and answers as one, so the same
	// wait finishes once it is serving.
	if err := outgoing.Close(); err != nil {
		t.Fatalf("closing the outgoing service: %v", err)
	}
	serveForTest(t, h)
	second, err := in.serviceHealth(t.Context())
	if err != nil {
		t.Fatalf("asking the replacement for a readiness answer: %v", err)
	}
	if second.Instance == first.Instance {
		t.Fatalf("two services of one home answer as the same process %q", second.Instance)
	}
	if _, err := in.waitReady(t.Context(), first.Instance); err != nil {
		t.Fatalf("the wait was not satisfied by the replacement: %v", err)
	}
}

// A verb that answers nothing writes nothing. assistant watch is the one that
// does, and a consumer reading one document per line must not be handed a
// trailing document that decodes to nothing.
func TestAVerbThatAnswersNothingWritesNoDocument(t *testing.T) {
	t.Parallel()
	var out, errs bytes.Buffer
	in := &invocation{env: Environment{Stdout: &out, Stderr: &errs}, json: true}
	if code := render(in, nil, nil); code != machine.ExitOK {
		t.Fatalf("answering nothing exited %s, want ok", code)
	}
	if out.Len() != 0 {
		t.Fatalf("a verb that answered nothing wrote %q to standard output", out.String())
	}
}

// newTestHome returns a home root short enough to hold a local socket path.
func newTestHome(t *testing.T) *home.Home {
	t.Helper()
	root, err := os.MkdirTemp("", "h")
	if err != nil {
		t.Fatalf("making a home root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	return h
}

// serveForTest opens a service on a home and serves it, returning it so a test
// whose subject is the replacement can close it when it chooses. Closing twice
// is what Close is built for, so the cleanup is unconditional.
func serveForTest(t *testing.T, h *home.Home) *service.Service {
	t.Helper()
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}
	running, err := service.Open(t.Context(), service.Options{
		Home:      h,
		NewStages: stages.All,
		NewFixer:  stages.PendingFixer,
		Build:     build,
	})
	if err != nil {
		requiresLocalSocketHere(t, err)
		t.Fatalf("opening the service: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- running.Serve(context.Background()) }()
	t.Cleanup(func() {
		_ = running.Close()
		if err := <-served; err != nil {
			t.Errorf("serving: %v", err)
		}
	})
	return running
}

// requiresLocalSocketHere skips a test whose service could not bind its
// socket, on the written-down platform predicate internal/ipc's own tests use.
// A platform that has the transport is one where a failure to bind is a
// failure rather than a skip.
func requiresLocalSocketHere(t *testing.T, cause error) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("%s has no local socket transport to serve this protocol over: %v", runtime.GOOS, cause)
	}
}
