package agents_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// requireClaudeVar names the environment variable that turns a missing
// Claude Code installation from a skip into a failure for the probe below,
// on the terms standin.RequireTmuxVar gives a missing tmux.
const requireClaudeVar = "ASSISTANT_REQUIRE_CLAUDE"

// installedClaude is the Claude Code binary on PATH, or a skip saying the
// probe against it was not run.
func installedClaude(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath(agents.ClaudeName)
	if err == nil {
		if abs, aerr := filepath.Abs(path); aerr == nil {
			return abs
		}
		return path
	}
	if os.Getenv(requireClaudeVar) != "" {
		t.Fatalf("%s is set and no %s binary is on PATH: %v", requireClaudeVar, agents.ClaudeName, err)
	}
	t.Skipf("no %s binary is on PATH (%v), so the terminal probe against an installed Claude Code was not run, "+
		"and the terminal_workers declaration rests on the stand-in's model of version %s; set %s to make that a failure",
		agents.ClaudeName, err, agents.ClaudeObservedVersion, requireClaudeVar)
	return ""
}

// fakeMessages is a local stand-in for the model's Messages API: it answers
// every message request with a short reply, streamed or not as asked, and
// keeps the text of the last user message of each request. It keeps no
// header, so whatever credential a request carried is never held.
type fakeMessages struct {
	mu    sync.Mutex
	texts []string
}

func (f *fakeMessages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/messages") || strings.Contains(r.URL.Path, "count_tokens") {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	text := ""
	for i := len(body.Messages) - 1; i >= 0; i-- {
		if body.Messages[i].Role != "user" {
			continue
		}
		var plain string
		if json.Unmarshal(body.Messages[i].Content, &plain) == nil {
			text = plain
			break
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(body.Messages[i].Content, &blocks)
		for _, b := range blocks {
			if b.Type == "text" {
				text = b.Text
			}
		}
		break
	}
	f.mu.Lock()
	f.texts = append(f.texts, text)
	n := len(f.texts)
	f.mu.Unlock()

	reply := fmt.Sprintf("probe reply %d", n)
	message := map[string]any{
		"id": fmt.Sprintf("msg_probe_%d", n), "type": "message", "role": "assistant", "model": body.Model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
	}
	if !body.Stream {
		message["content"] = []any{map[string]string{"type": "text", "text": reply}}
		message["stop_reason"] = "end_turn"
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(message)
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	event := func(name string, data any) {
		encoded, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	event("message_start", map[string]any{"type": "message_start", "message": message})
	event("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
		"content_block": map[string]string{"type": "text", "text": ""}})
	event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
		"delta": map[string]string{"type": "text_delta", "text": reply}})
	event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	event("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 1}})
	event("message_stop", map[string]any{"type": "message_stop"})
}

// received reports whether some request's last user message carried every
// one of parts.
func (f *fakeMessages) received(parts ...string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, text := range f.texts {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(text, p)
		}
		if all {
			return true
		}
	}
	return false
}

// awaitReceived waits until the fake has been sent a message carrying
// every one of parts, and fails with the worker's screen when it is not.
func (f *fakeMessages) awaitReceived(t *testing.T, w agents.Worker, parts ...string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !f.received(parts...) {
		if !time.Now().Before(deadline) {
			f.mu.Lock()
			defer f.mu.Unlock()
			t.Fatalf("no message carrying %q reached the model; it was sent %d messages:\n%q\nThe screen:\n%s",
				parts, len(f.texts), f.texts, w.Capture().Text)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The terminal_workers probe, held against an installed Claude Code rather
// than the stand-in. The CLI is the real binary in a real tmux pane; the
// model is fakeMessages on this machine, reached through ANTHROPIC_BASE_URL,
// with a placeholder key, a HOME of the test's own and no proxy, so no
// account is used and no model is called. It skips, saying so, where there
// is no claude on PATH or no tmux, and it logs the CLI version it ran
// against.
//
// It runs the conformance suite's own terminal probe first, the one
// TestTheShippedAdapterConformsToItsDeclaration runs against the stand-in,
// and then asks what the stand-in's model rests on: instructions beginning
// with a dash reach the model, a multi-line send reaches it as one message,
// and a send
// beginning with "!" or "/" reaches it as a message and runs nothing.
func TestTheTerminalProbeHoldsAgainstAnInstalledClaudeCode(t *testing.T) {
	tmux := standin.Tmux(t)
	claude := installedClaude(t)

	api := &fakeMessages{}
	server := httptest.NewServer(api)
	defer server.Close()
	home := t.TempDir()
	env := []string{
		"PATH=" + filepath.Dir(claude) + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin",
		"HOME=" + home,
		"LANG=C.UTF-8",
		"ANTHROPIC_BASE_URL=" + server.URL,
		agents.ClaudeKeyVariable + "=" + standinKey,
		// Without it the CLI was seen to write a copy of itself, a few
		// hundred megabytes, under a fresh HOME on every run.
		"DISABLE_AUTOUPDATER=1",
	}

	ask := exec.Command(claude, "--version")
	ask.Env = env
	version, err := ask.Output()
	if err != nil {
		t.Fatalf("asking %s its version: %v", claude, err)
	}
	ran := strings.Fields(string(version))
	if len(ran) == 0 {
		t.Fatalf("%s answered %q for its version", claude, version)
	}
	t.Logf("the terminal probe ran against Claude Code %s; the terminal mode's screen text was observed on %s",
		ran[0], agents.ClaudeObservedVersion)

	runner, err := agents.ClaudeFactory(
		agents.WithBinary(claude),
		agents.WithBaseEnvironment(env),
		agents.WithTerminationGrace(2*time.Second),
	).New(t.Context(), nil)
	if err != nil {
		t.Fatalf("building the adapter over %s: %v", claude, err)
	}
	p, _ := probeFor(agents.CapabilityTerminalWorkers)
	p.declared(t, runner)
	if t.Failed() {
		return
	}

	// The instructions begin with a dash, which a command line reads as a
	// flag unless they follow "--".
	spec := agents.WorkerSpec{Dir: t.TempDir(), Instructions: "- probe instructions alpha"}
	term := agents.Terminal{Tmux: tmux, Socket: filepath.Join(standin.SocketDir(t), "p")}
	worker, err := agents.OpenTerminalWorker(t.Context(), runner, term, spec)
	if err != nil {
		t.Fatalf("starting a terminal worker on the installed CLI: %v", err)
	}
	defer worker.Close()
	api.awaitReceived(t, worker, "- probe instructions alpha")

	for _, c := range []struct {
		send string
		want []string
	}{
		{"probe line one\nprobe line two\n\nprobe line four", []string{"probe line one\nprobe line two\n\nprobe line four"}},
		{"!touch probe-bang", []string{" !touch probe-bang"}},
		{"/probe-slash command", []string{" /probe-slash command"}},
	} {
		if err := worker.Send(t.Context(), c.send); err != nil {
			t.Fatalf("Send(%q) to the installed CLI: %v", c.send, err)
		}
		api.awaitReceived(t, worker, c.want...)
	}
	if _, err := os.Stat(filepath.Join(spec.Dir, "probe-bang")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a send beginning with ! ran as a shell command on the installed CLI: %v", err)
	}
	// The CLI kept its state in the worker's own directory, not the home's.
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the installed CLI wrote a state file under HOME: %v", err)
	}
}
