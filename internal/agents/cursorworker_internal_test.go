package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

// Interrupt returns only after the session/prompt result arrives. Answering
// session/cancel first and holding stopReason cancelled until the test
// releases it would leave a mutation that returns true right after write
// green against every other interrupt test, because those settle in the
// same receive burst as the cancel. Not parallel: it swaps
// waitAfterCursorInterruptCancel.
func TestCursorInterruptWaitsForCancelledAfterCancelReturns(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	turn := make(chan acpMessage, 1)
	w := &acpWorker{
		procWorker: &procWorker{
			stdin:        stdinW,
			out:          &tailWriter{limit: 1 << 20},
			availability: &workerAvailability{},
			done:         make(chan struct{}),
			slot:         make(chan struct{}, 1),
			frame:        plainLine,
			format:       OutputStreamJSON,
			stdinOpen:    true,
		},
		pending: map[uint64]chan acpMessage{},
		send:    make(chan struct{}, 1),
		turn:    turn,
		session: "session-wait",
	}
	releaseSettle := make(chan struct{})
	reachedWait := make(chan struct{})
	oldWait := waitAfterCursorInterruptCancel
	t.Cleanup(func() { waitAfterCursorInterruptCancel = oldWait })
	// Replacement waits on turn itself so a hook that only signals entry
	// and returns cancelled cannot pass: Interrupt must still be blocked
	// until settle is released.
	waitAfterCursorInterruptCancel = func(ctx context.Context, turn <-chan acpMessage, ended <-chan struct{}) (acpMessage, error) {
		close(reachedWait)
		select {
		case reply := <-turn:
			return reply, nil
		case <-ctx.Done():
			return acpMessage{}, ctx.Err()
		case <-ended:
			return acpMessage{}, ErrWorkerEnded
		}
	}
	go func() {
		defer stdinR.Close()
		sc := bufio.NewScanner(stdinR)
		for sc.Scan() {
			var req struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil || req.Method != "session/cancel" {
				continue
			}
			<-releaseSettle
			turn <- acpMessage{Result: json.RawMessage(`{"stopReason":"cancelled"}`)}
			return
		}
	}()

	done := make(chan error, 1)
	go func() {
		got, err := w.Interrupt(t.Context())
		if err != nil || !got {
			done <- fmt.Errorf("Interrupt: %t, %w", got, err)
			return
		}
		done <- nil
	}()

	select {
	case <-reachedWait:
	case err := <-done:
		if err == nil {
			err = errors.New("Interrupt returned on session/cancel alone")
		}
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("Interrupt never reached the post-cancel settle wait")
	}
	select {
	case err := <-done:
		if err == nil {
			err = errors.New("Interrupt returned before settle after reaching the wait")
		}
		t.Fatal(err)
	default:
	}
	close(releaseSettle)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCursorWorkerPermissionPolicyFailsClosed(t *testing.T) {
	w := acpWorker{route: "/opt/assistant --home /tmp/home worker"}
	for _, test := range []struct {
		kind, command string
		allowed       bool
	}{
		{"read", "", true}, {"edit", "", true}, {"search", "", true},
		{"execute", "git status", true},
		{"execute", "/opt/assistant --home /tmp/home worker event working", true},
		{"execute", "git status; rm -rf /tmp/subject", false},
		{"execute", "git status $(touch /tmp/unexpected)", false},
		{"execute", "echo git status", false},
		{"execute", "gitfoo status", false},
		{"execute", "/opt/assistant --home /tmp/home worker-unrelated", false},
		{"other", "", false},
	} {
		raw, _ := json.Marshal(map[string]any{"toolCall": map[string]any{"kind": test.kind, "rawInput": map[string]string{"command": test.command}}, "options": []any{map[string]string{"kind": "allow_once", "optionId": "yes"}}})
		if got := w.permission(raw); (got == "yes") != test.allowed {
			t.Errorf("%s %q: %q", test.kind, test.command, got)
		}
	}
	if got := w.permission(json.RawMessage(`{"toolCall":{"kind":"edit"},"options":[{"kind":"allow_always","optionId":"yes"}]}`)); got != "" {
		t.Fatal("permission was widened beyond once")
	}
}

// Under the subordinate grant the same handler allows reads, searches and
// the literal route alone: an edit and a git command are refused, and the
// route with a shell operator is refused as it is for a worker.
func TestCursorSubordinatePermissionPolicyDeniesEditsAndGit(t *testing.T) {
	w := acpWorker{route: "/opt/assistant --home /tmp/home coordinator", grant: GrantSubordinate}
	for _, test := range []struct {
		kind, command string
		allowed       bool
	}{
		{"read", "", true}, {"search", "", true}, {"edit", "", false},
		{"execute", "git status", false},
		{"execute", "git", false},
		{"execute", "/opt/assistant --home /tmp/home coordinator tasks --json", true},
		{"execute", "/opt/assistant --home /tmp/home coordinator; git status", false},
		{"execute", "echo hi", false},
		{"other", "", false},
	} {
		raw, _ := json.Marshal(map[string]any{"toolCall": map[string]any{"kind": test.kind, "rawInput": map[string]string{"command": test.command}}, "options": []any{map[string]string{"kind": "allow_once", "optionId": "yes"}}})
		if got := w.permission(raw); (got == "yes") != test.allowed {
			t.Errorf("%s %q: %q", test.kind, test.command, got)
		}
	}
}
