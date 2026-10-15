package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
)

// Which exit a failure from the service ends with is decided by the code it
// crossed the wire with, the same for every method: invalid-request is
// incorrect usage, and every other code is an operational failure.
//
// The service here is a stand-in behind a real ipc.Server on the home's
// socket, so each answer is put on the wire by the server's own encoding and
// read back by the client cli.Run dials, never stated as a typed value. It
// answers assistant runs --usage, whose method is open, with each code the
// protocol carries in turn. The real service raises only invalid-request and
// internal for that method; the others are codes other methods raise, and
// the mapping under test reads the code and never the method, which is the
// property this holds it to. Each answer also has to reach the caller's
// document with its code, so a case cannot pass because the request never
// got as far as the stand-in. invalid-request is the case that moves, and
// the rest are the control that the mapping did not widen into every
// failure.
func TestEveryInvalidRequestExitsAsUsageAndNoOtherCodeDoes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		code ipc.Code
		err  error
		want machine.Code
	}{
		{ipc.CodeInvalidRequest, ipc.ErrInvalidRequest, machine.ExitUsage},
		{ipc.CodeRefused, ipc.ErrRefused, machine.ExitFailure},
		{ipc.CodeInternal, errors.New("a store read failed"), machine.ExitFailure},
		{ipc.CodeWorker, ipc.ErrWorker, machine.ExitFailure},
		{ipc.CodeNotWorker, ipc.ErrNotWorker, machine.ExitFailure},
		{ipc.CodeUnavailable, ipc.ErrUnavailable, machine.ExitFailure},
		{ipc.CodeContained, ipc.ErrContained, machine.ExitFailure},
		{ipc.CodeUnidentifiedPeer, ipc.ErrUnidentifiedPeer, machine.ExitFailure},
		{ipc.CodeUnknownMethod, ipc.ErrUnknownMethod, machine.ExitFailure},
		{ipc.CodeConnectionBusy, ipc.ErrConnectionBusy, machine.ExitFailure},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			t.Parallel()
			h := newHome(t)
			answering(t, h, fmt.Errorf("%w: the stand-in's answer", tc.err))
			dir := t.TempDir()

			structured := run(t, h, dir, "--json", "runs", "--usage=24h")
			if structured.code != tc.want {
				t.Fatalf("a %s answer exited %s, want %s:\n%s%s", tc.code, structured.code, tc.want, structured.stdout, structured.stderr)
			}
			var failure machine.Failure
			if err := json.Unmarshal([]byte(structured.stdout), &failure); err != nil {
				t.Fatalf("a %s answer is not a failure document: %v\n%s", tc.code, err, structured.stdout)
			}
			if failure.Code != string(tc.code) {
				t.Fatalf("the failure crossed the wire as %q, want %q: %s", failure.Code, tc.code, failure.Error)
			}

			if plain := run(t, h, dir, "runs", "--usage=24h"); plain.code != tc.want {
				t.Fatalf("a %s answer read out exited %s, want %s:\n%s", tc.code, plain.code, tc.want, plain.stderr)
			}
		})
	}
}

// answering serves the home's socket with a handler that answers every
// request with err, through the protocol's own server.
func answering(t *testing.T, h *home.Home, err error) {
	t.Helper()
	if cerr := h.Create(); cerr != nil {
		t.Fatal(cerr)
	}
	events, perr := ipc.NewPublisher(ipc.PublisherConfig{})
	if perr != nil {
		t.Fatal(perr)
	}
	server, serr := ipc.NewServer(ipc.ServerConfig{
		Handler: ipc.HandlerFunc(func(context.Context, ipc.Request) (json.RawMessage, error) {
			return nil, err
		}),
		Ancestry: uncontained{},
		Events:   events,
	})
	if serr != nil {
		t.Fatal(serr)
	}
	listener, lerr := net.Listen("unix", h.Socket())
	if lerr != nil {
		if !platformIdentifiesPeers() {
			t.Skipf("%s has no local socket transport to serve the protocol over: %v", runtime.GOOS, lerr)
		}
		t.Fatal(lerr)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the stand-in's server did not return after Close")
		}
	})
}

// uncontained places no caller inside a validation stage. The one method the
// test calls is open, so the server never asks it.
type uncontained struct{}

// Contained answers that the caller is not contained.
func (uncontained) Contained(context.Context, ipc.Credentials) (ipc.Containment, error) {
	return ipc.Containment{}, nil
}
