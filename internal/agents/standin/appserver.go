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

// NewCodex scripts the production Codex adapter, including its app-server
// pipe worker. It records decoded turn text, never typed adapter results.
// The frames it writes are the ones Codex CLI 0.163.0 emitted, kept in
// internal/agents/testdata/codex-app-server-0.163.0.jsonl.
func NewCodex(tb testing.TB, script Script, opts ...agents.CodexOption) *Agent {
	tb.Helper()
	self, err := os.Executable()
	if err != nil {
		tb.Fatal(err)
	}
	control := prepare(tb, script)
	verify(tb, self, handshakeFlag)
	runner, err := agents.CodexFactory(slices.Concat(opts, []agents.CodexOption{agents.WithCodexBinary(self)})...).New(tb.Context(), []string{controlFlag + control})
	if err != nil {
		tb.Fatal(err)
	}
	return &Agent{tb: tb, control: control, runner: runner}
}

// serveAppServer models the newline JSON the installed Codex CLI 0.163.0
// accepted and emitted: initialize, an initialized notification, thread/start,
// then one turn/start per message. A turn's result is in progress; the turn
// is finished by thread/status/changed idle after the agent message. It
// requires that order, a workspace-write sandbox, and on-request approval.
func serveAppServer(control string, args []string) int {
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
	const threadID = "standin-codex-thread"
	for scanner.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Cwd      string `json:"cwd"`
				Sandbox  string `json:"sandbox"`
				Approval string `json:"approvalPolicy"`
				Thread   string `json:"threadId"`
				Input    []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"input"`
			} `json:"params"`
		}
		raw := scanner.Bytes()
		if json.Unmarshal(raw, &m) != nil {
			return exitNoStdin
		}
		var caps struct {
			Params struct {
				Capabilities struct {
					Experimental bool `json:"experimentalApi"`
				} `json:"capabilities"`
				Ephemeral bool `json:"ephemeral"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &caps)
		switch m.Method {
		case "initialize":
			if phase != 0 || !caps.Params.Capabilities.Experimental {
				return exitNoStdin
			}
			phase = 1
			if encoder.Encode(map[string]any{"id": m.ID, "result": map[string]any{"userAgent": "assistant/0.163.0"}}) != nil {
				return exitNoOutput
			}
		case "initialized":
			if phase != 1 || len(m.ID) != 0 {
				return exitNoStdin
			}
			phase = 2
		case "thread/start":
			if phase != 2 || m.Params.Cwd == "" || m.Params.Sandbox != "workspace-write" || m.Params.Approval != "on-request" || !caps.Params.Ephemeral {
				return exitNoStdin
			}
			phase = 3
			if encoder.Encode(map[string]any{"id": m.ID, "result": map[string]any{"thread": map[string]any{"id": threadID}}}) != nil {
				return exitNoOutput
			}
		case "turn/start":
			if phase != 3 || m.Params.Thread != threadID {
				return exitNoStdin
			}
			text := ""
			for _, block := range m.Params.Input {
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
				if encoder.Encode(map[string]any{"id": m.ID, "error": map[string]any{"code": -32603, "message": refusal}}) != nil {
					return exitNoOutput
				}
				continue
			}
			if encoder.Encode(map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]any{"id": "standin-codex-turn", "status": "inProgress"}}}) != nil {
				return exitNoOutput
			}
			reply := "standin: message received"
			ends := false
			var recorded []string
			for _, exchange := range worker.Exchanges {
				if !exchange.answers(text) {
					continue
				}
				if exchange.Run {
					_, command, _ := strings.Cut(text, exchange.Contains)
					if !appServerPermission(encoder, scanner, strings.TrimSpace(command)) {
						reply = "standin: command permission refused"
						break
					}
				}
				ends = exchange.Ends
				if len(exchange.Recorded) > 0 {
					recorded = exchange.Recorded
					break
				}
				var err error
				reply, err = exchange.answer(text)
				if err != nil {
					return exitNoOutput
				}
				break
			}
			if recorded != nil {
				// Recorded frames replace the agent message and the idle,
				// so a turn plays back what a real app-server was recorded
				// emitting after it accepted one, a failure or a partial
				// turn among them.
				for _, line := range recorded {
					if _, err := fmt.Fprintln(os.Stdout, line); err != nil {
						return exitNoOutput
					}
				}
			} else {
				if encoder.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"item": map[string]any{"type": "agentMessage", "text": reply}, "threadId": threadID}}) != nil {
					return exitNoOutput
				}
				if encoder.Encode(map[string]any{"method": "thread/status/changed", "params": map[string]any{"threadId": threadID, "status": map[string]string{"type": "idle"}}}) != nil {
					return exitNoOutput
				}
			}
			if ends {
				return worker.Exit
			}
		default:
			fmt.Fprintln(os.Stderr, "standin: unsupported app-server method", m.Method)
			return exitNoStdin
		}
	}
	return worker.Exit
}

// appServerPermission models item/commandExecution/requestApproval, the
// method Codex CLI 0.163.0's schema names for a turn started with turn/start.
// The decision vocabulary is that schema's. A Run exchange continues only
// after the client answers accept.
func appServerPermission(encoder *json.Encoder, scanner *bufio.Scanner, command string) bool {
	request := map[string]any{
		"id": 9001, "method": "item/commandExecution/requestApproval",
		"params": map[string]any{
			"command": command, "threadId": "standin-codex-thread", "turnId": "standin-codex-turn",
			"itemId": "item", "startedAtMs": 1,
		},
	}
	if encoder.Encode(request) != nil || !scanner.Scan() {
		return false
	}
	var response struct {
		Result struct {
			Decision string `json:"decision"`
		} `json:"result"`
	}
	return json.Unmarshal(scanner.Bytes(), &response) == nil && response.Result.Decision == "accept"
}
