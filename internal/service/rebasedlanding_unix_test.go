//go:build unix

package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// rebasedUpstream is the upstream the rebased-landing walk records, on the
// code host the scripted provider stands in for. Every repository that reads
// it, the gate's and the task's copy, maps it to the local bare upstream.
const rebasedUpstream = "https://github.com/dayamjz/assistant.git"

// rebasedPullRequest is the number the scripted provider gives the walk's
// pull request.
const rebasedPullRequest = 11

// PRD principles P12 and P6, end to end over a landing whose commits the gate
// rewrote. A delivery task's worker commits in its copy and pushes its branch
// to the gate through the real hooks, from a process group of its own, after
// the upstream's default branch moved on, so the run's real rebase stage
// replays the worker's commit onto the newer base and the real push stage
// publishes the replayed commit, with another identifier, to the task's
// branch upstream. The pull request is then merged at that published head,
// the way a person merges one: the upstream's default branch is moved to it
// and the scripted code host reads it merged there, and a later change on
// the default branch edits the line the worker wrote, as the next landing in
// a busy repository does. The service's read for a person's merge records
// the merge and asks the task's cleanup.
//
// The copy still holds the commit the worker made, which no upstream holds
// under that identifier. Each proof is asked about it: the branch the
// upstream holds for the task reaches the replayed commit and not the
// worker's, the merged head is the replayed commit, and the default branch,
// moved on again, would change if the worker's commit were integrated into
// it. What lets the copy go is the gate's own record that its rebase of the
// worker's commit published the commit both of the first two proofs reach.
// Asserted: the copy is removed and the task reads Complete, on the
// remote-branch proof while the upstream still has the task's branch and on
// the merged-pull-request proof once a host deleted it after the merge, and
// the commits the proof examined name both the worker's commit and the one
// the gate published for it.
func TestATaskCopyWhoseLandingTheGateRebasedIsRemovedOnTheRecordedRebase(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P12)
	principles.Cite(t, principles.P6)
	requiresIdentifiedPeer(t)
	for _, tc := range []struct {
		name         string
		deleteBranch bool
		want         store.CleanupProof
		branch       string
	}{
		{"branch kept", false, store.CleanupProofRemoteBranch, "task/rebased-kept"},
		{"branch deleted after the merge", true, store.CleanupProofMergedPullRequest, "task/rebased-deleted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rebasedLanding(t, tc.branch, tc.deleteBranch, false, func(w launched, host forge.Host, worker, published string) {
				got := waitLandedCleanup(t, w, host)
				if got.Source != taskstate.SourceRecord || got.State != taskstate.Complete || !removed(t, w) {
					t.Fatalf("the task reads %+v and the copy removed is %v, want Complete with the copy gone", got, removed(t, w))
				}
				record, err := recordsOf(t, w.home).TaskCleanup(t.Context(), w.task)
				if err != nil {
					t.Fatalf("reading the cleanup record: %v", err)
				}
				if record.Proof != tc.want {
					t.Fatalf("the copy was removed on %s, want %s", record.Proof, tc.want)
				}
				for _, commit := range []string{worker, published} {
					if !strings.Contains(strings.Join(record.Examined, " "), commit) {
						t.Fatalf("the proof examined %v, which does not name %s", record.Examined, commit)
					}
				}
			})
		})
	}
}

// mergeOnlyFile is the file the merge-only walk's merge adds and neither of
// its parents holds.
const mergeOnlyFile = "important.txt"

// PRD principles P6 and P12, over the same walk with one difference: the
// worker's head is a merge whose result adds a file neither parent holds.
// The gate's rebase replays the merge's parents and not the merge, without a
// conflict, so the commit it publishes lacks the file, and that is what is
// merged. The copy is the one place the file still is. Asserted: the
// cleanup refuses, the copy stands with the file in it, and no cleanup
// record is written, because no rebase record may say the replay carried a
// head whose content the published commit does not hold.
func TestATaskCopyWhoseRebasedLandingDroppedContentIsKept(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P12)
	requiresIdentifiedPeer(t)
	rebasedLanding(t, "task/merge-only", false, true, func(w launched, _ forge.Host, worker, published string) {
		if _, err := tryGitIn(t, w.gateRepo, "cat-file", "-e", published+":"+mergeOnlyFile); err == nil {
			t.Fatalf("the published %s holds %s, so the replay dropped nothing and the walk is not exercised", published, mergeOnlyFile)
		}
		deadline := time.Now().Add(60 * time.Second)
		for unprovenLines(t, w) == 0 {
			if removed(t, w) {
				log, _ := os.ReadFile(w.home.ServiceLog())
				t.Fatalf("the copy was removed with %s in it, which the published %s does not hold\nthe service log:\n%s",
					mergeOnlyFile, published, log)
			}
			if !time.Now().Before(deadline) {
				log, _ := os.ReadFile(w.home.ServiceLog())
				t.Fatalf("the cleanup was never refused; the task reads %+v and the copy removed is %v\nthe service log:\n%s",
					listedTask(t, w.running.client, w.task), removed(t, w), log)
			}
			time.Sleep(20 * time.Millisecond)
		}
		copyPath := resolvedCopy(t, w.home, w.task)
		if content, err := os.ReadFile(filepath.Join(copyPath, mergeOnlyFile)); err != nil || string(content) != "written only in the merge\n" {
			t.Fatalf("the copy's %s reads %q (%v), want the worker's content kept", mergeOnlyFile, content, err)
		}
		if _, err := recordsOf(t, w.home).TaskCleanup(t.Context(), w.task); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a cleanup record was written for a copy holding content no upstream has: %v", err)
		}
		if got := git(t, copyPath, "rev-parse", "HEAD"); got != worker {
			t.Fatalf("the copy's head is %s, want the worker's %s", got, worker)
		}
	})
}

// rebasedLanding builds the walk TestATaskCopyWhoseLandingTheGateRebasedIsRemovedOnTheRecordedRebase
// describes up to the merge read, and hands body the worker's commit and the
// commit the gate published for it once the read has asked the cleanup.
func rebasedLanding(t *testing.T, branch string, deleteBranch, mergeOnly bool,
	body func(w launched, host forge.Host, worker, published string)) {
	t.Helper()
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	local := git(t, subject, "remote", "get-url", "origin")
	// The task's copy is a clone of the subject, so it carries the subject's
	// work branch beside the task's own; that branch is already on the
	// upstream's default branch, as land in landed_unix_test.go has it, so
	// the one commit of the copy no upstream holds by its own identifier is
	// the worker's.
	git(t, subject, "push", "--quiet", "origin", "work:main")
	recordRepositoryWithUpstream(t, h, subject, rebasedUpstream)
	gateRepo := gateRepositoryOf(t, h, subject)
	quietMaintenance(t, gateRepo)
	git(t, gateRepo, "config", "url."+local+".insteadOf", rebasedUpstream)
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	repository, err := records.Repository(t.Context(), "subject")
	if err != nil {
		t.Fatal(err)
	}
	task := recordDispatchedTaskWith(t, h, repository, store.TaskDelivery, branch, store.Unknown[config.Delivery](),
		"Say where the line comes from", "Write the line in landed.txt.\n")

	open := forgetest.PullRequest(forgetest.PullRequestView{
		Number: rebasedPullRequest, State: "OPEN", Mergeable: "MERGEABLE", Head: branch, Base: "main",
	})
	provider := forgetest.New(t, forgetest.Script{
		forgetest.OpRepository: {forgetest.Repository("dayamjz/assistant")},
		forgetest.OpList:       {forgetest.Raw("[]"), forgetest.Raw(`[{"number":11}]`)},
		forgetest.OpCreate:     {forgetest.Raw("https://github.com/dayamjz/assistant/pull/11\n")},
		forgetest.OpView:       {open},
		forgetest.OpEdit:       {forgetest.Raw("")},
	})
	o, _ := workerOptions(t, h)
	o.Forge = provider.Host()
	o.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := stages.All(deps)
		all.Test = passingStage(pipeline.StageTest)
		all.Document = passingStage(pipeline.StageDocument)
		all.Lint = passingStage(pipeline.StageLint)
		all.CI = passingStage(pipeline.StageCI)
		return all
	}

	withServiceOptions(t, o, func(running serviceUnderTest) {
		sessions, held := launchedSession(t, running.service, task)
		waitCapture(t, sessions, held, "worker ready")
		w := launched{running: running, home: h, repository: repository, gateRepo: gateRepo, task: task,
			group: held.Reference().PID, provider: sessions, session: held}
		copyPath := resolvedCopy(t, h, task)
		quietMaintenance(t, copyPath)
		// The cleanup's proofs read the upstream from the copy, by the URL the
		// repository record holds, so the copy reaches the same local bare
		// repository the gate does.
		git(t, copyPath, "config", "url."+local+".insteadOf", rebasedUpstream)

		// The default branch moves on before the worker pushes, on a path the
		// worker does not touch, so the rebase stage replays the worker's
		// commit onto it cleanly.
		advanceUpstream(t, local, "elsewhere.txt", "a change that landed first\n", "land something first")

		if err := os.WriteFile(filepath.Join(copyPath, "landed.txt"), []byte("the worker's line\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGitAs(t, w, copyPath, "add", "-A")
		mustGitAs(t, w, copyPath, "commit", "--quiet", "-m", "write the line")
		if mergeOnly {
			// The worker's head is a merge of a side branch whose result adds a
			// file neither parent holds, which a replay that linearizes the
			// history does not carry.
			mustGitAs(t, w, copyPath, "switch", "--quiet", "-c", "side", "HEAD~1")
			if err := os.WriteFile(filepath.Join(copyPath, "side.txt"), []byte("a side branch\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mustGitAs(t, w, copyPath, "add", "-A")
			mustGitAs(t, w, copyPath, "commit", "--quiet", "-m", "a side branch")
			mustGitAs(t, w, copyPath, "switch", "--quiet", branch)
			mustGitAs(t, w, copyPath, "merge", "--quiet", "--no-ff", "--no-commit", "side")
			if err := os.WriteFile(filepath.Join(copyPath, mergeOnlyFile), []byte("written only in the merge\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mustGitAs(t, w, copyPath, "add", "-A")
			mustGitAs(t, w, copyPath, "commit", "--quiet", "-m", "merge the side branch")
			mustGitAs(t, w, copyPath, "branch", "--quiet", "-D", "side")
		}
		worker := mustGitAs(t, w, copyPath, "rev-parse", "HEAD")
		if out, err := gitAs(t, w, copyPath, "push", gate.RemoteName, branch); err != nil {
			t.Fatalf("the worker's push of its own branch was refused:\n%s", out)
		}
		run := runSubmitted(t, records, repository.ID, worker)
		waitRunPassed(t, h, records, run.ID)

		// The gate published another commit for the worker's: the rebase
		// rewrote it, which is the shape this walk exists for, and nothing
		// upstream reaches the worker's own identifier.
		published := git(t, local, "rev-parse", "refs/heads/"+branch)
		if published == worker {
			t.Fatalf("the gate published %s, the worker's own commit, so no rebase rewrote it", worker)
		}
		if reaches(t, local, worker, published) {
			t.Fatalf("the published %s reaches the worker's %s, so no rebase rewrote it", published, worker)
		}

		// The merge, as a person makes it on the code host, and the next
		// landing on the default branch after it.
		git(t, local, "update-ref", "refs/heads/main", published)
		advanceUpstream(t, local, "landed.txt", "the line, as the next change rewrote it\n", "rewrite the line")
		if deleteBranch {
			git(t, local, "update-ref", "-d", "refs/heads/"+branch)
		}
		merged := forgetest.PullRequestView{Number: rebasedPullRequest, State: "MERGED", Mergeable: "UNKNOWN", Head: branch,
			HeadCommit: published, Base: "main"}
		provider.Rescript(t, forgetest.Script{
			forgetest.OpRepository: {forgetest.Repository("dayamjz/assistant")},
			forgetest.OpView:       {forgetest.PullRequest(merged)},
		})
		running.service.ReadPersonMergesForTest(t.Context())
		if _, found, err := records.MergeRecord(t.Context(), run.ID); err != nil || !found {
			t.Fatalf("the read for merges recorded no merge for run %s (%v)", run.ID, err)
		}
		body(w, provider.Host(), worker, published)
	})
}

// reaches reports whether descendant reaches ancestor in repository.
func reaches(t *testing.T, repository, ancestor, descendant string) bool {
	t.Helper()
	_, err := tryGitIn(t, repository, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

// waitRunPassed polls the run's record until it reads passed, and fails the
// test with the service log if it ends any other way or never does.
func waitRunPassed(t *testing.T, h *home.Home, records *store.Store, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * launchWait)
	for {
		run, err := records.Run(t.Context(), id)
		if err != nil {
			t.Fatalf("reading run %s: %v", id, err)
		}
		switch run.Status {
		case store.RunPassed:
			return
		case store.RunPending, store.RunRunning:
		default:
			log, _ := os.ReadFile(h.ServiceLog())
			t.Fatalf("run %s ended %s, want passed\nthe service log:\n%s", id, run.Status, log)
		}
		if !time.Now().Before(deadline) {
			log, _ := os.ReadFile(h.ServiceLog())
			t.Fatalf("run %s still reads %s, want passed\nthe service log:\n%s", id, run.Status, log)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitLandedCleanup waits for the task to read Complete, and when it never
// does, fails with the service log and with what a cleanup decision over the
// same home answers now, proof by proof, which is the account the log is to
// carry.
func waitLandedCleanup(t *testing.T, w launched, host forge.Host) taskstate.Resolution {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		got := listedTask(t, w.running.client, w.task)
		if got.State == taskstate.Complete {
			return got
		}
		if !time.Now().Before(deadline) {
			log, _ := os.ReadFile(w.home.ServiceLog())
			t.Fatalf("the task never read Complete; it reads %+v\nthe service log:\n%s\na cleanup decision asked now: %s",
				got, log, cleanupAccount(t, w, host))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// cleanupAccount asks a cleanup decision built over the walk's home what it
// answers for the task's copy with its terminal Dead, and renders the
// refusal with every proof's account.
func cleanupAccount(t *testing.T, w launched, host forge.Host) string {
	t.Helper()
	records := openRecords(t, w.home)
	defer func() { _ = records.Close() }()
	cleaner, err := tasks.NewCleaner(tasks.CleanupDeps{Home: w.home, Store: records, Forge: host})
	if err != nil {
		return err.Error()
	}
	done, err := cleaner.CleanUp(t.Context(), tasks.TaskCopy{Task: w.task, Terminal: taskstate.Dead})
	var refused *tasks.CleanupRefusal
	if errors.As(err, &refused) {
		return refused.Error()
	}
	if err != nil {
		return err.Error()
	}
	return string(done.Result) + " on " + string(done.Proof)
}
