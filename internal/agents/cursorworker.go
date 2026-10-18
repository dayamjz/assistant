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

// Start launches Cursor's ACP server as a persistent pipe worker. The login
// authentication, protocol initialization and session creation must succeed
// before its first prompt is sent. Every later Send is a session/prompt on
// that same session, serialized after the preceding turn. The caller owns
// the process through Close; startup's context does not own its lifetime.
//
// Permission requests allow file reads/edits and literal git or worker-route
// commands, once. Unknown requests and shell operators are refused. This is
// a policy for requests the CLI asks; settings that preapprove tools are not
// narrowed here. Cursor terminal workers are not declared.
func (r *cursorRunner) Start(ctx context.Context, spec WorkerSpec) (Worker, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if !utf8.ValidString(spec.Instructions) {
		return nil, fmt.Errorf("%w: instructions are not UTF-8", ErrInvalidInvocation)
	}
	if err := AcceptsEffort(CursorName, spec.Effort); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.ContainsAny(spec.Route, ";&|><`$()\r\n") {
		return nil, fmt.Errorf("%w: Cursor worker route contains shell operators", ErrInvalidInvocation)
	}
	args := slices.Clone(r.extra)
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	args = append(args, "acp")
	cmd := exec.Command(r.resolved, args...)
	cmd.Dir = spec.Dir
	cmd.Env = withoutEnvNames(WorkerEnvironment(r.settings.base, spec.Env), CursorKeyVariable)
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	w := &acpWorker{route: spec.Route, pending: map[uint64]chan acpMessage{}, send: make(chan struct{}, 1)}
	w.procWorker = &procWorker{cmd: cmd, availability: &workerAvailability{}, stdin: input, out: &tailWriter{limit: r.settings.maxOut}, grace: r.settings.grace, done: make(chan struct{}), slot: make(chan struct{}, 1), frame: plainLine, format: OutputStreamJSON, stdinOpen: true}
	cmd.Stdout = &acpLines{worker: w, limit: r.settings.maxOut}
	cmd.Stderr = w.out
	cmd.WaitDelay = r.settings.grace
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { defer close(w.done); _ = cmd.Wait(); terminateTree(cmd.Process, r.settings.grace, false, nil) }()
	start, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	success := false
	defer func() {
		if !success {
			w.Close()
		}
	}()
	init, err := w.call(start, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}, "clientInfo": map[string]string{"name": "assistant", "version": "1"}})
	if err != nil {
		return nil, err
	}
	var initialized struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if json.Unmarshal(init.Result, &initialized) != nil || initialized.ProtocolVersion != 1 {
		return nil, errors.New("agents: unsupported Cursor ACP protocol")
	}
	if _, err = w.call(start, "authenticate", map[string]string{"methodId": "cursor_login"}); err != nil {
		return nil, err
	}
	session, err := w.call(start, "session/new", map[string]any{"cwd": spec.Dir, "mcpServers": []any{}})
	if err != nil {
		return nil, err
	}
	var created struct {
		ID string `json:"sessionId"`
	}
	if json.Unmarshal(session.Result, &created) != nil || created.ID == "" {
		return nil, errors.New("agents: Cursor ACP returned no session")
	}
	w.session = created.ID
	if err = w.Send(start, spec.Instructions); err != nil {
		return nil, err
	}
	success = true
	return w, nil
}

type acpMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}
type acpWorker struct {
	*procWorker
	mu             sync.Mutex
	next           uint64
	pending        map[uint64]chan acpMessage
	send           chan struct{}
	turn           <-chan acpMessage
	session, route string
}

func (w *acpWorker) request(ctx context.Context, method string, params any) (<-chan acpMessage, error) {
	w.mu.Lock()
	w.next++
	id := w.next
	reply := make(chan acpMessage, 1)
	w.pending[id] = reply
	w.mu.Unlock()
	encoded, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
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
func (w *acpWorker) call(ctx context.Context, method string, params any) (acpMessage, error) {
	reply, err := w.request(ctx, method, params)
	if err != nil {
		return acpMessage{}, err
	}
	select {
	case response := <-reply:
		if response.Error != nil {
			return response, w.failure(response.Error.Message)
		}
		return response, nil
	case <-ctx.Done():
		return acpMessage{}, ctx.Err()
	case <-w.done:
		return acpMessage{}, ErrWorkerEnded
	}
}
func (w *acpWorker) failure(message string) error {
	category := availabilityCategory(FailureAgent, message)
	return &WorkerError{Agent: CursorName, Failure: category, Message: message}
}

// Send queues one prompt, preserving its turn even if the caller stops waiting for the write.
func (w *acpWorker) Send(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !utf8.ValidString(text) || len(text) > MaxPromptBytes {
		return fmt.Errorf("%w: invalid Cursor worker message", ErrInvalidInvocation)
	}
	select {
	case w.send <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return ErrWorkerEnded
	}
	defer func() { <-w.send }()
	if w.turn != nil {
		select {
		case reply := <-w.turn:
			w.turn = nil
			if reply.Error != nil {
				return w.failure(reply.Error.Message)
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-w.done:
			return ErrWorkerEnded
		}
	}
	// Once queued, the write and its response remain ordered even when the
	// sender stops waiting. The next Send drains this turn before starting one.
	turn := make(chan acpMessage, 1)
	written := make(chan error, 1)
	w.turn = turn
	w.availability.set(FailureNone)
	go func() {
		reply, err := w.request(context.Background(), "session/prompt", map[string]any{"sessionId": w.session, "prompt": []any{map[string]string{"type": "text", "text": text}}})
		written <- err
		if err != nil {
			w.Close()
			return
		}
		select {
		case response := <-reply:
			turn <- response
		case <-w.done:
		}
	}()
	select {
	case err := <-written:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return ErrWorkerEnded
	}
}

func (w *acpWorker) emit(value any) {
	encoded, _ := json.Marshal(value)
	w.availability.observe(encoded)
	_, _ = w.out.Write(append(encoded, '\n'))
}
func (w *acpWorker) receive(line []byte) {
	var message acpMessage
	if json.Unmarshal(line, &message) != nil {
		w.emit(map[string]any{"type": "acp_unrecognized", "text": string(line)})
		return
	}
	if message.Method == "session/update" {
		var p struct {
			Update struct {
				Kind    string `json:"sessionUpdate"`
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if json.Unmarshal(message.Params, &p) == nil && p.Update.Kind == "agent_message_chunk" && p.Update.Content.Type == "text" {
			w.emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]string{"type": "text", "text": p.Update.Content.Text}}}})
		} else {
			w.emit(map[string]any{"type": "acp_update", "update": message.Params})
		}
		return
	}
	if message.Method != "" && len(message.ID) > 0 {
		result := map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
		if message.Method == "session/request_permission" {
			if id := w.permission(message.Params); id != "" {
				result = map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": id}}
			}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": message.ID}
		if message.Method == "session/request_permission" {
			response["result"] = result
		} else {
			response["error"] = map[string]any{"code": -32601, "message": "client does not support this request"}
		}
		encoded, _ := json.Marshal(response)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := w.write(ctx, string(encoded))
		cancel()
		if err != nil {
			go w.Close()
		}
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
	if message.Error != nil {
		w.emit(map[string]any{"type": "result", "is_error": true, "result": message.Error.Message})
	} else {
		var result struct {
			Stop string `json:"stopReason"`
		}
		if json.Unmarshal(message.Result, &result) == nil && result.Stop != "" {
			w.emit(map[string]any{"type": "result", "is_error": false, "result": ""})
		}
	}
	if reply != nil {
		reply <- message
	}
}
func (w *acpWorker) permission(params json.RawMessage) string {
	var p struct {
		Tool struct {
			Kind  string `json:"kind"`
			Input struct {
				Command string `json:"command"`
			} `json:"rawInput"`
		} `json:"toolCall"`
		Options []struct {
			ID   string `json:"optionId"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	allowed := p.Tool.Kind == "read" || p.Tool.Kind == "edit" || p.Tool.Kind == "search"
	if p.Tool.Kind == "execute" {
		command := strings.TrimSpace(p.Tool.Input.Command)
		literal := command != "" && !strings.ContainsAny(command, ";&|><`$()\r\n")
		allowed = literal && (command == "git" || strings.HasPrefix(command, "git ") || (w.route != "" && (command == w.route || strings.HasPrefix(command, w.route+" "))))
	}
	if allowed {
		for _, option := range p.Options {
			if option.Kind == "allow_once" {
				return option.ID
			}
		}
	}
	return ""
}

type acpLines struct {
	worker *acpWorker
	mu     sync.Mutex
	buffer []byte
	limit  int64
}

// Write processes complete bounded ACP frames and refuses an oversized frame.
func (l *acpLines) Write(p []byte) (int, error) {
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
			return 0, errors.New("agents: Cursor ACP frame exceeds output bound")
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
