package forge_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/forge/labtest"
	"github.com/dayamjz/assistant/internal/principles"
)

const conformanceHead = "6f1a3c2b0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a"

// A providerCase is one provider behind its stand-in. The scripts differ
// because the wires differ; the assertions below do not.
type providerCase struct {
	name   string
	checks func(t *testing.T, kind checkKind) forge.Provider
	merge  func(t *testing.T) (forge.Provider, func(head string) (called, bound bool))
	open   func(t *testing.T, reported string) (forge.Provider, func() int)
}

type checkKind int

const (
	checkEmpty checkKind = iota
	checkUnknown
	checkCancelled
	checkPassed
)

func providerCases() []providerCase {
	return []providerCase{githubProviderCase(), gitlabProviderCase()}
}

func githubProviderCase() providerCase {
	open := func(t *testing.T, script forgetest.Script) (*forgetest.Provider, forge.Provider) {
		t.Helper()
		p := forgetest.New(t, script)
		adapter, err := p.Host().Open("owner/name")
		if err != nil {
			t.Fatalf("opening the GitHub adapter: %v", err)
		}
		return p, adapter
	}
	return providerCase{
		name: "github",
		checks: func(t *testing.T, kind checkKind) forge.Provider {
			t.Helper()
			var runs []forgetest.CheckRunView
			switch kind {
			case checkUnknown:
				runs = []forgetest.CheckRunView{{Name: "ci", Status: "WEIRD"}}
			case checkCancelled:
				runs = []forgetest.CheckRunView{{Name: "ci", Status: "COMPLETED", Conclusion: "CANCELLED"}}
			case checkPassed:
				runs = []forgetest.CheckRunView{{Name: "ci", Status: "COMPLETED", Conclusion: "SUCCESS"}}
			}
			_, adapter := open(t, forgetest.Script{
				forgetest.OpChecks: {forgetest.Checks(conformanceHead, runs...)},
			})
			return adapter
		},
		merge: func(t *testing.T) (forge.Provider, func(head string) (bool, bool)) {
			t.Helper()
			p, adapter := open(t, forgetest.Script{
				forgetest.OpRepository:   {forgetest.Repository("owner/name")},
				forgetest.OpMergeMethods: {forgetest.MergeMethods(true, false, false)},
				forgetest.OpMerge:        {forgetest.Merged(12)},
				forgetest.OpView: {forgetest.PullRequest(forgetest.PullRequestView{
					Number: 12, State: "MERGED", Mergeable: "MERGEABLE", Head: "fm/work",
					HeadCommit: conformanceHead, Base: "main", Title: "feat: a change",
					URL: "https://github.com/owner/name/pull/12",
				})},
			})
			return adapter, func(head string) (bool, bool) {
				called, bound := false, false
				for _, c := range p.Calls() {
					if c.Op != forgetest.OpMerge {
						continue
					}
					called = true
					for _, arg := range c.Args {
						if arg == "--match-head-commit="+head {
							bound = true
						}
					}
				}
				return called, bound
			}
		},
		open: func(t *testing.T, reported string) (forge.Provider, func() int) {
			t.Helper()
			p, adapter := open(t, forgetest.Script{
				forgetest.OpRepository: {forgetest.Repository(reported)},
				forgetest.OpCreate:     {forgetest.Raw("https://github.com/owner/name/pull/12\n")},
				forgetest.OpList:       {forgetest.Raw("[{\"number\":12}]\n")},
				forgetest.OpView: {forgetest.PullRequest(forgetest.PullRequestView{
					Number: 12, State: "OPEN", Mergeable: "MERGEABLE", Head: "fm/work",
					HeadCommit: conformanceHead, Base: "main", Title: "feat: a change",
					URL: "https://github.com/owner/name/pull/12",
				})},
			})
			return adapter, func() int {
				n := 0
				for _, c := range p.Calls() {
					if c.Op == forgetest.OpCreate {
						n++
					}
				}
				return n
			}
		},
	}
}

func gitlabProviderCase() providerCase {
	open := func(t *testing.T, script labtest.Script) (*labtest.Provider, forge.Provider) {
		t.Helper()
		p := labtest.New(t, script)
		adapter, err := p.Host().Open("group/name")
		if err != nil {
			t.Fatalf("opening the GitLab adapter: %v", err)
		}
		return p, adapter
	}
	view := labtest.MergeRequest(labtest.MergeRequestView{
		IID: 12, State: "opened", MergeStatus: "can_be_merged", SourceBranch: "fm/work",
		TargetBranch: "main", SHA: conformanceHead, Title: "feat: a change",
	})
	merged := labtest.MergeRequest(labtest.MergeRequestView{
		IID: 12, State: "merged", MergeStatus: "can_be_merged", SourceBranch: "fm/work",
		TargetBranch: "main", SHA: conformanceHead, Title: "feat: a change",
	})
	return providerCase{
		name: "gitlab",
		checks: func(t *testing.T, kind checkKind) forge.Provider {
			t.Helper()
			var pipes []labtest.PipelineView
			switch kind {
			case checkUnknown:
				pipes = []labtest.PipelineView{{ID: 7, SHA: conformanceHead, Ref: "fm/work", Status: "exploded"}}
			case checkCancelled:
				pipes = []labtest.PipelineView{{ID: 7, SHA: conformanceHead, Ref: "fm/work", Status: "canceled"}}
			case checkPassed:
				pipes = []labtest.PipelineView{{ID: 7, SHA: conformanceHead, Ref: "fm/work", Status: "success"}}
			}
			_, adapter := open(t, labtest.Script{
				labtest.OpView:      {view},
				labtest.OpPipelines: {labtest.Pipelines(pipes...)},
				labtest.OpStatuses:  {labtest.Statuses()},
			})
			return adapter
		},
		merge: func(t *testing.T) (forge.Provider, func(head string) (bool, bool)) {
			t.Helper()
			p, adapter := open(t, labtest.Script{
				labtest.OpProject: {labtest.Project(3, "group/name")},
				labtest.OpMerge:   {merged},
				labtest.OpView:    {merged},
			})
			return adapter, func(head string) (bool, bool) {
				called, bound := false, false
				for _, c := range p.Calls() {
					if c.Op != labtest.OpMerge {
						continue
					}
					called = true
					if strings.Contains(c.RawQuery, "sha="+head) {
						bound = true
					}
				}
				return called, bound
			}
		},
		open: func(t *testing.T, reported string) (forge.Provider, func() int) {
			t.Helper()
			p, adapter := open(t, labtest.Script{
				labtest.OpProject: {labtest.Project(3, reported)},
				labtest.OpCreate:  {view},
				labtest.OpList: {labtest.MergeRequests(labtest.MergeRequestView{
					IID: 12, State: "opened", MergeStatus: "can_be_merged", SourceBranch: "fm/work",
					TargetBranch: "main", SHA: conformanceHead, Title: "feat: a change", SourceProjectID: 3,
				})},
				labtest.OpView: {view},
			})
			return adapter, func() int {
				n := 0
				for _, c := range p.Calls() {
					if c.Op == labtest.OpCreate {
						n++
					}
				}
				return n
			}
		},
	}
}

func TestConformanceEmptyChecksAreNotGreen(t *testing.T) {
	for _, h := range providerCases() {
		t.Run(h.name, func(t *testing.T) {
			report, err := h.checks(t, checkEmpty).Checks(context.Background(), 12)
			if err != nil {
				t.Fatalf("Checks: %v", err)
			}
			if report.HeadCommit != conformanceHead {
				t.Fatalf("HeadCommit = %q", report.HeadCommit)
			}
			if len(report.Runs) != 0 {
				t.Fatalf("empty check list has %d runs", len(report.Runs))
			}
			result := report.Evaluate(forge.DeclaredNoCI(config.Defaults()))
			if result.Green() || result.Verdict != forge.VerdictNoChecks {
				t.Fatalf("empty checks evaluated as %s", result.Verdict)
			}
		})
	}
}

func TestConformanceUnknownCheckStateWaits(t *testing.T) {
	for _, h := range providerCases() {
		t.Run(h.name, func(t *testing.T) {
			report, err := h.checks(t, checkUnknown).Checks(context.Background(), 12)
			if err != nil {
				t.Fatalf("Checks: %v", err)
			}
			if len(report.Runs) != 1 {
				t.Fatalf("got %d checks, want 1", len(report.Runs))
			}
			if report.Runs[0].State != forge.CheckStateUnrecognized || report.Runs[0].State.Settled() {
				t.Fatalf("unknown state = %s settled=%v, want unrecognized and waiting", report.Runs[0].State, report.Runs[0].State.Settled())
			}
			result := report.Evaluate(forge.DeclaredNoCI(config.Defaults()))
			if result.Verdict != forge.VerdictRunning || result.Green() {
				t.Fatalf("unknown check evaluated as %s", result.Verdict)
			}
		})
	}
}

func TestConformanceCancelledCheckIsSettledAndNotGreen(t *testing.T) {
	for _, h := range providerCases() {
		t.Run(h.name, func(t *testing.T) {
			report, err := h.checks(t, checkCancelled).Checks(context.Background(), 12)
			if err != nil {
				t.Fatalf("Checks: %v", err)
			}
			if len(report.Runs) != 1 {
				t.Fatalf("got %d checks, want 1", len(report.Runs))
			}
			state := report.Runs[0].State
			if state != forge.CheckStateCancelled || !state.Settled() || state.Passes() {
				t.Fatalf("cancelled state = %s settled=%v passes=%v", state, state.Settled(), state.Passes())
			}
			result := report.Evaluate(forge.DeclaredNoCI(config.Defaults()))
			if result.Verdict != forge.VerdictFailed || result.Green() {
				t.Fatalf("cancelled check evaluated as %s", result.Verdict)
			}
		})
	}
}

func TestConformanceAPassingCheckIsGreen(t *testing.T) {
	for _, h := range providerCases() {
		t.Run(h.name, func(t *testing.T) {
			report, err := h.checks(t, checkPassed).Checks(context.Background(), 12)
			if err != nil {
				t.Fatalf("Checks: %v", err)
			}
			result := report.Evaluate(forge.DeclaredNoCI(config.Defaults()))
			if !result.Green() || result.Verdict != forge.VerdictPassed {
				t.Fatalf("passing check evaluated as %s", result.Verdict)
			}
		})
	}
}

// TestConformanceMergeIsBoundToTheObservedHead is P6 at the provider: the
// merge request that goes out names the commit the caller observed, and a
// merge with no commit is refused before the provider is asked.
func TestConformanceMergeIsBoundToTheObservedHead(t *testing.T) {
	principles.Cite(t, principles.P6)
	for _, h := range providerCases() {
		t.Run(h.name, func(t *testing.T) {
			adapter, probe := h.merge(t)
			pr, err := adapter.Merge(context.Background(), 12, conformanceHead)
			if err != nil {
				t.Fatalf("Merge: %v", err)
			}
			if pr.State != forge.PullRequestStateMerged {
				t.Fatalf("merged pull request state = %s", pr.State)
			}
			called, bound := probe(conformanceHead)
			if !called || !bound {
				t.Fatalf("merge called=%v bound=%v, want the observed head on the request", called, bound)
			}

			unbound, unboundProbe := h.merge(t)
			_, err = unbound.Merge(context.Background(), 12, "")
			if !errors.Is(err, forge.ErrInvalidArgument) {
				t.Fatalf("unbound Merge error = %v, want an argument refusal", err)
			}
			called, _ = unboundProbe(conformanceHead)
			if called {
				t.Fatal("an unbound merge was sent to the provider")
			}
		})
	}
}

func TestConformanceAWriteConfirmsTheRepositoryFirst(t *testing.T) {
	spec := forge.OpenSpec{Head: "fm/work", Base: "main", Title: "feat: a change", Body: "the body"}
	for _, h := range providerCases() {
		t.Run(h.name, func(t *testing.T) {
			reported := "other/name"
			if h.name == "gitlab" {
				reported = "other/name"
			}
			adapter, creates := h.open(t, reported)
			_, err := adapter.Open(context.Background(), spec)
			var refusal *forge.Refusal
			if !errors.As(err, &refusal) || refusal.Reason != forge.ReasonWrongRepository {
				t.Fatalf("Open error = %v, want ReasonWrongRepository", err)
			}
			if creates() != 0 {
				t.Fatal("a write was sent after the provider reported a different repository")
			}

			matched := "owner/name"
			if h.name == "gitlab" {
				matched = "group/name"
			}
			adapter, creates = h.open(t, matched)
			if _, err := adapter.Open(context.Background(), spec); err != nil {
				t.Fatalf("Open against the confirmed repository: %v", err)
			}
			if creates() != 1 {
				t.Fatalf("create calls = %d, want 1", creates())
			}
		})
	}
}
