package service_test

import (
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// recordDispatchedTask writes, where a coordinator's intake would have, a
// task that resolves Dispatched: the record with its copy reserved at
// home.TaskCopy, and the instructions file intake writes after it. It is
// written through internal/store rather than task.create so the test holds a
// task that has not yet been dispatched, which task.create's background
// dispatch would race; coordinator_unix_test.go drives task.create itself.
func recordDispatchedTask(t *testing.T, h *home.Home, repository store.Repository, shape store.TaskShape, branch string) string {
	t.Helper()
	return recordDispatchedTaskUnder(t, h, repository, shape, branch, store.Unknown[config.Delivery]())
}

// recordDispatchedTaskUnder is recordDispatchedTask for a task recorded
// under a delivery posture, as intake records one.
func recordDispatchedTaskUnder(t *testing.T, h *home.Home, repository store.Repository, shape store.TaskShape, branch string,
	posture store.Optional[config.Delivery]) string {
	t.Helper()
	return recordDispatchedTaskWith(t, h, repository, shape, branch, posture, "one", "Do the work the coordinator asked for.\n")
}

// recordDispatchedTaskWith is recordDispatchedTaskUnder with the project
// the record carries and the instructions intake would have written.
func recordDispatchedTaskWith(t *testing.T, h *home.Home, repository store.Repository, shape store.TaskShape, branch string,
	posture store.Optional[config.Delivery], project, text string) string {
	t.Helper()
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	task := store.Task{
		ID: id, Shape: shape, Project: project, Mode: "interactive", WorktreePath: copyPath,
		RepositoryID: store.Known(repository.ID), Delivery: posture,
	}
	if branch != "" {
		task.Branch = store.Known(branch)
	}
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	if _, err := records.CreateTask(t.Context(), task, store.Disposition{}); err != nil {
		t.Fatalf("recording the task: %v", err)
	}
	instructions, err := h.TaskInstructions(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(instructions), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instructions, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

// sameFile reports whether two paths name one file, by identity.
func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	ia, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	ib, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	return os.SameFile(ia, ib)
}

// A delivery task that resolves Dispatched is cut a clone of the primary
// checkout at the path its record reserved, with the gate as its one remote
// by file identity and HEAD on the task's branch at the default branch's tip.
// Both snapshots are recorded, the second check is in its instructions, the
// attempt is one, and nothing launched, because the adapter here declares no
// worker sessions: the task still resolves Dispatched, and the primary is
// untouched, its own configuration included. The clone's own configuration
// turns automatic maintenance off, because the clone is the program's and the
// primary is the person's, which is PRD principle P1. Launching is
// launch_test.go's subject.
func TestADispatchedDeliveryTaskIsCutAnIsolatedClone(t *testing.T) {
	principles.Cite(t, principles.P1)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")
	gatePath := gateRepositoryOf(t, h, subject)
	mainTip := git(t, subject, "rev-parse", "refs/heads/main")
	workTip := git(t, subject, "rev-parse", "HEAD")
	primaryBefore := refsOf(t, subject)
	remotesBefore := git(t, subject, "remote")
	primaryConfigBefore := git(t, subject, "config", "--local", "--list")

	withServiceOptions(t, workerlessOptions(t, h), func(running serviceUnderTest) {
		if got := taskState(t, running.client, id); got.State != taskstate.Dispatched || got.Source != taskstate.SourceEvent {
			t.Fatalf("after dispatch the task resolves %s from %s; its adapter declares no worker sessions, so it stays dispatched",
				got.State, got.Source)
		}
	})

	copyPath, _ := h.TaskCopy(id)
	if got := git(t, copyPath, "remote"); got != gate.RemoteName {
		t.Fatalf("the copy's remotes are %q, want exactly %s", got, gate.RemoteName)
	}
	if url := git(t, copyPath, "remote", "get-url", gate.RemoteName); !sameFile(t, url, gatePath) {
		t.Fatalf("the remote %s is at %s, which is not this home's gate %s", gate.RemoteName, url, gatePath)
	}
	if out, err := exec.Command("git", "-C", copyPath, "config", "--get-all", "remote."+gate.RemoteName+".pushurl").CombinedOutput(); err == nil {
		t.Fatalf("the remote carries a push URL %q", strings.TrimSpace(string(out)))
	}
	if branch := git(t, copyPath, "symbolic-ref", "--short", "HEAD"); branch != "task/one" {
		t.Fatalf("the copy is on %q, want the task's branch", branch)
	}
	if head := git(t, copyPath, "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("the task's branch starts at %s, want the primary's default-branch tip %s", head, mainTip)
	}
	if dir := git(t, copyPath, "rev-parse", "--git-common-dir"); !sameFile(t, filepath.Join(copyPath, dir), filepath.Join(copyPath, ".git")) {
		t.Fatalf("the copy's common directory is %s, so it is a linked worktree and not a clone", dir)
	}
	if got := refsOf(t, subject); got != primaryBefore {
		t.Fatalf("the primary's references changed under the dispatch:\n%s\nwere:\n%s", got, primaryBefore)
	}
	if got := git(t, subject, "remote"); got != remotesBefore {
		t.Fatalf("the primary's remotes are %q after the dispatch, were %q", got, remotesBefore)
	}
	// The clone is the program's, so its own configuration turns automatic
	// maintenance off; the primary is the person's, so its own configuration
	// is read back whole and is what it was. The subject carries both keys
	// already, set by quietMaintenance, so the comparison is of the whole
	// file rather than of an absence.
	copyMaintenance, err := ownMaintenanceConfig(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"gc.auto": "0", "maintenance.auto": "false"}; !maps.Equal(copyMaintenance, want) {
		t.Fatalf("the copy's own configuration reads automatic maintenance as %v, want %v", copyMaintenance, want)
	}
	if got := git(t, subject, "config", "--local", "--list"); got != primaryConfigBefore {
		t.Fatalf("the primary's own configuration changed under the dispatch:\n%s\nwas:\n%s", got, primaryConfigBefore)
	}

	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	task, err := records.Task(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := task.StartSnapshot.Get()
	if !ok || snapshot.Head != mainTip || snapshot.Branch != "task/one" {
		t.Fatalf("the start snapshot is %+v, want HEAD %s on task/one", task.StartSnapshot, mainTip)
	}
	var sawBranch bool
	for _, ref := range snapshot.Refs {
		if ref.Name == "refs/heads/task/one" && ref.Commit == mainTip {
			sawBranch = true
		}
	}
	if !sawBranch {
		t.Fatalf("the start snapshot's references %+v do not carry the task's branch", snapshot.Refs)
	}
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("the task has attempts %+v (%v), want exactly the dispatch's", attempts, err)
	}
	primaries, err := records.PrimarySnapshots(t.Context(), id)
	if err != nil || len(primaries) != 1 {
		t.Fatalf("the task has primary snapshots %+v (%v), want one for the attempt", primaries, err)
	}
	primary := primaries[0]
	if primary.Attempt != 1 || primary.Head != workTip || primary.HeadBranch.Or("") != "work" {
		t.Fatalf("the primary snapshot is %+v, want HEAD %s on work for attempt 1", primary, workTip)
	}
	branches := map[string]string{}
	for _, b := range primary.Branches {
		branches[b.Name] = b.Commit
	}
	if branches["refs/heads/main"] != mainTip || branches["refs/heads/work"] != workTip {
		t.Fatalf("the primary snapshot's branches are %v", branches)
	}
	instructions, _ := h.TaskInstructions(id)
	text, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "Do the work the coordinator asked for.") ||
		!strings.Contains(string(text), "git rev-parse --show-toplevel") {
		t.Fatalf("the instructions do not carry both the coordinator's text and the second check:\n%s", text)
	}
}

// An investigation task's clone carries no remote at all and a detached HEAD
// at the default branch's tip. The adapter declares no worker sessions, so
// the copy is read as dispatch left it.
func TestADispatchedInvestigationTaskIsCutADetachedCloneWithNoRemote(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskInvestigation, "")
	mainTip := git(t, subject, "rev-parse", "refs/heads/main")

	withServiceOptions(t, workerlessOptions(t, h), func(running serviceUnderTest) {
		if got := taskState(t, running.client, id); got.State != taskstate.Dispatched {
			t.Fatalf("after dispatch the task resolves %s", got.State)
		}
	})

	copyPath, _ := h.TaskCopy(id)
	if got := git(t, copyPath, "remote"); got != "" {
		t.Fatalf("the investigation copy carries remotes %q, want none", got)
	}
	// git config exits 1 with no output when nothing matches, which is the
	// answer wanted: no remote.* key survived the clone.
	if out, err := exec.Command("git", "-C", copyPath, "config", "--local", "--name-only", "--get-regexp", `^remote\.`).CombinedOutput(); err == nil {
		t.Fatalf("the investigation copy's configuration still names a remote: %s", out)
	}
	out, err := exec.Command("git", "-C", copyPath, "symbolic-ref", "--quiet", "HEAD").CombinedOutput()
	if err == nil {
		t.Fatalf("the investigation copy's HEAD is on %s, want detached", strings.TrimSpace(string(out)))
	}
	if head := git(t, copyPath, "rev-parse", "HEAD"); head != mainTip {
		t.Fatalf("the investigation copy stands at %s, want the default-branch tip %s", head, mainTip)
	}
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	task, err := records.Task(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, ok := task.StartSnapshot.Get(); !ok || snapshot.Head != mainTip || snapshot.Branch != "" {
		t.Fatalf("the start snapshot is %+v, want detached at %s", task.StartSnapshot, mainTip)
	}
}

// A service that died between claiming the dispatch and finishing the clone
// leaves an attempt and a half-written path. The retry at the next open
// claims the next attempt, observes what stands there, and refuses it with
// the typed reason recorded; the path is left exactly as it was found, which
// is PRD principle P6's never-lose-work applied to a directory nobody can
// account for. A second task whose standing copy is a repository of the
// wrong shape is refused with the shape's own token, so the two refusals
// read apart in the history.
func TestARetryAfterACrashRefusesTheHalfWrittenCopyAndLeavesIt(t *testing.T) {
	principles.Cite(t, principles.P6)
	service.RequireSweepClearsHere(t, "a dispatch retry refusing the half-written copy for isolation")
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	half := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/half")
	wrong := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/wrong")

	halfPath, _ := h.TaskCopy(half)
	if err := os.MkdirAll(halfPath, 0o700); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(halfPath, "partly-written.txt")
	if err := os.WriteFile(stray, []byte("left by a service that died\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrongPath, _ := h.TaskCopy(wrong)
	git(t, filepath.Dir(wrongPath), "clone", "--quiet", subject, wrongPath)
	quietMaintenance(t, wrongPath)
	git(t, wrongPath, "switch", "--quiet", "-c", "task/wrong")
	func() {
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		for _, id := range []string{half, wrong} {
			if _, err := records.BeginTaskAttempt(t.Context(), id, 0); err != nil {
				t.Fatalf("beginning the crashed attempt: %v", err)
			}
		}
	}()

	withService(t, h, func(running serviceUnderTest) {
		for _, id := range []string{half, wrong} {
			if got := taskState(t, running.client, id); got.State != taskstate.Failed || got.Source != taskstate.SourceEvent {
				t.Fatalf("after the retry task %s resolves %s from %s, want failed from the refusal", id, got.State, got.Source)
			} else if detail := got.Detail.Or(""); strings.Contains(detail, string(taskstate.RefusedNotIsolated)) ||
				strings.Contains(detail, string(taskstate.RefusedWrongShape)) {
				t.Fatalf("the resolution of %s carries the reason in its detail %q; the reason is evidence in the "+
					"history, not part of the answer", id, detail)
			}
		}
	})

	if got, err := os.ReadFile(stray); err != nil || string(got) != "left by a service that died\n" {
		t.Fatalf("the half-written path was not left as found: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(halfPath, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the retry wrote into the half-written path: %v", err)
	}
	if got := git(t, wrongPath, "remote"); got != "origin" {
		t.Fatalf("the wrongly shaped copy's remotes are %q after the retry, want it untouched", got)
	}

	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	for id, want := range map[string]taskstate.IsolationRefusal{half: taskstate.RefusedNotIsolated, wrong: taskstate.RefusedWrongShape} {
		attempts, err := records.TaskAttempts(t.Context(), id)
		if err != nil || len(attempts) != 2 {
			t.Fatalf("task %s has attempts %+v (%v), want the crashed one and the retry", id, attempts, err)
		}
		events, err := records.TaskEvents(t.Context(), id, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var refusals []store.TaskEvent
		for _, e := range events {
			if e.Kind == "isolation-refused" {
				refusals = append(refusals, e)
			}
		}
		if len(refusals) != 1 {
			t.Fatalf("task %s has refusals %+v, want exactly one", id, refusals)
		}
		// The typed reason round-trips through the kind row's own decoder,
		// which is what makes the detail a token rather than prose.
		reason, err := taskstate.ParseIsolationRefusal(refusals[0].Detail)
		if err != nil || reason != want {
			t.Fatalf("the refusal of %s carries %q (%v), want %s", id, refusals[0].Detail, err, want)
		}
		task, err := records.Task(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if task.StartSnapshot.IsKnown() {
			t.Fatalf("a refused dispatch of %s recorded a start snapshot %+v", id, task.StartSnapshot)
		}
	}
}

// The second check the dispatch wrote into the instructions is a real shell
// line: run with real git it fails in the primary checkout, fails in a linked
// worktree of the primary, and passes in the copy.
func TestTheSecondCheckInTheInstructionsDiscriminatesTheCopy(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("the second check is a POSIX shell line and this platform has no sh")
	}
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")
	withService(t, h, func(serviceUnderTest) {})

	instructions, _ := h.TaskInstructions(id)
	text, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, candidate := range strings.Split(string(text), "\n") {
		if strings.HasPrefix(candidate, "    test ") {
			line = strings.TrimSpace(candidate)
		}
	}
	if line == "" {
		t.Fatalf("no second check in the instructions:\n%s", text)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, subject, "worktree", "add", "--quiet", "--detach", linked)
	copyPath, _ := h.TaskCopy(id)
	run := func(dir string) error {
		cmd := exec.Command("sh", "-c", line)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_SYSTEM="+filepath.Join(dir, ".gitconfig-absent"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return errors.New(strings.TrimSpace(string(out)) + ": " + err.Error())
		}
		return nil
	}
	if err := run(subject); err == nil {
		t.Error("the second check passes in the primary checkout")
	}
	if err := run(linked); err == nil {
		t.Error("the second check passes in a linked worktree of the primary")
	}
	if err := run(copyPath); err != nil {
		t.Errorf("the second check fails in the copy: %v", err)
	}
}
