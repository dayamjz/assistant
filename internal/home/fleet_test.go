package home_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
)

// validTaskID is one identifier in the generator's format, written out so a
// test that only needs a valid one does not depend on the generator.
const validTaskID = "abcdefghij23"

// holdLockVar is the environment variable that turns the test binary into a
// process that holds a home's lock: it names the root. The process reports
// "held" on its standard output once it has the lock and lets go when its
// standard input closes, so a test can hold a real lock in a real second
// process rather than through a second handle in its own.
const holdLockVar = "HOME_TEST_HOLD_LOCK"

func TestMain(m *testing.M) {
	if root := os.Getenv(holdLockVar); root != "" {
		os.Exit(holdLock(root))
	}
	os.Exit(m.Run())
}

// holdLock is the body of the lock-holding process.
func holdLock(root string) int {
	h, err := home.Open(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	lock, err := h.Acquire(context.Background(), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("held")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := lock.Release(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// The generator's format is what the rule accepts, and every way an identifier
// could stop being one path element is refused on every platform rather than
// corrected: traversal, separators, absolute paths, the wrong length, and the
// names Windows reserves. The refusal is the same on ubuntu, macos and
// windows, because an identifier is minted once for a home a person may copy
// between machines.
func TestATaskIdentifierIsOneRuleAndIsRefusedRatherThanSanitized(t *testing.T) {
	t.Parallel()
	for range 32 {
		id, err := home.NewTaskID()
		if err != nil {
			t.Fatalf("minting an identifier: %v", err)
		}
		if err := home.ValidTaskID(id); err != nil {
			t.Fatalf("the generator minted %q, which the rule refuses: %v", id, err)
		}
		if len(id) != home.TaskIDLength {
			t.Fatalf("the generator minted %q of %d characters, want %d", id, len(id), home.TaskIDLength)
		}
	}
	if err := home.ValidTaskID(validTaskID); err != nil {
		t.Fatalf("a hand-written identifier in the format is refused: %v", err)
	}
	refused := map[string]string{
		"empty":                  "",
		"traversal":              "..",
		"traversal with padding": "..abcdefghij",
		"dot":                    ".abcdefghij2",
		"separator":              "abcde/ghij23",
		"backslash":              "abcde\\ghij23",
		"absolute unix path":     "/abcdefghij2",
		"absolute windows path":  "c:\\abcdefghi",
		"too short":              "abcdefghij2",
		"too long":               "abcdefghij234",
		"upper case":             "ABCDEFGHIJ23",
		"digit outside base32":   "abcdefghij01",
		"space":                  "abcdefghij 2",
		"non-ascii":              "abcdefghijé2",
		// The bare reserved names are what the reserved-name rule alone
		// discriminates, since nothing else in the rule refuses "con". The
		// two dotted rows below are refused by the dot being outside the
		// alphabet as well, so they show the rule reaches an extension but
		// would not catch the reserved-name check being deleted.
		"reserved con":              "con",
		"reserved nul":              "NUL",
		"reserved com1":             "com1",
		"reserved lpt9":             "LPT9",
		"reserved with extension":   "con.abcdefgh",
		"reserved padded to length": "aux.abcdefgh",
	}
	for name, id := range refused {
		err := home.ValidTaskID(id)
		if !errors.Is(err, home.ErrTaskID) {
			t.Fatalf("the %s identifier %q was accepted (%v), want ErrTaskID", name, id, err)
		}
		if _, err := open(t, t.TempDir()).Task(id); !errors.Is(err, home.ErrTaskID) {
			t.Fatalf("Task composed a path from the %s identifier %q: %v", name, id, err)
		}
		if _, err := open(t, t.TempDir()).TaskCopy(id); !errors.Is(err, home.ErrTaskID) {
			t.Fatalf("TaskCopy composed a path from the %s identifier %q: %v", name, id, err)
		}
	}
	// The reserved-name refusal is its own rule rather than a consequence of
	// the length rule, so a reserved name is refused as one and not as the
	// wrong length.
	if err := home.ValidTaskID("con"); !strings.Contains(err.Error(), "reserves") {
		t.Fatalf("a reserved name was refused as %v, want the reserved-name refusal", err)
	}
}

// A task's copy is under the task's own directory, so the two are one tree
// and a task removed is its copy removed with it.
func TestATaskCopyIsUnderTheTaskDirectory(t *testing.T) {
	t.Parallel()
	h := open(t, t.TempDir())
	task := taskPath(t, h, validTaskID)
	if want := filepath.Join(h.Root(), "tasks", validTaskID); task != want {
		t.Fatalf("Task = %q, want %q", task, want)
	}
	if got, want := copyPath(t, h, validTaskID), filepath.Join(task, "copy"); got != want {
		t.Fatalf("TaskCopy = %q, want %q", got, want)
	}
	if h.InstanceFile() != filepath.Join(h.Root(), "instance") {
		t.Fatalf("InstanceFile = %q", h.InstanceFile())
	}
	if h.Beacon() != filepath.Join(h.Root(), "beacon") {
		t.Fatalf("Beacon = %q", h.Beacon())
	}
}

// The probe answers Free for a created home nobody holds, Held while a lock
// is held, and Free again once it is released. The holder here is a second
// handle in this process, which the lock is per handle enough to refuse; the
// cross-process case is the next test.
func TestTheLockProbeFollowsAnInProcessHolder(t *testing.T) {
	t.Parallel()
	h := created(t)
	held, err := h.Acquire(t.Context(), 0)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	probe(t, h, home.LockHeld)
	if err := held.Release(); err != nil {
		t.Fatalf("releasing the lock: %v", err)
	}
	probe(t, h, home.LockFree)
	// The probe left the lock takeable: a probe that held on would make the
	// next service wait on a reader.
	again, err := h.Acquire(t.Context(), 0)
	if err != nil {
		t.Fatalf("taking the lock after a probe: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatalf("releasing the lock: %v", err)
	}
}

// The probe answers Held while a real second process holds the lock through
// Acquire, which is the case a command outside the service asks it for, and
// Free once that process lets go.
func TestTheLockProbeSeesAHolderInAnotherProcess(t *testing.T) {
	t.Parallel()
	h := created(t)
	probe(t, h, home.LockFree)
	holder := exec.CommandContext(t.Context(), os.Args[0])
	holder.Env = append(os.Environ(), holdLockVar+"="+h.Root())
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatalf("piping to the holder: %v", err)
	}
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatalf("piping from the holder: %v", err)
	}
	holder.Stderr = os.Stderr
	if err := holder.Start(); err != nil {
		t.Fatalf("starting the holder: %v", err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "held" {
		t.Fatalf("the holder reported %q, %v; want held", line, err)
	}
	probe(t, h, home.LockHeld)
	if err := stdin.Close(); err != nil {
		t.Fatalf("telling the holder to let go: %v", err)
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("the holder exited with %v", err)
	}
	probe(t, h, home.LockFree)
}

// A home that was never served has no lock file. The probe says Free with a
// detail naming that, and it leaves no lock file behind, because a probe is a
// read and a read may not bring a home into being. A home that was never
// created is a different answer: its root is not there, and that is Unknown,
// so a wrong home path cannot read as a home with no service.
func TestTheLockProbeNeverCreatesTheLockFile(t *testing.T) {
	t.Parallel()
	h := created(t)
	reading, err := h.LockState(t.Context())
	if err != nil {
		t.Fatalf("probing a home never served: %v", err)
	}
	if reading.State != home.LockFree {
		t.Fatalf("a home never served probed as %v, want free", reading.State)
	}
	if !strings.Contains(reading.Detail, "no lock file") {
		t.Fatalf("a home never served answered free with the detail %q, which does not say the lock file is missing", reading.Detail)
	}
	if _, err := os.Stat(h.LockFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probing a home never served created %s (%v)", h.LockFile(), err)
	}
	absent := open(t, filepath.Join(t.TempDir(), "home"))
	reading, err = absent.LockState(t.Context())
	if err == nil || reading.State != home.LockUnknown {
		t.Fatalf("a home never created probed as (%+v, %v), want unknown with the error", reading, err)
	}
	if _, err := os.Stat(absent.Root()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probing a home never created made its root (%v)", err)
	}
	// The detail is the no-lock-file case's alone: a home that has been
	// served answers Free without it.
	h = created(t)
	held, err := h.Acquire(t.Context(), 0)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("releasing the lock: %v", err)
	}
	reading, err = h.LockState(t.Context())
	if err != nil || reading.State != home.LockFree || reading.Detail != "" {
		t.Fatalf("a served home probed as (%+v, %v), want free with no detail", reading, err)
	}
}

// A lock the probe cannot ask about is Unknown with the error, never Free: a
// guard that reads Free as "no service" must not be handed Free by a failure
// to open the file. The lock file here is a directory, which no platform the
// module builds for opens for writing; the second case is a context already
// done.
func TestTheLockProbeAnswersUnknownAndNeverFreeWhenItCannotAsk(t *testing.T) {
	t.Parallel()
	h := created(t)
	if err := os.Mkdir(h.LockFile(), 0o700); err != nil {
		t.Fatalf("standing a directory where the lock file goes: %v", err)
	}
	reading, err := h.LockState(t.Context())
	if err == nil {
		t.Fatalf("probing a lock file that cannot be opened answered %+v with no error", reading)
	}
	if reading.State != home.LockUnknown {
		t.Fatalf("probing a lock file that cannot be opened answered %v, want unknown", reading.State)
	}
	done, cancel := context.WithCancel(t.Context())
	cancel()
	reading, err = created(t).LockState(done)
	if err == nil || reading.State != home.LockUnknown {
		t.Fatalf("probing under a done context answered (%+v, %v), want unknown with the error", reading, err)
	}
}

// A root the caller cannot read, and a lock file the caller cannot open for
// writing, each answer Unknown. Both rest on file permissions denying the
// caller, which is not something every platform or every user can be denied:
// Windows does not honour a mode bit as a denial, and root reads and writes
// through one, so the test skips there and says why rather than passing on
// nothing.
func TestTheLockProbeAnswersUnknownForAHomeItCannotRead(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("a permission bit does not deny the caller on windows, so an unreadable root cannot be arranged here")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which a permission bit does not deny, so an unreadable root cannot be arranged here")
	}
	unreadable := created(t)
	if err := os.Chmod(unreadable.Root(), 0); err != nil {
		t.Fatalf("making the root unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable.Root(), 0o700) })
	reading, err := unreadable.LockState(t.Context())
	if err == nil || reading.State != home.LockUnknown {
		t.Fatalf("probing an unreadable root answered (%+v, %v), want unknown with the error", reading, err)
	}
	unwritable := created(t)
	if err := os.WriteFile(unwritable.LockFile(), nil, 0o400); err != nil {
		t.Fatalf("standing a read-only lock file: %v", err)
	}
	reading, err = unwritable.LockState(t.Context())
	if err == nil || reading.State != home.LockUnknown {
		t.Fatalf("probing a lock file the caller cannot write answered (%+v, %v), want unknown with the error", reading, err)
	}
}

// The instance file is written after the lock is taken and is readable while
// the lock is held, which is the whole reason it is a file apart from the
// lock. A second write replaces the first whole, and a reader that holds the
// file open for a moment does not defeat it: where the platform refuses a
// rename over a file a reader holds open, the writer retries past the reader
// letting go. The reader here lets go after a delay inside the retry bound,
// so the write succeeds on every platform CI runs, through the retry on the
// one that needs it. What this does not show is that the retry was taken on
// any particular platform: a platform that allows the rename at once passes
// without it.
func TestTheInstanceFileIsReadableWhileTheLockIsHeld(t *testing.T) {
	t.Parallel()
	h := created(t)
	held, err := h.Acquire(t.Context(), 0)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	t.Cleanup(func() { _ = held.Release() })
	if err := h.WriteInstance("first"); err != nil {
		t.Fatalf("writing the instance file: %v", err)
	}
	if got := readInstance(t, h); got != "first\n" {
		t.Fatalf("the instance file holds %q, want the first identity", got)
	}
	reader, err := os.Open(h.InstanceFile())
	if err != nil {
		t.Fatalf("holding the instance file open: %v", err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(50 * time.Millisecond)
		_ = reader.Close()
	}()
	if err := h.WriteInstance("second"); err != nil {
		t.Fatalf("writing the instance file while a reader held it: %v", err)
	}
	<-released
	if got := readInstance(t, h); got != "second\n" {
		t.Fatalf("the instance file holds %q, want the second identity", got)
	}
	entries, err := os.ReadDir(h.Root())
	if err != nil {
		t.Fatalf("listing the root: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "instance-") {
			t.Fatalf("the write left %s behind", entry.Name())
		}
	}
	// A temporary file a crashed predecessor left between making it and
	// renaming it is swept by the next write, on the strength of the lock
	// this test holds.
	stale := filepath.Join(h.Root(), "instance-stale.tmp")
	if err := os.WriteFile(stale, []byte("crashed\n"), 0o600); err != nil {
		t.Fatalf("planting a stale temporary file: %v", err)
	}
	if err := h.WriteInstance("third"); err != nil {
		t.Fatalf("writing the instance file beside a stale temporary file: %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the write left the stale temporary file in place (%v)", err)
	}
	if got := readInstance(t, h); got != "third\n" {
		t.Fatalf("the instance file holds %q, want the third identity", got)
	}
	for _, bad := range []string{"", "two\nlines", "cr\r"} {
		if err := h.WriteInstance(bad); !errors.Is(err, home.ErrInstanceID) {
			t.Fatalf("WriteInstance(%q) = %v, want ErrInstanceID", bad, err)
		}
	}
	if got := readInstance(t, h); got != "third\n" {
		t.Fatalf("a refused write changed the instance file to %q", got)
	}
}

// probe asserts the lock's state, failing on an error because every state
// this asks for is one the probe answers without one.
func probe(t *testing.T, h *home.Home, want home.LockState) {
	t.Helper()
	reading, err := h.LockState(t.Context())
	if err != nil {
		t.Fatalf("probing the lock: %v", err)
	}
	if reading.State != want {
		t.Fatalf("the lock probed as %v (%q), want %v", reading.State, reading.Detail, want)
	}
}

// readInstance reads the instance file as a reader outside the service would.
func readInstance(t *testing.T, h *home.Home) string {
	t.Helper()
	body, err := os.ReadFile(h.InstanceFile())
	if err != nil {
		t.Fatalf("reading the instance file: %v", err)
	}
	return string(body)
}
