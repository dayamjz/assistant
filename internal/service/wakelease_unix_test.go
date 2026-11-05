//go:build unix

package service_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// held is a process holding the wake stream as assistant coordinator
// listen does: this test binary under holdVerb, which a test can stop and
// continue with a signal.
type held struct {
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	exited chan error
}

// holdStream starts a process holding the wake stream of the service on
// socket and returns once the stream opened with its gap. The process is
// ended by its pid when the test ends, continued first so a stopped one can
// take the signal.
func holdStream(t *testing.T, socket string) held {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(testCommand(t), holdVerb, socket, ready)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the listen: %v", err)
	}
	h := held{cmd: cmd, stderr: &stderr, exited: make(chan error, 1)}
	go func() { h.exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGCONT)
		_ = cmd.Process.Kill()
		<-h.exited
	})
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return h
		}
		select {
		case err := <-h.exited:
			t.Fatalf("the listen ended before its stream opened: %v: %s", err, stderr.String())
		default:
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the listen's stream never opened")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A stopped listen is detached three beacon intervals after its last beat,
// and what was handed out under it comes back, which is GAP-05's residual on
// delivery attachment closed for a stream that stops beating; and a second
// stream replaces the first rather than counting beside it.
//
// The home is configured with a one-second supervision_beacon_interval. A
// process holds the wake stream the way assistant coordinator listen does,
// and the worker is ended so the loop queues its terminal-gone wake. A drain
// while the stream is held hands it out, and a second does not: the
// coordinator is attached. The listen is then sent SIGSTOP, which is a
// coordinator whose host died with the listen process left behind and not
// reading, and drains made with no stream of their own are polled: none
// hands the row out again before two intervals have passed since the stop
// (the last beat was at most one interval before it), and one does within a
// bound, which is the lease expiring and not the process ending, since the
// process is still there, stopped. Continued, the listen reads that its
// stream ended because its lease expired.
//
// A drain from a new stream is then handed the row, and holding that stream
// past three intervals does not detach it: a listen that beats keeps its
// lease, the positive control for the expiry. A second stream opened beside
// it replaces it: the first is ended as replaced, and the row is handed out
// again under the second. Closing the second leaves nothing attached, so the
// next drain hands the row out again, which two overlapping streams counted
// as one stretch would not.
//
// It cites P9: a coordinator that went away does not keep a wake from
// reaching the one that replaced it.
func TestAStoppedListenIsDetachedAndAReplacedOneDoesNotCountTwice(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P9)
	const interval = time.Second
	launchedWorker(t, "task/lease", func(w launched) {
		socket := w.running.service.Socket()
		listen := holdStream(t, socket)

		finishWorker(t, w, w.task)
		var gone int64
		deadline := time.Now().Add(60 * time.Second)
		for gone == 0 {
			for _, wake := range mustList(t, w.running.client).Wakes {
				if wake.TaskID == w.task && wake.Kind == string(supervisor.KindTerminalGone) {
					gone = wake.ID
				}
			}
			if gone == 0 && !time.Now().Before(deadline) {
				t.Fatal("the loop never queued the ended worker's terminal-gone wake")
			}
			time.Sleep(50 * time.Millisecond)
		}
		if again := mustList(t, w.running.client); listed(again, gone) {
			t.Fatalf("a second drain while the listen is held handed wake %d out again: %+v", gone, again)
		}

		if err := listen.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatalf("stopping the listen: %v", err)
		}
		stopped := time.Now()
		for {
			got := mustList(t, w.running.client)
			if handedOut(got, gone, 2) {
				if since := time.Since(stopped); since < 2*interval {
					t.Fatalf("wake %d was handed out again %s after the listen stopped, before its lease could have expired", gone, since)
				}
				break
			}
			if time.Since(stopped) > 30*time.Second {
				t.Fatalf("wake %d was never handed out again after the listen stopped: %+v", gone, got)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err := listen.cmd.Process.Signal(syscall.SIGCONT); err != nil {
			t.Fatalf("continuing the listen: %v", err)
		}
		select {
		case err := <-listen.exited:
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 4 || strings.TrimSpace(listen.stderr.String()) != "lease-expired" {
				t.Fatalf("the continued listen ended with %v and %q, want its stream ended as lease-expired", err, listen.stderr.String())
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the continued listen still holds a stream its lease should have ended")
		}

		first, err := w.running.client.SubscribeTo(t.Context(), ipc.MethodWakesSubscribe, nil)
		if err != nil {
			t.Fatalf("opening a new wake stream: %v", err)
		}
		defer first.Close()
		if !handedOut(mustList(t, w.running.client), gone, 3) {
			t.Fatalf("a drain from a new stream was not handed wake %d", gone)
		}
		time.Sleep(4 * interval)
		if again := mustList(t, w.running.client); listed(again, gone) {
			t.Fatalf("a stream that beats was detached past three intervals: wake %d handed out again: %+v", gone, again)
		}

		other, err := ipc.Dial(t.Context(), socket, ipc.ClientConfig{})
		if err != nil {
			t.Fatalf("dialing %s: %v", socket, err)
		}
		defer func() { _ = other.Close() }()
		second, err := other.SubscribeTo(t.Context(), ipc.MethodWakesSubscribe, nil)
		if err != nil {
			t.Fatalf("opening a second wake stream: %v", err)
		}
		defer second.Close()
		for {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			_, err := first.Recv(ctx)
			cancel()
			if err == nil {
				continue
			}
			var wire *ipc.Error
			if !errors.As(err, &wire) || wire.Code != "stream-replaced" {
				t.Fatalf("the first stream ended with %v once a second opened, want it ended as stream-replaced", err)
			}
			break
		}
		if !handedOut(mustList(t, w.running.client), gone, 4) {
			t.Fatalf("a drain under the stream that replaced the first was not handed wake %d", gone)
		}
		second.Close()
		deadline = time.Now().Add(30 * time.Second)
		for !handedOut(mustList(t, w.running.client), gone, 5) {
			if !time.Now().Before(deadline) {
				t.Fatalf("wake %d was not handed out again once the one stream left was closed", gone)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}
