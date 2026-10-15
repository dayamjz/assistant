//go:build unix

package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
)

// A person's push through the real gate hooks opens its pull request titled
// with the subject of the commit it submitted, and a push of a commit whose
// message is empty opens one titled with the branch (#217).
//
// The pushes come from this test's own process, so neither is a worker's and
// neither run carries an intent: the subject is the only thing that can title
// the first pull request, and the branch is what is left for the second. The
// run walks every stage as one that reports and passes except the pull
// request stage, which is the real body over the scripted provider, so the
// title asserted is the argument the real adapter put on the provider's
// command line.
//
// The subject carries a credential in a URL, and the run's recorded state is
// read back to show the service removed it before the subject reached graph
// state. The adapter redacts its own arguments too, so the title alone could
// not show that: the checkpoint is what does, and it is compared exactly.
//
// TestAWorkersPushCarriesItsTasksIntentAndAPersonsDoesNot is the third case:
// a worker's commit there has a subject of its own, and its pull request is
// still titled with the task's project.
func TestAPersonsPushIsTitledWithItsCommitsSubjectOrItsBranch(t *testing.T) {
	requiresIdentifiedPeer(t)

	const (
		upstream = "https://github.com/dayamjz/assistant.git"
		titled   = "person/titled"
		blank    = "person/blank"
	)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	local := git(t, subject, "remote", "get-url", "origin")
	recordRepositoryWithUpstream(t, h, subject, upstream)
	gateRepo := gateRepositoryOf(t, h, subject)
	quietMaintenance(t, gateRepo)
	git(t, gateRepo, "config", "url."+local+".insteadOf", upstream)
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	repository, err := records.Repository(t.Context(), "subject")
	if err != nil {
		t.Fatal(err)
	}

	git(t, subject, "commit", "--quiet", "--allow-empty", "-m",
		"Mirror from https://me:s3cret@example.invalid/f.git\n\nThe body is not the title.")
	titledHead := git(t, subject, "rev-parse", "HEAD")
	git(t, subject, "commit", "--quiet", "--allow-empty", "--allow-empty-message", "-m", "")
	blankHead := git(t, subject, "rev-parse", "HEAD")
	wantSubject := "Mirror from https://" + redact.Marker + "@example.invalid/f.git"

	provider := forgetest.New(t, forgetest.Script{
		forgetest.OpRepository: {forgetest.Repository("dayamjz/assistant")},
		forgetest.OpList:       {forgetest.Raw("[]")},
		forgetest.OpCreate:     {forgetest.Raw("https://github.com/dayamjz/assistant/pull/7\n")},
	})
	o := options(t, h)
	o.Forge = provider.Host()
	o.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := pipeline.ConstantStages("nothing to report")
		all.PR = stages.All(deps).PR
		return all
	}

	withServiceOptions(t, o, func(serviceUnderTest) {
		for _, push := range []struct{ head, branch string }{{titledHead, titled}, {blankHead, blank}} {
			if out, err := tryGitIn(t, subject, "push", gate.RemoteName, push.head+":refs/heads/"+push.branch); err != nil {
				t.Fatalf("the person's push of %s was refused:\n%s", push.branch, out)
			}
		}

		// The adapter rewrites a URL's userinfo with a marker of its own, so
		// the title is the subject up to that span, and the exact subject is
		// what the checkpoint below is held to.
		if title := flagValue(createdFor(t, provider, titled).Args, "--title="); !strings.HasPrefix(title, "Mirror from https://") ||
			!strings.HasSuffix(title, "@example.invalid/f.git") || strings.Contains(title, "s3cret") {
			t.Fatalf("the pull request for %s was opened titled %q, want the commit's subject %q",
				titled, title, wantSubject)
		}
		if title := flagValue(createdFor(t, provider, blank).Args, "--title="); title != blank {
			t.Fatalf("the pull request for %s was opened titled %q, want the branch, since its commit has no subject",
				blank, title)
		}

		recorded := checkpoints.New(records)
		for _, c := range []struct{ head, want string }{{titledHead, wantSubject}, {blankHead, ""}} {
			run := runSubmitted(t, records, repository.ID, c.head)
			latest, err := recorded.Latest(t.Context(), run.ID)
			if err != nil {
				t.Fatalf("reading run %s's checkpoint: %v", run.ID, err)
			}
			value, ok := latest.State.Get(string(pipeline.KeySubmittedSubject))
			got, _ := value.Text()
			if !ok || got != c.want {
				t.Fatalf("run %s records the subject %q (held %t), want %q", run.ID, got, ok, c.want)
			}
		}
	})
}

// createdFor polls the scripted provider's record until it holds a pull
// request creation for head, and returns it.
func createdFor(t *testing.T, provider *forgetest.Provider, head string) forgetest.Call {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		for _, c := range provider.Calls() {
			if c.Op == forgetest.OpCreate && flagValue(c.Args, "--head=") == head {
				return c
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the provider was never asked to create a pull request for %s; it was asked for %+v",
				head, provider.Calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
