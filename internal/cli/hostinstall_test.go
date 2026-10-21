package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
)

// installer is a home, a person's own directory standing in for HOME, and
// the binary path the line names, so each case drives "assistant host
// install" through cli.Run with its own streams against a temporary HOME
// and never the process's.
type installer struct {
	home       *home.Home
	userHome   string
	executable string
}

// newInstaller names an executable under a directory with a space in its
// name, so every case holds the line to the quoting a pasted command needs
// whatever the temporary directory is called (#415).
func newInstaller(t *testing.T) installer {
	t.Helper()
	return installer{home: newHome(t), userHome: t.TempDir(), executable: filepath.Join(t.TempDir(), "with space", "assistant")}
}

// settings is where the host's user settings live under this HOME.
func (i installer) settings() string {
	return filepath.Join(i.userHome, ".claude", "settings.json")
}

// command is the hook's command line, written out here rather than asked of
// the package, so a line that named another binary or another home fails.
// Each path is one shell word through hookWord, so the line is the one a
// person can paste whatever the temporary directory is called (#415).
func (i installer) command() string {
	return stopLine(i.executable, i.home.Root())
}

// stopLine is the stop guard's command line for binary over the home at
// root, each path rendered as one shell word.
func stopLine(binary, root string) string {
	return hookWord(binary) + " --home " + hookWord(root) + " host stop"
}

// hookWord renders path as one POSIX shell word the way a person would
// write it: as it is when every byte is one no shell treats specially, and
// otherwise in single quotes, with a single quote in it closed, escaped and
// reopened. It is written here rather than asked of the package, so a line
// the installer quoted wrongly, or left unquoted, fails the comparison.
func hookWord(path string) string {
	plain := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@%=,", r)
	}
	for _, r := range path {
		if !plain(r) {
			return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
		}
	}
	return path
}

// run drives the installer with env as the whole environment.
func (i installer) run(t *testing.T, env map[string]string, args ...string) invocation {
	t.Helper()
	var out, errs bytes.Buffer
	code := cli.Run(t.Context(), cli.Environment{
		Args:       append([]string{"--home", i.home.Root(), "host", "install"}, args...),
		Stdout:     &out,
		Stderr:     &errs,
		Getenv:     func(key string) string { return env[key] },
		WorkingDir: t.TempDir(),
		Executable: i.executable,
		Version:    "assistant (test)",
	})
	return invocation{code: code, stdout: out.String(), stderr: errs.String()}
}

// env is HOME and nothing else.
func (i installer) env() map[string]string { return map[string]string{"HOME": i.userHome} }

// plant writes the settings file as a person or the host left it.
func (i installer) plant(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(i.settings()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(i.settings(), []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

// decodeInstall reads the document the installer wrote under --json.
func decodeInstall(t *testing.T, got invocation) machine.HostInstall {
	t.Helper()
	var answer machine.HostInstall
	if err := json.Unmarshal([]byte(got.stdout), &answer); err != nil {
		t.Fatalf("decoding the answer: %v\n%s%s", err, got.stdout, got.stderr)
	}
	return answer
}

// wantEntry is the one entry the line adds under the turn-end event, as the
// host's documented settings shape spells it.
func wantEntry(command string) any {
	return map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}
}

// decodeAny reads JSON text into Go's generic values, for comparing content
// regardless of layout.
func decodeAny(t *testing.T, text []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(text, &v); err != nil {
		t.Fatalf("decoding %s: %v", text, err)
	}
	return v
}

// existing is a settings file holding other settings and another program's
// turn-end hook, the shapes the host's own documentation shows, so a merge
// that dropped, reordered or replaced any of it is caught.
const existing = `{
  "model": "a-model",
  "permissions": {"allow": ["Bash(make check)"]},
  "hooks": {
    "PostToolUse": [{"matcher": "Write|Edit", "hooks": [{"type": "command", "command": "gofmt -l ."}]}],
    "Stop": [{"matcher": "", "hooks": [{"type": "command", "command": "~/.claude/stop-hook-git-check.sh"}]}]
  },
  "env": {"A": "1"}
}
`

// P1: installing the stop guard's hook changes the host's configuration,
// which nothing consented to until a person passes the explicit flag. Without
// it the command prints the exact line and the file it belongs in and writes
// nothing: an existing settings file is byte for byte what it was, and an
// absent one, and the directory it would be in, stay absent. The positive
// control is the same command with --write, which does change the file, so
// the unchanged file is the flag's doing and not a command that writes
// nowhere.
func TestHostInstallWritesNothingWithoutTheExplicitFlag(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)
	vocabtest.Control(t)

	i := newInstaller(t)
	got := i.run(t, i.env())
	if got.code != machine.ExitOK {
		t.Fatalf("the default exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if _, err := os.Stat(filepath.Dir(i.settings())); !os.IsNotExist(err) {
		t.Fatalf("the default created the host's settings directory: %v", err)
	}
	asDocument := decodeInstall(t, i.run(t, i.env(), "--json"))
	if asDocument.Result != machine.HostInstallPrinted || asDocument.Settings != i.settings() || asDocument.Command != i.command() || asDocument.Host != "claude" {
		t.Fatalf("the default answered %+v, want the line for %s printed for claude", asDocument, i.settings())
	}
	// The line is the settings document the host needs, holding exactly the
	// one entry, and it is printed as it is, after the file it belongs in.
	line := decodeAny(t, []byte(asDocument.Line))
	want := map[string]any{"hooks": map[string]any{"Stop": []any{wantEntry(i.command())}}}
	if !reflect.DeepEqual(line, want) {
		t.Fatalf("the line is %s, want %v", asDocument.Line, want)
	}
	if strings.Contains(asDocument.Line, "\n") {
		t.Fatalf("the line spans lines: %q", asDocument.Line)
	}
	if !strings.Contains(got.stdout, "\nLine: "+asDocument.Line+"\n") || !strings.Contains(got.stdout, "Settings file: "+i.settings()+"\n") {
		t.Fatalf("the default did not print the file and the exact line:\n%s", got.stdout)
	}
	phrase := strings.SplitN(got.stdout, "\n", 2)[0]
	if phrase != machine.HostInstallPhrase(asDocument).String() {
		t.Fatalf("the first line is %q, want the printed row", phrase)
	}
	vocabtest.Assert(t, phrase)

	// Over a file that exists, the same: printed, and the file untouched.
	i.plant(t, existing)
	before, err := os.Stat(i.settings())
	if err != nil {
		t.Fatal(err)
	}
	if got := i.run(t, i.env()); got.code != machine.ExitOK {
		t.Fatalf("the default over a settings file exited %s:\n%s", got.code, got.stderr)
	}
	after, err := os.Stat(i.settings())
	if err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(i.settings()); string(content) != existing || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("the default changed the settings file:\n%s", content)
	}

	// The positive control: the flag is what writes.
	if written := decodeInstall(t, i.run(t, i.env(), "--json", "--write")); written.Result != machine.HostInstallWritten {
		t.Fatalf("with --write the answer is %+v, want written", written)
	}
	if content, _ := os.ReadFile(i.settings()); string(content) == existing {
		t.Fatal("with --write the file is unchanged, so the default's unchanged file shows nothing")
	}
}

// With the flag, the line is merged into the file: the turn-end list gains
// exactly the one entry after the hook already there, and every other setting
// and hook is kept, with its keys in the order the file had them. Running it
// again finds the line present and writes nothing, and an absent file is
// created holding the line's document and nothing else.
func TestHostInstallWritesExactlyTheLineAndKeepsEverythingElse(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)

	i := newInstaller(t)
	i.plant(t, existing)
	got := i.run(t, i.env(), "--write")
	if got.code != machine.ExitOK {
		t.Fatalf("--write exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "Settings file: "+i.settings()+"\n") {
		t.Fatalf("--write did not say which file it wrote:\n%s", got.stdout)
	}
	content, err := os.ReadFile(i.settings())
	if err != nil {
		t.Fatal(err)
	}
	merged := decodeAny(t, content)
	want := decodeAny(t, []byte(existing))
	stop := want["hooks"].(map[string]any)["Stop"].([]any)
	want["hooks"].(map[string]any)["Stop"] = append(stop, wantEntry(i.command()))
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("the merged file is\n%s\nwant the original plus exactly the one entry", content)
	}
	if order := topKeys(t, content); !reflect.DeepEqual(order, []string{"model", "permissions", "hooks", "env"}) {
		t.Fatalf("the merged file's keys are %v, want the order the file had", order)
	}
	if info, err := os.Stat(i.settings()); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("the merged file's mode is %v (%v), want the 0640 it had", info.Mode().Perm(), err)
	}

	again := decodeInstall(t, i.run(t, i.env(), "--json", "--write"))
	if again.Result != machine.HostInstallPresent {
		t.Fatalf("a second --write answered %+v, want present", again)
	}
	if now, _ := os.ReadFile(i.settings()); !bytes.Equal(now, content) {
		t.Fatalf("a second --write changed the file:\n%s", now)
	}

	// An absent file, in a directory named by the host's own variable rather
	// than HOME, is created holding the line's document and nothing else.
	fresh := newInstaller(t)
	dir := filepath.Join(t.TempDir(), "host-config")
	answer := decodeInstall(t, fresh.run(t, map[string]string{"HOME": fresh.userHome, "CLAUDE_CONFIG_DIR": dir}, "--json", "--write"))
	if answer.Result != machine.HostInstallWritten || answer.Settings != filepath.Join(dir, "settings.json") {
		t.Fatalf("--write over no file answered %+v", answer)
	}
	created, err := os.ReadFile(answer.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeAny(t, created), decodeAny(t, []byte(answer.Line))) {
		t.Fatalf("the created file is\n%s\nwant the line's document %s", created, answer.Line)
	}
	if _, err := os.Stat(fresh.settings()); !os.IsNotExist(err) {
		t.Fatalf("a file was written under HOME although the host's variable named another directory: %v", err)
	}
}

// topKeys is a document's top-level keys in the order it writes them.
func topKeys(t *testing.T, text []byte) []string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(text))
	if _, err := decoder.Token(); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key.(string))
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

// A file the line cannot be added to without overriding something is refused
// as a typed result that exits as an operational failure, and the file is
// byte for byte what it was: a turn-end hook already running this program's
// stop guard with another command line, a file that does not parse, a key
// written twice where the merge reads, and a turn-end event that is not a
// list. The other program's hook in the merge test above is the positive
// control: a turn-end hook that is not this program's is not a conflict.
func TestHostInstallRefusesAConflictAndLeavesTheFileAsItWas(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)
	vocabtest.Control(t)

	other := stopLine(filepath.Join(t.TempDir(), "assistant"), filepath.Join(t.TempDir(), "elsewhere"))
	cases := []struct {
		name    string
		content string
		refusal machine.HostInstallRefusal
	}{
		{"this program's stop guard over another binary and home",
			`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "` + other + `"}]}]}, "model": "a-model"}`,
			machine.HostInstallConflict},
		{"not a settings document", `{"hooks": `, machine.HostInstallUnreadable},
		{"the hooks key written twice", `{"hooks": {}, "hooks": {"Stop": []}}`, machine.HostInstallUnreadable},
		{"the turn-end event is not a list", `{"hooks": {"Stop": {"hooks": []}}}`, machine.HostInstallUnreadable},
		{"an empty file", ``, machine.HostInstallUnreadable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			i := newInstaller(t)
			i.plant(t, c.content)
			got := i.run(t, i.env(), "--json", "--write")
			if got.code != machine.ExitFailure {
				t.Fatalf("exited %s, want an operational failure:\n%s%s", got.code, got.stdout, got.stderr)
			}
			answer := decodeInstall(t, got)
			if answer.Result != machine.HostInstallRefused || answer.Refusal != c.refusal {
				t.Fatalf("answered %+v, want refused as %s", answer, c.refusal)
			}
			if c.refusal == machine.HostInstallConflict && answer.Conflict != other {
				t.Fatalf("the conflict names %q, want the command the file holds, %q", answer.Conflict, other)
			}
			if content, _ := os.ReadFile(i.settings()); string(content) != c.content {
				t.Fatalf("the refused file changed:\n%s", content)
			}
			// What a person reads: the refusal's row, the file, the line
			// they may add themselves, and for a conflict what is there.
			human := i.run(t, i.env(), "--write")
			if human.code != machine.ExitFailure {
				t.Fatalf("the human rendering exited %s", human.code)
			}
			phrase := strings.SplitN(human.stdout, "\n", 2)[0]
			if phrase != machine.HostInstallPhrase(answer).String() {
				t.Fatalf("the first line is %q, want the refusal's row", phrase)
			}
			vocabtest.Assert(t, phrase)
			if !strings.Contains(human.stdout, "Line: "+answer.Line+"\n") {
				t.Fatalf("a refusal does not print the line to add by hand:\n%s", human.stdout)
			}
			if c.refusal == machine.HostInstallConflict && !strings.Contains(human.stdout, "Already there: "+other+"\n") {
				t.Fatalf("a conflict does not print what is there:\n%s", human.stdout)
			}
			if content, _ := os.ReadFile(i.settings()); string(content) != c.content {
				t.Fatalf("the refused file changed on the second refusal:\n%s", content)
			}
		})
	}
}

// Every result and every refusal has a row of its own, so a person can tell
// what happened from the phrase alone, and the rows are distinct.
func TestEveryInstallResultHasItsOwnRow(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	answers := []machine.HostInstall{}
	for _, result := range machine.HostInstallResults() {
		if result != machine.HostInstallRefused {
			answers = append(answers, machine.HostInstall{Result: result})
		}
	}
	for _, refusal := range machine.HostInstallRefusals() {
		answers = append(answers, machine.HostInstall{Result: machine.HostInstallRefused, Refusal: refusal})
	}
	for _, a := range answers {
		row := machine.HostInstallPhrase(a).String()
		if seen[row] {
			t.Fatalf("%+v reads the same as another answer: %q", a, row)
		}
		seen[row] = true
	}
}
