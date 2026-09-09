package forge_test

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The adapter tests drive a stand-in provider command rather than a real gh
// installation, so what they prove is a property of this package rather than
// of whatever happens to be installed. The stand-in is this test binary
// re-executed with fakeGHDir naming a directory that holds the responses it
// should give and the calls it recorded. It needs no build step and works on
// every platform the module targets.
//
// The responses the tests write are the bytes gh puts on standard output for
// the field sets this adapter asks for, including the fields the adapter does
// not read. A stand-in that answered in some tidier shape would let a test
// pass over an answer the real command cannot produce.
const (
	fakeGHDir      = "FORGE_TEST_FAKE_GH_DIR"
	fakeGHHold     = "FORGE_TEST_HOLD_PIPES"
	fakeGHScript   = "script.json"
	fakeGHCalls    = "calls.json"
	fakeGHNoScript = 97
)

// fakeGHHoldFor is how long a descendant of the stand-in provider holds the
// output pipes it inherited. It only has to outlast the grace period a test
// gives the adapter, and it is short so that nothing lingers after the run.
const fakeGHHoldFor = 5 * time.Second

// ghResponse is one scripted answer from the stand-in provider.
type ghResponse struct {
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Exit    int    `json:"exit"`
	SleepMs int    `json:"sleep_ms"`
	// Kill ends the stand-in with a signal instead of an exit, so it reports
	// no status of its own. It is the shape a provider the system kills puts
	// on the wire, and standInSelfKill says where it can be produced.
	Kill bool `json:"kill"`
	// HoldPipes leaves a descendant behind that inherited the stand-in's
	// standard output and error and outlives it. The stand-in itself exits
	// normally, so what keeps the invocation waiting afterwards is the
	// descendant holding pipes nobody is going to close.
	HoldPipes bool `json:"hold_pipes"`
}

// ghScript maps a call key to the answers the stand-in gives for it, in order.
// The last entry answers every call past the ones listed, so a test states a
// second answer only where the second call differs.
type ghScript map[string][]ghResponse

// ghCall is one recorded invocation of the stand-in provider.
type ghCall struct {
	Key   string   `json:"key"`
	Args  []string `json:"args"`
	Env   []string `json:"env"`
	Stdin string   `json:"stdin"`
	Dir   string   `json:"dir"`
}

// callKey names the operation an argument vector performs, which is what a
// test scripts an answer for. It reads the vector the way the real command
// does: the subcommand pair, and for a read the field set that distinguishes a
// pull request read from a check read.
func callKey(args []string) string {
	if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
		return "repo"
	}
	if len(args) < 2 || args[0] != "pr" {
		return "other"
	}
	switch args[1] {
	case "list":
		return "list"
	case "create":
		return "create"
	case "edit":
		return "edit"
	case "view":
		for _, a := range args {
			if strings.HasPrefix(a, "--json=") && strings.Contains(a, "statusCheckRollup") {
				return "checks"
			}
		}
		return "view"
	default:
		return "other"
	}
}

// subcommandFlags is which flags each subcommand this adapter runs accepts,
// written down from the real command's own help rather than from what the
// adapter happens to send. It is deliberately narrow: only the flags a test
// could plausibly see are listed, and anything else is refused.
//
// The repository is the entry that matters. The pull request subcommands take
// it as --repo and the repository read takes it as an operand, so a vector
// carrying --repo into a repository read is refused here exactly as the real
// command refuses it.
var subcommandFlags = map[string]map[string]bool{
	"pr": {
		"--head": true, "--state": true, "--limit": true, "--json": true,
		"--base": true, "--title": true, "--body-file": true, "--draft": true,
		"--repo": true,
	},
	"repo": {"--json": true},
}

// unsupportedFlag reports the first flag in args that the subcommand does not
// accept.
func unsupportedFlag(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	accepted, known := subcommandFlags[args[0]]
	if !known {
		return "", false
	}
	for _, a := range args[1:] {
		if !strings.HasPrefix(a, "--") {
			continue
		}
		name, _, _ := strings.Cut(a, "=")
		if !accepted[name] {
			return name, true
		}
	}
	return "", false
}

// carriesCredential reports whether anything the adapter sent this invocation
// holds the secret these tests plant, and which side it came in on.
//
// It looks for the literal secret rather than for text that looks unredacted,
// and that choice is what makes it usable as a blanket check. A pattern for
// "a userinfo that was not redacted" has to know what a redaction looks like,
// and this package's tests supply their own Redactor whose marker is its own
// business, so such a pattern would fail every properly redacted write. The
// secret is unambiguous: if it left the process, it was published.
//
// The reach of that is worth stating rather than implying away. It catches an
// outbound path that carries the secret these tests plant, which is every path
// a test exercises with one; it says nothing about a credential of some other
// shape, and what the adapter removes is internal/redact's business and stated
// there.
func carriesCredential(args []string, stdin string) (string, bool) {
	for _, a := range args {
		if strings.Contains(a, secret) {
			return "argument", true
		}
	}
	if strings.Contains(stdin, secret) {
		return "body", true
	}
	return "", false
}

// TestMain runs the stand-in provider when the environment asks for it, and
// the tests otherwise.
func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeGHDir); dir != "" {
		os.Exit(fakeGHMain(dir))
	}
	os.Exit(m.Run())
}

func fakeGHMain(dir string) int {
	if os.Getenv(fakeGHHold) != "" {
		// The descendant of a stand-in asked to leave its pipes behind. It
		// reads no script and prints nothing; holding the standard output and
		// error it inherited is the whole of what it does.
		time.Sleep(fakeGHHoldFor)
		return 0
	}

	args := os.Args[1:]
	if flag, bad := unsupportedFlag(args); bad {
		// The real command refuses a flag the subcommand does not define, and
		// so does this: a stand-in that accepted an argument vector gh rejects
		// would let a test pass over a command line that cannot run. This
		// package shipped exactly that once, appending the pull request
		// commands' repository flag to a repository read that takes an
		// operand.
		os.Stderr.WriteString("unknown flag: " + flag + "\n")
		return 1
	}
	key := callKey(args)

	// Standard input is read to the end so a test can see what the adapter
	// sent there. A terminal would block here; os.DevNull ends at once.
	stdin, _ := io.ReadAll(os.Stdin)
	wd, _ := os.Getwd()

	// The credential check comes after standard input has been read, because
	// a body arrives there and standard input can only be read once. Ordering
	// it before the read is a guard that inspects an empty string and passes
	// whatever the adapter sent, which is what it looked like when this was
	// first written.
	//
	// It fails the invocation rather than recording it, so a write path added
	// later that skips redaction fails whatever test exercises it instead of
	// failing only a test written to look.
	//
	// The real gh accepts such text, so this is stricter than the command it
	// stands in for. That is deliberate and it is the one place this stand-in
	// may be: everywhere else it may only refuse what gh refuses, because a
	// fake that rejects what the real thing accepts makes a test fail for a
	// reason the product does not have. Here the stricter answer is the
	// adapter's own contract rather than this file's invention.
	if where, carried := carriesCredential(args, string(stdin)); carried {
		os.Stderr.WriteString("stand-in provider: outbound " + where + " carries a credential\n")
		return 1
	}

	calls := readCalls(dir)
	seen := 0
	for _, c := range calls {
		if c.Key == key {
			seen++
		}
	}
	calls = append(calls, ghCall{Key: key, Args: args, Env: os.Environ(), Stdin: string(stdin), Dir: wd})
	blob, err := json.Marshal(calls)
	if err != nil {
		return fakeGHNoScript
	}
	if err := os.WriteFile(filepath.Join(dir, fakeGHCalls), blob, 0o600); err != nil {
		return fakeGHNoScript
	}

	var script ghScript
	raw, err := os.ReadFile(filepath.Join(dir, fakeGHScript))
	if err != nil || json.Unmarshal(raw, &script) != nil {
		os.Stderr.WriteString("stand-in provider: no script\n")
		return fakeGHNoScript
	}
	answers, ok := script[key]
	if !ok || len(answers) == 0 {
		os.Stderr.WriteString("stand-in provider: no scripted answer for " + key + "\n")
		return fakeGHNoScript
	}
	answer := answers[len(answers)-1]
	if seen < len(answers) {
		answer = answers[seen]
	}
	if answer.HoldPipes && !startPipeHolder() {
		os.Stderr.WriteString("stand-in provider: could not leave a descendant holding the pipes\n")
		return fakeGHNoScript
	}
	if answer.SleepMs > 0 {
		time.Sleep(time.Duration(answer.SleepMs) * time.Millisecond)
	}
	os.Stdout.WriteString(answer.Stdout)
	os.Stderr.WriteString(answer.Stderr)
	if answer.Kill {
		standInSelfKill()
	}
	return answer.Exit
}

// startPipeHolder starts a descendant that inherits this process's standard
// output and error and outlives it, and reports whether it started. Its
// working directory is deliberately not this one, so a test's temporary
// directory can be removed while it is still running.
func startPipeHolder() bool {
	child := exec.Command(os.Args[0])
	child.Dir = os.TempDir()
	child.Env = append(os.Environ(), fakeGHHold+"=1")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	return child.Start() == nil
}

func readCalls(dir string) []ghCall {
	raw, err := os.ReadFile(filepath.Join(dir, fakeGHCalls))
	if err != nil {
		return nil
	}
	var calls []ghCall
	if json.Unmarshal(raw, &calls) != nil {
		return nil
	}
	return calls
}

// testRedactor is a Redactor of the shape internal/vcs describes: it replaces
// the userinfo of a URL that carries a scheme. It is written here rather than
// taken from that package because that package's own implementation is
// unexported, and a caller of this one supplies its own for the same reason.
var testRedactor = vcs.RedactorFunc(func(s string) string {
	return urlUserinfo.ReplaceAllString(s, "${1}REDACTED@")
})

var urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s]+)@`)

// secretURL is a remote URL carrying a credential, and secret is the part of
// it that must never reach a caller. Tests put the URL where provider text and
// caller arguments travel, and assert the secret does not come back.
const (
	secret    = "ghp_notarealtokenbutlooksrightXY"
	secretURL = "https://x-access-token:" + secret + "@github.com/owner/name.git"
)

// harnessRepository is the repository a write harness addresses, and the one
// its stand-in reports back when the confirmation asks. A read harness names
// none, because a read does not need one.
const harnessRepository = "owner/name"

// repoViewJSON is the answer gh puts on the wire for the field set the
// repository confirmation asks for. A test states the repository the provider
// resolves the adapter's specifier to, which is the whole of what the
// confirmation compares.
func repoViewJSON(nameWithOwner string) string {
	return `{"nameWithOwner":"` + nameWithOwner + `"}`
}

// harness is a stand-in provider together with the adapter that talks to it.
type harness struct {
	t   *testing.T
	dir string
	gh  *forge.GitHub
}

// newHarness writes a script for the stand-in provider and returns an adapter
// wired to it. The adapter is built through the ordinary exported constructor,
// so the tests exercise the same construction path a caller uses.
func newHarness(t *testing.T, script ghScript, opts ...forge.Option) *harness {
	t.Helper()
	dir := t.TempDir()
	writeScript(t, dir, script)
	base := append([]forge.Option{
		forge.WithBinary(os.Args[0]),
		forge.WithBaseEnvironment([]string{fakeGHDir + "=" + dir}),
		forge.WithDirectory(dir),
	}, opts...)
	gh, err := forge.NewGitHub(testRedactor, base...)
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	return &harness{t: t, dir: dir, gh: gh}
}

// newHarnessWithEnv returns a harness whose invocations start from a base
// environment carrying extra alongside the stand-in's own directory. A test
// uses it to state what an operator's environment holds, so an assertion about
// what an invocation is given is made against something that had to be
// overridden.
func newHarnessWithEnv(t *testing.T, script ghScript, extra ...string) *harness {
	t.Helper()
	dir := t.TempDir()
	writeScript(t, dir, script)
	gh, err := forge.NewGitHub(testRedactor,
		forge.WithBinary(os.Args[0]),
		forge.WithBaseEnvironment(append([]string{fakeGHDir + "=" + dir}, extra...)),
		forge.WithDirectory(dir),
	)
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	return &harness{t: t, dir: dir, gh: gh}
}

// writeScript writes the answers the stand-in provider should give into the
// directory it reads them from.
func writeScript(t *testing.T, dir string, script ghScript) {
	t.Helper()
	blob, err := json.Marshal(script)
	if err != nil {
		t.Fatalf("marshalling the script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, fakeGHScript), blob, 0o600); err != nil {
		t.Fatalf("writing the script: %v", err)
	}
}

// newWriteHarness returns a harness whose adapter may perform an outward-facing
// write: it names a repository, and its stand-in confirms that specifier as the
// repository it resolves to.
//
// A test that scripts its own "repo" answer keeps it, which is how a test
// states a provider resolving the specifier somewhere else.
func newWriteHarness(t *testing.T, script ghScript, opts ...forge.Option) *harness {
	t.Helper()
	if _, stated := script["repo"]; !stated {
		script["repo"] = []ghResponse{{Stdout: repoViewJSON(harnessRepository)}}
	}
	return newHarness(t, script, append([]forge.Option{forge.WithRepository(harnessRepository)}, opts...)...)
}

// calls returns every invocation the stand-in provider recorded, in order.
func (h *harness) calls() []ghCall {
	h.t.Helper()
	return readCalls(h.dir)
}

// callsFor returns the recorded invocations of one operation.
func (h *harness) callsFor(key string) []ghCall {
	h.t.Helper()
	var out []ghCall
	for _, c := range h.calls() {
		if c.Key == key {
			out = append(out, c)
		}
	}
	return out
}

// hasArg reports whether an invocation carried an argument exactly.
func (c ghCall) hasArg(want string) bool {
	for _, a := range c.Args {
		if a == want {
			return true
		}
	}
	return false
}
