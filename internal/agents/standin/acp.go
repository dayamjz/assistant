package standin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// NewCursor scripts the production Cursor adapter, including its ACP worker
// protocol. It records decoded prompt messages, never typed adapter results.
func NewCursor(tb testing.TB, script Script, opts ...agents.CursorOption) *Agent {
	tb.Helper()
	self, err := os.Executable()
	if err != nil {
		tb.Fatal(err)
	}
	control := prepare(tb, script)
	verify(tb, self, handshakeFlag)
	runner, err := agents.CursorFactory(slices.Concat(opts, []agents.CursorOption{agents.WithCursorBinary(self)})...).New(tb.Context(), []string{controlFlag + control})
	if err != nil {
		tb.Fatal(err)
	}
	return &Agent{tb: tb, control: control, runner: runner}
}

// serveACP models the documented newline JSON-RPC protocol: initialize,
// authenticate, create a session, then prompts. It requires the handshake in
// that order; a client that merely writes messages cannot pass conformance.
func serveACP(control string, args []string) int {
	bytes, err := os.ReadFile(scriptPath(control))
	if err != nil {
		return exitNoScript
	}
	var script Script
	if json.Unmarshal(bytes, &script) != nil {
		return exitNoScript
	}
	worker := script.Worker
	if worker == nil {
		worker = &WorkerScript{}
	}
	dir, _ := os.Getwd()
	call := Call{Args: args, Dir: dir, Step: -1, Worker: true, Stream: true}
	seq, err := claimCall(control, call)
	if err != nil {
		return exitNoRecord
	}
	call.Seq = seq
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 8*agents.MaxPromptBytes)
	phase := 0
	for scanner.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Protocol int    `json:"protocolVersion"`
				Auth     string `json:"methodId"`
				Session  string `json:"sessionId"`
				Prompt   []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"prompt"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			return exitNoStdin
		}
		result := any(map[string]any{})
		switch m.Method {
		case "initialize":
			if phase != 0 || m.Params.Protocol != 1 {
				return exitNoStdin
			}
			phase = 1
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}, "authMethods": []any{map[string]string{"id": "cursor_login"}}}
		case "authenticate":
			if phase != 1 || m.Params.Auth != "cursor_login" {
				return exitNoStdin
			}
			phase = 2
		case "session/new":
			if phase != 2 {
				return exitNoStdin
			}
			phase = 3
			result = map[string]string{"sessionId": "standin-acp-session"}
		case "session/prompt":
			if phase != 3 || m.Params.Session != "standin-acp-session" {
				return exitNoStdin
			}
			text := ""
			for _, block := range m.Params.Prompt {
				if block.Type != "text" {
					return exitNoStdin
				}
				text += block.Text
			}
			call.Lines = append(call.Lines, text)
			if record(callsPath(control), call) != nil {
				return exitNoRecord
			}
			if refusal, ok := worker.refusal(text); ok {
				if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32603, "message": refusal}}) != nil {
					return exitNoOutput
				}
				continue
			}
			reply := "standin: message received"
			ends := false
			for _, exchange := range worker.Exchanges {
				if exchange.answers(text) {
					if exchange.Run {
						_, command, _ := strings.Cut(text, exchange.Contains)
						if !acpPermission(encoder, scanner, strings.TrimSpace(command)) {
							reply = "standin: command permission refused"
							break
						}
					}
					reply, err = exchange.answer(text)
					if err != nil {
						return exitNoOutput
					}
					ends = exchange.Ends
					break
				}
			}
			if encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "standin-acp-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": reply}}}}) != nil {
				return exitNoOutput
			}
			result = map[string]string{"stopReason": "end_turn"}
			if ends {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
				return worker.Exit
			}
		default:
			fmt.Fprintln(os.Stderr, "standin: unsupported ACP method", m.Method)
			return exitNoStdin
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}) != nil {
			return exitNoOutput
		}
	}
	return worker.Exit
}

// ACP permission requests use the documented tool-call input and allow-once
// option. A Run exchange executes only after the real client selects it.
func acpPermission(encoder *json.Encoder, scanner *bufio.Scanner, command string) bool {
	request := map[string]any{"jsonrpc": "2.0", "id": "permission", "method": "session/request_permission", "params": map[string]any{"sessionId": "standin-acp-session", "toolCall": map[string]any{"toolCallId": "tool-1", "kind": "execute", "rawInput": map[string]string{"command": command}}, "options": []any{map[string]string{"optionId": "once", "kind": "allow_once"}, map[string]string{"optionId": "reject", "kind": "reject_once"}}}}
	if encoder.Encode(request) != nil || !scanner.Scan() {
		return false
	}
	var response struct {
		ID     string `json:"id"`
		Result struct {
			Outcome struct {
				Kind   string `json:"outcome"`
				Option string `json:"optionId"`
			} `json:"outcome"`
		} `json:"result"`
	}
	return json.Unmarshal(scanner.Bytes(), &response) == nil && response.ID == "permission" && response.Result.Outcome.Kind == "selected" && response.Result.Outcome.Option == "once"
}
