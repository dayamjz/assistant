package service

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

const mergeReadRun = "run-merge"

// mergeReadFixture is a service built by hand, on the terms the stranded
// push test builds one, over a store, a durable checkpoint store, a run
// service resolved over the stand-in agent, and a code host that is the real
// GitHub adapter reading a scripted gh.
type mergeReadFixture struct {
	s       *Service
	records *store.Store
	home    *home.Home
	host    *forgetest.Provider
	built   *driver
}

func newMergeReadFixture(t *testing.T, script forgetest.Script) mergeReadFixture {
	t.Helper()
	ctx := t.Context()
	root := t.TempDir()
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	seedDatabase(t, h.Database())
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = records.Close() })
	log, err := h.OpenLog(0)
	if err != nil {
		t.Fatalf("opening the log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	events, err := ipc.NewPublisher(ipc.PublisherConfig{})
	if err != nil {
		t.Fatalf("opening the publisher: %v", err)
	}
	t.Cleanup(func() { _ = events.Close() })
	host := forgetest.New(t, script)
	stopCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	s := &Service{
		home: h, store: records, log: log, events: events, forge: host.Host(),
		checkpoints: checkpoints.New(records),
		catalog:     agents.NewCatalog(),
		starting:    make(map[branchKey]*branchGate),
		advancing:   make(map[string]*slot),
		stopCtx:     stopCtx, stopCancel: stop,
		cfg: config.Config{GitDeadline: config.DefaultGitDeadline},
	}
	if s.queue, err = s.newQueue(); err != nil {
		t.Fatalf("building the queue: %v", err)
	}
	if s.cleaner, err = tasks.NewCleaner(tasks.CleanupDeps{Home: h, Store: records, Forge: s.forge, GitDeadline: config.DefaultGitDeadline}); err != nil {
		t.Fatalf("building the cleaner: %v", err)
	}
	resolution, err := agents.Resolve(ctx, []string{agents.AutoEntry},
		agents.NewCatalog(fixedRunner{runner: standin.New(t, standin.Script{}).Runner()}))
	if err != nil {
		t.Fatalf("resolving the stand-in: %v", err)
	}
	runService, err := runs.New(runs.Options{Store: records, Agent: resolution})
	if err != nil {
		t.Fatalf("building the run service: %v", err)
	}
	s.built = &driver{agent: resolution, runs: runService}
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: filepath.Join(root, "checkout"),
		UpstreamURL: "https://github.com/" + autonomyRepository + ".git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	return mergeReadFixture{s: s, records: records, home: h, host: host, built: s.built}
}

// passedRunState is what a run recorded on its way to checks-passed: the
// repository on the code host, the commit its push stage forwarded, and the
// pull request number its pull request stage recorded. An empty field is one
// the run did not record.
type passedRunState struct {
	repository string
	pushed     string
	number     string
}

// passRun records a run, drives the real pipeline to completion over the
// service's durable checkpoint store with stage bodies that write what state
// says, and moves the record to passed the way settle does for a run that
// completed with no pull request read ending it. The state the read finds is
// therefore what the executor wrote, not one stated here.
func (f mergeReadFixture) passRun(t *testing.T, state passedRunState) {
	t.Helper()
	ctx := t.Context()
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}
	if _, err := f.records.CreateRun(ctx, store.Run{
		ID: mergeReadRun, RepositoryID: "subject", Branch: "work", SubmittedHead: autonomyHead, Base: "main",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatalf("recording the run: %v", err)
	}
	if _, err := f.records.TransitionRun(ctx, mergeReadRun, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("starting the run: %v", err)
	}
	var all pipeline.Stages
	for _, stage := range pipeline.Order() {
		*all.At(stage) = writingStage(stage, nil)
	}
	if state.pushed != "" {
		*all.At(pipeline.StagePush) = writingStage(pipeline.StagePush,
			map[pipeline.Key]graph.Value{pipeline.KeyPushed: graph.TextValue(state.pushed)})
	}
	if state.number != "" {
		*all.At(pipeline.StagePR) = writingStage(pipeline.StagePR,
			map[pipeline.Key]graph.Value{pipeline.KeyPullRequest: graph.TextValue(state.number)})
	}
	p, err := pipeline.New(pipeline.Options{Stages: all, Budget: 40})
	if err != nil {
		t.Fatalf("building the pipeline: %v", err)
	}
	initial, err := p.NewState(pipeline.Start{Repository: "subject", Run: mergeReadRun, ForgeRepository: state.repository,
		Branch: "work", Base: "main", Submitted: autonomyHead})
	if err != nil {
		t.Fatalf("building the initial state: %v", err)
	}
	executor, err := p.Executor(f.s.checkpoints)
	if err != nil {
		t.Fatalf("building the executor: %v", err)
	}
	result, err := executor.Run(ctx, mergeReadRun, initial)
	if err != nil || result.Status != graph.StatusCompleted {
		t.Fatalf("the run ended %s (%v), want completed", result.Status, err)
	}
	if _, err := f.built.runs.Pass(ctx, mergeReadRun); err != nil {
		t.Fatalf("passing the run: %v", err)
	}
}

// writingStage is a stage body that reports nothing and writes writes.
func writingStage(stage pipeline.Stage, writes map[pipeline.Key]graph.Value) pipeline.Implementation {
	var keys []pipeline.Key
	for k := range writes {
		keys = append(keys, k)
	}
	return pipeline.Implementation{
		Writes: keys,
		NewBody: func() pipeline.Body {
			return func(context.Context, pipeline.Input) (pipeline.Output, error) {
				return pipeline.Output{Report: findings.Report{Summary: stage.String() + " found nothing"}, Writes: writes}, nil
			}
		},
	}
}

// outcome is the run's outcome as the service answers for it.
func (f mergeReadFixture) outcome(t *testing.T) machine.Outcome {
	t.Helper()
	answer, err := f.s.report(t.Context(), mergeReadRun, nil)
	if err != nil {
		t.Fatalf("reporting the run: %v", err)
	}
	return answer.Outcome
}

// views counts the pull request reads the code host was asked for.
func views(calls []forgetest.Call) int {
	n := 0
	for _, c := range calls {
		if c.Op == forgetest.OpView {
			n++
		}
	}
	return n
}

// personMerged is the code host after a person merged pull request 12 with
// its head at head.
func personMerged(head string) forgetest.Script {
	v := readyView()
	v.State, v.Mergeable, v.HeadCommit = "MERGED", "UNKNOWN", head
	return forgetest.Script{forgetest.OpView: {forgetest.PullRequest(v)}}
}

// A person's merge at the head the run observed is recorded by the pass,
// through the one read the program's own merge is recorded with: one pull
// request read, a merge record naming the pull request, the observed head and
// the merged end, and the run's outcome derived from it as passed (P6). The
// run's own row does not move and gains nothing: it says passed before and
// after, and the passed outcome is read off the merge record alone, which is
// the one owner of the fact (P14).
func TestAPersonsMergeAtTheObservedHeadIsRecorded(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P14)
	f := newMergeReadFixture(t, personMerged(autonomyHead))
	f.passRun(t, passedRunState{repository: autonomyRepository, pushed: autonomyHead, number: "12"})
	if got := f.outcome(t); got != machine.OutcomeChecksPassed {
		t.Fatalf("before the read the run is %s, want checks-passed", got)
	}
	before, err := f.records.Run(t.Context(), mergeReadRun)
	if err != nil {
		t.Fatal(err)
	}

	results := f.s.readPersonMerges(t.Context())
	if results[mergeReadRun] != mergeReadRecorded {
		t.Fatalf("the pass did %q, want the merge recorded; log:\n%s", results[mergeReadRun], serviceLog(t, f.home))
	}
	record, ok, err := f.records.MergeRecord(t.Context(), mergeReadRun)
	if err != nil || !ok {
		t.Fatalf("the merge record is %v (%v), want one", ok, err)
	}
	if record.PullRequest != autonomyNumber || record.Head != autonomyHead || record.End != store.PullRequestMerged {
		t.Fatalf("the merge record is %+v, want pull request %d merged at %s", record, autonomyNumber, autonomyHead)
	}
	if got := f.outcome(t); got != machine.OutcomePassed {
		t.Fatalf("after the read the run is %s, want passed", got)
	}
	after, err := f.records.Run(t.Context(), mergeReadRun)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("the run's own row moved from %+v to %+v; the merge record is the one place the merge is recorded", before, after)
	}
	if n := views(f.host.Calls()); n != 1 {
		t.Fatalf("the code host was asked to read the pull request %d times, want once", n)
	}
}

// Every answer short of a merge at the observed head records nothing and
// leaves the run checks-passed: merged at another head, which is not this
// run's merge and is reported, closed, still open, and a read the provider
// refused. A run whose record names no pull request number, or no repository
// on the code host, is passed over before the code host is asked anything,
// and so is a run that recorded no observed head, which nothing could anchor
// a merge to (P6).
func TestOnlyAMergeAtTheObservedHeadIsRecorded(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	open := readyView()
	closed := readyView()
	closed.State = "CLOSED"
	recorded := passedRunState{repository: autonomyRepository, pushed: autonomyHead, number: "12"}
	cases := []struct {
		name   string
		script forgetest.Script
		state  passedRunState
		want   mergeReadResult
		reads  int
		logged string
	}{
		{"merged at another head", personMerged(strings.Repeat("b", 40)), recorded, mergeReadElsewhere, 1,
			"not this run's merge"},
		{"closed", forgetest.Script{forgetest.OpView: {forgetest.PullRequest(closed)}}, recorded, mergeReadNotMerged, 1, ""},
		{"still open", forgetest.Script{forgetest.OpView: {forgetest.PullRequest(open)}}, recorded, mergeReadNotMerged, 1, ""},
		{"a read failure", forgetest.Script{forgetest.OpView: {forgetest.Exit(1, "HTTP 502: Bad Gateway")}}, recorded,
			mergeReadFailed, 1, "could not be read"},
		{"no pull request number", personMerged(autonomyHead),
			passedRunState{repository: autonomyRepository, pushed: autonomyHead}, mergeReadSkipped, 0, ""},
		{"no repository", personMerged(autonomyHead),
			passedRunState{pushed: autonomyHead, number: "12"}, mergeReadSkipped, 0, ""},
		{"no observed head", personMerged(autonomyHead),
			passedRunState{repository: autonomyRepository, number: "12"}, mergeReadSkipped, 0, "no head it observed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newMergeReadFixture(t, c.script)
			f.passRun(t, c.state)
			results := f.s.readPersonMerges(t.Context())
			if results[mergeReadRun] != c.want {
				t.Fatalf("the pass did %q, want %q; log:\n%s", results[mergeReadRun], c.want, serviceLog(t, f.home))
			}
			if _, ok, err := f.records.MergeRecord(t.Context(), mergeReadRun); ok || err != nil {
				t.Fatalf("a merge record was written (%v)", err)
			}
			if got := f.outcome(t); got != machine.OutcomeChecksPassed {
				t.Fatalf("the run is %s, want checks-passed still", got)
			}
			calls := f.host.Calls()
			if n := views(calls); n != c.reads {
				t.Fatalf("the code host was asked to read the pull request %d times, want %d", n, c.reads)
			}
			if c.reads == 0 && len(calls) != 0 {
				t.Fatalf("the code host was asked %d things about a run it should never hear of", len(calls))
			}
			if c.logged != "" && !strings.Contains(serviceLog(t, f.home), c.logged) {
				t.Fatalf("the log does not report %q:\n%s", c.logged, serviceLog(t, f.home))
			}
		})
	}
}

// A merge is recorded once. The pass after the one that recorded it decides
// from the records as they stand now, a passed run with a merge record, so it
// passes the run over without asking the code host anything (P8), and a
// second write of the same read is refused by the store with
// ErrMergeRecorded rather than written again. A merge at another head is
// final, so the pass after the one that reported it does not read it again.
func TestAMergeIsRecordedOnceAndReadNoMore(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	f := newMergeReadFixture(t, personMerged(autonomyHead))
	f.passRun(t, passedRunState{repository: autonomyRepository, pushed: autonomyHead, number: "12"})
	if got := f.s.readPersonMerges(t.Context())[mergeReadRun]; got != mergeReadRecorded {
		t.Fatalf("the first pass did %q, want the merge recorded", got)
	}
	first, _, err := f.records.MergeRecord(t.Context(), mergeReadRun)
	if err != nil {
		t.Fatal(err)
	}
	if got, counted := f.s.readPersonMerges(t.Context())[mergeReadRun]; counted {
		t.Fatalf("the second pass did %q about a run that is no longer checks-passed", got)
	}
	if n := views(f.host.Calls()); n != 1 {
		t.Fatalf("the code host was read %d times over two passes, want once", n)
	}

	record, err := f.records.Run(t.Context(), mergeReadRun)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := f.host.Host().Open(autonomyRepository)
	if err != nil {
		t.Fatal(err)
	}
	facts := passedPullRequest{specifier: autonomyRepository, number: autonomyNumber, observed: autonomyHead}
	if _, err := f.s.recordMergeRead(t.Context(), f.built, record, provider, facts); !errors.Is(err, store.ErrMergeRecorded) {
		t.Fatalf("a second record of the same merge = %v, want ErrMergeRecorded", err)
	}
	again, _, err := f.records.MergeRecord(t.Context(), mergeReadRun)
	if err != nil || again != first {
		t.Fatalf("the merge record is %+v (%v) after the second read, want %+v unchanged", again, err, first)
	}

	g := newMergeReadFixture(t, personMerged(strings.Repeat("b", 40)))
	g.passRun(t, passedRunState{repository: autonomyRepository, pushed: autonomyHead, number: "12"})
	for range 2 {
		g.s.readPersonMerges(t.Context())
	}
	if n := views(g.host.Calls()); n != 1 {
		t.Fatalf("a merge at another head was read %d times over two passes, want once", n)
	}
}

// The read runs as the service starts its background work, without waiting
// for the interval: a merge a person made while no service was serving the
// home is recorded by the first pass.
func TestTheFirstPassRunsWhenTheReadsStart(t *testing.T) {
	t.Parallel()
	f := newMergeReadFixture(t, personMerged(autonomyHead))
	f.passRun(t, passedRunState{repository: autonomyRepository, pushed: autonomyHead, number: "12"})
	f.s.startMergeReads()
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, ok, err := f.records.MergeRecord(t.Context(), mergeReadRun)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no merge record after the first pass; log:\n%s", serviceLog(t, f.home))
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.s.stopCancel()
	f.s.stopWork()
	f.s.work.Wait()
}
