package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Start launches Codex's app-server as a persistent pipe worker. The
// protocol initialization and thread creation must succeed before the first
// turn is sent. Every later Send is a turn/start on that same thread,
// serialized after the preceding turn. The thread is created here and is
// not a fixer session: review and fix still run through codex exec, and
// nothing in this method resumes one. The caller owns the process through
// Close; startup's context does not own its lifetime.
//
// Command approvals allow literal git commands and commands beginning with
// the worker route. The command is the request's command string; a request
// that does not carry one there is declined. File-change approvals are
// accepted without this package reading the paths, so the workspace-write
// sandbox is the CLI's limit on where a write lands. Any other approval is
// declined, and a server request this client does not understand is
// refused. A command the sandbox runs without asking is the CLI's own.
// Terminal workers are not declared.
//
// A subordinate coordinator is refused with ErrGrantUnenforced before
// anything starts: the commands the workspace-write sandbox runs without
// asking never reach this client, so it cannot deny git, or every command
// but the route, as GrantSubordinate states.
func (r *codexRunner) WorkerLaunchRefusal(_ Terminal, spec WorkerSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	return codexGrantRefusal(spec)
}

// codexGrantRefusal refuses a spec whose grant this adapter cannot hold
// the agent to, on WorkerLaunchRefusal's terms.
func codexGrantRefusal(spec WorkerSpec) error {
	if spec.Grant() == GrantWorker {
		return nil
	}
	return &invocationFieldError{
		field:  "grant",
		reason: fmt.Sprintf("the Codex app-server runs sandboxed commands without asking, so the %s grant cannot be enforced", spec.Grant()),
		also:   ErrGrantUnenforced,
	}
}

func (r *codexRunner) Start(ctx context.Context, spec WorkerSpec) (Worker, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if err := codexGrantRefusal(spec); err != nil {
		return nil, err
	}
	if !utf8.ValidString(spec.Instructions) {
		return nil, fmt.Errorf("%w: instructions are not UTF-8", ErrInvalidInvocation)
	}
	if err := AcceptsEffort(CodexName, spec.Effort); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.ContainsAny(spec.Route, ";&|><`$()\r\n") {
		return nil, fmt.Errorf("%w: Codex worker route contains shell operators", ErrInvalidInvocation)
	}
	args := slices.Clone(r.extra)
	args = append(args, "app-server")
	cmd := exec.Command(r.resolved, args...)
	cmd.Dir = spec.Dir
	cmd.Env = withoutEnvNames(WorkerEnvironment(r.settings.base, spec.Env), CodexKeyVariable)
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	w := &codexWorker{
		route: spec.Route, model: spec.Model, effort: spec.Effort,
		pending: map[uint64]chan codexMessage{}, send: make(chan struct{}, 1),
	}
	w.procWorker = &procWorker{
		cmd: cmd, availability: &workerAvailability{agent: CodexName}, stdin: input,
		out: &tailWriter{limit: r.settings.maxOut}, grace: r.settings.grace, hurry: hurryOf(ctx),
		done: make(chan struct{}), slot: make(chan struct{}, 1),
		frame: plainLine, format: OutputStreamJSON, stdinOpen: true,
	}
	cmd.Stdout = &codexLines{worker: w, limit: r.settings.maxOut}
	cmd.Stderr = w.out
	cmd.WaitDelay = r.settings.grace
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		defer close(w.done)
		w.exited(cmd.Wait())
		terminateTree(cmd.Process, r.settings.grace, false, nil, w.hurry)
	}()
	go w.closeOnHurry()
	start, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	success := false
	defer func() {
		if !success {
			w.Close()
		}
	}()
	if err := w.open(start, spec.Dir); err != nil {
		return nil, err
	}
	if err := w.Send(start, spec.Instructions); err != nil {
		return nil, err
	}
	success = true
	return w, nil
}

type codexMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type codexWorker struct {
	*procWorker
	mu      sync.Mutex
	next    uint64
	pending map[uint64]chan codexMessage
	send    chan struct{}
	settled chan struct{}
	// ended is the typed failure of an app-server that exited while a turn
	// it was sent was still running, nil otherwise. It is written once, by
	// exited, before done is closed, so whoever sees done closed reads it.
	ended         *WorkerError
	thread, route string
	model, effort string
}

func (w *codexWorker) open(ctx context.Context, dir string) error {
	if _, err := w.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "assistant", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		return err
	}
	if err := w.write(ctx, `{"method":"initialized"}`); err != nil {
		return err
	}
	params := map[string]any{
		"cwd": dir, "sandbox": "workspace-write", "approvalPolicy": "on-request", "ephemeral": true,
	}
	if w.model != "" {
		params["model"] = w.model
	}
	started, err := w.call(ctx, "thread/start", params)
	if err != nil {
		return err
	}
	var created struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(started.Result, &created) != nil || created.Thread.ID == "" {
		return errors.New("agents: Codex app-server returned no thread")
	}
	w.thread = created.Thread.ID
	return nil
}

func (w *codexWorker) request(ctx context.Context, method string, params any) (<-chan codexMessage, error) {
	w.mu.Lock()
	w.next++
	id := w.next
	reply := make(chan codexMessage, 1)
	w.pending[id] = reply
	w.mu.Unlock()
	encoded, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err == nil {
		err = w.write(ctx, string(encoded))
	}
	if err != nil {
		w.mu.Lock()
		delete(w.pending, id)
		w.mu.Unlock()
		return nil, err
	}
	return reply, nil
}

func (w *codexWorker) call(ctx context.Context, method string, params any) (codexMessage, error) {
	reply, err := w.request(ctx, method, params)
	if err != nil {
		return codexMessage{}, err
	}
	select {
	case response := <-reply:
		if response.Error != nil {
			return response, w.failure(response.Error.Message)
		}
		return response, nil
	case <-ctx.Done():
		return codexMessage{}, ctx.Err()
	case <-w.done:
		return codexMessage{}, w.endedError()
	}
}

// failure is the typed account of a JSON-RPC error the app-server answered
// a request with. One availabilityMarkers recognizes is also recorded, the
// way completeTurn records a failed turn, because a turn/start refused this
// way is followed by no turn/completed: without the record, Send would
// return the typed error while CurrentWorkerFailure, which a worker handoff
// polls, kept reading FailureNone. An error the table does not recognize is
// the request's failure and is not recorded, on completeTurn's terms.
func (w *codexWorker) failure(message string) error {
	category, cause := availabilityCategory(FailureAgent, message)
	failure := &WorkerError{Agent: CodexName, Failure: category, Message: message, Err: cause}
	if category != FailureAgent {
		w.availability.fail(failure)
	}
	return failure
}

// Send writes one turn/start and returns when that request is accepted.
// The turn's idle arrives later. The next Send waits for it before writing,
// so two turns do not overlap, and a caller that stops waiting is not told
// the text was refused after the server has already taken it.
func (w *codexWorker) Send(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !utf8.ValidString(text) || len(text) > MaxPromptBytes || strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: invalid Codex worker message", ErrInvalidInvocation)
	}
	select {
	case w.send <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return w.endedError()
	}
	defer func() { <-w.send }()
	if err := w.finishTurn(ctx); err != nil {
		return err
	}
	w.mu.Lock()
	w.settled = make(chan struct{})
	w.mu.Unlock()
	w.availability.set(FailureNone)
	params := map[string]any{
		"threadId": w.thread,
		"input":    []any{map[string]string{"type": "text", "text": text}},
	}
	if w.model != "" {
		params["model"] = w.model
	}
	if w.effort != "" {
		params["effort"] = w.effort
	}
	if _, err := w.call(ctx, "turn/start", params); err != nil {
		w.markIdle()
		return err
	}
	return nil
}

// finishTurn waits for the turn a previous Send accepted. The channel is
// taken under the same lock that markIdle closes it with, so an idle that
// arrives as this starts cannot be observed as both finished and still
// pending, and its close cannot complete a later turn.
func (w *codexWorker) finishTurn(ctx context.Context) error {
	w.mu.Lock()
	settled := w.settled
	w.mu.Unlock()
	if settled == nil {
		return nil
	}
	select {
	case <-settled:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return w.endedError()
	}
}

// markIdle closes the accepted turn's channel and forgets it, once.
func (w *codexWorker) markIdle() {
	w.mu.Lock()
	settled := w.settled
	w.settled = nil
	w.mu.Unlock()
	if settled != nil {
		close(settled)
	}
}

func (w *codexWorker) emitText(text string) {
	if text == "" {
		return
	}
	encoded, err := json.Marshal(map[string]any{
		"type":    "assistant",
		"message": map[string]any{"content": []any{map[string]string{"type": "text", "text": text}}},
	})
	if err != nil {
		return
	}
	w.availability.observe(encoded)
	_, _ = w.out.Write(append(encoded, '\n'))
}

func (w *codexWorker) receive(line []byte) {
	var message codexMessage
	if json.Unmarshal(line, &message) != nil {
		return
	}
	if message.Method != "" && len(message.ID) > 0 && len(message.Result) == 0 && message.Error == nil {
		w.answerServer(message)
		return
	}
	if message.Method == "item/completed" {
		var item struct {
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(message.Params, &item) == nil && item.Item.Type == "agentMessage" {
			w.emitText(item.Item.Text)
		}
		return
	}
	if message.Method == "thread/status/changed" {
		var status struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		}
		if json.Unmarshal(message.Params, &status) != nil || status.Status.Type != "idle" {
			return
		}
		w.markIdle()
		return
	}
	if message.Method == "turn/completed" {
		w.completeTurn(message.Params)
		return
	}
	if message.Method != "" || len(message.ID) == 0 {
		return
	}
	var id uint64
	if json.Unmarshal(message.ID, &id) != nil {
		return
	}
	w.mu.Lock()
	reply := w.pending[id]
	delete(w.pending, id)
	w.mu.Unlock()
	if reply != nil {
		reply <- message
	}
}

// completeTurn reads a turn/completed notification. Only a failed one acts:
// Codex CLI 0.163.0 was observed to end a failed turn this way after moving
// the thread to systemError, with no idle after it, so the turn is settled
// here or the next Send would wait on an idle that never comes. A completed
// turn is still settled by its idle, as before, so an idle that follows a
// turn/completed cannot settle the turn after it. The failure is classified
// by codexTurnFailure, and only a recognized one is recorded, because a
// recorded failure is what a worker handoff acts on and an ordinary failed
// turn is not a reason to change agents.
func (w *codexWorker) completeTurn(params json.RawMessage) {
	var completed struct {
		Turn struct {
			Status string `json:"status"`
			Error  *struct {
				Message string          `json:"message"`
				Info    json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &completed) != nil || completed.Turn.Status != "failed" {
		return
	}
	var message string
	var info json.RawMessage
	if e := completed.Turn.Error; e != nil {
		message, info = e.Message, e.Info
	}
	if failure := codexTurnFailure(info, message); failure != nil {
		w.availability.fail(failure)
	}
	w.markIdle()
}

// codexTurnFailure classifies a failed turn's error, structured field
// first. codexErrorInfo is either a string or an object naming one variant
// whose value may carry httpStatusCode. A 401 on any variant is a missing or
// rejected credential: Codex CLI 0.163.0 reported httpConnectionFailed with
// httpStatusCode 401 both with no credential and with a key the provider
// refused, recorded in testdata/codex-app-server-unauthenticated-0.163.0.jsonl
// and testdata/codex-app-server-rejected-0.163.0.jsonl. The string
// "unauthorized" is read the same way because the schema that CLI version
// generates (`codex app-server generate-json-schema`) names it; it was not
// observed on the wire. Anything else falls back on availabilityMarkers'
// text match over the message, and an error neither recognizes is nil.
func codexTurnFailure(info json.RawMessage, message string) *WorkerError {
	credential := false
	var named string
	if json.Unmarshal(info, &named) == nil {
		credential = named == "unauthorized"
	} else {
		var variants map[string]struct {
			Status *int `json:"httpStatusCode"`
		}
		if json.Unmarshal(info, &variants) == nil {
			for _, v := range variants {
				if v.Status != nil && *v.Status == 401 {
					credential = true
				}
			}
		}
	}
	if credential {
		return &WorkerError{Agent: CodexName, Failure: FailureUnavailable, Message: message, Err: ErrCredentialRefused}
	}
	if category, cause := availabilityCategory(FailureAgent, message); category != FailureAgent {
		return &WorkerError{Agent: CodexName, Failure: category, Message: message, Err: cause}
	}
	return nil
}

// exited records how the app-server's process ended when it ended on its
// own while a turn it was sent had not settled: neither an idle nor a failed
// turn/completed arrived first. A process Close ended is not that, and is
// told apart by Close having closed standard input before it signals.
func (w *codexWorker) exited(err error) {
	w.stdinMu.Lock()
	closing := !w.stdinOpen
	w.stdinMu.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	if closing || w.settled == nil {
		return
	}
	status := "exit status 0"
	if err != nil {
		status = err.Error()
	}
	w.ended = &WorkerError{
		Agent: CodexName, Failure: FailureProcess, Err: ErrWorkerEnded,
		Message: "the app-server ended (" + status + ") before the turn it was sent finished",
	}
}

// endedError is what a call or a Send refuses with once the process is
// gone: the mid-turn failure exited recorded, which also matches
// ErrWorkerEnded, or ErrWorkerEnded alone.
func (w *codexWorker) endedError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended != nil {
		return w.ended
	}
	return ErrWorkerEnded
}

func (w *codexWorker) answerServer(message codexMessage) {
	decision := "decline"
	switch message.Method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		var command struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(message.Params, &command) == nil && w.commandAllowed(command.Command) {
			decision = "accept"
		}
	case "item/fileChange/requestApproval", "applyPatchApproval":
		decision = "accept"
	default:
		if !strings.Contains(message.Method, "Approval") && !strings.Contains(message.Method, "requestApproval") {
			w.reply(message.ID, map[string]any{"error": map[string]any{"code": -32601, "message": "client does not support this request"}})
			return
		}
	}
	w.reply(message.ID, map[string]any{"result": map[string]string{"decision": decision}})
}

func (w *codexWorker) reply(id json.RawMessage, body map[string]any) {
	body["id"] = id
	encoded, err := json.Marshal(body)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = w.write(ctx, string(encoded))
	cancel()
	if err != nil {
		go w.Close()
	}
}

func (w *codexWorker) commandAllowed(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, ";&|><`$()\r\n") {
		return false
	}
	if command == "git" || strings.HasPrefix(command, "git ") {
		return true
	}
	return w.route != "" && (command == w.route || strings.HasPrefix(command, w.route+" "))
}

type codexLines struct {
	worker *codexWorker
	mu     sync.Mutex
	buffer []byte
	limit  int64
}

// Write processes complete bounded app-server frames and refuses an oversized frame.
func (l *codexLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	size := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			end = len(p)
		}
		if int64(len(l.buffer)+end) > l.limit {
			go l.worker.Close()
			return 0, errors.New("agents: Codex app-server frame exceeds output bound")
		}
		l.buffer = append(l.buffer, p[:end]...)
		if end == len(p) {
			break
		}
		l.worker.receive(l.buffer)
		l.buffer = nil
		p = p[end+1:]
	}
	return size, nil
}
