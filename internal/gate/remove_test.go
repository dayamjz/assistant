package gate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// *vcs.Repository is what gate.WorkingCopy documents as the implementation
// this product uses, so a drift in either method has to fail the build rather
// than rot into a claim nobody checks.
var _ gate.WorkingCopy = (*vcs.Repository)(nil)

// detachable is *vcs.Repository plus the one operation internal/vcs does not
// carry yet. It runs the git command that operation will run, so it models
// what the real mechanism will put on the wire rather than a shape the real
// mechanism could not produce: a remote that is not there fails, exactly as
// gate.Detacher requires and as git reports it.
type detachable struct {
	*vcs.Repository
}

func (d detachable) RemoveRemote(_ context.Context, name string) error {
	if _, err := tryRawGit(d.Path(), "remote", "get-url", name); err != nil {
		return vcs.ErrRemoteNotFound
	}
	if out, err := tryRawGit(d.Path(), "remote", "remove", name); err != nil {
		return errors.New("git remote remove: " + out)
	}
	return nil
}

var _ gate.Detacher = detachable{}

func detachingOpener(ctx context.Context, path string) (gate.WorkingCopy, error) {
	repository, err := vcs.OpenWorktree(ctx, path)
	if err != nil {
		return nil, err
	}
	return detachable{Repository: repository}, nil
}

// TestRemoveRemoteIsStillTheNamedFollowUp fails on the day internal/vcs grows
// the operation gate.Detacher declares, because on that day the paragraph in
// git.go calling it a pending follow-up stops being true. The compile-time
// assertion above cannot notice, since the shim's own method shadows a
// promoted one.
func TestRemoveRemoteIsStillTheNamedFollowUp(t *testing.T) {
	t.Parallel()
	var repository any = (*vcs.Repository)(nil)
	if _, ok := repository.(gate.Detacher); ok {
		t.Fatal("*vcs.Repository now satisfies gate.Detacher, so RemoveRemote has landed in internal/vcs: git.go still calls it a pending follow-up and has to be corrected")
	}
}

// TestRemoveRefusesBeforeDeletingAnythingWhenItCannotDetach is the fail-closed
// half of removal. A gate whose repository is gone and whose remote still
// names it has not been removed, so nothing is deleted unless the whole thing
// can be.
func TestRemoveRefusesBeforeDeletingAnythingWhenItCannotDetach(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// The default opener is *vcs.Repository, which cannot remove a remote.
	if err := gate.Remove(ctx(t), spec); !errors.Is(err, gate.ErrDetachUnsupported) {
		t.Fatalf("Remove error = %v, want ErrDetachUnsupported", err)
	}
	if _, err := os.Stat(g.Repository()); err != nil {
		t.Fatalf("the refused removal deleted %s anyway: %v", g.Repository(), err)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); !ok || url != g.Repository() {
		t.Fatalf("the refused removal changed the %s remote to %q", gate.RemoteName, url)
	}
}

// TestRemoveLeavesTheWorkingCopyUsableAndOriginIntact is the acceptance
// criterion for removal: what is taken away is the gate and nothing else.
func TestRemoveLeavesTheWorkingCopyUsableAndOriginIntact(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, log := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	originURL, _ := remoteURL(t, wc.path, "origin")

	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(g.Repository()); !os.IsNotExist(err) {
		t.Fatalf("the gate repository survived removal (stat error %v)", err)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); ok {
		t.Fatalf("the %s remote survived removal, pointing at %q", gate.RemoteName, url)
	}
	if url, ok := remoteURL(t, wc.path, "origin"); !ok || url != originURL {
		t.Fatalf("origin is now %q, want %q", url, originURL)
	}

	// Usable means usable: the working copy still commits and still pushes to
	// origin, and nothing is left that could start a run.
	head := commitMore(t, wc, "after removal\n")
	rawGit(t, wc.path, "push", "--quiet", "origin", "main")
	if got, want := refs(t, wc.origin), []string{"refs/heads/main " + head}; !equal(got, want) {
		t.Fatalf("origin refs after the push = %v, want %v", got, want)
	}
	if got := invocations(t, log); len(got) != 0 {
		t.Fatalf("something was still invoked after removal: %v", got)
	}
}

// TestRemoveIsSafeToRepeat covers the second call, which has nothing left to
// remove and must say so rather than reaching for something else.
func TestRemoveIsSafeToRepeat(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	if _, err := gate.Initialize(ctx(t), spec); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); !errors.Is(err, gate.ErrNoGate) {
		t.Fatalf("Remove again = %v, want ErrNoGate", err)
	}
}

// TestRemoveRefusesAPathOutsideTheHomesRepositories is what stands between
// this package and deleting a directory only because a remote pointed at it.
func TestRemoveRefusesAPathOutsideTheHomesRepositories(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	if _, err := gate.Initialize(ctx(t), spec); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "remote", "set-url", gate.RemoteName, wc.origin)

	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); !errors.Is(err, gate.ErrNotAGate) {
		t.Fatalf("Remove error = %v, want ErrNotAGate", err)
	}
	if _, err := os.Stat(wc.origin); err != nil {
		t.Fatalf("the refused removal deleted origin: %v", err)
	}
	if _, ok := remoteURL(t, wc.path, gate.RemoteName); !ok {
		t.Fatal("the refused removal gave up the remote")
	}
}

// TestRemoveRefusesARepositoryCarryingNoGateRecord is the same guard for a
// path that is in the right place but cannot be identified as a gate. The
// record is what says a directory is one, per PRD principle P14.
func TestRemoveRefusesARepositoryCarryingNoGateRecord(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := os.Remove(filepath.Join(g.Repository(), "assistant-gate.json")); err != nil {
		t.Fatalf("remove the record: %v", err)
	}

	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); !errors.Is(err, gate.ErrNotAGate) {
		t.Fatalf("Remove error = %v, want ErrNotAGate", err)
	}
	if _, err := os.Stat(g.Repository()); err != nil {
		t.Fatalf("the refused removal deleted the repository: %v", err)
	}
	if _, ok := remoteURL(t, wc.path, gate.RemoteName); !ok {
		t.Fatal("the refused removal gave up the remote")
	}
}

// TestRemoveFromACopyRefusesAndLeavesTheOriginalsGate is the ownership check on
// the destructive side. A copied project directory carries the original's
// configuration, so its assistant remote names a gate that is not its own, and
// a removal that trusted that remote would delete the original's repository:
// every reference in it, and everything recorded against its identifier. That
// is the one loss in this package nothing can undo.
func TestRemoveFromACopyRefusesAndLeavesTheOriginalsGate(t *testing.T) {
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

	err = gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate}, gate.WithOpener(detachingOpener))
	if !errors.Is(err, gate.ErrGateClaimed) {
		t.Fatalf("Remove from the copy = %v, want ErrGateClaimed", err)
	}
	// An operator meeting this on a copy has to be told what was found, who
	// holds it, and who asked, or the refusal reads as a mystery.
	for _, want := range []string{original.Repository(), original.WorkingPath(), resolved(t, duplicate)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}

	// The original keeps its gate, its references, and its remote.
	if _, statErr := os.Stat(original.Repository()); statErr != nil {
		t.Fatalf("the refused removal deleted the original's gate: %v", statErr)
	}
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the original's gate holds %v, want %v", got, want)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the original's %s remote is %q, want %q", gate.RemoteName, url, original.Repository())
	}
	if url, ok := remoteURL(t, duplicate, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the refused removal changed the copy's %s remote to %q; it refuses without removing anything", gate.RemoteName, url)
	}

	// The original can still remove its own gate, so the guard refuses the
	// copy rather than removal in general.
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("Remove from the working copy the gate belongs to: %v", err)
	}
	if _, statErr := os.Stat(original.Repository()); !os.IsNotExist(statErr) {
		t.Fatalf("the gate survived removal by its own working copy (stat error %v)", statErr)
	}
}
