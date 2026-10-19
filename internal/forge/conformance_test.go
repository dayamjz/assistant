package forge_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/forge/labtest"
	"github.com/dayamjz/assistant/internal/principles"
)

// The conformance suite holds every adapter to the same assertions about the
// facts a caller acts on: an empty check list is not green, a state the
// adapter does not recognize keeps the caller waiting, a cancelled check is
// settled and not a pass, a merge goes out bound to the head the caller
// observed or not at all, a merge the host refuses is a rejection, and a write
// is preceded by a confirmation that stops it when the host reports another
// repository. Each adapter reaches it through its own wire-level stand-in -
// forgetest for gh, labtest for GitLab's API - scripted with the shapes that
// tool or that API produces, so the scripts differ and the assertions do not.

const conformanceHead = "6f1a3c2b0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a"

// checkCase is one of the check lists the suite reads.
type checkCase int

const (
	checksEmpty checkCase = iota
	checksUnrecognized
	checksCancelled
	checksPassed
)

// adapterCase is one adapter behind its stand-in.
type adapterCase struct {
	name string
	// checks opens an adapter whose provider reports c on conformanceHead.
	checks func(t *testing.T, c checkCase) forge.Provider
	// merge opens an adapter whose provider accepts a merge when accept is
	// set and refuses it otherwise, and returns the heads of the merges it
	// was asked for, with "" for one bound to nothing.
	merge func(t *testing.T, accept bool) (forge.Provider, func() []string)
	// open opens an adapter whose provider reports its repository as
	// reported, and returns how many creates it was asked for.
	open func(t *testing.T, reported string) (forge.Provider, func() int)
	// addressed is the specifier the adapter is opened on, and elsewhere is
	// what the host reports for a different repository.
	addressed, elsewhere string
}

func adapterCases() []adapterCase {
	return []adapterCase{githubCase(), gitlabCase()}
}

func githubCase() adapterCase {
	open := func(t *testing.T, script forgetest.Script) (*forgetest.Provider, forge.Provider) {
		t.Helper()
		p := forgetest.New(t, script)
		adapter, err := p.Host().Open("owner/name")
		if err != nil {
			t.Fatalf("opening the GitHub adapter: %v", err)
		}
		return p, adapter
	}
	view := func(state string) forgetest.Response {
		return forgetest.PullRequest(forgetest.PullRequestView{
			Number: 12, State: state, Mergeable: "MERGEABLE", Head: "fm/work",
			HeadCommit: conformanceHead, Base: "main", Title: "feat: a change",
			URL: "https://github.com/owner/name/pull/12",
		})
	}
	return adapterCase{
		name:      "github",
		addressed: "owner/name",
		elsewhere: "other/name",
		checks: func(t *testing.T, c checkCase) forge.Provider {
			var runs []forgetest.CheckRunView
			switch c {
			case checksUnrecognized:
				runs = []forgetest.CheckRunView{{Name: "ci", Status: "SOMETHING_NEW"}}
			case checksCancelled:
				runs = []forgetest.CheckRunView{{Name: "ci", Status: "COMPLETED", Conclusion: "CANCELLED"}}
			case checksPassed:
				runs = []forgetest.CheckRunView{{Name: "ci", Status: "COMPLETED", Conclusion: "SUCCESS"}}
			}
			_, adapter := open(t, forgetest.Script{forgetest.OpChecks: {forgetest.Checks(conformanceHead, runs...)}})
			return adapter
		},
		merge: func(t *testing.T, accept bool) (forge.Provider, func() []string) {
			answer := forgetest.Merged(12)
			if !accept {
				answer = forgetest.Exit(1, "GraphQL: Pull request is not mergeable (mergePullRequest)")
			}
			p, adapter := open(t, forgetest.Script{
				forgetest.OpRepository:   {forgetest.Repository("owner/name")},
				forgetest.OpMergeMethods: {forgetest.MergeMethods(true, false, false)},
				forgetest.OpMerge:        {answer},
				forgetest.OpView:         {view("MERGED")},
			})
			return adapter, func() []string {
				var heads []string
				for _, c := range p.Calls() {
					if c.Op != forgetest.OpMerge {
						continue
					}
					head := ""
					for _, arg := range c.Args {
						if v, ok := strings.CutPrefix(arg, "--match-head-commit="); ok {
							head = v
						}
					}
					heads = append(heads, head)
				}
				return heads
			}
		},
		open: func(t *testing.T, reported string) (forge.Provider, func() int) {
			p, adapter := open(t, forgetest.Script{
				forgetest.OpRepository: {forgetest.Repository(reported)},
				forgetest.OpCreate:     {forgetest.Raw("https://github.com/owner/name/pull/12\n")},
				forgetest.OpList:       {forgetest.Raw("[{\"number\":12}]\n")},
				forgetest.OpView:       {view("OPEN")},
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

func gitlabCase() adapterCase {
	open := func(t *testing.T, script labtest.Script) (*labtest.Provider, forge.Provider) {
		t.Helper()
		p := labtest.New(t, script)
		adapter, err := p.Host().Open("gitlab.com/group/name")
		if err != nil {
			t.Fatalf("opening the GitLab adapter: %v", err)
		}
		return p, adapter
	}
	view := func(state string) labtest.MergeRequestView {
		return labtest.MergeRequestView{
			IID: 12, State: state, MergeStatus: "can_be_merged", SourceBranch: "fm/work",
			TargetBranch: "main", SHA: conformanceHead, Title: "feat: a change",
		}
	}
	return adapterCase{
		name:      "gitlab",
		addressed: "group/name",
		elsewhere: "other/name",
		checks: func(t *testing.T, c checkCase) forge.Provider {
			var pipelines []labtest.PipelineView
			switch c {
			case checksUnrecognized:
				pipelines = []labtest.PipelineView{{ID: 7, SHA: conformanceHead, Ref: "fm/work", Status: "something_new"}}
			case checksCancelled:
				pipelines = []labtest.PipelineView{{ID: 7, SHA: conformanceHead, Ref: "fm/work", Status: "canceled"}}
			case checksPassed:
				pipelines = []labtest.PipelineView{{ID: 7, SHA: conformanceHead, Ref: "fm/work", Status: "success"}}
			}
			_, adapter := open(t, labtest.Script{
				labtest.OpView:      {labtest.MergeRequest(view("opened"))},
				labtest.OpPipelines: {labtest.Pipelines(pipelines...)},
			})
			return adapter
		},
		merge: func(t *testing.T, accept bool) (forge.Provider, func() []string) {
			answer := labtest.MergeRequest(view("merged"))
			if !accept {
				answer = labtest.Error(405, "405 Method Not Allowed")
			}
			p, adapter := open(t, labtest.Script{
				labtest.OpProject: {labtest.Project(3, "group/name")},
				labtest.OpMerge:   {answer},
			})
			return adapter, func() []string {
				var heads []string
				for _, c := range p.CallsFor(labtest.OpMerge) {
					var body struct {
						SHA string `json:"sha"`
					}
					_ = json.Unmarshal([]byte(c.Body), &body)
					heads = append(heads, body.SHA)
				}
				return heads
			}
		},
		open: func(t *testing.T, reported string) (forge.Provider, func() int) {
			p, adapter := open(t, labtest.Script{
				labtest.OpProject: {labtest.Project(3, reported)},
				labtest.OpCreate:  {labtest.Created(view("opened"))},
			})
			return adapter, func() int { return len(p.CallsFor(labtest.OpCreate)) }
		},
	}
}

// An empty check list reads as no checks registered, which is not green and
// is not turned green by anything but the no_ci declaration.
func TestConformanceAnEmptyCheckListIsNotGreen(t *testing.T) {
	for _, a := range adapterCases() {
		t.Run(a.name, func(t *testing.T) {
			report, err := a.checks(t, checksEmpty).Checks(context.Background(), 12)
			if err != nil {
				t.Fatalf("Checks: %v", err)
			}
			if report.HeadCommit != conformanceHead || len(report.Runs) != 0 {
				t.Fatalf("Checks answered %+v, want no runs on %s", report, conformanceHead)
			}
			if result := report.Evaluate(forge.DeclaredNoCI(config.Defaults())); result.Green() || result.Verdict != forge.VerdictNoChecks {
				t.Fatalf("an empty check list evaluated as %s", result)
			}
			declared := config.Defaults()
			declared.NoCI = true
			if result := report.Evaluate(forge.DeclaredNoCI(declared)); !result.Green() {
				t.Fatalf("an empty check list under the no_ci declaration evaluated as %s", result)
			}
		})
	}
}

// A state the adapter does not recognize is not settled, so the caller keeps
// waiting rather than concluding.
func TestConformanceAnUnrecognizedStateKeepsTheCallerWaiting(t *testing.T) {
	for _, a := range adapterCases() {
		t.Run(a.name, func(t *testing.T) {
			report, err := a.checks(t, checksUnrecognized).Checks(context.Background(), 12)
			if err != nil {
				t.Fatalf("Checks: %v", err)
			}
			if len(report.Runs) != 1 || report.Runs[0].State != forge.CheckStateUnrecognized || report.Runs[0].Reported == "" {
				t.Fatalf("Checks answered %+v, want one unrecognized run naming the provider's word", report.Runs)
			}
			if result := report.Evaluate(forge.NoCIDeclaration{}); result.Verdict != forge.VerdictRunning {
				t.Fatalf("an unrecognized state evaluated as %s, want running", result)
			}
		})
	}
}

// A cancelled check is settled and does not pass, so the run stops waiting
// and does not go green.
func TestConformanceACancelledCheckIsSettledAndNotAPass(t *testing.T) {
	for _, a := range adapterCases() {
		t.Run(a.name, func(t *testing.T) {
			report, err := a.checks(t, checksCancelled).Checks(context.Background(), 12)
			if err != nil {
				t.Fatalf("Checks: %v", err)
			}
			if len(report.Runs) != 1 || report.Runs[0].State != forge.CheckStateCancelled {
				t.Fatalf("Checks answered %+v, want one cancelled run", report.Runs)
			}
			if result := report.Evaluate(forge.NoCIDeclaration{}); result.Verdict != forge.VerdictFailed {
				t.Fatalf("a cancelled check evaluated as %s, want failed", result)
			}
		})
	}
}

// The positive control for the three above: a passing check is green, so the
// suite is not passing because every adapter answers "not green" to
// everything.
func TestConformanceAPassingCheckIsGreen(t *testing.T) {
	for _, a := range adapterCases() {
		t.Run(a.name, func(t *testing.T) {
			report, err := a.checks(t, checksPassed).Checks(context.Background(), 12)
			if err != nil {
				t.Fatalf("Checks: %v", err)
			}
			if result := report.Evaluate(forge.NoCIDeclaration{}); !result.Green() {
				t.Fatalf("a passing check evaluated as %s", result)
			}
		})
	}
}

// TestConformanceAMergeIsBoundToTheObservedHead is P6 at every adapter: the
// merge that goes out names the commit the caller observed, a merge with no
// commit is refused before the provider is asked anything, and a merge the
// host refuses is a rejection reported once and not asked again.
func TestConformanceAMergeIsBoundToTheObservedHead(t *testing.T) {
	principles.Cite(t, principles.P6)
	for _, a := range adapterCases() {
		t.Run(a.name, func(t *testing.T) {
			adapter, heads := a.merge(t, true)
			pr, err := adapter.Merge(context.Background(), 12, conformanceHead)
			if err != nil {
				t.Fatalf("Merge: %v", err)
			}
			if pr.State != forge.PullRequestStateMerged {
				t.Fatalf("Merge answered %s, want merged", pr.State)
			}
			if got := heads(); !slices.Equal(got, []string{conformanceHead}) {
				t.Fatalf("the merges asked for were bound to %q, want exactly one bound to %s", got, conformanceHead)
			}

			unbound, heads := a.merge(t, true)
			if _, err := unbound.Merge(context.Background(), 12, ""); !errors.Is(err, forge.ErrInvalidArgument) {
				t.Fatalf("a merge bound to nothing answered %v, want an argument refusal", err)
			}
			if got := heads(); len(got) != 0 {
				t.Fatalf("a merge bound to nothing was sent to the provider: %q", got)
			}

			refused, heads := a.merge(t, false)
			_, err = refused.Merge(context.Background(), 12, conformanceHead)
			var refusal *forge.Refusal
			if !errors.As(err, &refusal) || refusal.Reason != forge.ReasonRejected {
				t.Fatalf("a merge the host refused answered %v, want a rejection", err)
			}
			if got := heads(); len(got) != 1 {
				t.Fatalf("a refused merge was asked %d times, want once", len(got))
			}
		})
	}
}

// TestConformanceAWriteGoesWhereTheRunSaidOrNowhere is P1 at every adapter:
// a create is preceded by the confirmation of the repository it names, a
// host reporting another repository stops it before anything is created, and
// the same adapter given a host reporting the addressed repository creates
// exactly one, so the refusal is the confirmation's and not a write that
// never works.
func TestConformanceAWriteGoesWhereTheRunSaidOrNowhere(t *testing.T) {
	principles.Cite(t, principles.P1)
	spec := forge.OpenSpec{Head: "fm/work", Base: "main", Title: "feat: a change", Body: "the body"}
	for _, a := range adapterCases() {
		t.Run(a.name, func(t *testing.T) {
			adapter, creates := a.open(t, a.elsewhere)
			_, err := adapter.Open(context.Background(), spec)
			var refusal *forge.Refusal
			if !errors.As(err, &refusal) || refusal.Reason != forge.ReasonWrongRepository {
				t.Fatalf("Open answered %v, want a refusal naming the wrong repository", err)
			}
			if n := creates(); n != 0 {
				t.Fatalf("the provider was asked to create %d times after it reported another repository", n)
			}

			adapter, creates = a.open(t, a.addressed)
			pr, err := adapter.Open(context.Background(), spec)
			if err != nil {
				t.Fatalf("Open on the confirmed repository: %v", err)
			}
			if n := creates(); n != 1 || pr.Number != 12 || pr.Head != "fm/work" || pr.Base != "main" {
				t.Fatalf("Open created %d and answered %+v, want one pull request 12 from fm/work into main", n, pr)
			}
		})
	}
}
