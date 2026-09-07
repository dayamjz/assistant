package home_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
)

func TestTheEnvironmentRelocatesTheRoot(t *testing.T) {
	t.Parallel()
	// A leading separator is not an absolute path everywhere: Windows wants a
	// volume too, so a hand-composed one is refused there and this test would
	// be checking the platform rather than the relocation. A temporary
	// directory is absolute on every platform the module builds for, and the
	// named root does not have to exist for Resolve to answer with it.
	want := filepath.Join(t.TempDir(), "somewhere", "else")
	root, err := home.Resolve(func(string) string { return want })
	if err != nil {
		t.Fatalf("resolving a named root: %v", err)
	}
	if root != want {
		t.Fatalf("Resolve = %q, want %q", root, want)
	}
}

func TestARelativeRootIsRefusedRatherThanResolvedAgainstTheWorkingDirectory(t *testing.T) {
	t.Parallel()
	if _, err := home.Resolve(func(string) string { return "relative/home" }); !errors.Is(err, home.ErrRelativeRoot) {
		t.Fatalf("Resolve of a relative root = %v, want ErrRelativeRoot", err)
	}
	if _, err := home.Open("relative/home"); !errors.Is(err, home.ErrRelativeRoot) {
		t.Fatalf("Open of a relative root = %v, want ErrRelativeRoot", err)
	}
}

func TestAnUnsetVariableDefaultsUnderTheUserHomeDirectory(t *testing.T) {
	t.Parallel()
	user, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("this platform reports no user home directory: %v", err)
	}
	root, err := home.Resolve(func(string) string { return "" })
	if err != nil {
		t.Fatalf("resolving the default root: %v", err)
	}
	if want := filepath.Join(user, home.DirName); root != want {
		t.Fatalf("Resolve = %q, want %q", root, want)
	}
}

func TestEveryPathIsUnderTheRoot(t *testing.T) {
	t.Parallel()
	h := open(t, t.TempDir())
	paths := map[string]string{
		"config file": h.ConfigFile(),
		"database":    h.Database(),
		"socket":      h.Socket(),
		"lock":        h.LockFile(),
		"worktree":    h.Worktree("repo", "run"),
		"evidence":    h.Evidence("run"),
		"task":        h.Task("task"),
		"queue":       h.Queue(),
		"stage log":   h.StageLog("run", "review"),
		"service log": h.ServiceLog(),
	}
	for name, path := range paths {
		if !strings.HasPrefix(path, h.Root()+string(filepath.Separator)) {
			t.Fatalf("the %s at %q is not under the root %q", name, path, h.Root())
		}
	}
}

func TestOpenDoesNotCreateAnythingAndCreateIsWhatDoes(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "home")
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open created %s", root)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("Create left %s as (%v, %v)", root, info, err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("Create against an existing home: %v", err)
	}
	// The database and the socket are made by whoever opens them, so a home
	// that has never been served is distinguishable from one that has.
	for _, path := range []string{h.Database(), h.Socket()} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Create made %s", path)
		}
	}
}

func TestOneHomeIsHeldByOneProcessAtATime(t *testing.T) {
	t.Parallel()
	h := created(t)
	held, err := h.Acquire(t.Context(), 0)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	// A second Home value over the same root is the closest this test can get
	// to a second process in one binary: the lock is per file, and taking it
	// twice through one process's descriptors is what an in-process test can
	// observe. What it demonstrates is that the second attempt does not
	// silently succeed against a file that is already locked elsewhere.
	second := open(t, h.Root())
	if _, err := second.Acquire(t.Context(), 0); !errors.Is(err, home.ErrLocked) {
		t.Fatalf("a second acquisition of a held home lock reported %v, want ErrLocked", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("releasing the lock: %v", err)
	}
	again, err := second.Acquire(t.Context(), 0)
	if err != nil {
		t.Fatalf("taking the lock after it was released: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatalf("releasing the lock again: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatalf("releasing an already-released lock: %v", err)
	}
}

func TestABoundedWaitGivesUpRatherThanBlockingForever(t *testing.T) {
	t.Parallel()
	h := created(t)
	held, err := h.Acquire(t.Context(), 0)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	t.Cleanup(func() { _ = held.Release() })
	started := time.Now()
	if _, err := open(t, h.Root()).Acquire(t.Context(), 60*time.Millisecond); err == nil {
		t.Fatal("a bounded wait for a held lock succeeded")
	}
	if waited := time.Since(started); waited < 40*time.Millisecond {
		t.Fatalf("a 60ms bounded wait gave up after %v, so it did not wait", waited)
	}
}

func TestTheServiceLogStaysInsideItsBoundAndKeepsTheRecentEnd(t *testing.T) {
	t.Parallel()
	h := created(t)
	const limit = 512
	log, err := h.OpenLog(limit)
	if err != nil {
		t.Fatalf("opening the log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	for i := range 200 {
		log.Printf("line %d with enough text on it to fill the bound quickly", i)
	}
	info, err := os.Stat(h.ServiceLog())
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	if info.Size() > limit {
		t.Fatalf("the log is %d bytes, past its bound of %d", info.Size(), limit)
	}
	body, err := os.ReadFile(h.ServiceLog())
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	if !strings.Contains(string(body), "line 199") {
		t.Fatalf("the log dropped its most recent line; it holds %q", body)
	}
	if strings.HasPrefix(string(body), "line") {
		t.Fatalf("the log begins mid-line: %q", body)
	}
}

func TestRotationKeepsWritingToTheSameFile(t *testing.T) {
	t.Parallel()
	h := created(t)
	log, err := h.OpenLog(256)
	if err != nil {
		t.Fatalf("opening the log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	before, err := os.Open(h.ServiceLog())
	if err != nil {
		t.Fatalf("opening the log independently: %v", err)
	}
	t.Cleanup(func() { _ = before.Close() })
	for i := range 100 {
		log.Printf("filling the log so it rotates at least once: %d", i)
	}
	// A descriptor opened before the rotation still names the file the log is
	// writing to, which is what truncating in place buys and what a rename
	// would have lost.
	held, err := before.Stat()
	if err != nil {
		t.Fatalf("stat on the held descriptor: %v", err)
	}
	current, err := os.Stat(h.ServiceLog())
	if err != nil {
		t.Fatalf("stat on the log path: %v", err)
	}
	if !os.SameFile(held, current) {
		t.Fatal("the log rotated onto a different file, so a held descriptor writes to an orphan")
	}
}

// open returns a home at root, failing the test rather than the caller.
func open(t *testing.T, root string) *home.Home {
	t.Helper()
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening the home at %s: %v", root, err)
	}
	return h
}

// created returns a home whose directories exist.
func created(t *testing.T) *home.Home {
	t.Helper()
	h := open(t, t.TempDir())
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	return h
}
