package agents_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// The tests drive a stand-in agent rather than a real Claude Code
// installation, so what they prove is a property of this package rather than
// of whatever happens to be installed. The stand-in is this test binary
// re-executed with helperModeVar set, which needs no build step and works on
// every platform the module targets.
// helperHold is how long the stand-in agent's waiting modes stay alive. It is
// far longer than any assertion below allows, so a process still running when
// one of them looks is a process this package did not end, and short enough
// that a broken guard fails in seconds rather than minutes.
const helperHold = 60 * time.Second

const (
	helperModeVar   = "AGENTS_HELPER_MODE"
	helperResultVar = "AGENTS_HELPER_RESULT"
	helperStdoutVar = "AGENTS_HELPER_STDOUT"
	helperStderrVar = "AGENTS_HELPER_STDERR"
	helperExitVar   = "AGENTS_HELPER_EXIT"
	helperBytesVar  = "AGENTS_HELPER_BYTES"
	helperPidVar    = "AGENTS_HELPER_PIDFILE"
	helperEchoVar   = "AGENTS_HELPER_ECHO"
)

// TestMain turns this binary into the stand-in agent when the mode variable is
// set, and runs the tests otherwise.
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeVar); mode != "" {
		helperMain(mode)
		return
	}
	os.Exit(m.Run())
}

// helperMain is the stand-in agent. Each mode is one thing an agent can do to
// this package, and the environment carries the details, which is also how the
// tests exercise per-invocation environment.
func helperMain(mode string) {
	switch mode {
	case "envelope":
		// Print a well-formed result envelope carrying the configured result.
		fmt.Print(helperEnvelope(os.Getenv(helperResultVar), helperSession()))
	case "raw":
		// Print whatever the test configured, envelope or not.
		fmt.Print(os.Getenv(helperStdoutVar))
	case "args":
		// Report the command line this package built, as the result.
		fmt.Print(helperEnvelope(strings.Join(os.Args[1:], "\n"), helperSession()))
	case "env":
		// Report the value of one environment variable, as the result.
		fmt.Print(helperEnvelope(os.Getenv(os.Getenv(helperEchoVar)), helperSession()))
	case "error-envelope":
		// A result envelope in which the agent reports its own failure.
		out := map[string]any{
			"is_error": true,
			"subtype":  "error_during_execution",
			"result":   os.Getenv(helperResultVar),
		}
		encoded, _ := json.Marshal(out)
		fmt.Print(string(encoded))
	case "oversize":
		n, _ := strconv.Atoi(os.Getenv(helperBytesVar))
		_, _ = os.Stdout.Write(make([]byte, n))
	case "fail":
		fmt.Fprint(os.Stderr, os.Getenv(helperStderrVar))
		code, _ := strconv.Atoi(os.Getenv(helperExitVar))
		os.Exit(code)
	case "spawn-and-exit":
		// Start a child, then finish successfully and leave it running. It is
		// the completion path: nothing was cancelled and the agent did nothing
		// wrong, so only the invocation's ownership of the tree ends the
		// child.
		helperSpawn(false, false)
		fmt.Print(helperEnvelope("finished, and left something running", helperSession()))
	case "spawn":
		helperSpawn(false, true)
	case "spawn-stubborn":
		// Both the agent and its child ignore the polite signal, so only the
		// forceful one ends them.
		helperSpawn(true, true)
	case "hold":
		if os.Getenv(helperStderrVar) == "stubborn" {
			signal.Ignore(helperPoliteSignals()...)
		}
		time.Sleep(helperHold)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode "+mode)
		os.Exit(2)
	}
	os.Exit(0)
}

// helperSpawn starts a grandchild that stays in the invocation's process
// group, records both process identifiers where the test can read them, and
// then waits. It is what makes "a cancelled invocation leaves no surviving
// child process" a claim about a tree rather than about one process.
func helperSpawn(stubborn, wait bool) {
	self, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	child := exec.Command(self)
	child.Env = []string{helperModeVar + "=hold"}
	if stubborn {
		child.Env = append(child.Env, helperStderrVar+"=stubborn")
	}
	if err := child.Start(); err != nil {
		os.Exit(4)
	}
	pidFile := os.Getenv(helperPidVar)
	line := strconv.Itoa(os.Getpid()) + "\n" + strconv.Itoa(child.Process.Pid) + "\n"
	if err := os.WriteFile(pidFile, []byte(line), 0o600); err != nil {
		os.Exit(5)
	}
	if !wait {
		return
	}
	if stubborn {
		signal.Ignore(helperPoliteSignals()...)
	}
	time.Sleep(helperHold)
}

// helperSession reports the session identifier the stand-in agent claims. It
// echoes back a resumed session so a test can tell a continued conversation
// from a fresh one.
func helperSession() string {
	args := os.Args[1:]
	for i, a := range args {
		if a == "--resume" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "session-opened"
}

// helperEnvelope renders a result envelope of the shape the adapter reads.
func helperEnvelope(result, session string) string {
	out := map[string]any{
		"type":       "result",
		"subtype":    "success",
		"is_error":   false,
		"result":     result,
		"session_id": session,
		"model":      "stand-in-model",
		"num_turns":  3,
		"usage": map[string]any{
			"input_tokens":                11,
			"output_tokens":               22,
			"cache_read_input_tokens":     33,
			"cache_creation_input_tokens": 44,
		},
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// helperBinary is the path tests point the adapter at.
func helperBinary(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	return self
}

// newRunner builds a Runner over the stand-in agent, with an empty base
// environment so an invocation's own Env is the whole environment and a short
// grace so termination tests do not wait on the real one.
func newRunner(t *testing.T, opts ...agents.ClaudeOption) agents.Runner {
	t.Helper()
	base := []agents.ClaudeOption{
		agents.WithBinary(helperBinary(t)),
		agents.WithBaseEnvironment(nil),
		agents.WithTerminationGrace(300 * time.Millisecond),
	}
	runner, err := agents.ClaudeFactory(append(base, opts...)...).New(t.Context(), nil)
	if err != nil {
		t.Fatalf("building a runner over the stand-in agent: %v", err)
	}
	return runner
}

// invocation is a valid Invocation in a directory the test owns, with the
// stand-in agent's mode and details supplied per invocation.
func invocation(t *testing.T, shape agents.Shape, env map[string]string) agents.Invocation {
	t.Helper()
	return agents.Invocation{
		Prompt: "review this change",
		Shape:  shape,
		Dir:    t.TempDir(),
		Env:    env,
	}
}

// recorder collects every Record an invocation produced.
type recorder struct{ records []agents.Record }

func (r *recorder) RecordInvocation(rec agents.Record) { r.records = append(r.records, rec) }

// reportJSON is a stage report the findings package accepts.
const reportJSON = `{"summary":"one thing was checked","findings":[` +
	`{"description":"a real defect","action":"fix","severity":"error"}]}`
