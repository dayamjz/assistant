package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// A run ended through the protocol while a segment is inside a stage body is
// not picked up again, and this drives the whole path from the ending to the
// decision rather than either end of it.
//
// The two ends are checked apart elsewhere, and neither of those says they are
// wired together: a segment's release reports the ending, and an ending maps to
// a disposition nothing continues. Between them sits advance, which reads the
// one into the other, and carryOn, which dispatches on it. Either silently
// disconnected puts a cancelled run back to walking stage bodies.
//
// The second case is the one that discriminates, and the first is why it has
// to. cancel moves the record before it answers, so by the time an end-to-end
// test can look, attach's own reconcile refuses to resume a terminated run
// whatever carryOn decided - which is a second line of defence, not this one,
// and it makes the ordinary path pass with the guard removed. What the guard
// answers for is the window where the ending is recorded and the record has
// not moved yet, which is exactly the interleaving round one found. Holding the
// record still is how that window is made deterministic instead of raced.
//
// P6 is cited because a continuation there would execute nodes past a position
// the run was ended at, and the run's checkpoint history is what would then
// record work nobody asked for.
func TestARunEndedThroughTheProtocolIsNotCarriedOn(t *testing.T) {
	principles.Cite(t, principles.P6)

	t.Run("the record has moved by the time the segment returns", func(t *testing.T) {
		held := newHeldService(t)
		record := held.begin(t)

		if _, err := held.service.cancel(t.Context(), machine.CancelRequest{Run: record.ID}); err != nil {
			t.Fatalf("ending the run: %v", err)
		}
		held.awaitSegment(t)

		if got := held.status(t, record.ID); got != store.RunTerminated {
			t.Fatalf("the ended run is recorded as %s, want terminated", got)
		}
		held.assertNotResumed(t, record.ID)
	})

	t.Run("the ending is recorded before the record moves", func(t *testing.T) {
		held := newHeldService(t)
		record := held.begin(t)

		// The slot carries the ending and the record still says running,
		// which is what a continuation reading that record would resume from.
		// The ending is written here and never finished, so the record stays
		// where it is for the whole of the check below.
		held.service.endRun(record.ID)
		held.awaitSegment(t)

		if got := held.status(t, record.ID); got != store.RunRunning {
			t.Fatalf("the record moved to %s; this case is about the window before it does", got)
		}
		held.assertNotResumed(t, record.ID)
	})
}

// heldService is a service whose first stage body stays inside itself until
// the ending under test cancels it, so a segment certainly holds the run's
// slot when that ending arrives rather than being caught there. let is the
// teardown's way out for a body no ending cancelled.
type heldService struct {
	service *Service
	inside  chan struct{}
	release chan struct{}
	let     func()
	// head is the commit the subject repository stands at, which is what a
	// run of it validates: begin builds the run's isolated copy before any
	// stage body runs, so the commit has to be one that copy can be cut at.
	head string
	// entries counts every entry into the stage body, including one a
	// continuation would make.
	entries atomic.Int64
	// returned is closed when the segment that begins the run has returned,
	// which is after its own carryOn has decided.
	returned chan struct{}
}

// begin starts a run and returns its record once the segment advancing it is
// inside the stage body.
func (h *heldService) begin(t *testing.T) store.Run {
	t.Helper()
	record, err := h.service.create(t.Context(), run{
		repository: "subject",
		branch:     "main",
		head:       h.head,
		intent:     "held open for the length of an ending",
		source:     intentSourceSupplied,
		supplied:   true,
	})
	if err != nil {
		t.Fatalf("recording a run: %v", err)
	}
	go func() {
		defer close(h.returned)
		// The context is the service's own, so nothing about this goroutine
		// going away ends the segment: what ends it is the ending under test.
		_, _ = h.service.begin(context.WithoutCancel(t.Context()), record, pipeline.Start{
			Branch:         record.Branch,
			Base:           "main",
			Submitted:      record.SubmittedHead,
			Intent:         record.Intent,
			IntentSupplied: true,
		})
	}()
	select {
	case <-h.inside:
	case <-time.After(30 * time.Second):
		t.Fatal("the run never reached the stage body")
	}
	return record
}

// awaitSegment waits for the segment that began the run to return, which is
// the point at which its carryOn has decided.
func (h *heldService) awaitSegment(t *testing.T) {
	t.Helper()
	select {
	case <-h.returned:
	case <-time.After(30 * time.Second):
		t.Fatal("the segment never returned after the stage body was let out")
	}
}

// status reads the run's recorded status straight from the store, without the
// reconciliation a read through the service would do.
func (h *heldService) status(t *testing.T, runID string) store.RunStatus {
	t.Helper()
	record, err := h.service.store.Run(t.Context(), runID)
	if err != nil {
		t.Fatalf("reading run %s: %v", runID, err)
	}
	return record.Status
}

// assertNotResumed fails if anything picked the run up after its segment
// ended. A continuation takes the run's slot and walks the graph from where it
// stood, so it reaches the stage body a second time and is counted on the way
// in rather than on the way out; the wait is what turns "not yet" into "not at
// all".
func (h *heldService) assertNotResumed(t *testing.T, runID string) {
	t.Helper()
	for range 25 {
		if h.entries.Load() > 1 {
			t.Fatalf("the stage body ran %d times: something carried on a run that was ended", h.entries.Load())
		}
		if h.service.isAdvancing(runID) {
			t.Fatal("a segment is advancing a run that was ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newHeldService opens a service on a home of its own, with a repository
// recorded and a first stage body that waits to be let out.
func newHeldService(t *testing.T) *heldService {
	t.Helper()
	root, err := os.MkdirTemp("", "h")
	if err != nil {
		t.Fatalf("making a home root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}

	held := &heldService{
		inside:   make(chan struct{}),
		release:  make(chan struct{}),
		returned: make(chan struct{}),
	}
	var entered, released sync.Once
	held.let = func() { released.Do(func() { close(held.release) }) }
	served := func(deps stages.StageDeps) pipeline.Stages {
		nine := stages.All(deps)
		nine.Intent = pipeline.Implementation{
			NewBody: func() pipeline.Body {
				return func(ctx context.Context, _ pipeline.Input) (pipeline.Output, error) {
					held.entries.Add(1)
					entered.Do(func() { close(held.inside) })
					select {
					case <-held.release:
					case <-ctx.Done():
					}
					// The context decides, not the select. Both channels are
					// ready once an ending cancels the segment and the teardown
					// lets the body out, and a select over two ready cases picks
					// between them at random - which would have the body report
					// a decision on a run that was ended, at whatever rate the
					// scheduler happens to produce.
					if err := ctx.Err(); err != nil {
						return pipeline.Output{}, err
					}
					return pipeline.Output{Report: findings.Report{
						Summary: "the stage was held open for the length of the ending",
						Findings: []findings.Finding{
							{ID: "stand-in", Action: findings.ActionAsk, Description: "a decision"},
						},
					}}, nil
				}
			},
		}
		return nine
	}

	running, err := Open(t.Context(), Options{
		Home:      h,
		NewStages: served,
		NewFixer:  stages.Fix,
		Build:     build,
		Catalog:   agents.NewCatalog(fixedRunner{runner: standin.New(t, standin.Script{}).Runner()}),
		LockWait:  -1,
	})
	if err != nil {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skipf("%s has no local socket transport to serve this protocol over: %v", runtime.GOOS, err)
		}
		t.Fatalf("opening the service: %v", err)
	}
	held.service = running
	t.Cleanup(func() {
		if err := running.Close(); err != nil {
			t.Errorf("closing the service: %v", err)
		}
	})
	// Registered after the service, so it runs before the service is closed. A
	// test that fails while the body is still held would otherwise leave the
	// teardown waiting on a stage nothing is going to release.
	t.Cleanup(held.let)

	// The repository is real and bound to a gate, because begin builds the
	// run's isolated copy before its record moves to running: a record naming
	// a working copy no commit can be fetched from is a run that fails before
	// the stage body this fixture holds open is ever reached.
	subject, upstream, head := heldSubject(t)
	held.head = head
	if _, err := running.store.UpsertRepository(t.Context(), store.Repository{
		ID:            "subject",
		WorkingPath:   subject,
		UpstreamURL:   upstream,
		DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	command, err := os.Executable()
	if err != nil {
		t.Fatalf("finding this test binary: %v", err)
	}
	if _, err := gate.Initialize(t.Context(), gate.Spec{
		Home:        h.Root(),
		WorkingPath: subject,
		// The hooks are written but never fire here: nothing in these tests
		// pushes to the gate, and what they need out of it is the repository
		// the run's isolated copy is cut from.
		Command: command,
	}, gate.WithIndex(running.store)); err != nil {
		t.Fatalf("initializing the gate: %v", err)
	}
	return held
}

// heldSubject builds the working copy a held run is of: one commit on its
// default branch, a local bare upstream beside it so the record's upstream
// resolves without a network, and the path resolved the way the service
// resolves the recorded one. It shells out on the same terms the package's
// external helpers do: a subject assembled with the code under test could not
// show that code wrong.
func heldSubject(t *testing.T) (path, upstream, head string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatalf("making a subject repository: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	heldGit(t, dir, "init", "--quiet", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("writing a file: %v", err)
	}
	heldGit(t, dir, "add", "-A")
	heldGit(t, dir, "commit", "--quiet", "-m", "first")
	remote, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatalf("making an upstream repository: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(remote) })
	heldGit(t, remote, "init", "--quiet", "--bare", "-b", "main", ".")
	heldGit(t, dir, "remote", "add", "origin", remote)
	heldGit(t, dir, "push", "--quiet", "origin", "main")
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving the subject path: %v", err)
	}
	return resolved, remote, heldGit(t, dir, "rev-parse", "HEAD")
}

// heldGit runs a git command for building that subject, insulated from the
// machine's own git configuration.
func heldGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixedRunner hands back a Runner somebody else built, so nothing here can
// produce an adapter the production path could not.
type fixedRunner struct{ runner agents.Runner }

func (f fixedRunner) Name() string { return f.runner.Name() }

func (f fixedRunner) New(context.Context, []string) (agents.Runner, error) { return f.runner, nil }

// A run a push supersedes is ended by the same seam a cancel is, so it is not
// picked up again either.
//
// signalCancellation is what claimPush ends the displaced run with, and it is
// driven here rather than a whole push. The record is what a push moves next,
// and moving it would put attach's own reconcile in the way - the second line
// of defence the test above describes, which passes whatever carryOn decided.
// What this answers for is the window claimPush leaves open between signalling
// the displaced run and committing its terminated record: a segment cancelled
// there against a record that still says running is exactly what a
// continuation would resume, and the run it would resume is one a newer push
// has already taken the branch from.
//
// P6 is cited for the same reason as above: a continuation there executes
// nodes past the position the run was displaced at, and writes them into a
// checkpoint history nobody asked for.
func TestARunSupersededByAPushIsNotCarriedOn(t *testing.T) {
	principles.Cite(t, principles.P6)

	held := newHeldService(t)
	record := held.begin(t)

	held.service.signalCancellation(record.ID)
	held.awaitSegment(t)

	if got := held.status(t, record.ID); got != store.RunRunning {
		t.Fatalf("the record moved to %s; this case is about the window before a push moves it", got)
	}
	held.assertNotResumed(t, record.ID)
}
