package service_test

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/forge/labtest"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// The GitLab project the autonomy walks below run against, the specifier
// internal/forge reads out of its upstream, and the merge request number the
// pull request stage's stub records.
const (
	autonomyLabProject  = "dayamjz/assistant"
	autonomyLabUpstream = "https://gitlab.com/" + autonomyLabProject + ".git"
	autonomyLabSpec     = "gitlab.com/" + autonomyLabProject
	autonomyLabMR       = 7
	autonomyLabURL      = "https://gitlab.com/" + autonomyLabProject + "/-/merge_requests/7"
	// mergeAnswerDecoySHA is a nonempty head GitLab's merge_request entity
	// could carry on an accepted merge (its documented sha field) but that
	// must not be what the merge record stores; the read after the merge does.
	mergeAnswerDecoySHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// gitLabAutonomyWalk is a home and a subject whose recorded upstream is a
// gitlab.com project, with the service's code host built from two wire-level
// stand-ins: a gh that answers nothing, so any request reaching it is a
// failure the test counts, and the GitLab API answering script. head is the
// commit the run validates, which the merge request stands at.
type gitLabAutonomyWalk struct {
	home    *home.Home
	subject string
	head    string
	lab     *labtest.Provider
	gh      *forgetest.Provider
}

// newGitLabAutonomyWalk arranges the walk. listed is the global-only
// autonomy.merge the operator's file carries, written as JSON; empty writes
// no file. merge is what GitLab answers the merge with.
func newGitLabAutonomyWalk(t *testing.T, listed string, merge labtest.Response) gitLabAutonomyWalk {
	t.Helper()
	h := newHome(t)
	subject := newSubject(t)
	local := git(t, subject, "remote", "get-url", "origin")
	recordRepositoryWithUpstream(t, h, subject, autonomyLabUpstream)
	// The record's URL names a host nothing here reaches; the gate fetches
	// the trusted copy from the subject's own upstream, on the terms
	// observeForgeRepository states.
	git(t, gateRepositoryOf(t, h, subject), "config", "url."+local+".insteadOf", autonomyLabUpstream)
	if listed != "" {
		if err := os.WriteFile(h.ConfigFile(), []byte(`{"autonomy": {"merge": `+listed+`}}`+"\n"), 0o600); err != nil {
			t.Fatalf("writing the home's configuration: %v", err)
		}
	}
	head := git(t, subject, "rev-parse", "HEAD")
	open := labtest.MergeRequestView{
		IID: autonomyLabMR, State: "opened", MergeStatus: "can_be_merged", SourceBranch: "work",
		TargetBranch: "main", SHA: head, Title: "feat: a change", WebURL: autonomyLabURL,
	}
	merged := open
	merged.State = "merged"
	lab := labtest.New(t, labtest.Script{
		labtest.OpProject: {labtest.Project(3, autonomyLabProject)},
		// The checks stage reads the merge request and then its checks, and
		// the adapter's Checks begins with a read of the merge request too;
		// the live read before the merge makes the same two. The read after
		// the merge is the fifth, and the last answer serves every read past
		// it.
		labtest.OpView: {
			labtest.MergeRequest(open), labtest.MergeRequest(open),
			labtest.MergeRequest(open), labtest.MergeRequest(open),
			labtest.MergeRequest(merged),
		},
		labtest.OpPipelines: {labtest.Pipelines(labtest.PipelineView{ID: 41, SHA: head, Ref: "work", Status: "success"})},
		labtest.OpMerge:     {merge},
	})
	return gitLabAutonomyWalk{home: h, subject: subject, head: head, lab: lab,
		gh: forgetest.New(t, forgetest.Script{})}
}

// options opens the service on the walk: the real checks body over the
// GitLab stand-in behind stubs that pass every stage before it, the push
// stage's stub recording the validated head as the commit it forwarded and
// the pull request stage's recording the merge request's number, the way the
// real bodies record what they did.
func (w gitLabAutonomyWalk) options(t *testing.T) service.Options {
	t.Helper()
	opts := options(t, w.home)
	opts.Forge = w.gh.Host()
	opts.GitLab = w.lab.Host()
	opts.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := stages.All(deps)
		for _, stage := range pipeline.Order() {
			switch stage {
			case pipeline.StageCI:
				// The real body, handed the service's own dependencies.
			case pipeline.StagePush:
				*all.At(stage) = recordingStage(stage, pipeline.KeyPushed, w.head)
			case pipeline.StagePR:
				*all.At(stage) = recordingStage(stage, pipeline.KeyPullRequest, "7")
			default:
				*all.At(stage) = passingStage(stage)
			}
		}
		return all
	}
	return opts
}

// recordingStage is a stage body that reports nothing and writes value
// under key.
func recordingStage(stage pipeline.Stage, key pipeline.Key, value string) pipeline.Implementation {
	return pipeline.Implementation{
		Writes: []pipeline.Key{key},
		NewBody: func() pipeline.Body {
			return func(context.Context, pipeline.Input) (pipeline.Output, error) {
				return pipeline.Output{
					Report: findings.Report{Summary: stage.String() + " recorded " + value},
					Writes: map[pipeline.Key]graph.Value{key: graph.TextValue(value)},
				}, nil
			}
		},
	}
}

// walk starts the run, which the service walks to its end and settles
// before it answers, and returns the run as the service then answers for it.
func (w gitLabAutonomyWalk) walk(t *testing.T) machine.Run {
	t.Helper()
	var run machine.Run
	withServiceOptions(t, w.options(t), func(running serviceUnderTest) {
		started := startRun(t, running.client, w.subject)
		run = runByID(t, running.client, started.Record.ID)
	})
	if calls := w.gh.Calls(); len(calls) != 0 {
		t.Fatalf("a gitlab.com run reached gh %d times: %+v", len(calls), calls)
	}
	return run
}

// A passed run of a GitLab project autonomy.merge lists by its host-qualified
// specifier is merged on GitLab, and the merge is bound to the head the run
// observed: the one merge request GitLab is sent carries that commit as its
// sha and nothing else (P6). The merge writes nothing itself; the read after
// it finds the merge request merged at that head and writes the merge
// record, which carries the merge request's number and the commit, and the
// run reads passed from it.
func TestAListedGitLabProjectIsMergedAtTheObservedHead(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	requiresIdentifiedPeer(t)
	w := newGitLabAutonomyWalk(t, `["`+autonomyLabSpec+`"]`, labtest.MergeRequest(labtest.MergeRequestView{
		IID: autonomyLabMR, State: "merged", MergeStatus: "can_be_merged", SourceBranch: "work",
		TargetBranch: "main", SHA: mergeAnswerDecoySHA, Title: "feat: a change", WebURL: autonomyLabURL,
	}))
	// GitLab's accepted merge answers with the merge_request entity, including
	// sha; the fifth GET names the observed head instead. The record must
	// match the GET, not this merge answer.
	run := w.walk(t)

	merges := w.lab.CallsFor(labtest.OpMerge)
	if len(merges) != 1 || merges[0].Project != autonomyLabProject || merges[0].Method != http.MethodPut {
		t.Fatalf("GitLab was asked to merge as %+v, want once on %s; calls %+v log:\n%s", merges, autonomyLabProject, w.lab.Calls(), serviceLog(t, w.home))
	}
	if want := `{"sha":"` + w.head + `"}`; merges[0].Body != want {
		t.Fatalf("the merge was sent %s, want it bound to the observed head and nothing else, %s", merges[0].Body, want)
	}
	if !strings.HasSuffix(merges[0].Path, "/merge_requests/7/merge") {
		t.Fatalf("the merge was sent to %s, want merge request %d", merges[0].Path, autonomyLabMR)
	}
	record := readMergeRecord(t, w.home, run.Record.ID)
	if record == nil || record.PullRequest != autonomyLabMR || record.Head != w.head || record.End != store.PullRequestMerged {
		t.Fatalf("the merge record is %+v, want merge request %d merged at %s", record, autonomyLabMR, w.head)
	}
	if record.Head == mergeAnswerDecoySHA {
		t.Fatalf("the merge record head is the merge answer's sha %s, want the read after the merge's %s", mergeAnswerDecoySHA, w.head)
	}
	if run.Outcome != machine.OutcomePassed {
		t.Fatalf("the run reads %s, want passed from the merge record; log:\n%s", run.Outcome, serviceLog(t, w.home))
	}
	if log := serviceLog(t, w.home); !strings.Contains(log, "merged "+autonomyLabURL+" under autonomy.merge") {
		t.Fatalf("the service log does not name the merged merge request's URL:\n%s", log)
	}
}

// A GitLab project autonomy.merge does not list is never merged: the run
// ends checks-passed and GitLab is asked nothing past the checks stage's own
// read, which includes the project read a merge's confirmation begins with.
// The two lists here are the default, no file at all, and the project's path
// written as owner/name, which is a GitHub repository's specifier and never
// a GitLab one's. The positive control is the listed walk above.
func TestAnUnlistedGitLabProjectIsNeverMerged(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	for name, listed := range map[string]string{
		"the default":                    "",
		"the path as a GitHub specifier": `["` + autonomyLabProject + `"]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newGitLabAutonomyWalk(t, listed, labtest.Error(http.StatusMethodNotAllowed, "405 Method Not Allowed"))
			run := w.walk(t)
			if run.Outcome != machine.OutcomeChecksPassed {
				t.Fatalf("the run reads %s, want checks-passed; log:\n%s", run.Outcome, serviceLog(t, w.home))
			}
			for _, op := range []labtest.Op{labtest.OpMerge, labtest.OpProject} {
				if calls := w.lab.CallsFor(op); len(calls) != 0 {
					t.Fatalf("GitLab was asked %d %s requests for a project autonomy.merge does not list", len(calls), op)
				}
			}
			if views := w.lab.CallsFor(labtest.OpView); len(views) != 2 {
				t.Fatalf("the merge request was read %d times, want the checks stage's two reads alone", len(views))
			}
			if record := readMergeRecord(t, w.home, run.Record.ID); record != nil {
				t.Fatalf("an unmerged run has the merge record %+v", record)
			}
		})
	}
}

// A merge GitLab refuses is reported as the refusal it is and never retried
// around. The statuses are the two GitLab's merge endpoint documents for a
// merge request it will not merge: 405 for one that cannot be accepted, and
// 406 for a branch that cannot be merged. Each is asked once, bound to the
// observed head, the run stays checks-passed for a person, nothing is
// recorded, and the service log carries GitLab's own status and message.
// The calls are counted after the service has closed, so a retry made at any
// point in the service's life would be among them.
func TestAMergeGitLabRefusesIsReportedAndNotRetried(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	requiresIdentifiedPeer(t)
	for _, refusal := range []struct {
		status  int
		message string
	}{
		{http.StatusMethodNotAllowed, "405 Method Not Allowed"},
		{http.StatusNotAcceptable, "Branch cannot be merged"},
	} {
		t.Run(http.StatusText(refusal.status), func(t *testing.T) {
			t.Parallel()
			w := newGitLabAutonomyWalk(t, `["`+autonomyLabSpec+`"]`, labtest.Error(refusal.status, refusal.message))
			run := w.walk(t)
			merges := w.lab.CallsFor(labtest.OpMerge)
			if len(merges) != 1 || merges[0].Body != `{"sha":"`+w.head+`"}` {
				t.Fatalf("the refused merge was asked as %+v, want once, bound to %s, and never worked around", merges, w.head)
			}
			if run.Outcome != machine.OutcomeChecksPassed {
				t.Fatalf("a refused merge left the run %s, want checks-passed", run.Outcome)
			}
			if record := readMergeRecord(t, w.home, run.Record.ID); record != nil {
				t.Fatalf("a refused merge wrote the merge record %+v", record)
			}
			log := serviceLog(t, w.home)
			if !strings.Contains(log, "run "+run.Record.ID+": ") || !strings.Contains(log, "HTTP "+strconv.Itoa(refusal.status)) ||
				!strings.Contains(log, refusal.message) {
				t.Fatalf("the service log does not report GitLab's refusal of run %s:\n%s", run.Record.ID, log)
			}
		})
	}
}
