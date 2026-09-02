package forge_test

import (
	"encoding/json"
	"io"
	"os"
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
	fakeGHScript   = "script.json"
	fakeGHCalls    = "calls.json"
	fakeGHNoScript = 97
)

// ghResponse is one scripted answer from the stand-in provider.
type ghResponse struct {
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Exit    int    `json:"exit"`
	SleepMs int    `json:"sleep_ms"`
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

// TestMain runs the stand-in provider when the environment asks for it, and
// the tests otherwise.
func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeGHDir); dir != "" {
		os.Exit(fakeGHMain(dir))
	}
	os.Exit(m.Run())
}

func fakeGHMain(dir string) int {
	args := os.Args[1:]
	key := callKey(args)

	// Standard input is read to the end so a test can see what the adapter
	// sent there. A terminal would block here; os.DevNull ends at once.
	stdin, _ := io.ReadAll(os.Stdin)
	wd, _ := os.Getwd()

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
	if answer.SleepMs > 0 {
		time.Sleep(time.Duration(answer.SleepMs) * time.Millisecond)
	}
	os.Stdout.WriteString(answer.Stdout)
	os.Stderr.WriteString(answer.Stderr)
	return answer.Exit
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
