//go:build unix

package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// The run a task's worker starts by pushing its own branch through the real
// gate hooks takes its intent from the task, and a person's push of the same
// branch does not.
//
// The worker is the stand-in in worker mode, launched by the service for a
// dispatched task, and the push is git run inside its process group, so the
// worker relation is the one the service derives from the kernel's
// credentials. The run walks the real intent, rebase, review and pull
// request stages, the review over the scripted reviewer and the pull request
// over the scripted provider; the other five are stages that report and
// pass, so the walk reaches the pull request without a person answering
// anything.
//
// Asserted: the run's record carries the project as its first line and the
// instructions intake wrote as its body, with the isolation check dispatch
// appended left out and a credential in the instructions redacted, under the
// source "task" and never "supplied"; the pull request the stage opened is
// titled with the project; its body frames the intent as a hint, carries the
// scope lens's own note about the path the reviewer did not trace, and not
// the note the lens leaves when it did not run. Then a person's push of the
// same branch, from outside every worker group, starts a run with no intent,
// recorded as absent, and the body its run gives the pull request says so
// and carries the note the lens leaves when it did not run (P1: a person's
// push is consent to validation, and nothing about it is read off a task).
func TestAWorkersPushCarriesItsTasksIntentAndAPersonsDoesNot(t *testing.T) {
	principles.Cite(t, principles.P1)
	requiresIdentifiedPeer(t)

	const (
		upstream = "https://github.com/dayamjz/assistant.git"
		branch   = "task/intent"
		project  = "Cap the weight of every page's images"
		number   = 7
	)
	instructions := "Keep each page's images under 200 KB.\n" +
		"The fixtures mirror from https://worker:s3cret@example.invalid/images.git.\n"

	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	local := git(t, subject, "remote", "get-url", "origin")
	recordRepositoryWithUpstream(t, h, subject, upstream)
	gateRepo := gateRepositoryOf(t, h, subject)
	quietMaintenance(t, gateRepo)
	git(t, gateRepo, "config", "url."+local+".insteadOf", upstream)
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
		project, instructions)
	// The control for TestAPromotedTasksWorkerPushCarriesItsDeliveryIntent:
	// a task never promoted records no delivery intent, so its run takes its
	// body from the instructions intake wrote.
	if recorded, err := records.Task(t.Context(), task); err != nil || recorded.DeliveryIntent.IsKnown() {
		t.Fatalf("the task intake recorded reads %+v (%v), want no delivery intent", recorded, err)
	}

	provider := forgetest.New(t, forgetest.Script{
		forgetest.OpRepository: {forgetest.Repository("dayamjz/assistant")},
		forgetest.OpList:       {forgetest.Raw("[]"), forgetest.Raw(`[{"number":7}]`)},
		forgetest.OpCreate:     {forgetest.Raw("https://github.com/dayamjz/assistant/pull/7\n")},
		forgetest.OpView: {forgetest.PullRequest(forgetest.PullRequestView{
			Number: number, State: "OPEN", Mergeable: "MERGEABLE", Head: branch, Base: "main",
		})},
		forgetest.OpEdit: {forgetest.Raw("")},
	})
	o, _ := workerOptions(t, h)
	o.Forge = provider.Host()
	o.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := stages.All(deps)
		all.Test = passingStage(pipeline.StageTest)
		all.Document = passingStage(pipeline.StageDocument)
		all.Lint = passingStage(pipeline.StageLint)
		all.Push = passingStage(pipeline.StagePush)
		all.CI = passingStage(pipeline.StageCI)
		return all
	}

	withServiceOptions(t, o, func(running serviceUnderTest) {
		sessions, launched := launchedSession(t, running.service, task)
		waitCapture(t, sessions, launched, "worker ready")
		group := launched.Reference().PID
		copyPath := resolvedCopy(t, h, task)
		quietMaintenance(t, copyPath)
		if err := os.WriteFile(filepath.Join(copyPath, "images.txt"), []byte("capped\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGitAs(t, group, copyPath, "add", "-A")
		mustGitAs(t, group, copyPath, "commit", "--quiet", "-m", "cap the images")
		head := mustGitAs(t, group, copyPath, "rev-parse", "HEAD")
		if out, err := gitAs(t, group, copyPath, "push", gate.RemoteName, branch); err != nil {
			t.Fatalf("the worker's push of its own branch was refused:\n%s", out)
		}

		worker := runSubmitted(t, records, repository.ID, head)
		wantIntent := project + "\n\nKeep each page's images under 200 KB.\n" +
			"The fixtures mirror from https://" + redact.Marker + "@example.invalid/images.git."
		if worker.Intent != wantIntent {
			t.Fatalf("the worker's run records the intent\n%q\nwant\n%q", worker.Intent, wantIntent)
		}
		if worker.IntentSource != "task" {
			t.Fatalf("the worker's run records the intent source %q, want task", worker.IntentSource)
		}

		created := waitForgeCall(t, provider, forgetest.OpCreate)
		if title := flagValue(created.Args, "--title="); title != project {
			t.Fatalf("the pull request was opened titled %q, want the task's project %q", title, project)
		}
		body := created.Stdin
		for _, want := range []string{
			"offered as a hint about the change rather than as acceptance criteria",
			project,
			"images.txt and the review traced none of it to the recorded intent",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("the pull request body does not carry %q:\n%s", want, body)
			}
		}
		for _, absent := range []string{"was supplied", "The scope lens did not run", "s3cret", "written by the program"} {
			if strings.Contains(body, absent) {
				t.Fatalf("the pull request body carries %q:\n%s", absent, body)
			}
		}

		// A person's push of the same branch, from this test's own process
		// and so from outside every worker group, of a commit of their own.
		// Their commit is the subject's work branch, which changes work.txt,
		// so the run has a change to review rather than ending at the
		// rebase stage's empty-diff short circuit.
		primary := repository.WorkingPath
		own := git(t, primary, "rev-parse", "refs/heads/work")
		if out, err := tryGitIn(t, primary, "push", "--force", gate.RemoteName, "work:refs/heads/"+branch); err != nil {
			t.Fatalf("the person's push was refused:\n%s", out)
		}
		person := runSubmitted(t, records, repository.ID, own)
		if person.Intent != "" || person.IntentSource != "absent" {
			t.Fatalf("the person's run records the intent %q from %q, want none, recorded as absent",
				person.Intent, person.IntentSource)
		}
		// The pull request already exists, so the person's run replaces its
		// body. That body is the control for the worker's: the lens did not
		// run, which is the note the worker's body was checked not to carry.
		edited := waitForgeCall(t, provider, forgetest.OpEdit)
		for _, want := range []string{"No intent was recorded for this run", "The scope lens did not run"} {
			if !strings.Contains(edited.Stdin, want) {
				t.Fatalf("the person's pull request body does not carry %q:\n%s", want, edited.Stdin)
			}
		}
	})
}

// A promoted task's worker push starts a run whose intent is the task's
// project and the delivery intent its promotion recorded, under the source
// "task", and not the investigation instructions intake wrote: the
// investigation is launched by the service, promoted through task.promote
// from outside every worker group with an intent carrying a credential, and
// its worker pushes the promoted branch from inside its process group
// through the real gate hooks. The run's record carries the project and the
// delivery intent with the credential redacted, and neither the intake text
// nor anything of the section promotion appended to the instructions file.
// TestAWorkersPushCarriesItsTasksIntentAndAPersonsDoesNot is the control: a
// task never promoted carries its intake instructions as before. Then a
// person's push of the promoted branch, from outside every worker group,
// starts a run with no intent, recorded as absent (P1: a person's push is
// consent to validation, and nothing about it is read off a task).
func TestAPromotedTasksWorkerPushCarriesItsDeliveryIntent(t *testing.T) {
	principles.Cite(t, principles.P1)
	const (
		branch   = "task/promoted"
		delivery = "Cap the images at 200 KB, mirroring from https://worker:s3cret@example.invalid/images.git."
	)
	launchedWorkerOf(t, store.TaskInvestigation, "", func(w launched) {
		var promoted machine.TaskPromoted
		if err := w.running.client.Call(t.Context(), ipc.MethodTaskPromote, machine.TaskPromoteRequest{
			Task: w.task, Repository: w.repository.ID, Branch: branch, Intent: "\n" + delivery + "\n",
		}, &promoted); err != nil {
			t.Fatalf("the promotion: %v", err)
		}
		records := openRecords(t, w.home)
		defer func() { _ = records.Close() }()
		recorded, err := records.Task(t.Context(), w.task)
		if err != nil || recorded.Shape != store.TaskDelivery || recorded.DeliveryIntent != store.Known(delivery) {
			t.Fatalf("the promoted task reads %+v (%v), want a delivery task recording the intent it was promoted with",
				recorded, err)
		}
		// The instructions file holds both texts, so the run's intent below
		// is decided by which one the service read and not by what the file
		// happened to hold.
		path, err := w.home.TaskInstructions(w.task)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Do the work the coordinator asked for.", delivery} {
			if !strings.Contains(string(file), want) {
				t.Fatalf("the instructions file does not carry %q:\n%s", want, file)
			}
		}

		head := workerPushes(t, w, branch)
		worker := runSubmitted(t, records, w.repository.ID, head)
		wantIntent := recorded.Project + "\n\nCap the images at 200 KB, mirroring from https://" + redact.Marker +
			"@example.invalid/images.git."
		if worker.Intent != wantIntent {
			t.Fatalf("the promoted worker's run records the intent\n%q\nwant\n%q", worker.Intent, wantIntent)
		}
		if worker.IntentSource != "task" {
			t.Fatalf("the promoted worker's run records the intent source %q, want task", worker.IntentSource)
		}
		for _, absent := range []string{"Do the work the coordinator asked for", "written by the program", "s3cret"} {
			if strings.Contains(worker.Intent, absent) {
				t.Fatalf("the promoted worker's run records an intent carrying %q:\n%s", absent, worker.Intent)
			}
		}

		primary := w.repository.WorkingPath
		own := git(t, primary, "rev-parse", "refs/heads/work")
		if out, err := tryGitIn(t, primary, "push", "--force", gate.RemoteName, "work:refs/heads/"+branch); err != nil {
			t.Fatalf("the person's push was refused:\n%s", out)
		}
		person := runSubmitted(t, records, w.repository.ID, own)
		if person.Intent != "" || person.IntentSource != "absent" {
			t.Fatalf("the person's run records the intent %q from %q, want none, recorded as absent",
				person.Intent, person.IntentSource)
		}
	})
}

// runSubmitted is the repository's one run recorded for the commit head,
// read from the record. A push records its run before the notification
// answers, so it is there once the push returns.
func runSubmitted(t *testing.T, records *store.Store, repository, head string) store.Run {
	t.Helper()
	runs, err := records.RunsForRepository(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	var found []store.Run
	for _, r := range runs {
		if r.SubmittedHead == head {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d runs were recorded for %s, want one: %+v", len(found), head, runs)
	}
	return found[0]
}

// waitForgeCall polls the scripted provider's record until it holds a call
// of op, and returns the first.
func waitForgeCall(t *testing.T, provider *forgetest.Provider, op forgetest.Op) forgetest.Call {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		for _, c := range provider.Calls() {
			if c.Op == op {
				return c
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the provider was never asked for %s; it was asked for %+v", op, provider.Calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// flagValue is the value of the first argument carrying prefix.
func flagValue(args []string, prefix string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, prefix); ok {
			return v
		}
	}
	return ""
}
