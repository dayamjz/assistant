package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
)

// codexInstaller is like installer but targets Codex's hooks file under a
// temporary CODEX_HOME rather than Claude's settings under HOME.
type codexInstaller struct {
	installer
	codexHome string
}

func newCodexInstaller(t *testing.T) codexInstaller {
	t.Helper()
	base := newInstaller(t)
	dir := filepath.Join(t.TempDir(), "codex-home")
	return codexInstaller{installer: base, codexHome: dir}
}

func (i codexInstaller) hooks() string {
	return filepath.Join(i.codexHome, "hooks.json")
}

func (i codexInstaller) env() map[string]string {
	return map[string]string{"HOME": i.userHome, "CODEX_HOME": i.codexHome}
}

func (i codexInstaller) plant(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(i.codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(i.hooks(), []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

// codexTurnEnd is the Stop hook input Codex documents: the same
// stop_hook_active member Claude uses, with Codex's turn_id beside it.
func codexTurnEnd(t *testing.T, active any) string {
	t.Helper()
	event := map[string]any{
		"session_id":             "sess_abc",
		"turn_id":                "turn_123",
		"transcript_path":        "/tmp/rollout.jsonl",
		"cwd":                    "/tmp/work",
		"hook_event_name":        "Stop",
		"stop_hook_active":       active,
		"last_assistant_message": "Done.",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Selecting Codex installs into hooks.json under CODEX_HOME and never writes
// Claude's settings file.
func TestHostInstallRefusesAnUnknownHost(t *testing.T) {
	t.Parallel()
	i := newInstaller(t)
	got := i.run(t, i.env(), "--json", "--host", "cursor")
	if got.code != machine.ExitUsage {
		t.Fatalf("exited %s, want incorrect usage:\n%s%s", got.code, got.stdout, got.stderr)
	}
}

func TestHostInstallCodexTargetsHooksNotClaudeSettings(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)

	i := newCodexInstaller(t)
	answer := decodeInstall(t, i.run(t, i.env(), "--json", "--host", "codex"))
	if answer.Host != "codex" || answer.Settings != i.hooks() {
		t.Fatalf("answered %+v, want codex hooks at %s", answer, i.hooks())
	}
	if _, err := os.Stat(i.settings()); !os.IsNotExist(err) {
		t.Fatalf("codex install touched Claude settings: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(i.settings())); !os.IsNotExist(err) {
		t.Fatalf("codex install created Claude's config directory: %v", err)
	}

	written := decodeInstall(t, i.run(t, i.env(), "--json", "--host", "codex", "--write"))
	if written.Result != machine.HostInstallWritten {
		t.Fatalf("with --write answered %+v, want written", written)
	}
	content, err := os.ReadFile(i.hooks())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), i.command()) {
		t.Fatalf("hooks.json is\n%s\nwant the stop command", content)
	}
	if _, err := os.Stat(i.settings()); !os.IsNotExist(err) {
		t.Fatalf("codex --write touched Claude settings")
	}
}

// With the flag, Codex hooks merge like Claude settings: the Stop list gains
// exactly one entry and every other hook is kept.
const codexExisting = `{
  "description": "team hooks",
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "echo start"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "~/.codex/other-stop.sh"}]}]
  }
}
`

func TestHostInstallCodexKeepsUnrelatedHooks(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)

	i := newCodexInstaller(t)
	i.plant(t, codexExisting)
	got := i.run(t, i.env(), "--host", "codex", "--write")
	if got.code != machine.ExitOK {
		t.Fatalf("--write exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	content, err := os.ReadFile(i.hooks())
	if err != nil {
		t.Fatal(err)
	}
	merged := decodeAny(t, content)
	want := decodeAny(t, []byte(codexExisting))
	stop := want["hooks"].(map[string]any)["Stop"].([]any)
	want["hooks"].(map[string]any)["Stop"] = append(stop, wantCodexEntry(i.command()))
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("merged hooks.json is\n%s\nwant the original plus exactly the one entry", content)
	}
	if order := topKeys(t, content); !reflect.DeepEqual(order, []string{"description", "hooks"}) {
		t.Fatalf("top-level keys are %v, want description then hooks", order)
	}

	again := decodeInstall(t, i.run(t, i.env(), "--json", "--host", "codex", "--write"))
	if again.Result != machine.HostInstallPresent {
		t.Fatalf("second --write answered %+v, want present", again)
	}

	fresh := newCodexInstaller(t)
	dir := filepath.Join(t.TempDir(), "codex-config")
	answer := decodeInstall(t, fresh.run(t, map[string]string{"HOME": fresh.userHome, "CODEX_HOME": dir}, "--json", "--host", "codex", "--write"))
	if answer.Result != machine.HostInstallWritten || answer.Settings != filepath.Join(dir, "hooks.json") {
		t.Fatalf("--write over no file answered %+v", answer)
	}
	if _, err := os.Stat(fresh.hooks()); !os.IsNotExist(err) {
		t.Fatalf("wrote under CODEX_HOME but also under the default ~/.codex path")
	}
}

// wantCodexEntry is the Stop entry Codex install writes, with async false so
// the hook runs synchronously and can block a turn.
func wantCodexEntry(command string) any {
	return map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "async": false}}}
}

// A Stop hook with this program's command marked async cannot block a turn;
// install refuses rather than reporting present.
func TestHostInstallCodexRefusesAsyncStopGuard(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)
	vocabtest.Control(t)

	i := newCodexInstaller(t)
	i.plant(t, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"`+i.command()+`","async":true}]}]}}`)
	answer := decodeInstall(t, i.run(t, i.env(), "--json", "--host", "codex", "--write"))
	if answer.Result != machine.HostInstallRefused || answer.Refusal != machine.HostInstallConflict {
		t.Fatalf("answered %+v, want conflict refusal", answer)
	}
	if answer.Conflict != i.command() {
		t.Fatalf("conflict %q, want %q", answer.Conflict, i.command())
	}
	if strings.Contains(answer.Detail, "different command") {
		t.Fatalf("detail calls an identical command a different one: %s", answer.Detail)
	}
	plain := i.run(t, i.env(), "--host", "codex", "--write")
	vocabtest.Assert(t, plain.stdout)
	if !strings.Contains(plain.stdout, machine.HostInstallPhrase(answer).Outcome) {
		t.Fatalf("stdout does not say the check runs in the background:\n%s", plain.stdout)
	}
}

// A synchronous copy of the stop command does not hide a later background
// copy of the same command in the same Stop entry.
func TestHostInstallCodexRefusesABackgroundCopyBesideASynchronousOne(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)

	i := newCodexInstaller(t)
	command := i.command()
	i.plant(t, `{"hooks":{"Stop":[{"hooks":[`+
		`{"type":"command","command":"`+command+`","async":false},`+
		`{"type":"command","command":"`+command+`","async":true}`+
		`]}]}}`)
	answer := decodeInstall(t, i.run(t, i.env(), "--json", "--host", "codex", "--write"))
	if answer.Result != machine.HostInstallRefused || answer.Refusal != machine.HostInstallConflict || answer.Conflict != command {
		t.Fatalf("answered %+v, want a conflict on %s", answer, command)
	}
	if strings.Contains(answer.Detail, "different command") {
		t.Fatalf("detail calls an identical command a different one: %s", answer.Detail)
	}
}

// Work in flight with no live supervisor blocks when Codex-shaped input
// arrives on standard input, using exit status 2 and a message on standard
// error, which is the block path Codex documents for Stop hooks.
func TestHostStopBlocksCodexTurnEndWhenNothingWatches(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P10)
	vocabtest.Control(t)
	h := unwatchedHome(t)

	got := hostStopWith(t, h, nil, strings.NewReader(codexTurnEnd(t, false)), "--json")
	answer := decodeHostStop(t, got)
	if answer.Allowed || answer.ReEntered || got.code != machine.ExitUsage {
		t.Fatalf("the answer is %+v and exited %s, want blocked with status 2", answer, got.code)
	}
	if !strings.HasPrefix(got.stderr, machine.PhraseTurnBlocked) {
		t.Fatalf("standard error does not say the turn cannot end:\n%s", got.stderr)
	}
	vocabtest.Assert(t, got.stderr)
	plain := hostStopWith(t, h, nil, strings.NewReader(codexTurnEnd(t, false)))
	if plain.code != machine.ExitUsage {
		t.Fatalf("blocked codex stop exited %s, want status 2:\n%s%s", plain.code, plain.stdout, plain.stderr)
	}
	if plain.stdout != "" {
		t.Fatalf("blocked codex stop wrote %q on standard output, want nothing for the host hook", plain.stdout)
	}
}

// Re-entry through stop_hook_active on Codex-shaped input allows exactly one
// invocation, judged afresh on the next.
func TestHostStopAllowsCodexReEntry(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P10)
	vocabtest.Control(t)
	h := unwatchedHome(t)

	blocked := decodeHostStop(t, hostStopWith(t, h, nil, strings.NewReader(codexTurnEnd(t, false)), "--json"))
	if blocked.Allowed {
		t.Fatalf("without re-entry allowed the turn: %+v", blocked)
	}

	got := hostStopWith(t, h, nil, strings.NewReader(codexTurnEnd(t, true)), "--json")
	reEntered := decodeHostStop(t, got)
	if !reEntered.Allowed || !reEntered.ReEntered || got.code != machine.ExitOK {
		t.Fatalf("with stop_hook_active true answered %+v and exited %s, want allowed", reEntered, got.code)
	}
	if again := decodeHostStop(t, hostStopWith(t, h, nil, strings.NewReader(codexTurnEnd(t, false)), "--json")); again.Allowed {
		t.Fatalf("after re-entry the next turn is %+v, want blocked", again)
	}
}

// A fresh supervisor lets a Codex-shaped turn end: exit success and nothing
// on standard output, which is what the Stop hook expects when the turn may
// end.
func TestHostStopAllowsCodexTurnWhenSupervisorHoldsTheHome(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P10)
	h := createdHome(t)
	oneTaskInFlight(t, h)
	holdInAnotherProcess(t, h)
	writeInstance(t, h)
	writeBeacon(t, h, time.Now())

	got := hostStopWith(t, h, nil, strings.NewReader(codexTurnEnd(t, false)), "--json")
	answer := decodeHostStop(t, got)
	if !answer.Allowed || got.code != machine.ExitOK || got.stderr != "" {
		t.Fatalf("allowed turn: answer=%+v code=%s stderr=%q", answer, got.code, got.stderr)
	}
	plain := hostStopWith(t, h, nil, strings.NewReader(codexTurnEnd(t, false)))
	if plain.code != machine.ExitOK || plain.stderr != "" || plain.stdout != "" {
		t.Fatalf("plain allowed turn: code=%s stdout=%q stderr=%q", plain.code, plain.stdout, plain.stderr)
	}
}

// TestRecordsInstalledCodexVersionWhenLive logs the installed Codex CLI
// version when ASSISTANT_CODEX_HOST_LIVE=1. It does not drive a Codex turn or
// invoke hooks through the CLI; Stop behaviour is covered by the unit tests
// above that call host stop with Codex-shaped stdin.
func TestRecordsInstalledCodexVersionWhenLive(t *testing.T) {
	t.Parallel()
	if os.Getenv("ASSISTANT_CODEX_HOST_LIVE") != "1" {
		t.Skip("set ASSISTANT_CODEX_HOST_LIVE=1 to record the installed Codex CLI version")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("codex --version: %v\n%s", err, out)
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		t.Fatal("codex --version produced no output")
	}
	t.Logf("installed Codex CLI: %s", version)
}
