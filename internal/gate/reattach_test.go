package gate_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
)

// TestAMovedWorkingCopyKeepsItsGate is the reattachment PRD section 5
// requires. The gate keeps its identifier, and with it everything recorded
// against that identifier, rather than being replaced by a second one at the
// hash of the new path.
func TestAMovedWorkingCopyKeepsItsGate(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	first, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	moved := filepath.Join(filepath.Dir(wc.path), "moved")
	if err := os.Rename(wc.path, moved); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}

	second, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command})
	if err != nil {
		t.Fatalf("Initialize after the move: %v", err)
	}
	if !second.Reattached() {
		t.Fatal("a moved working copy was not reported as reattached")
	}
	if second.ID() != first.ID() {
		t.Fatalf("the moved working copy got identifier %q, want the original %q", second.ID(), first.ID())
	}
	if second.Repository() != first.Repository() {
		t.Fatalf("the moved working copy got gate %q, want the original %q", second.Repository(), first.Repository())
	}
	if got, want := refs(t, second.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the reattached gate lost its history: %v, want %v", got, want)
	}
	if second.WorkingPath() != resolved(t, moved) {
		t.Fatalf("the reattached gate reports working path %q, want %q", second.WorkingPath(), resolved(t, moved))
	}
	if url, ok := remoteURL(t, moved, gate.RemoteName); !ok || url != first.Repository() {
		t.Fatalf("the moved working copy's %s remote is %q, want %q", gate.RemoteName, url, first.Repository())
	}

	// Reattachment is not a one-off: the moved working copy is now an
	// ordinary one, and initializing it again neither moves it nor reports a
	// second reattachment.
	third, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command})
	if err != nil {
		t.Fatalf("Initialize the moved working copy again: %v", err)
	}
	if third.Reattached() {
		t.Fatal("initializing an already reattached gate reported another reattachment")
	}
	if third.Repository() != first.Repository() {
		t.Fatalf("the third initialization moved the gate to %q", third.Repository())
	}
}

// TestACopiedWorkingCopyDoesNotStealTheOriginalsGate is the other half of the
// same question. The copy carries the original's remote in its configuration,
// so the only thing that keeps it from sharing the gate is the check that the
// original is still there and still bound to it.
func TestACopiedWorkingCopyDoesNotStealTheOriginalsGate(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	duplicate := filepath.Join(filepath.Dir(wc.path), "copy")
	copyTree(t, wc.path, duplicate)
	if url, ok := remoteURL(t, duplicate, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the copy's %s remote is %q, want the original's gate %q; the fixture does not pose the question this test asks",
			gate.RemoteName, url, original.Repository())
	}

	copied, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate, Command: command})
	if err != nil {
		t.Fatalf("Initialize the copy: %v", err)
	}
	if copied.Reattached() {
		t.Fatal("the copy reported a reattachment, so it took over the original's gate")
	}
	if copied.Repository() == original.Repository() {
		t.Fatalf("the copy shares the original's gate at %q", copied.Repository())
	}
	wantID, err := gate.Identify(duplicate)
	if err != nil {
		t.Fatalf("Identify the copy: %v", err)
	}
	if copied.ID() != wantID {
		t.Fatalf("the copy's identifier is %q, want %q", copied.ID(), wantID)
	}
	if got := refs(t, copied.Repository()); len(got) != 0 {
		t.Fatalf("the copy's own gate started with references in it: %v", got)
	}

	// The original is untouched in both directions.
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the original's gate changed: %v, want %v", got, want)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the original's %s remote is now %q, want %q", gate.RemoteName, url, original.Repository())
	}
}

// TestARepositoryBornCarryingHooksIsRefused covers the route internal/vcs
// documents that it cannot close: a git template chosen by a configuration
// file this process is pointed at. A hook that arrives that way is code
// something outside this process chose to run inside the gate, so it is
// refused rather than preserved as somebody's custom hook.
func TestARepositoryBornCarryingHooksIsRefused(t *testing.T) {
	cfg := gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	template := t.TempDir()
	writeScript(t, filepath.Join(template, "hooks", gate.AdmissionHook), "#!/bin/sh\nexit 0\n")
	appendConfig(t, cfg, "[init]\n\ttemplateDir = "+template+"\n")

	_, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if !errors.Is(err, gate.ErrTemplateHooks) {
		t.Fatalf("Initialize error = %v, want ErrTemplateHooks", err)
	}

	id, err := gate.Identify(wc.path)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	repo := filepath.Join(resolved(t, home), "repos", id+".git")
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		// Leaving it would be worse than the refusal: the next attempt would
		// find the template's hook in a repository that already existed and
		// preserve it as somebody's own.
		t.Fatalf("the refused initialization left %s behind (stat error %v)", repo, err)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); ok {
		t.Fatalf("the refused initialization pointed the %s remote at %q", gate.RemoteName, url)
	}
}
