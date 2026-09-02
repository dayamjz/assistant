package agents_test

import (
	"encoding/json"
	"fmt"
	"io"
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
// re-executed with a mode named in helperModeVar, or on its command line where
// the environment under test carries nothing. It needs no build step and works
// on every platform the module targets.
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

// helperModeFlag carries the stand-in agent's mode on its command line, for
// the tests that must run it with no environment at all. A configured entry's
// own arguments reach the agent through ClaudeFactory.New, so this is a mode
// the agent can be given without the per-invocation environment carrying
// anything.
const helperModeFlag = "--agents-helper-mode="

// helperModeFromArgs is the mode named on the command line, empty when none
// is. The flag is spelled distinctly enough that the real test binary, which
// is this same executable, cannot be given one by accident.
func helperModeFromArgs() string {
	for _, a := range os.Args[1:] {
		if after, ok := strings.CutPrefix(a, helperModeFlag); ok {
			return after
		}
	}
	return ""
}

// TestMain turns this binary into the stand-in agent when a mode is named,
// either in the environment or on the command line, and runs the tests
// otherwise.
func TestMain(m *testing.M) {
	mode := os.Getenv(helperModeVar)
	if mode == "" {
		mode = helperModeFromArgs()
	}
	if mode != "" {
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
		// Print whatever the test configured, envelope or not, and exit with
		// the status it configured, so the two can be combined.
		fmt.Fprint(os.Stderr, os.Getenv(helperStderrVar))
		fmt.Print(os.Getenv(helperStdoutVar))
		os.Exit(helperExitCode())
	case "call":
		// Report how this package called the agent: the command line it built
		// and what reached the agent's standard input.
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "reading standard input: "+err.Error())
			os.Exit(6)
		}
		encoded, err := json.Marshal(agentCall{Args: os.Args[1:], Stdin: string(input)})
		if err != nil {
			fmt.Fprintln(os.Stderr, "encoding the call: "+err.Error())
			os.Exit(7)
		}
		fmt.Print(helperEnvelope(string(encoded), helperSession()))
	case "environment":
		// Report the whole environment the agent was given, as the result. It
		// takes its mode from the command line, so it is reachable with an
		// environment that carries nothing at all, and it is encoded rather
		// than joined so that an empty environment is still a result.
		encoded, err := json.Marshal(os.Environ())
		if err != nil {
			fmt.Fprintln(os.Stderr, "encoding the environment: "+err.Error())
			os.Exit(8)
		}
		fmt.Print(helperEnvelope(string(encoded), helperSession()))
	case "env":
		// Report the value of one environment variable, as the result.
		fmt.Print(helperEnvelope(os.Getenv(os.Getenv(helperEchoVar)), helperSession()))
	case "error-envelope":
		// A result envelope in which the agent reports its own failure.
		fmt.Print(helperErrorEnvelope(os.Getenv(helperResultVar)))
	case "oversize":
		n, _ := strconv.Atoi(os.Getenv(helperBytesVar))
		_, _ = os.Stdout.Write(make([]byte, n))
	case "fail":
		fmt.Fprint(os.Stderr, os.Getenv(helperStderrVar))
		os.Exit(helperExitCode())
	case "self-kill":
		// End without reporting a status of the agent's own, which is what an
		// agent the system kills looks like to this package.
		helperSelfKill()
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
	case "envelope-then-hold":
		// Print a complete result envelope, say so where the test can see it,
		// and then outlive the invocation. It is an agent that reported what
		// it spent and was then ended by something other than itself.
		fmt.Print(helperEnvelope(os.Getenv(helperResultVar), helperSession()))
		if err := os.WriteFile(os.Getenv(helperPidVar),
			[]byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
			os.Exit(5)
		}
		time.Sleep(helperHold)
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

// helperExitCode is the status the stand-in agent exits with, zero when the
// test configured none.
func helperExitCode() int {
	code, _ := strconv.Atoi(os.Getenv(helperExitVar))
	return code
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
	// The child runs outside the invocation's working directory, which the
	// test owns and removes when the test returns. A running process holds its
	// working directory open on Windows, and a descendant that outlives the
	// invocation is a documented residual gap there, so a child left in that
	// directory would fail the test's own cleanup for a reason none of these
	// tests are about. Where the child runs is not part of what any of them
	// assert; that it is still in the invocation's process group is, and that
	// is unaffected.
	child.Dir = os.TempDir()
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
	return helperResultEnvelope(false, "success", result, session)
}

// helperErrorEnvelope renders a result envelope in which the agent reports its
// own failure. It carries the same usage as a successful one, because an agent
// that failed still spent what it spent.
func helperErrorEnvelope(result string) string {
	return helperResultEnvelope(true, "error_during_execution", result, "session-opened")
}

func helperResultEnvelope(isError bool, subtype, result, session string) string {
	out := map[string]any{
		"type":       "result",
		"subtype":    subtype,
		"is_error":   isError,
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

// helperUsage is the usage every envelope the stand-in agent prints reports.
func helperUsage() agents.Usage {
	return agents.Usage{
		InputTokens:         agents.ReportedCount(11),
		OutputTokens:        agents.ReportedCount(22),
		CacheReadTokens:     agents.ReportedCount(33),
		CacheCreationTokens: agents.ReportedCount(44),
		Turns:               agents.ReportedCount(3),
	}
}

// agentCall is what the stand-in agent's "call" mode reports: the command line
// this package built and what reached the agent's standard input.
type agentCall struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

// decodeCall reads what the "call" mode reported out of an invocation's text.
func decodeCall(t *testing.T, text string) agentCall {
	t.Helper()
	var call agentCall
	if err := json.Unmarshal([]byte(text), &call); err != nil {
		t.Fatalf("the stand-in agent did not report how it was called: %v (%q)", err, text)
	}
	return call
}

// resumedSession is the session the command line asked to continue, empty when
// it asked for none.
func (c agentCall) resumedSession() string {
	for i, a := range c.Args {
		if a == "--resume" && i+1 < len(c.Args) {
			return c.Args[i+1]
		}
	}
	return ""
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
