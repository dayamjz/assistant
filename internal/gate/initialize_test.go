package gate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/gate"
)

// TestOrdinaryPushToOriginIsUnaffected is PRD principle P1 as a test rather
// than as a comment. After a gate exists, a push to origin has to reach the
// same repository with the same references and start nothing.
//
// The negative half of that is only worth something next to the positive half,
// so the same recorder that stayed silent for the push to origin is shown
// firing for a push to the gate. Without that pairing, a recorder that could
// never fire would pass this test.
func TestOrdinaryPushToOriginIsUnaffected(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, log := recorderCommand(t, 0)

	originURLBefore, ok := remoteURL(t, wc.path, "origin")
	if !ok {
		t.Fatal("the fixture working copy has no origin remote")
	}

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if url, _ := remoteURL(t, wc.path, "origin"); url != originURLBefore {
		t.Fatalf("origin URL changed from %q to %q", originURLBefore, url)
	}
	if hooks := activeHookNames(t, wc.origin); len(hooks) != 0 {
		t.Fatalf("initialization installed hooks into origin: %v", hooks)
	}

	rawGit(t, wc.path, "push", "--quiet", "origin", "main")

	if got, want := refs(t, wc.origin), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("origin refs after the push = %v, want %v", got, want)
	}
	if got := invocations(t, log); len(got) != 0 {
		t.Fatalf("a push to origin invoked the gate command: %v", got)
	}
	if got := refs(t, g.Repository()); len(got) != 0 {
		t.Fatalf("a push to origin wrote references into the gate: %v", got)
	}

	// The paired positive: the same recorder does fire for a push to the gate.
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")
	if got := invocations(t, log); len(got) == 0 {
		t.Fatal("a push to the gate invoked nothing, so the check above proved nothing")
	}
}

// TestAdmissionRunsBeforeAnyReferenceChanges pins the ordering PRD section 5
// requires: a refusal happens instead of a change, not after one.
func TestAdmissionRunsBeforeAnyReferenceChanges(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, log := recorderCommand(t, 1)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	out, err := tryRawGit(wc.path, "push", gate.RemoteName, "main")
	if err == nil {
		t.Fatalf("the push succeeded although admission exited non-zero:\n%s", out)
	}
	if got := refs(t, g.Repository()); len(got) != 0 {
		t.Fatalf("the refused push still moved references in the gate: %v", got)
	}

	got := invocations(t, log)
	if len(got) == 0 {
		t.Fatal("admission was never invoked")
	}
	for _, line := range got {
		if strings.Contains(line, "gate notify") {
			t.Fatalf("notification ran although admission refused: %v", got)
		}
	}
}

// TestAdmissionReceivesTheReferenceUpdatesAndThenNotificationRuns covers the
// accepted path: admission is handed git's reference update lines, and the
// notification runs after the push has been accepted.
func TestAdmissionReceivesTheReferenceUpdatesAndThenNotificationRuns(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, log := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	lines := invocations(t, log)
	if len(lines) != 4 {
		t.Fatalf("invocations = %v, want a command and an input line for each of admission and notification", lines)
	}
	wantOrder := []string{
		"command gate admit --gate " + g.ID(),
		"command gate notify --gate " + g.ID(),
	}
	if lines[0] != wantOrder[0] {
		t.Fatalf("first invocation = %q, want %q", lines[0], wantOrder[0])
	}
	if lines[2] != wantOrder[1] {
		t.Fatalf("second invocation = %q, want %q", lines[2], wantOrder[1])
	}
	wantInput := "input " + strings.Repeat("0", 40) + " " + wc.commit + " refs/heads/main;"
	if lines[1] != wantInput {
		t.Fatalf("admission standard input = %q, want %q", lines[1], wantInput)
	}
	if lines[3] != wantInput {
		t.Fatalf("notification standard input = %q, want %q", lines[3], wantInput)
	}
	if got, want := refs(t, g.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("gate refs = %v, want %v", got, want)
	}
}

// TestCustomHookIsPreservedAndStillRuns is the promise that installing a gate
// over a repository somebody already put a hook in does not take the hook
// away.
func TestCustomHookIsPreservedAndStillRuns(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, log := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Somebody replaces the gate's admission hook with one of their own, the
	// way they would in any bare repository they own.
	custom := filepath.Join(g.Repository(), "hooks", gate.AdmissionHook)
	writeScript(t, custom, "#!/bin/sh\nprintf 'custom %s\\n' \"$(cat | tr '\\n' ';')\" >>"+shellQuoteForTest(log)+"\nexit 0\n")

	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}); err != nil {
		t.Fatalf("Initialize again: %v", err)
	}

	saved := custom + gate.CustomHookSuffix
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("the custom hook was not preserved at %s: %v", saved, err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	lines := invocations(t, log)
	admit, customRan := -1, -1
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "command gate admit"):
			admit = i
		case strings.HasPrefix(line, "custom "):
			customRan = i
		}
	}
	if customRan < 0 {
		t.Fatalf("the preserved hook did not run: %v", lines)
	}
	if admit < 0 || admit > customRan {
		t.Fatalf("admission did not run before the preserved hook: %v", lines)
	}
	if want := "custom " + strings.Repeat("0", 40) + " " + wc.commit + " refs/heads/main;"; lines[customRan] != want {
		t.Fatalf("the preserved hook read %q, want %q", lines[customRan], want)
	}
}

// TestPreservedHookCanStillRejectAPush shows that preservation is real rather
// than decorative: the custom hook's exit status is still the one git sees.
func TestPreservedHookCanStillRejectAPush(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	writeScript(t, filepath.Join(g.Repository(), "hooks", gate.AdmissionHook), "#!/bin/sh\ncat >/dev/null\nexit 3\n")
	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}); err != nil {
		t.Fatalf("Initialize again: %v", err)
	}

	out, err := tryRawGit(wc.path, "push", gate.RemoteName, "main")
	if err == nil {
		t.Fatalf("the push succeeded although the preserved hook refused it:\n%s", out)
	}
	if got := refs(t, g.Repository()); len(got) != 0 {
		t.Fatalf("the refused push still moved references: %v", got)
	}
}

// TestRepeatedInitializationDoesNotAdoptItsOwnHooks is the other half of
// idempotence. Preservation exists for a hook somebody else wrote, so a
// managed hook has to be recognizable as this package's own; a repair that
// mistook it for a custom one would set it aside, install a second copy in
// front of it, and refuse the time after that with nothing having gone wrong.
func TestRepeatedInitializationDoesNotAdoptItsOwnHooks(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	var g *gate.Gate
	for round := 1; round <= 3; round++ {
		var err error
		g, err = gate.Initialize(ctx(t), spec)
		if err != nil {
			t.Fatalf("Initialize round %d: %v", round, err)
		}
		want := []string{gate.AdmissionHook, gate.NotificationHook}
		got := activeHookNames(t, g.Repository())
		sort.Strings(got)
		sort.Strings(want)
		if !equal(got, want) {
			t.Fatalf("after round %d the gate has hooks %v, want exactly %v", round, got, want)
		}
	}
}

// TestChainingStopsAfterOneLevel covers the arrangement a person can create by
// hand after installation: a copy of the managed hook sitting at the .local
// name the managed hook chains to. Without a bound, that push never returns,
// so the failure this test guards against is a hang rather than a wrong
// answer, and it runs under a deadline for that reason. A regression here does
// not stop at one hung push: each level of the chain starts the next, so the
// deadline ends this test while the process tree it started has to be killed
// by hand.
func TestChainingStopsAfterOneLevel(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, log := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	managed := filepath.Join(g.Repository(), "hooks", gate.AdmissionHook)
	writeScript(t, managed+gate.CustomHookSuffix, readFile(t, managed))

	deadline, cancel := context.WithTimeout(ctx(t), 15*time.Second)
	defer cancel()
	if out, err := tryRawGitWithin(deadline, wc.path, "push", "--quiet", gate.RemoteName, "main"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}

	admissions := 0
	for _, line := range invocations(t, log) {
		if strings.HasPrefix(line, "command gate admit") {
			admissions++
		}
	}
	if admissions != 2 {
		t.Fatalf("admission ran %d times, want the managed hook and the one level it chains to", admissions)
	}
}

// TestCustomHookConflictRefusesAndChangesNothing covers the one shape this
// package will not resolve on somebody's behalf, because resolving it means
// discarding a file somebody wrote.
func TestCustomHookConflictRefusesAndChangesNothing(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	hooks := filepath.Join(g.Repository(), "hooks")
	writeScript(t, filepath.Join(hooks, gate.AdmissionHook), "#!/bin/sh\nexit 0\n# mine\n")
	writeScript(t, filepath.Join(hooks, gate.AdmissionHook+gate.CustomHookSuffix), "#!/bin/sh\nexit 0\n# also mine\n")
	notificationBefore := readFile(t, filepath.Join(hooks, gate.NotificationHook))

	_, err = gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if !errors.Is(err, gate.ErrCustomHookConflict) {
		t.Fatalf("Initialize error = %v, want ErrCustomHookConflict", err)
	}
	if got := readFile(t, filepath.Join(hooks, gate.AdmissionHook)); !strings.Contains(got, "# mine") {
		t.Fatalf("the refused initialization replaced the custom hook: %q", got)
	}
	if got := readFile(t, filepath.Join(hooks, gate.AdmissionHook+gate.CustomHookSuffix)); !strings.Contains(got, "# also mine") {
		t.Fatalf("the refused initialization replaced the saved hook: %q", got)
	}
	if got := readFile(t, filepath.Join(hooks, gate.NotificationHook)); got != notificationBefore {
		t.Fatal("the refused initialization installed the notification hook anyway")
	}
}

// TestInitializeRepairsADamagedGate is the idempotence PRD section 5 asks for,
// stated as repair rather than as a second call that happens not to fail.
func TestInitializeRepairsADamagedGate(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, log := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	first, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	// Damage every part of the gate independently: both hooks, the record,
	// and the remote that names it.
	for _, name := range []string{gate.AdmissionHook, gate.NotificationHook} {
		if err := os.Remove(filepath.Join(first.Repository(), "hooks", name)); err != nil {
			t.Fatalf("remove %s: %v", name, err)
		}
	}
	if err := os.Remove(filepath.Join(first.Repository(), "assistant-gate.json")); err != nil {
		t.Fatalf("remove the record: %v", err)
	}
	rawGit(t, wc.path, "remote", "remove", gate.RemoteName)

	second, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize again: %v", err)
	}
	if second.ID() != first.ID() {
		t.Fatalf("repair changed the identifier from %q to %q", first.ID(), second.ID())
	}
	if second.Reattached() {
		t.Fatal("repairing a gate in place reported a reattachment")
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); !ok || url != first.Repository() {
		t.Fatalf("the %s remote is %q, want %q", gate.RemoteName, url, first.Repository())
	}
	if got, want := refs(t, second.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("repair lost the gate's references: %v, want %v", got, want)
	}

	if err := os.Truncate(log, 0); err != nil {
		t.Fatalf("truncate %s: %v", log, err)
	}
	commitMore(t, wc, "second\n")
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")
	if got := invocations(t, log); len(got) == 0 {
		t.Fatal("the repaired gate ran no hooks")
	}
}

// activeHookNames lists the hooks a repository would actually run, ignoring
// the disabled .sample copies a new repository arrives with.
func activeHookNames(t *testing.T, repo string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repo, "hooks"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read the hooks of %s: %v", repo, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".sample") {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEveryEarlyRefusalLeavesTheGateRefusingPushes is the invariant on the
// paths that reach no hooks at all. An initialization can refuse before it
// looks at the gate's hooks, and a gate that has lost its admission hook is
// then left taking every push with nothing running.
//
// That failure is worse than it sounds and worse than losing history, which is
// why it is tested per refusal rather than once. The notification hook still
// fires on such a push, so the run is recorded as one admission saw: not a
// missing check, a false record of a check that never happened.
func TestEveryEarlyRefusalLeavesTheGateRefusingPushes(t *testing.T) {
	gitEnvironment(t)
	command, _ := recorderCommand(t, 0)

	// Each case damages a gate the same way, then makes an initialization
	// refuse before it reaches the hooks, by a different route.
	cases := []struct {
		name string
		// refuse returns the spec to initialize with and the error wanted.
		refuse func(t *testing.T, home string, wc workingCopy, g *gate.Gate) (gate.Spec, error)
	}{
		{
			name: "the hook command is gone",
			refuse: func(t *testing.T, home string, wc workingCopy, _ *gate.Gate) (gate.Spec, error) {
				return gate.Spec{
					Home:        home,
					WorkingPath: wc.path,
					Command:     filepath.Join(t.TempDir(), "upgraded-away"),
				}, gate.ErrInvalidSpec
			},
		},
		{
			name: "the record cannot be read",
			refuse: func(t *testing.T, home string, wc workingCopy, g *gate.Gate) (gate.Spec, error) {
				replaced, err := json.Marshal(map[string]any{
					"version": 99, "id": g.ID(), "workingPath": g.WorkingPath(),
				})
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				writeFile(t, filepath.Join(g.Repository(), "assistant-gate.json"), string(replaced))
				return gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, gate.ErrMalformedRecord
			},
		},
		{
			name: "another working copy holds the gate",
			refuse: func(t *testing.T, home string, wc workingCopy, g *gate.Gate) (gate.Spec, error) {
				// The gate's record is rewritten to a copy that is still bound
				// to it, so the working copy standing at the hashed path is
				// refused ownership of its own identifier.
				duplicate := filepath.Join(filepath.Dir(wc.path), "copy")
				copyTree(t, wc.path, duplicate)
				writeFile(t, filepath.Join(g.Repository(), "assistant-gate.json"),
					fmt.Sprintf(`{"version":1,"id":%q,"workingPath":%q}`, g.ID(), resolved(t, duplicate)))
				return gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, gate.ErrGateClaimed
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wc := newWorkingCopy(t)
			home := t.TempDir()
			g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
			if err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

			// The damage TestInitializeRepairsADamagedGate inflicts, which
			// this package treats as an ordinary thing to repair.
			if err := os.Remove(filepath.Join(g.Repository(), "hooks", gate.AdmissionHook)); err != nil {
				t.Fatalf("remove the admission hook: %v", err)
			}

			spec, want := c.refuse(t, home, wc, g)
			if _, err := gate.Initialize(ctx(t), spec); !errors.Is(err, want) {
				t.Fatalf("Initialize error = %v, want %v", err, want)
			}

			// The gate must not take a push now, and nothing must run on one.
			// The recorder is shared across the subtests, so what is asserted
			// is the push, which is the observable this invariant is about.
			commitMore(t, wc, "after the refusal\n")
			before := refs(t, g.Repository())
			out, pushErr := tryRawGit(wc.path, "push", gate.RemoteName, "main")
			if pushErr == nil {
				t.Fatalf("the gate accepted a push after a refused initialization:\n%s", out)
			}
			if got := refs(t, g.Repository()); !equal(got, before) {
				t.Fatalf("the refused push moved references in the gate: %v, want %v", got, before)
			}
		})
	}
}
