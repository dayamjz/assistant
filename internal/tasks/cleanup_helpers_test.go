package tasks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/store/storetest"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The cleanup tests run over real repositories: an upstream the repository
// record names, cloned into a task's copy the way a dispatch cuts one, with
// commits made and pushed through git directly, on the terms isolationSubject
// states for rawGit. The upstream is named by the github.com URL the
// repository record carries, so the merged-pull-request proof has a
// repository on the code host to read, and git's own url.insteadOf rewrite
// in the test's global configuration points that URL at the bare
// repository on disk; what the proofs read is the URL as recorded, and
// where the bytes come from is the rewrite's, which keeps every test here
// off the network without weakening what it observes.

const (
	cleanupRepository = "repo-1"
	upstreamURL       = "https://github.com/owner/name.git"
	upstreamSpecifier = "owner/name"
)

// cleanupSubject is a home, a store and an upstream the cleanup decision is
// run over.
type cleanupSubject struct {
	home     *home.Home
	store    *store.Store
	upstream string
	forge    *forgetest.Provider
	cleaner  *Cleaner
}

// newCleanupSubject builds the subject with the code host scripted as
// given; an empty script answers every read as a provider that refuses.
func newCleanupSubject(t *testing.T, script forgetest.Script) *cleanupSubject {
	t.Helper()
	ctx := context.Background()
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = Test\n\temail = test@example.invalid\n"+
		"[init]\n\tdefaultBranch = main\n[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n"+
		// The path is written in slash form. Written as filepath.Join gives
		// it, a Windows path reached git from this section with every
		// backslash gone (windows-latest, the first CI run of this file),
		// and a slash-form path names a local remote on every platform.
		"[url \""+filepath.ToSlash(upstream)+"\"]\n\tinsteadOf = "+upstreamURL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	if err := os.Mkdir(upstream, 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, upstream, "init", "-q", "--bare", "-b", "main")
	seed := filepath.Join(t.TempDir(), "seed")
	rawGit(t, filepath.Dir(seed), "clone", "-q", upstream, seed)
	if err := os.WriteFile(filepath.Join(seed, "f.txt"), []byte(twelveLines), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, seed, "add", "f.txt")
	rawGit(t, seed, "commit", "-q", "-m", "base")
	rawGit(t, seed, "push", "-q", "origin", "main")

	root := t.TempDir()
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("home.Open: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("home.Create: %v", err)
	}
	// Seeded for time alone, as internal/store/storetest's comment says.
	storetest.Seed(t, h.Database())
	s, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.UpsertRepository(ctx, store.Repository{
		ID: cleanupRepository, WorkingPath: filepath.Join(root, "checkout"), UpstreamURL: upstreamURL, DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	provider := forgetest.New(t, script)
	cleaner, err := NewCleaner(CleanupDeps{Home: h, Store: s, Forge: provider.Host(), GitDeadline: vcs.DefaultInvocationDeadline})
	if err != nil {
		t.Fatalf("NewCleaner: %v", err)
	}
	return &cleanupSubject{home: h, store: s, upstream: upstream, forge: provider, cleaner: cleaner}
}

// twelveLines is the base file: lines far enough apart that two edits
// integrate without touching each other's context.
const twelveLines = "one\nsame\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\nsame\ntwelve\n"

// task is one task over the subject with its copy cut.
type task struct {
	id   string
	copy string
}

// deliveryTask records a delivery task on branch with its first attempt
// begun, cuts its copy as a dispatch does, a clone of the upstream with the
// gate remote on the task's branch, and records the copy's start snapshot.
func (s *cleanupSubject) deliveryTask(t *testing.T, branch string) task {
	t.Helper()
	id := s.record(t, store.TaskDelivery, store.Known(branch))
	copyPath, _ := s.home.TaskCopy(id)
	if _, err := vcs.Clone(context.Background(), s.upstream, copyPath, gate.RemoteName); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	rawGit(t, copyPath, "switch", "-q", "-c", branch)
	s.snapshot(t, id, copyPath)
	return task{id: id, copy: copyPath}
}

// investigationTask records an investigation task of the repository, cuts
// its copy as a dispatch does, a clone with no remote detached at main, and
// records the start snapshot.
func (s *cleanupSubject) investigationTask(t *testing.T) task {
	t.Helper()
	id := s.record(t, store.TaskInvestigation, store.Unknown[string]())
	copyPath, _ := s.home.TaskCopy(id)
	if _, err := vcs.Clone(context.Background(), s.upstream, copyPath, ""); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	rawGit(t, copyPath, "switch", "-q", "--detach")
	s.snapshot(t, id, copyPath)
	return task{id: id, copy: copyPath}
}

func (s *cleanupSubject) record(t *testing.T, shape store.TaskShape, branch store.Optional[string]) string {
	t.Helper()
	ctx := context.Background()
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err := s.home.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.CreateTask(ctx, store.Task{
		ID: id, Shape: shape, Project: "one", Mode: "interactive", WorktreePath: copyPath,
		RepositoryID: store.Known(cleanupRepository), Branch: branch,
	}, store.Disposition{}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := s.store.BeginFirstTaskAttempt(ctx, id, store.OnThisMachine()); err != nil {
		t.Fatalf("BeginTaskAttempt: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	return id
}

// snapshot records what the copy holds now as the task's start snapshot,
// read through git directly so the record is of the copy and not of the
// package under test.
func (s *cleanupSubject) snapshot(t *testing.T, id, copyPath string) {
	t.Helper()
	if err := s.store.SetTaskStartSnapshot(context.Background(), id, readSnapshot(t, copyPath)); err != nil {
		t.Fatalf("SetTaskStartSnapshot: %v", err)
	}
}

// readSnapshot reads what the copy holds now, through git directly, in the
// shape a dispatch records as the task's start snapshot.
func readSnapshot(t *testing.T, copyPath string) store.CopySnapshot {
	t.Helper()
	snap := store.CopySnapshot{Head: rawGit(t, copyPath, "rev-parse", "HEAD"), Refs: []store.SnapshotRef{}}
	if out, err := tryRawGit(copyPath, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		snap.Branch = strings.TrimSpace(out)
	}
	for _, line := range strings.Split(rawGit(t, copyPath, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(*objectname)"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		commit := fields[1]
		if fields[2] != "" {
			commit = fields[2]
		}
		snap.Refs = append(snap.Refs, store.SnapshotRef{Name: fields[0], Commit: commit})
	}
	return snap
}

// tryRawGit runs git and returns its combined output and error, for a
// question whose answer may be a non-zero status.
func tryRawGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// commit writes content to file in the copy and commits it, returning the
// commit.
func commit(t *testing.T, copyPath, file, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(copyPath, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, copyPath, "add", file)
	rawGit(t, copyPath, "commit", "-q", "-m", message)
	return rawGit(t, copyPath, "rev-parse", "HEAD")
}

// push pushes the copy's branch to the upstream, which is what a worker's
// push through the gate lands there.
func (s *cleanupSubject) push(t *testing.T, copyPath, branch string) {
	t.Helper()
	rawGit(t, copyPath, "push", "-q", gate.RemoteName, branch+":"+branch)
}

// mergeIntoMain lands the branch's content on the upstream's main: squashed
// when squash is set, as a fast-forward otherwise. It returns main's new
// tip.
func (s *cleanupSubject) mergeIntoMain(t *testing.T, branch string, squash bool) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "merger")
	rawGit(t, filepath.Dir(work), "clone", "-q", s.upstream, work)
	rawGit(t, work, "fetch", "-q", "origin", branch)
	if squash {
		rawGit(t, work, "merge", "-q", "--squash", "origin/"+branch)
		rawGit(t, work, "commit", "-q", "-m", branch+", squashed")
	} else {
		rawGit(t, work, "merge", "-q", "--ff-only", "origin/"+branch)
	}
	rawGit(t, work, "push", "-q", "origin", "main")
	return rawGit(t, work, "rev-parse", "HEAD")
}

// deleteUpstreamBranch deletes the branch on the upstream, as a code host
// does after a merge.
func (s *cleanupSubject) deleteUpstreamBranch(t *testing.T, branch string) {
	t.Helper()
	rawGit(t, s.upstream, "update-ref", "-d", "refs/heads/"+branch)
}

// landedRun records a run of the task's branch, attributed to it, that its
// pull request's read ended on a merge: the merge record names number and
// head, which is what the merged-pull-request proof reads.
func (s *cleanupSubject) landedRun(t *testing.T, branch string, number int, head string) string {
	t.Helper()
	ctx := context.Background()
	id := "run-" + strings.ReplaceAll(branch, "/", "-")
	if _, err := s.store.CreateRun(ctx, store.Run{
		ID: id, RepositoryID: cleanupRepository, Branch: branch, SubmittedHead: head, Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := s.store.TransitionRun(ctx, id, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	if _, err := s.store.TransitionRunOnPullRequest(ctx, id, []store.RunStatus{store.RunRunning}, store.RunPassed,
		store.PullRequestRead{PullRequest: number, Head: head, End: store.PullRequestMerged}, store.HoldClosure{}); err != nil {
		t.Fatalf("TransitionRunOnPullRequest: %v", err)
	}
	return id
}

// pendingRun records a run of the repository on another branch, for a record
// that is to name one.
func (s *cleanupSubject) pendingRun(t *testing.T, id string) {
	t.Helper()
	if _, err := s.store.CreateRun(context.Background(), store.Run{
		ID: id, RepositoryID: cleanupRepository, Branch: "elsewhere", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
}

// cleanUp runs the decision over the task with its terminal observed Dead,
// which is the one liveness a copy is examined under.
func (s *cleanupSubject) cleanUp(t *testing.T, id string) (Cleanup, error) {
	t.Helper()
	return s.cleaner.CleanUp(context.Background(), TaskCopy{Task: id, Terminal: taskstate.Dead})
}

// refusedWith asserts that err is a *CleanupRefusal for the reason, matching
// ErrCleanupRefused, and returns it.
func refusedWith(t *testing.T, err error, want CleanupReason) *CleanupRefusal {
	t.Helper()
	var refusal *CleanupRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want a *CleanupRefusal naming %s", err, want)
	}
	if !errors.Is(err, ErrCleanupRefused) {
		t.Errorf("%v does not match ErrCleanupRefused", err)
	}
	if refusal.Reason != want {
		t.Errorf("refused as %s (%s), want %s", refusal.Reason, refusal.Detail, want)
	}
	return refusal
}

// removedOn asserts the cleanup removed the copy on the proof, and that the
// record says so with the removal marked.
func (s *cleanupSubject) removedOn(t *testing.T, tk task, got Cleanup, err error, proof store.CleanupProof) store.TaskCleanup {
	t.Helper()
	if err != nil {
		t.Fatalf("CleanUp: %v", err)
	}
	if got.Result != CleanupRemoved || got.Proof != proof {
		t.Fatalf("CleanUp = %+v, want removed on %s", got, proof)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the copy still stands at %s after a removal: %v", tk.copy, err)
	}
	record, err := s.store.TaskCleanup(context.Background(), tk.id)
	if err != nil || record.Proof != proof || !record.RemovedAt.IsKnown() {
		t.Fatalf("the cleanup record is %+v (%v), want %s with the removal marked", record, err, proof)
	}
	return record
}

// stillStands asserts the copy is where it was.
func stillStands(t *testing.T, tk task) {
	t.Helper()
	if _, err := os.Lstat(tk.copy); err != nil {
		t.Fatalf("the copy of %s was taken away: %v", tk.id, err)
	}
}

// attemptFor returns what a refusal says the proof found.
func attemptFor(t *testing.T, r *CleanupRefusal, proof store.CleanupProof) string {
	t.Helper()
	for _, a := range r.Attempts {
		if a.Proof == proof {
			return a.Detail
		}
	}
	t.Fatalf("the refusal %v says nothing about the %s proof", r, proof)
	return ""
}

// aReport is a report with one ask finding and one fix finding, the shape a
// worker files.
const aReport = `{"summary":"looked at the thing","findings":[` +
	`{"severity":"warning","action":"ask","location":"pkg/a.go:3","description":"should the retry be bounded?"},` +
	`{"severity":"warning","action":"fix","location":"pkg/b.go","description":"a typo in the comment"}]}`

// storeReport stores aReport for the task and returns what was stored.
func (s *cleanupSubject) storeReport(t *testing.T, id string) StoredReport {
	t.Helper()
	stored, err := s.cleaner.StoreReport(context.Background(), id, aReport)
	if err != nil {
		t.Fatalf("StoreReport: %v", err)
	}
	return stored
}
