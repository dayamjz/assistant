package stages_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// checksTimeout is the checks_timeout the wait tests run under. It is short
// so a wait the body paces a minute at a time is a few dozen reads of a real
// upstream rather than ten thousand; nothing about the rule depends on it.
const checksTimeout = 30 * time.Minute

// checksProvider is the code host a checks test reads from. It answers Get
// with the pull request as state says it stands and Checks from answer, which
// a test points at the report the provider would give at the current read,
// and refuses everything else: the checks stage asks for nothing else.
//
// The reports a test states are ones the GitHub adapter produces: a head
// commit, and checks named and placed in pending or succeeded, which are
// states its rollup reading maps a check onto. The pull request it answers is
// one that adapter produces too: numbered as asked, which the adapter refuses
// to answer otherwise, in a state its reading maps gh's OPEN, MERGED and
// CLOSED onto, or the zero state, which is what any other word maps to.
type checksProvider struct {
	answer func() forge.ChecksReport
	reads  int
	// state is where the pull request stands at the current read, and gets
	// counts the reads of it.
	state forge.PullRequestState
	gets  int
	// refuseGet makes every read of the pull request a refusal the provider
	// made on its own, with the state above never reached, and refuseChecks
	// does the same for the checks. Both are the refusal the GitHub adapter
	// returns for a provider that ran, answered, and refused the request.
	refuseGet    bool
	refuseChecks bool
	// stall makes a read wait until its context ends and then refuse, which
	// is what the GitHub adapter does with a provider command that never
	// answers: a context ending kills the command and the read fails.
	stall bool
}

func (p *checksProvider) Find(context.Context, string) (forge.PullRequest, bool, error) {
	return forge.PullRequest{}, false, errors.New("the checks stage does not find pull requests")
}

func (p *checksProvider) Open(context.Context, forge.OpenSpec) (forge.PullRequest, error) {
	return forge.PullRequest{}, errors.New("the checks stage does not open pull requests")
}

func (p *checksProvider) UpdateBody(context.Context, int, string) (forge.PullRequest, error) {
	return forge.PullRequest{}, errors.New("the checks stage does not update pull requests")
}

func (p *checksProvider) Get(ctx context.Context, number int) (forge.PullRequest, error) {
	p.gets++
	if err := ctx.Err(); err != nil {
		return forge.PullRequest{}, err
	}
	if p.stall {
		<-ctx.Done()
		return forge.PullRequest{}, ctx.Err()
	}
	if p.refuseGet {
		return forge.PullRequest{}, &forge.Refusal{Reason: forge.ReasonRejected, Op: "get",
			Detail: "the provider refused to describe the pull request"}
	}
	return forge.PullRequest{Number: number, State: p.state, HeadCommit: "0123abc"}, nil
}

func (p *checksProvider) Checks(ctx context.Context, _ int) (forge.ChecksReport, error) {
	p.reads++
	// The adapter runs a provider command under ctx, and a command started
	// under a context that has already ended is killed before it answers.
	if err := ctx.Err(); err != nil {
		return forge.ChecksReport{}, err
	}
	if p.stall {
		<-ctx.Done()
		return forge.ChecksReport{}, ctx.Err()
	}
	if p.refuseChecks {
		return forge.ChecksReport{}, &forge.Refusal{Reason: forge.ReasonRejected, Op: "checks",
			Detail: "the provider refused to list the checks"}
	}
	return p.answer(), nil
}

var _ forge.Provider = (*checksProvider)(nil)

func pending() forge.ChecksReport {
	return forge.ChecksReport{HeadCommit: "0123abc", Runs: []forge.CheckRun{
		{Name: "build", State: forge.CheckStatePending},
	}}
}

func green() forge.ChecksReport {
	return forge.ChecksReport{HeadCommit: "0123abc", Runs: []forge.CheckRun{
		{Name: "build", State: forge.CheckStateSucceeded},
	}}
}

func noChecks() forge.ChecksReport { return forge.ChecksReport{HeadCommit: "0123abc"} }

// checksRun is one run standing at the checks stage: a real upstream holding
// the base branch, the run's isolated copy reaching it, a real store holding
// the wait, and a clock the test drives.
type checksRun struct {
	home     *home.Home
	upstream string
	store    *store.Store
	provider *checksProvider

	start time.Time
	now   time.Time
	// pauses counts the waits the body asked for, which is how a test tells a
	// body that waited from one that answered on its first read.
	pauses int
	// onPause runs after each pause has moved the clock, so a test can change
	// the world partway through a wait.
	onPause func(r *checksRun)
}

func newChecksRun(t *testing.T) *checksRun {
	t.Helper()
	r := &checksRun{
		home:     newHome(t),
		upstream: filepath.Join(t.TempDir(), "upstream.git"),
		provider: &checksProvider{answer: pending, state: forge.PullRequestStateOpen},
		start:    time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
	r.now = r.start
	git(t, filepath.Dir(r.upstream), "init", "--quiet", "--bare", "-b", "main", r.upstream)

	source := t.TempDir()
	git(t, source, "init", "--quiet", "-b", "main")
	write(t, source, "base.go", "package subject\n")
	git(t, source, "add", ".")
	git(t, source, "commit", "--quiet", "-m", "base")
	git(t, source, "remote", "add", "origin", r.upstream)
	git(t, source, "push", "--quiet", "origin", "main")

	copied := r.home.Worktree("repository-1", "run-1")
	if err := os.MkdirAll(filepath.Dir(copied), 0o700); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(copied), err)
	}
	git(t, source, "worktree", "add", "--quiet", "--detach", copied, "HEAD")

	opened, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	r.store = opened
	return r
}

// advanceBase puts a commit on the upstream's base branch from a checkout of
// its own, which is the base advancing while the run waits.
func (r *checksRun) advanceBase(t *testing.T) {
	t.Helper()
	other := t.TempDir()
	git(t, other, "clone", "--quiet", r.upstream, other)
	write(t, other, "elsewhere.go", "package subject\n\n// landed while the run waited\n")
	git(t, other, "add", ".")
	git(t, other, "commit", "--quiet", "-m", "the base advances")
	git(t, other, "push", "--quiet", "origin", "main")
}

// deps are the stage's dependencies for this run, with the store as the wait
// record unless withoutWaits is set.
func (r *checksRun) deps(ctx context.Context, t *testing.T, withoutWaits bool) stages.StageDeps {
	t.Helper()
	cfg := config.Defaults()
	cfg.ChecksTimeout = checksTimeout
	deps := stages.NewStageDeps(agents.StageAgent{}, r.home, cfg, hostFor{provider: r.provider}, redact.New())
	if !withoutWaits {
		deps = deps.WithChecksWaits(r.store)
	}
	return stages.WithChecksClock(deps,
		func() time.Time { return r.now },
		func(waitCtx context.Context, d time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if d <= 0 || d > time.Minute {
				t.Errorf("the body asked to wait %s between reads, want a positive wait of at most a minute", d)
			}
			r.now = r.now.Add(d)
			r.pauses++
			if r.onPause != nil {
				r.onPause(r)
			}
			return waitCtx.Err()
		})
}

// run runs the checks body once, as the stage node would, and returns the
// normalized report or the error the body failed the step with.
func (r *checksRun) run(ctx context.Context, t *testing.T, withoutWaits bool) (findings.Report, error) {
	t.Helper()
	out, err := r.runOut(ctx, t, withoutWaits)
	return out.Report, err
}

// runOut is run with the whole output, for a test reading what the body
// wrote beside its report. The report in it is normalized.
func (r *checksRun) runOut(ctx context.Context, t *testing.T, withoutWaits bool) (pipeline.Output, error) {
	t.Helper()
	impl := stages.CI(r.deps(ctx, t, withoutWaits))
	allowed := make(map[pipeline.Key]bool, len(impl.Reads))
	for _, key := range impl.Reads {
		allowed[key] = true
	}
	out, err := impl.NewBody()(ctx, pipeline.Input{
		Stage: pipeline.StageCI,
		State: declaredReader{allowed: allowed, state: map[pipeline.Key]graph.Value{
			pipeline.KeyRepository:      graph.TextValue("repository-1"),
			pipeline.KeyRun:             graph.TextValue("run-1"),
			pipeline.KeyBase:            graph.TextValue("main"),
			pipeline.KeyForgeRepository: graph.TextValue("dayamjz/assistant"),
			pipeline.KeyPullRequest:     graph.TextValue("7"),
		}},
	})
	if err != nil {
		return pipeline.Output{}, err
	}
	out.Report = out.Report.Normalize()
	if err := out.Report.Validate(); err != nil {
		t.Fatalf("the checks stage produced a report the pipeline refuses: %v", err)
	}
	for key := range out.Writes {
		if !slices.Contains(impl.Writes, key) {
			t.Fatalf("the body wrote %s, which it did not declare", key)
		}
	}
	return out, nil
}

// pullRequestEnd is what the body wrote under pipeline.KeyPullRequestEnd,
// empty when it wrote nothing there.
func pullRequestEnd(out pipeline.Output) string {
	text, _ := out.Writes[pipeline.KeyPullRequestEnd].Text()
	return text
}

// pullRequestEndHead is what the body wrote under
// pipeline.KeyPullRequestEndHead.
func pullRequestEndHead(out pipeline.Output) string {
	text, _ := out.Writes[pipeline.KeyPullRequestEndHead].Text()
	return text
}

func (r *checksRun) mustRun(t *testing.T) findings.Report {
	t.Helper()
	report, err := r.run(t.Context(), t, false)
	if err != nil {
		t.Fatalf("running the checks stage: %v", err)
	}
	return report
}

// Checks that are still running are a wait, not a hold: the body re-reads
// them and reports what they settled on, so a run whose checks go green
// passes the stage without anyone answering anything.
//
// The pause count is the control. A body that held on the first read, which
// is what this stage did before checks_timeout was applied, reports a hold and
// pauses never; one that read once and reported the second answer without
// waiting would pause never too.
func TestTheChecksStageWaitsForRunningChecksAndReportsWhatTheySettleOn(t *testing.T) {
	t.Parallel()
	r := newChecksRun(t)
	r.onPause = func(r *checksRun) {
		if r.pauses == 3 {
			r.provider.answer = green
		}
	}

	report := r.mustRun(t)

	if report.HasHeld() {
		t.Fatalf("the stage held while checks were still running and the timeout had not elapsed: %+v", report)
	}
	if r.pauses != 3 || r.provider.reads != 4 {
		t.Fatalf("the body paused %d times over %d reads, want 3 pauses and the fourth read settling", r.pauses, r.provider.reads)
	}
	if !strings.Contains(report.Summary, "passed") {
		t.Fatalf("the report does not say the checks passed: %q", report.Summary)
	}
}

// Checks that never settle hold the run once checks_timeout has elapsed, and
// not before: PRD section 10's "on elapse the run holds rather than ending".
//
// The read made at the deadline answered, so the report may not say it did
// not: that confession is only for a read the timeout ran out on.
func TestChecksThatNeverSettleHoldTheRunWhenTheTimeoutElapses(t *testing.T) {
	t.Parallel()
	r := newChecksRun(t)

	report := r.mustRun(t)

	if !report.HasHeld() || !hasFinding(report, "ci-running") {
		t.Fatalf("checks that never settled did not hold the stage with ci-running: %+v", report)
	}
	if waited := r.now.Sub(r.start); waited != checksTimeout {
		t.Fatalf("the stage held after waiting %s, want exactly the %s checks_timeout", waited, checksTimeout)
	}
	if !strings.Contains(report.Summary, "checks_timeout") {
		t.Fatalf("the summary does not say the timeout elapsed: %q", report.Summary)
	}
	mustNotConfessAnUnansweredRead(t, report)
}

// unansweredRead is the text an elapsed report carries only when a read made
// as the wait ran out did not answer in time.
const unansweredRead = "did not answer in time"

func mustNotConfessAnUnansweredRead(t *testing.T, report findings.Report) {
	t.Helper()
	if strings.Contains(report.Summary, unansweredRead) {
		t.Fatalf("every read answered, and the report says one did not: %q", report.Summary)
	}
}

// No registered checks and no no_ci declaration is waited through on the same
// terms, because checks may still register, and holds as ci-no-checks when the
// timeout elapses.
func TestNoRegisteredChecksAreWaitedForUntilTheTimeoutElapses(t *testing.T) {
	t.Parallel()
	r := newChecksRun(t)
	r.provider.answer = noChecks

	report := r.mustRun(t)

	if !report.HasHeld() || !hasFinding(report, "ci-no-checks") {
		t.Fatalf("an unregistered check list did not hold the stage with ci-no-checks: %+v", report)
	}
	if waited := r.now.Sub(r.start); waited != checksTimeout {
		t.Fatalf("the stage held after waiting %s, want %s", waited, checksTimeout)
	}
	mustNotConfessAnUnansweredRead(t, report)
}

// A base that advances while the run waits re-arms the timeout. The
// never-settling test above is the control: the same run with the base left
// alone holds at exactly checks_timeout.
func TestTheBaseAdvancingReArmsTheChecksTimeout(t *testing.T) {
	t.Parallel()
	r := newChecksRun(t)
	advanceAt := checksTimeout - 10*time.Minute
	r.onPause = func(r *checksRun) {
		if r.now.Sub(r.start) == advanceAt {
			r.advanceBase(t)
		}
	}

	report := r.mustRun(t)

	if !report.HasHeld() {
		t.Fatalf("checks that never settled did not hold: %+v", report)
	}
	// The advance lands after the pause that reached advanceAt, and the next
	// read of the base sees it and re-arms there.
	if want := advanceAt + checksTimeout; r.now.Sub(r.start) != want {
		t.Fatalf("the stage held after %s, want %s: the base advancing at %s should have re-armed the timeout",
			r.now.Sub(r.start), want, advanceAt)
	}
	mustNotConfessAnUnansweredRead(t, report)
}

// A segment that ends mid-wait does not reset the timeout. A restart, a caller
// that walked away, or an ending through the protocol all leave the run at the
// checks stage, and whatever resumes it re-enters the same wait.
//
// The control is a run whose wait was ended between the two segments, which
// is what a wait that began again on every resume would look like: it waits
// the whole timeout a second time.
//
// This is P6's "work the pipeline holds always has a path back into your
// hands, including after a crash, a cancellation" for a run waiting on its
// checks: the segment that ended leaves the run resumable, and the resumed
// one still reaches the hold that hands it to a person, on the deadline the
// first one armed.
func TestResumingTheChecksStageKeepsTheWaitItBegan(t *testing.T) {
	principles.Cite(t, principles.P6)
	t.Parallel()
	for _, tc := range []struct {
		name     string
		endWait  bool
		wantHold time.Duration
	}{
		{"resumed", false, checksTimeout},
		{"control: the wait ended between segments", true, 10*time.Minute + checksTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newChecksRun(t)

			interrupted, stop := context.WithCancel(t.Context())
			r.onPause = func(r *checksRun) {
				if r.now.Sub(r.start) == 10*time.Minute {
					stop()
				}
			}
			if _, err := r.run(interrupted, t, false); !errors.Is(err, context.Canceled) {
				t.Fatalf("a segment ended mid-wait returned %v, want the context's error", err)
			}
			if tc.endWait {
				if err := r.store.EndChecksWait(t.Context(), "run-1"); err != nil {
					t.Fatalf("ending the wait: %v", err)
				}
			}

			r.onPause = nil
			report := r.mustRun(t)
			if !report.HasHeld() {
				t.Fatalf("checks that never settled did not hold: %+v", report)
			}
			if got := r.now.Sub(r.start); got != tc.wantHold {
				t.Fatalf("the stage held %s after the wait began, want %s", got, tc.wantHold)
			}
		})
	}
}

// Checks that settle end the wait, so the next one - after a fix round, say -
// is armed when it begins rather than inheriting time already spent.
func TestSettledChecksEndTheWait(t *testing.T) {
	t.Parallel()
	r := newChecksRun(t)
	r.onPause = func(r *checksRun) {
		if r.pauses == 20 {
			r.provider.answer = green
		}
	}
	r.mustRun(t)

	r.provider.answer = pending
	r.onPause = nil
	settledAt := r.now
	report := r.mustRun(t)
	if !report.HasHeld() {
		t.Fatalf("checks that never settled did not hold: %+v", report)
	}
	if got := r.now.Sub(settledAt); got != checksTimeout {
		t.Fatalf("the second wait held after %s, want a fresh %s", got, checksTimeout)
	}
}

// A checks body with no wait record refuses rather than holding on the first
// unsettled read, which would be a timeout of zero nobody configured. Settled
// checks need no record, and that half is the control: the body still runs.
func TestTheChecksStageRefusesToWaitWithoutAWaitRecord(t *testing.T) {
	t.Parallel()
	r := newChecksRun(t)
	if _, err := r.run(t.Context(), t, true); err == nil {
		t.Fatal("an unsettled read with no wait record was not refused")
	}
	r.provider.answer = green
	report, err := r.run(t.Context(), t, true)
	if err != nil || report.HasHeld() {
		t.Fatalf("settled checks with no wait record returned %+v, %v; want a pass", report, err)
	}
}

// A read that does not answer cannot outlast checks_timeout: once a wait is
// armed, every read is bounded by what is left of it, and running out during
// one holds the run on the same terms as running out between reads.
//
// The control is the same stalled read ended by the segment rather than by
// the timeout, which is a failed step and not a hold: a body that read every
// context ending as an elapsed wait would hold a run that was cancelled.
func TestAReadThatDoesNotAnswerCannotOutlastTheChecksTimeout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		bySegment bool
	}{
		{"the timeout runs out mid-read", false},
		{"control: the segment ends mid-read", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newChecksRun(t)
			segment, stop := context.WithCancel(t.Context())
			defer stop()
			r.onPause = func(r *checksRun) {
				if r.pauses != 2 {
					return
				}
				r.provider.stall = true
				if tc.bySegment {
					go func() { time.Sleep(20 * time.Millisecond); stop() }()
					return
				}
				// Leave the next read a few real milliseconds of the wait.
				r.now = r.start.Add(checksTimeout - 20*time.Millisecond)
			}

			report, err := r.run(segment, t, false)
			if tc.bySegment {
				if err == nil || report.HasHeld() {
					t.Fatalf("a read the segment ended returned %+v, %v; want a failed step", report, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("a read the timeout ran out on failed the step: %v", err)
			}
			if !report.HasHeld() || !hasFinding(report, "ci-running") || !strings.Contains(report.Summary, unansweredRead) {
				t.Fatalf("a read the timeout ran out on did not hold saying so: %+v", report)
			}
		})
	}
}

// The read made once the wait has run out is a real read with a bound of its
// own, so checks that went green in the last interval pass. A body that held
// on reaching the deadline without reading, or read under a bound already
// spent, would hold a run whose checks had passed.
//
// The never-settling test is the control: the same deadline with the checks
// still pending holds.
func TestChecksThatGoGreenAtTheDeadlinePass(t *testing.T) {
	t.Parallel()
	r := newChecksRun(t)
	r.onPause = func(r *checksRun) {
		if r.now.Sub(r.start) == checksTimeout {
			r.provider.answer = green
		}
	}

	report := r.mustRun(t)

	if report.HasHeld() || !strings.Contains(report.Summary, "passed") {
		t.Fatalf("checks that went green by the deadline were not passed: %+v", report)
	}
	if r.now.Sub(r.start) != checksTimeout {
		t.Fatalf("the final read was made at %s, want the deadline %s", r.now.Sub(r.start), checksTimeout)
	}
}

// An elapsed report is recorded in the run's graph state, which is the
// convergence fingerprint, so it may not carry when the wait was armed or
// against which base: two elapses that differ only in those must report the
// same thing. The two runs here differ in both, because each has its own
// upstream and its own clock.
func TestAnElapsedReportDoesNotCarryWhenTheWaitWasArmed(t *testing.T) {
	t.Parallel()
	first := newChecksRun(t)
	second := newChecksRun(t)
	second.start = first.start.Add(37 * time.Hour)
	second.now = second.start
	second.advanceBase(t)

	a, b := first.mustRun(t), second.mustRun(t)

	if !reflect.DeepEqual(a, b) {
		t.Fatalf("two elapses armed at different instants against different bases reported differently:\n%+v\n%+v", a, b)
	}
	if !a.HasHeld() {
		t.Fatalf("the elapses did not hold, so their equality shows nothing: %+v", a)
	}
}

// An elapsed wait is ended, as a settled one is: the hold it leaves is answered
// by a person, and a row left behind would be found only by a later wait that
// should begin afresh.
func TestAnElapsedWaitIsEnded(t *testing.T) {
	t.Parallel()
	r := newChecksRun(t)
	r.mustRun(t)

	// Armed against the base the elapsed wait was armed against, so a row
	// left behind would be kept rather than re-armed, and this would see it.
	base := git(t, r.upstream, "rev-parse", "refs/heads/main")
	later := r.now.Add(time.Hour)
	wait, err := r.store.ArmChecksWait(t.Context(), "run-1", base, later)
	if err != nil {
		t.Fatalf("arming: %v", err)
	}
	if !wait.ArmedAt.Equal(later) {
		t.Fatalf("a wait armed after an elapse began at %s, want a fresh one at %s", wait.ArmedAt, later)
	}
}

// A pull request read as merged ends the wait: the body reports that the run
// is complete and not newly validated, with the checks unread to a verdict and
// the no_ci rule unapplied, writes the merge under pipeline.KeyPullRequestEnd
// for the service to settle the record from, and ends its wait row. The read
// precedes the checks, so the read that found the merge read no checks.
//
// The running-checks test above is the control: the same wait with the pull
// request left open reads the checks on every read and settles on them.
func TestAMergedPullRequestEndsTheWaitWithoutReadingTheChecks(t *testing.T) {
	t.Parallel()
	r := newChecksRun(t)
	r.onPause = func(r *checksRun) {
		if r.pauses == 3 {
			r.provider.state = forge.PullRequestStateMerged
		}
	}

	out, err := r.runOut(t.Context(), t, false)
	if err != nil {
		t.Fatalf("running the checks stage: %v", err)
	}

	if out.Report.HasHeld() || len(out.Report.Findings) != 0 {
		t.Fatalf("a merged pull request did not pass the stage cleanly: %+v", out.Report)
	}
	for _, want := range []string{"merged", "not newly validated", "not read to a verdict", "no_ci"} {
		if !strings.Contains(out.Report.Summary, want) {
			t.Fatalf("the report does not say %q: %q", want, out.Report.Summary)
		}
	}
	if got := pullRequestEnd(out); got != string(store.PullRequestMerged) {
		t.Fatalf("the body wrote %q under the pull request end, want merged", got)
	}
	if got := pullRequestEndHead(out); got != "0123abc" {
		t.Fatalf("the body wrote %q as the head it read the merge at, want the provider's head", got)
	}
	if r.provider.gets != 4 || r.provider.reads != 3 {
		t.Fatalf("the body read the pull request %d times and the checks %d times, want 4 and 3: "+
			"the read that found the merge reads no checks", r.provider.gets, r.provider.reads)
	}
	mustHaveEndedTheWait(t, r)
}

// A pull request read as closed without a merge ends the wait the same way,
// with the close written for the service and the checks reported unread. The
// report says what was read and nothing about what the run becomes: that the
// record fails is internal/service's rule, read off the key.
func TestAClosedPullRequestEndsTheWait(t *testing.T) {
	t.Parallel()
	r := newChecksRun(t)
	r.onPause = func(r *checksRun) {
		if r.pauses == 2 {
			r.provider.state = forge.PullRequestStateClosed
		}
	}

	out, err := r.runOut(t.Context(), t, false)
	if err != nil {
		t.Fatalf("running the checks stage: %v", err)
	}

	if out.Report.HasHeld() || len(out.Report.Findings) != 0 {
		t.Fatalf("a closed pull request did not end the stage cleanly: %+v", out.Report)
	}
	if !strings.Contains(out.Report.Summary, "closed without being merged") ||
		!strings.Contains(out.Report.Summary, "not read to a verdict") {
		t.Fatalf("the report does not say the pull request closed with the checks unread: %q", out.Report.Summary)
	}
	if got := pullRequestEnd(out); got != string(store.PullRequestClosed) {
		t.Fatalf("the body wrote %q under the pull request end, want closed", got)
	}
	mustHaveEndedTheWait(t, r)
}

// mustHaveEndedTheWait asserts the run's wait row is gone, on the terms
// TestAnElapsedWaitIsEnded does: a wait armed afterwards against the same base
// begins afresh rather than keeping the instant the old row held.
func mustHaveEndedTheWait(t *testing.T, r *checksRun) {
	t.Helper()
	base := git(t, r.upstream, "rev-parse", "refs/heads/main")
	later := r.now.Add(time.Hour)
	wait, err := r.store.ArmChecksWait(t.Context(), "run-1", base, later)
	if err != nil {
		t.Fatalf("arming: %v", err)
	}
	if !wait.ArmedAt.Equal(later) {
		t.Fatalf("the wait row was left behind: a wait armed afterwards began at %s, want %s", wait.ArmedAt, later)
	}
}

// Only a positively read merged or closed state ends the wait. An open pull
// request, one in a state this build does not recognize, and one the provider
// refuses to describe all keep the run waiting through to the hold, and the
// pull request is read before the checks on every one of those reads. These
// are the controls for the two tests above.
//
// The refused read is the one read whose refusal the body waits through; the
// control for that is the checks read refusing, which fails the step as it
// always did.
func TestOnlyAMergedOrClosedPullRequestEndsTheWait(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		state forge.PullRequestState
		// refuse makes the pull request read refuse rather than answer.
		refuse bool
	}{
		{"open", forge.PullRequestStateOpen, false},
		{"unrecognized", forge.PullRequestStateUnknown, false},
		{"not read", forge.PullRequestStateOpen, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newChecksRun(t)
			r.provider.state = tc.state
			r.provider.refuseGet = tc.refuse

			out, err := r.runOut(t.Context(), t, false)
			if err != nil {
				t.Fatalf("running the checks stage: %v", err)
			}
			if !out.Report.HasHeld() || !hasFinding(out.Report, "ci-running") {
				t.Fatalf("a %s pull request did not keep the run waiting to the hold: %+v", tc.name, out.Report)
			}
			if got := pullRequestEnd(out); got != "" {
				t.Fatalf("a %s pull request wrote %q under the pull request end", tc.name, got)
			}
			if r.provider.gets == 0 || r.provider.gets != r.provider.reads {
				t.Fatalf("the pull request was read %d times and the checks %d times, want one read of each "+
					"per read of the wait", r.provider.gets, r.provider.reads)
			}
			if waited := r.now.Sub(r.start); waited != checksTimeout {
				t.Fatalf("the stage held after %s, want the %s checks_timeout", waited, checksTimeout)
			}
		})
	}
	t.Run("control: a refused checks read fails the step", func(t *testing.T) {
		t.Parallel()
		r := newChecksRun(t)
		r.onPause = func(r *checksRun) {
			if r.pauses == 2 {
				r.provider.refuseChecks = true
			}
		}
		_, err := r.run(t.Context(), t, false)
		if !errors.Is(err, forge.ErrRefused) {
			t.Fatalf("a refused checks read answered %v, want the step failed with the refusal", err)
		}
		if r.provider.gets != 3 || r.provider.reads != 3 {
			t.Fatalf("the pull request was read %d times and the checks %d times, want 3 and 3",
				r.provider.gets, r.provider.reads)
		}
	})
}

// A segment resumed mid-wait reads the pull request on its first read, before
// any checks, so a pull request merged while nothing was reading it ends the
// run as soon as something resumes. That is PRD section 11's recovery step 1
// for this stage, and the resuming-keeps-the-wait test above is the control:
// the same resume with the pull request still open re-enters the wait.
func TestTheFirstReadAfterAResumeReadsThePullRequest(t *testing.T) {
	t.Parallel()
	r := newChecksRun(t)
	interrupted, stop := context.WithCancel(t.Context())
	r.onPause = func(r *checksRun) {
		if r.now.Sub(r.start) == 10*time.Minute {
			stop()
		}
	}
	if _, err := r.run(interrupted, t, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("a segment ended mid-wait returned %v, want the context's error", err)
	}
	// Merged while nothing was reading.
	r.provider.state = forge.PullRequestStateMerged
	r.provider.gets, r.provider.reads, r.pauses = 0, 0, 0
	r.onPause = nil

	out, err := r.runOut(t.Context(), t, false)
	if err != nil {
		t.Fatalf("resuming the checks stage: %v", err)
	}
	if got := pullRequestEnd(out); got != string(store.PullRequestMerged) {
		t.Fatalf("the resumed segment wrote %q under the pull request end, want merged", got)
	}
	if r.provider.gets != 1 || r.provider.reads != 0 || r.pauses != 0 {
		t.Fatalf("the resumed segment read the pull request %d times, the checks %d times and paused %d times, "+
			"want one read of the pull request and nothing else", r.provider.gets, r.provider.reads, r.pauses)
	}
}

// ChecksTimeoutHeld recognizes the hold an elapsed wait raises and nothing
// else, because it is what tells the service which hold it may re-read the
// pull request before resuming. The controls are every other report this
// stage produces and the one the stage node assembles: an elapsed report with
// a fix round's hold appended is a hold on a person's decision too, and is not
// this one.
func TestChecksTimeoutHeldRecognizesOnlyTheElapsedHold(t *testing.T) {
	t.Parallel()
	elapsed := newChecksRun(t).mustRun(t)
	unregistered := newChecksRun(t)
	unregistered.provider.answer = noChecks
	elapsedOnNoChecks := unregistered.mustRun(t)
	settling := newChecksRun(t)
	settling.provider.answer = green
	passed := settling.mustRun(t)
	merging := newChecksRun(t)
	merging.provider.state = forge.PullRequestStateMerged
	ended := merging.mustRun(t)

	withJudgeHold := elapsed
	withJudgeHold.Findings = append(slices.Clone(elapsed.Findings), findings.Finding{
		ID: "judge-path", Action: findings.ActionAsk, Severity: findings.SeverityWarning,
		Description: "a fix round touched a judge path, which a person decides about",
	})

	for _, tc := range []struct {
		name   string
		report findings.Report
		want   bool
	}{
		{"checks running when the timeout elapsed", elapsed, true},
		{"no checks registered when the timeout elapsed", elapsedOnNoChecks, true},
		{"control: checks passed", passed, false},
		{"control: the pull request ended the wait", ended, false},
		{"control: the elapsed hold with a fix round's hold appended", withJudgeHold, false},
		{"control: no report", findings.Report{}, false},
	} {
		if got := stages.ChecksTimeoutHeld(tc.report); got != tc.want {
			t.Errorf("ChecksTimeoutHeld(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
	if !elapsed.HasHeld() || !elapsedOnNoChecks.HasHeld() || passed.HasHeld() || ended.HasHeld() {
		t.Fatalf("the reports are not what the cases say they are: %+v, %+v, %+v, %+v",
			elapsed, elapsedOnNoChecks, passed, ended)
	}
}
