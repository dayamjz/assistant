package forge_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/forge/labtest"
)

func TestGitLabRepositoryReadsOnlyGitLab(t *testing.T) {
	cases := []struct {
		remote string
		want   string
	}{
		{"https://gitlab.com/dayamjz/assistant.git", "dayamjz/assistant"},
		{"git@gitlab.com:group/sub/project.git", "group/sub/project"},
		{"https://gitlab.com/dayamjz/assistant/-/blob/main/README.md", ""},
		{"https://github.com/dayamjz/assistant.git", ""},
		{"https://gitlab.example.com/dayamjz/assistant.git", ""},
		{"https://gitlab.com/dayamjz", ""},
	}
	for _, c := range cases {
		got, ok := forge.GitLabRepository(c.remote)
		if c.want == "" {
			if ok {
				t.Errorf("GitLabRepository(%q) = %q, want no specifier", c.remote, got)
			}
			continue
		}
		if got != c.want || !ok {
			t.Errorf("GitLabRepository(%q) = %q, %v; want %q", c.remote, got, ok, c.want)
		}
	}
	spec, kind, ok := forge.HostedRepository("https://gitlab.com/dayamjz/assistant.git")
	if !ok || kind != forge.RemoteGitLab || spec != "dayamjz/assistant" {
		t.Fatalf("HostedRepository = %q, %v, %v", spec, kind, ok)
	}
	if _, kind, ok = forge.HostedRepository("https://github.com/dayamjz/assistant.git"); !ok || kind != forge.RemoteGitHub {
		t.Fatalf("a GitHub remote was hosted as %v, %v", kind, ok)
	}
}

func TestGitLabEndpointCannotLeaveGitLab(t *testing.T) {
	if _, err := forge.NewGitLab(testRedactor, forge.WithGitLabRepository("group/name"),
		forge.WithGitLabEndpoint("https://gitlab.example.com/api/v4")); err == nil {
		t.Fatal("an endpoint on another host was accepted")
	}
	if _, err := forge.NewGitLab(testRedactor, forge.WithGitLabRepository("group/name"),
		forge.WithGitLabEndpoint("https://user:token@gitlab.com/api/v4")); err == nil {
		t.Fatal("an endpoint carrying userinfo was accepted")
	}
}

func TestGitLabLockedStateAndUncheckedMergeStayUnknown(t *testing.T) {
	p := labtest.New(t, labtest.Script{
		labtest.OpView: {labtest.MergeRequest(labtest.MergeRequestView{
			IID: 12, State: "locked", MergeStatus: "unchecked", SourceBranch: "fm/work",
			TargetBranch: "main", SHA: conformanceHead, Title: "feat: a change",
		})},
	})
	adapter, err := p.Host().Open("group/name")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pr, err := adapter.Get(context.Background(), 12)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pr.State != forge.PullRequestStateUnknown || pr.Mergeability != forge.MergeabilityUnknown {
		t.Fatalf("state=%s mergeability=%s, want both unknown", pr.State, pr.Mergeability)
	}
}

func TestGitLabConflictIsSeparateFromChecks(t *testing.T) {
	p := labtest.New(t, labtest.Script{
		labtest.OpView: {labtest.MergeRequest(labtest.MergeRequestView{
			IID: 12, State: "opened", MergeStatus: "cannot_be_merged", SourceBranch: "fm/work",
			TargetBranch: "main", SHA: conformanceHead, Title: "feat: a change",
		})},
	})
	adapter, err := p.Host().Open("group/name")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pr, err := adapter.Get(context.Background(), 12)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pr.Mergeability != forge.MergeabilityConflicted || pr.State != forge.PullRequestStateOpen {
		t.Fatalf("state=%s mergeability=%s", pr.State, pr.Mergeability)
	}
}

func TestGitLabBritishSpellingOfCancelledWaits(t *testing.T) {
	p := labtest.New(t, labtest.Script{
		labtest.OpView: {labtest.MergeRequest(labtest.MergeRequestView{
			IID: 12, State: "opened", MergeStatus: "can_be_merged", SHA: conformanceHead,
			SourceBranch: "fm/work", TargetBranch: "main",
		})},
		labtest.OpPipelines: {labtest.Pipelines(labtest.PipelineView{
			ID: 7, SHA: conformanceHead, Status: "cancelled",
		})},
		labtest.OpStatuses: {labtest.Statuses()},
	})
	adapter, err := p.Host().Open("group/name")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	report, err := adapter.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if len(report.Runs) != 1 || report.Runs[0].State != forge.CheckStateUnrecognized || report.Runs[0].State.Settled() {
		t.Fatalf("runs = %+v, want one unrecognized unsettled check", report.Runs)
	}
}

func TestGitLabRedirectIsNotFollowed(t *testing.T) {
	p := labtest.New(t, labtest.Script{
		labtest.OpView: {labtest.Redirect("https://evil.example/steal")},
	})
	adapter, err := p.Host().Open("group/name")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = adapter.Get(context.Background(), 12)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != forge.ReasonRejected {
		t.Fatalf("Get error = %v, want a rejected redirect", err)
	}
	if strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("the refusal carries the redirect target: %v", err)
	}
	if len(p.Calls()) != 1 {
		t.Fatalf("calls = %d, want the one request that was not followed", len(p.Calls()))
	}
}

func TestGitLabUnauthenticatedIsNotAnEmptyAnswer(t *testing.T) {
	p := labtest.New(t, labtest.Script{
		labtest.OpView: {labtest.Unauthorized()},
	})
	adapter, err := p.Host().Open("group/name")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = adapter.Get(context.Background(), 12)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != forge.ReasonUnauthenticated {
		t.Fatalf("Get error = %v, want unauthenticated", err)
	}
}

func TestGitLabFindFromAForkKeepsThatFork(t *testing.T) {
	p := labtest.New(t, labtest.Script{
		labtest.OpProject: {labtest.Project(4, "fork/name")},
		labtest.OpList: {labtest.MergeRequests(
			labtest.MergeRequestView{IID: 8, SourceProjectID: 9, State: "opened", SourceBranch: "fm/work", SHA: conformanceHead},
			labtest.MergeRequestView{IID: 12, SourceProjectID: 4, State: "opened", SourceBranch: "fm/work", SHA: conformanceHead},
		)},
		labtest.OpView: {labtest.MergeRequest(labtest.MergeRequestView{
			IID: 12, State: "opened", MergeStatus: "can_be_merged", SourceBranch: "fm/work",
			TargetBranch: "main", SHA: conformanceHead, Title: "feat: a change",
		})},
	})
	adapter, err := p.Host().OpenFork("group/name", "fork/name")
	if err != nil {
		t.Fatalf("OpenFork: %v", err)
	}
	pr, found, err := adapter.Find(context.Background(), "fm/work")
	if err != nil || !found || pr.Number != 12 {
		t.Fatalf("Find = %+v, %v, %v; want merge request 12", pr, found, err)
	}
}

func TestGitLabOpenFromAForkNamesTheUpstreamAsTarget(t *testing.T) {
	created := labtest.MergeRequest(labtest.MergeRequestView{
		IID: 12, State: "opened", MergeStatus: "unchecked", SourceBranch: "fm/work",
		TargetBranch: "main", SHA: conformanceHead, Title: "feat: a change", SourceProjectID: 4,
	})
	p := labtest.New(t, labtest.Script{
		labtest.OpProject: {
			labtest.Project(3, "group/name"),
			labtest.Project(4, "fork/name"),
		},
		labtest.OpCreate: {created},
		labtest.OpList: {labtest.MergeRequests(labtest.MergeRequestView{
			IID: 12, SourceProjectID: 4, State: "opened", SourceBranch: "fm/work", SHA: conformanceHead,
		})},
		labtest.OpView: {created},
	})
	adapter, err := p.Host().OpenFork("group/name", "fork/name")
	if err != nil {
		t.Fatalf("OpenFork: %v", err)
	}
	if _, err := adapter.Open(context.Background(), forge.OpenSpec{
		Head: "fm/work", Base: "main", Title: "feat: a change", Body: "body",
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var create labtest.Call
	for _, c := range p.Calls() {
		if c.Op == labtest.OpCreate {
			create = c
		}
	}
	if create.Op == "" {
		t.Fatal("no create was sent")
	}
	if !strings.Contains(create.Path, "fork%2Fname") {
		t.Fatalf("create path = %s, want the fork project", create.Path)
	}
	if !strings.Contains(create.Body, `"target_project_id":3`) {
		t.Fatalf("create body = %s, want the upstream project id", create.Body)
	}
}
