package agents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// The interrupt fixture is the request and follow-on notifications Codex
// CLI 0.166.0's generated app-server schema names for turn/interrupt
// (TurnInterruptParams with threadId and turnId; TurnStatus interrupted;
// then idle). It is not a live stdio capture: the ids are taken from the
// 0.163.0 happy-path recording so a reader can join the two, and the
// method names and status are what generate-json-schema emitted for
// 0.166.0. Playing frames through receive settles the turn on
// turn/completed interrupted alone: idle may follow, but Interrupt must
// not depend on it (the same no-idle shape failed turns already use).
func TestSchemaCodexTurnInterruptFramesSettleTheWorker(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex-app-server-turn-interrupt-0.166.0.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	settled := make(chan struct{})
	w := &codexWorker{
		procWorker: &procWorker{out: &tailWriter{limit: 1 << 20}, availability: &workerAvailability{}},
		settled:    settled,
		turn:       "01a14d4d-cbd4-7262-95c0-856c2dab1db0",
		thread:     "01a14d4d-cb48-7a02-be16-3b3737212b9e",
	}
	var sawInterrupt, sawInterrupted bool
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var message codexMessage
		if json.Unmarshal([]byte(line), &message) != nil {
			t.Fatalf("fixture line does not decode: %s", line)
		}
		if message.Method == "turn/interrupt" {
			sawInterrupt = true
			var params struct {
				Thread string `json:"threadId"`
				Turn   string `json:"turnId"`
			}
			if json.Unmarshal(message.Params, &params) != nil || params.Thread == "" || params.Turn == "" {
				t.Fatalf("turn/interrupt params are %s, want threadId and turnId", message.Params)
			}
			continue
		}
		if message.Method == "thread/status/changed" {
			break // prove settle without the trailing idle
		}
		if message.Method == "turn/completed" {
			var completed struct {
				Turn struct {
					Status string `json:"status"`
				} `json:"turn"`
			}
			if json.Unmarshal(message.Params, &completed) != nil || completed.Turn.Status != "interrupted" {
				t.Fatalf("turn/completed is %s, want status interrupted", message.Params)
			}
			sawInterrupted = true
		}
		w.receive([]byte(line))
	}
	if !sawInterrupt || !sawInterrupted {
		t.Fatalf("fixture missing interrupt request (%t) or interrupted status (%t)", sawInterrupt, sawInterrupted)
	}
	select {
	case <-settled:
	default:
		t.Fatal("turn/completed interrupted did not finish the turn without idle")
	}
	if w.turn != "" {
		t.Fatalf("after interrupted the worker still names turn %q", w.turn)
	}
}

// Interrupt returns only after settled closes. Answering turn/interrupt
// first and holding turn/completed interrupted until the test releases it
// would leave a mutation that returns true right after call green against
// every other interrupt test, because those settle in the same receive
// burst as the cancel. Not parallel: it swaps waitAfterCodexInterruptCancel.
func TestCodexInterruptWaitsForSettledAfterCancelReturns(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	settled := make(chan struct{})
	w := &codexWorker{
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
		pending: map[uint64]chan codexMessage{},
		send:    make(chan struct{}, 1),
		settled: settled,
		turn:    "turn-wait",
		thread:  "thread-wait",
	}
	releaseSettle := make(chan struct{})
	reachedWait := make(chan struct{})
	oldWait := waitAfterCodexInterruptCancel
	t.Cleanup(func() { waitAfterCodexInterruptCancel = oldWait })
	// Replacement waits on settled itself so a hook that only signals entry
	// and returns true cannot pass: Interrupt must still be blocked until
	// settle is released.
	waitAfterCodexInterruptCancel = func(ctx context.Context, settled <-chan struct{}, ended <-chan struct{}, endedErr func() error) (bool, error) {
		close(reachedWait)
		select {
		case <-settled:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ended:
			return false, endedErr()
		}
	}
	go func() {
		defer stdinR.Close()
		sc := bufio.NewScanner(stdinR)
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil || req.Method != "turn/interrupt" {
				continue
			}
			w.receive([]byte(`{"id":` + string(req.ID) + `,"result":{}}`))
			<-releaseSettle
			w.receive([]byte(`{"method":"turn/completed","params":{"turn":{"id":"turn-wait","status":"interrupted"}}}`))
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
			err = errors.New("Interrupt returned on turn/interrupt alone")
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

// The lines in testdata are the Codex CLI 0.163.0's app-server frames,
// recorded from a live stdio session that answered "ready". The home path
// in those frames is replaced; the method names, the thread id's place,
// the in-progress turn result, the agentMessage item, and the idle status
// are the bytes the CLI wrote.
func TestRecordedCodexAppServerFramesDriveTheWorker(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex-app-server-0.163.0.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	settled := make(chan struct{})
	w := &codexWorker{
		procWorker: &procWorker{out: &tailWriter{limit: 1 << 20}, availability: &workerAvailability{}},
		settled:    settled,
	}
	var thread string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		w.receive([]byte(line))
		var message codexMessage
		if json.Unmarshal([]byte(line), &message) != nil || message.Method != "" {
			continue
		}
		var created struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if json.Unmarshal(message.Result, &created) == nil && created.Thread.ID != "" {
			thread = created.Thread.ID
		}
	}
	if thread == "" {
		t.Fatal("the recorded thread/start result carried no thread id")
	}
	transcript := ReadTranscript(w.Capture().Text, OutputStreamJSON, false)
	var saw bool
	for _, event := range transcript.Events {
		if event.Kind == TranscriptUnrecognized {
			t.Fatalf("the recorded reply was unrecognized: %+v", event)
		}
		if event.Kind == TranscriptText && event.Text == "ready" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("the recorded agent message captured %q, want assistant text ready", w.Capture().Text)
	}
	select {
	case <-settled:
	default:
		t.Fatal("the recorded idle status did not finish the turn")
	}
}

// recordedReplies is the standard input of a worker the recorded frames
// drive: every reply the worker writes, as written.
type recordedReplies struct {
	mu    sync.Mutex
	lines bytes.Buffer
}

func (r *recordedReplies) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lines.Write(p)
}

func (r *recordedReplies) Close() error { return nil }

// decisions reads back each reply's decision, by the request id it answers.
func (r *recordedReplies) decisions(t *testing.T) map[int]string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int]string{}
	for _, line := range strings.Split(strings.TrimSpace(r.lines.String()), "\n") {
		var reply struct {
			ID     int `json:"id"`
			Result struct {
				Decision string `json:"decision"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("the worker wrote %q, not a reply: %v", line, err)
		}
		out[reply.ID] = reply.Result.Decision
	}
	return out
}

// The lines in testdata are the Codex CLI 0.166.0's app-server frames of
// one turn that asked for three shell commands, recorded from a live stdio
// session this adapter drove. Each command came to the client as
// item/commandExecution/requestApproval of kind command whose command
// string is the shell line the CLI runs, /bin/zsh -lc and the command in
// single quotes; the adapter declined the echo and accepted the git commit
// and the git push, and the CLI completed the declined item as declined
// and ran the accepted two, the push reaching a repository outside the
// workspace. The paths in those frames are replaced, the initialize
// result's user agent is cut to the CLI and platform, its home path and
// the thread's instruction sources are blanked, and notifications the
// adapter does not read (account, rate-limit, token-usage, MCP-startup and
// moderation frames) are left out; every other line is the bytes the CLI
// wrote. Replayed into the worker, the same three requests are answered as
// they were on the wire, with a worker grant and no route, and the turn
// settles on the final idle.
func TestRecordedCodexApprovalsAreAnsweredOnTheWorkerGrant(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex-app-server-approvals-0.166.0.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	replies := &recordedReplies{}
	settled := make(chan struct{})
	w := &codexWorker{
		procWorker: &procWorker{
			out: &tailWriter{limit: 1 << 20}, availability: &workerAvailability{},
			stdin: replies, stdinOpen: true, frame: plainLine,
			done: make(chan struct{}), slot: make(chan struct{}, 1),
		},
		settled: settled,
	}
	commands := map[int]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var request struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
			Params struct {
				Kind    string `json:"kind"`
				Command string `json:"command"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(line), &request) == nil && request.ID != nil && request.Method == "item/commandExecution/requestApproval" {
			if request.Params.Kind != "command" {
				t.Fatalf("the recorded approval %d is of kind %q", *request.ID, request.Params.Kind)
			}
			commands[*request.ID] = request.Params.Command
		}
		w.receive([]byte(line))
	}
	want := map[int]string{0: "decline", 1: "accept", 2: "accept"}
	if got := replies.decisions(t); !maps.Equal(got, want) {
		t.Fatalf("the recorded approvals %v were answered %v, want %v", commands, got, want)
	}
	if !strings.HasPrefix(commands[2], "/bin/zsh -lc 'git push ") {
		t.Fatalf("the recorded push was asked as %q, not inside the shell line this test is about", commands[2])
	}
	select {
	case <-settled:
	default:
		t.Fatal("the recorded idle did not finish the turn")
	}
	if !strings.Contains(w.Capture().Text, "PROBE_DONE") {
		t.Fatalf("the recorded final answer was not captured: %s", w.Capture().Text)
	}
}

// codexApprovedCommand reads a shell string only in the form the recording
// shows: the command inside its single quotes is what the grant is held
// to, a shell string in any other form is declined whatever it would run,
// and a string whose first word is no shell is read as the command itself.
func TestACodexApprovalIsReadInsideItsShellLine(t *testing.T) {
	w := &codexWorker{route: "/opt/assistant worker"}
	for _, test := range []struct {
		command string
		allowed bool
	}{
		{"/bin/zsh -lc 'git push origin HEAD:refs/heads/probe'", true},
		{"/bin/bash -c 'git status'", true},
		{"/bin/sh -lc 'git'", true},
		{"/bin/sh -lc git", false},
		{"zsh", false},
		{"/bin/zsh -lc 'git status' ", false},
		{"git push origin task/one", true},
		{"/bin/zsh -lc '/opt/assistant worker event done'", true},
		{"/bin/zsh -lc 'echo declined-probe'", false},
		{"/bin/zsh -lc 'git status; rm -rf .'", false},
		{"/bin/zsh -lc 'git commit -m '\"'\"'it'\"'\"''", false},
		{"/bin/zsh -lc git' push'", false},
		{"/bin/zsh -l -c 'git status'", false},
		{"/usr/bin/fish -c 'git status'", false},
		{"/bin/zsh -lc 'git status' extra", false},
		{"/bin/zsh -lc ''", false},
		{"/bin/zsh -lc", false},
		{"", false},
	} {
		if got := w.commandAllowed(test.command); got != test.allowed {
			t.Errorf("commandAllowed(%q) = %t, want %t", test.command, got, test.allowed)
		}
	}
}

// A command approval of any kind but command is declined whatever command
// it names: writeStdin is input to a process already running, and null is
// no kind the schema has. An approval with no kind, a server older than the
// field, is a command, as the schema's default says.
func TestACodexApprovalToWriteStdinIsDeclined(t *testing.T) {
	for kind, want := range map[string]string{`"writeStdin"`: "decline", `"command"`: "accept", "null": "decline", "": "accept"} {
		replies := &recordedReplies{}
		w := &codexWorker{procWorker: &procWorker{
			stdin: replies, stdinOpen: true, frame: plainLine,
			done: make(chan struct{}), slot: make(chan struct{}, 1),
		}}
		field := ""
		if kind != "" {
			field = `"kind":` + kind + `,`
		}
		w.receive([]byte(`{"method":"item/commandExecution/requestApproval","id":7,"params":{` + field + `"command":"/bin/zsh -lc 'git status'"}}`))
		if got := replies.decisions(t)[7]; got != want {
			t.Errorf("an approval of kind %s was answered %q, want %q", kind, got, want)
		}
	}
}

// An idle that arrives as the next Send checks the outstanding turn must
// finish that turn and no later one. The two run together so the check and
// the close meet in either order.
func TestAnIdleCannotFinishALaterTurn(t *testing.T) {
	w := &codexWorker{procWorker: &procWorker{done: make(chan struct{})}}
	for range 100 {
		w.mu.Lock()
		w.settled = make(chan struct{})
		w.mu.Unlock()
		go w.markIdle()
		if err := w.finishTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		w.mu.Lock()
		w.settled = make(chan struct{})
		fresh := w.settled
		w.mu.Unlock()
		select {
		case <-fresh:
			t.Fatal("an earlier idle closed the next turn's channel")
		default:
		}
		w.markIdle()
	}
}

// codexTurnFailure reads the structured field before any text. The
// recorded failed turn/completed of both credential recordings classifies
// as a credential; the same frame with its status changed to 500 does not,
// even though its message still names a status, and with the field removed
// the message alone is read through the marker table, which takes the
// recorded 401 line. The schema's "unauthorized" string is a credential
// too, and an unrecognized string with an unrecognized message is nil.
func TestCodexTurnFailureReadsTheStructuredFieldFirst(t *testing.T) {
	for _, name := range []string{"codex-app-server-unauthenticated-0.163.0.jsonl", "codex-app-server-rejected-0.163.0.jsonl"} {
		raw, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		var completed struct {
			Params struct {
				Turn struct {
					Status string `json:"status"`
					Error  struct {
						Message string          `json:"message"`
						Info    json.RawMessage `json:"codexErrorInfo"`
					} `json:"error"`
				} `json:"turn"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &completed); err != nil || completed.Params.Turn.Status != "failed" {
			t.Fatalf("%s does not end in a failed turn/completed: %v", name, err)
		}
		e := completed.Params.Turn.Error
		got := codexTurnFailure(e.Info, e.Message)
		if got == nil || got.Failure != FailureUnavailable || !errors.Is(got, ErrCredentialRefused) {
			t.Fatalf("%s: the recorded failure classified as %+v", name, got)
		}
		server := json.RawMessage(strings.ReplaceAll(string(e.Info), "401", "500"))
		if got := codexTurnFailure(server, "unexpected status 500 Internal Server Error"); got != nil {
			t.Fatalf("%s: a 500 classified as %+v", name, got)
		}
		if got := codexTurnFailure(nil, e.Message); got == nil || !errors.Is(got, ErrCredentialRefused) {
			t.Fatalf("%s: the message alone classified as %+v", name, got)
		}
	}
	if got := codexTurnFailure(json.RawMessage(`"unauthorized"`), "denied"); got == nil || !errors.Is(got, ErrCredentialRefused) {
		t.Fatalf("the unauthorized string classified as %+v", got)
	}
	if got := codexTurnFailure(json.RawMessage(`"contextWindowExceeded"`), "context window exceeded"); got != nil {
		t.Fatalf("an unrecognized failure classified as %+v", got)
	}
}
