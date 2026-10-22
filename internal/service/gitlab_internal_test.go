package service

import (
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/forge/labtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// gitLabProject is the project the GitLab fixture's repository record names,
// and gitLabSpecifier the specifier internal/forge reads out of its upstream.
const (
	gitLabProject   = "group/name"
	gitLabSpecifier = "gitlab.com/" + gitLabProject
)

// newGitLabFixture is the merge read fixture with the service's code host
// built as the product builds it, a forge.Hosts, over two wire-level
// stand-ins: a gh that answers nothing, so any request that reaches it is a
// failure the test can count, and a GitLab API answering script. The
// repository record's upstream is on gitlab.com, so what the run carries is
// what forge.RepositoryOf reads out of it.
func newGitLabFixture(t *testing.T, script labtest.Script) (mergeReadFixture, *labtest.Provider) {
	t.Helper()
	f := newMergeReadFixture(t, forgetest.Script{})
	lab := labtest.New(t, script)
	f.s.forge = forge.Hosts{GitHub: f.host.Host(), GitLab: lab.Host()}
	var err error
	if f.s.cleaner, err = tasks.NewCleaner(tasks.CleanupDeps{Home: f.home, Store: f.records, Forge: f.s.forge, GitDeadline: config.DefaultGitDeadline}); err != nil {
		t.Fatalf("building the cleaner: %v", err)
	}
	if _, err := f.records.UpsertRepository(t.Context(), store.Repository{
		ID: "subject", WorkingPath: filepath.Join(f.home.Root(), "checkout"),
		UpstreamURL: "https://gitlab.com/" + gitLabProject + ".git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	spec, kind, ok := forge.RepositoryOf("https://gitlab.com/" + gitLabProject + ".git")
	if !ok || kind != forge.RemoteGitLab || spec != gitLabSpecifier {
		t.Fatalf("the fixture's upstream reads as %q, %s, %v", spec, kind, ok)
	}
	return f, lab
}

// A person's merge of a gitlab.com run's merge request is read on GitLab and
// recorded on the same rule a GitHub merge is (P6): the read reaches the
// GitLab stand-in through the service's forge.Hosts, finds the merge request
// merged at the head the run observed, and writes the merge record. Nothing
// reaches gh, which is the half of the routing a specifier sent to the wrong
// host would break: a GitLab path read on github.com is a different
// repository's pull request.
func TestAGitLabRunsMergeIsReadOnGitLabAndNowhereElse(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	merged := labtest.MergeRequestView{
		IID: autonomyNumber, State: "merged", MergeStatus: "can_be_merged", SourceBranch: "work",
		TargetBranch: "main", SHA: autonomyHead, Title: "feat: a change",
	}
	f, lab := newGitLabFixture(t, labtest.Script{labtest.OpView: {labtest.MergeRequest(merged)}})
	f.passRun(t, passedRunState{repository: gitLabSpecifier, pushed: autonomyHead, number: "12"})

	results := f.s.readPersonMerges(t.Context())
	if results[mergeReadRun] != mergeReadRecorded {
		t.Fatalf("the pass did %q, want the merge recorded; log:\n%s", results[mergeReadRun], serviceLog(t, f.home))
	}
	record, ok, err := f.records.MergeRecord(t.Context(), mergeReadRun)
	if err != nil || !ok || record.PullRequest != autonomyNumber || record.Head != autonomyHead || record.End != store.PullRequestMerged {
		t.Fatalf("the merge record is %+v, %v (%v); want merge request %d merged at %s", record, ok, err, autonomyNumber, autonomyHead)
	}
	views := lab.CallsFor(labtest.OpView)
	if len(views) == 0 || views[0].Project != gitLabProject {
		t.Fatalf("the merge request was read as %+v, want on %s", views, gitLabProject)
	}
	if calls := f.host.Calls(); len(calls) != 0 {
		t.Fatalf("a gitlab.com run's read reached gh %d times: %+v", len(calls), calls)
	}
}

// A GitHub entry never reaches a GitLab run, even where it is the project's
// path: an owner/name entry is a GitHub repository's specifier and a GitLab
// run's specifier names its host, so the two never match and neither code
// host is asked anything. The control is the same entry matching a GitHub
// specifier; a GitLab project listed by its own specifier is merged, which
// gitlabautonomy_test.go walks through the service.
func TestAGitHubEntryNeverReachesAGitLabRun(t *testing.T) {
	t.Parallel()
	f, lab := newGitLabFixture(t, labtest.Script{})
	f.s.cfg.AutonomyMerge = []string{gitLabProject}
	if !f.s.cfg.MergesIn(gitLabProject) {
		t.Fatal("the control: an owner/name entry does not match the same owner/name, so this test checks nothing")
	}
	record := store.Run{ID: mergeReadRun, RepositoryID: "subject"}
	facts := passedPullRequest{specifier: gitLabSpecifier, number: autonomyNumber, observed: autonomyHead}
	if err := f.s.mergeUnderAutonomy(t.Context(), f.built, record, facts); err != nil {
		t.Fatalf("a GitLab run was acted on under autonomy: %v", err)
	}
	if calls := lab.Calls(); len(calls) != 0 {
		t.Fatalf("GitLab was asked %d things for a run autonomy does not merge: %+v", len(calls), calls)
	}
	if calls := f.host.Calls(); len(calls) != 0 {
		t.Fatalf("gh was asked %d things for a run autonomy does not merge: %+v", len(calls), calls)
	}
}
