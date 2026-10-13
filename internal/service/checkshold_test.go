package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// scriptedHost is the code host a checks-hold test hands the service: one
// provider for every repository, which is the GitHub host's shape for a
// service that serves one repository.
type scriptedHost struct {
	provider *scriptedProvider
	// failOpen makes Open refuse the one way the GitHub host's Open refuses,
	// with an error wrapping forge.ErrInvalidArgument and no provider: that
	// host refuses an argument it cannot use and nothing else, since a
	// provider command line that cannot be run surfaces from a read, as the
	// scripted provider's unavailable does. It is read under the provider's
	// lock so a test can set it while the service runs.
	failOpen *bool
}

func (h scriptedHost) Open(repository string) (forge.Provider, error) {
	if repository == "" {
		return nil, forge.ErrInvalidArgument
	}
	h.provider.mu.Lock()
	failing := h.failOpen != nil && *h.failOpen
	h.provider.mu.Unlock()
	if failing {
		return nil, fmt.Errorf("forge: repository %w", forge.ErrInvalidArgument)
	}
	return h.provider, nil
}

var _ forge.Host = scriptedHost{}

// scriptedProvider answers Get with the pull request as the test says it
// stands and Checks with one pending check, and refuses everything else. It
// answers only shapes the GitHub adapter produces: a pull request numbered as
// asked, in a state the adapter's reading maps gh's words onto or the zero
// state any other word maps to, and a check list placed on a head commit. It
// is safe for concurrent use because the service reads it from its own
// goroutines.
type scriptedProvider struct {
	mu    sync.Mutex
	state forge.PullRequestState
	gets  int
	// refuse makes every read of the pull request the refusal the GitHub
	// adapter returns for a provider that ran, answered, and refused, and
	// unavailable the one it returns for a provider command line that could
	// not be run at all, which is where a missing gh surfaces.
	refuse      bool
	unavailable bool
	// deadlines records, per read, whether the context the read ran under
	// carried a deadline, which is what a bounded read looks like from the
	// provider's side.
	deadlines []bool
	// onGet runs on each read, outside the lock, before the answer, with the
	// count of reads so far; a test uses it to act while a read is in
	// flight.
	onGet func(reads int)
}

func (p *scriptedProvider) set(state forge.PullRequestState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = state
}

func (p *scriptedProvider) reads() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gets
}

func (p *scriptedProvider) boundedReads() []bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]bool(nil), p.deadlines...)
}

func (p *scriptedProvider) Get(ctx context.Context, number int) (forge.PullRequest, error) {
	p.mu.Lock()
	p.gets++
	reads := p.gets
	_, bounded := ctx.Deadline()
	p.deadlines = append(p.deadlines, bounded)
	state, refuse, unavailable, hook := p.state, p.refuse, p.unavailable, p.onGet
	p.mu.Unlock()
	if hook != nil {
		hook(reads)
	}
	if err := ctx.Err(); err != nil {
		return forge.PullRequest{}, err
	}
	if unavailable {
		return forge.PullRequest{}, &forge.Refusal{Reason: forge.ReasonUnavailable, Op: "get",
			Detail: "the provider command line could not be run"}
	}
	if refuse {
		return forge.PullRequest{}, &forge.Refusal{Reason: forge.ReasonRejected, Op: "get",
			Detail: "the provider refused to describe the pull request"}
	}
	return forge.PullRequest{Number: number, State: state, HeadCommit: "0123abc"}, nil
}

func (p *scriptedProvider) Checks(ctx context.Context, _ int) (forge.ChecksReport, error) {
	if err := ctx.Err(); err != nil {
		return forge.ChecksReport{}, err
	}
	return forge.ChecksReport{HeadCommit: "0123abc", Runs: []forge.CheckRun{
		{Name: "build", State: forge.CheckStatePending},
	}}, nil
}

func (p *scriptedProvider) Find(context.Context, string) (forge.PullRequest, bool, error) {
	return forge.PullRequest{}, false, errors.New("nothing here finds a pull request: a merged or closed one is read by number")
}

func (p *scriptedProvider) Open(context.Context, forge.OpenSpec) (forge.PullRequest, error) {
	return forge.PullRequest{}, errors.New("nothing here opens a pull request")
}

func (p *scriptedProvider) UpdateBody(context.Context, int, string) (forge.PullRequest, error) {
	return forge.PullRequest{}, errors.New("nothing here updates a pull request")
}

var _ forge.Provider = (*scriptedProvider)(nil)

// checksHoldSubject is a home and a subject arranged so that a run walks
// straight to the checks stage: the repository record names a repository on
// the code host, the gate reaches the subject's own upstream for it, and the
// operator's configuration bounds the checks wait by a timeout of seconds.
type checksHoldSubject struct {
	home     *home.Home
	subject  string
	provider *scriptedProvider
	failOpen *bool
}

// setFailOpen makes or unmakes the host refuse to open a provider.
func (c checksHoldSubject) setFailOpen(failing bool) {
	c.provider.mu.Lock()
	defer c.provider.mu.Unlock()
	*c.failOpen = failing
}

// checksTimeoutUnderTest is the checks_timeout the home is configured with.
// The checks body paces a wait a minute at a time or the time left, so a run
// holds after one pause of this length.
const checksTimeoutUnderTest = "2s"

func newChecksHoldSubject(t *testing.T) checksHoldSubject {
	t.Helper()
	const upstream = "https://github.com/dayamjz/assistant.git"
	h := newHome(t)
	subject := newSubject(t)
	local := git(t, subject, "remote", "get-url", "origin")
	recordRepositoryWithUpstream(t, h, subject, upstream)
	// The record's URL names a host nothing here reaches; the gate fetches
	// it from the subject's own upstream, on the terms observeForgeRepository
	// states.
	git(t, gateRepositoryOf(t, h, subject), "config", "url."+local+".insteadOf", upstream)
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"checks_timeout": "`+checksTimeoutUnderTest+`"}`+"\n"), 0o600); err != nil {
		t.Fatalf("writing the home's configuration: %v", err)
	}
	return checksHoldSubject{home: h, subject: subject,
		provider: &scriptedProvider{state: forge.PullRequestStateOpen}, failOpen: new(bool)}
}

// options opens the service on this subject: the real checks body behind
// stubs that pass every stage before it, the pull request stage's stub
// recording pull request 7 the way the real one records the number it opened,
// and the scripted host. holdAtIntent makes the first stage hold on a person
// instead, for the test about a hold that is not the checks stage's.
func (c checksHoldSubject) options(t *testing.T, holdAtIntent bool) service.Options {
	t.Helper()
	opts := options(t, c.home)
	opts.Forge = scriptedHost{provider: c.provider, failOpen: c.failOpen}
	opts.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := stages.All(deps)
		for _, stage := range pipeline.Order() {
			switch stage {
			case pipeline.StageCI:
				// The real body, handed the service's own dependencies.
			case pipeline.StagePR:
				*all.At(stage) = pipeline.Implementation{
					Writes: []pipeline.Key{pipeline.KeyPullRequest},
					NewBody: func() pipeline.Body {
						return func(context.Context, pipeline.Input) (pipeline.Output, error) {
							return pipeline.Output{
								Report: findings.Report{Summary: "recorded pull request 7"},
								Writes: map[pipeline.Key]graph.Value{pipeline.KeyPullRequest: graph.TextValue("7")},
							}, nil
						}
					},
				}
			case pipeline.StageIntent:
				*all.At(stage) = passingStage(stage)
				if holdAtIntent {
					*all.At(stage) = pipeline.ConstantStages("held for a decision", findings.Finding{
						ID: "decide", Action: findings.ActionAsk, Severity: findings.SeverityWarning,
						Description: "a decision a person has to make",
					}).Intent
				}
			default:
				*all.At(stage) = passingStage(stage)
			}
		}
		return all
	}
	return opts
}

// passingStage is a stage body that reports nothing, so a run walks past it.
func passingStage(stage pipeline.Stage) pipeline.Implementation {
	return pipeline.Implementation{
		NewBody: func() pipeline.Body {
			return func(context.Context, pipeline.Input) (pipeline.Output, error) {
				return pipeline.Output{Report: findings.Report{Summary: stage.String() + " found nothing"}}, nil
			}
		},
	}
}

// startAtChecksHold starts a run and asserts it stopped at the hold the
// checks stage raises when checks_timeout elapses, returning the run.
func startAtChecksHold(t *testing.T, c checksHoldSubject, client *ipc.Client) machine.Run {
	t.Helper()
	run := startRun(t, client, c.subject)
	decision := holdingAt(t, run, pipeline.StageCI)
	if !stages.ChecksTimeoutHeld(findings.Report{Findings: decision.Findings}) {
		t.Fatalf("the run holds at the checks stage on something other than an elapsed wait: %+v", decision.Findings)
	}
	return run
}

// readHold reads the hold the service files for a run's elapsed checks wait,
// and reports whether there is one.
func readHold(t *testing.T, h *home.Home, runID string) (store.Hold, bool) {
	t.Helper()
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	hold, err := records.Hold(t.Context(), service.ChecksHoldKey(runID))
	if errors.Is(err, store.ErrNotFound) {
		return store.Hold{}, false
	}
	if err != nil {
		t.Fatalf("reading the checks hold of run %s: %v", runID, err)
	}
	return hold, true
}

// mustBeResolvedByTheProgram asserts the hold carries the resolution PRD
// section 8 gives the service: itself as resolver, the state it read as what
// it recorded.
func mustBeResolvedByTheProgram(t *testing.T, hold store.Hold, state string) {
	t.Helper()
	by, known := hold.ResolvedBy.Get()
	if !known || by != store.ResolvedByProgram() {
		t.Fatalf("the hold was resolved by %v, want the program", hold.ResolvedBy)
	}
	resolution, known := hold.Resolution.Get()
	if !known || !strings.Contains(resolution, "read as "+state) {
		t.Fatalf("the hold's resolution is %v, want the state read, %s", hold.Resolution, state)
	}
}

// A run held because checks_timeout elapsed, whose pull request was merged
// while the service was down, is ended by recovery before anything is served:
// PRD section 11's step 3. The hold is closed by an explicit resolution the
// service records as its own with the state it read, and the run reads as
// passed, with the record saying a merge ended it; the checks stage's report
// is untouched, so it still says the checks were not read to a verdict.
func TestRecoveryEndsAnElapsedChecksHoldWhosePullRequestWasMerged(t *testing.T) {
	requiresIdentifiedPeer(t)
	c := newChecksHoldSubject(t)

	var runID string
	withServiceOptions(t, c.options(t, false), func(running serviceUnderTest) {
		runID = startAtChecksHold(t, c, running.client).Record.ID
	})
	if _, filed := readHold(t, c.home, runID); filed {
		t.Fatal("a hold was filed for the run before anything closed it")
	}
	c.provider.set(forge.PullRequestStateMerged)
	before := c.provider.reads()

	withServiceOptions(t, c.options(t, false), func(running serviceUnderTest) {
		run := runByID(t, running.client, runID)
		if run.Outcome != machine.OutcomePassed {
			t.Fatalf("after recovery the run reads %s, want passed: %+v", run.Outcome, run)
		}
		if run.MergeRecord == nil || run.MergeRecord.End != store.PullRequestMerged ||
			run.MergeRecord.PullRequest != 7 || run.MergeRecord.Head != "0123abc" || run.MergeRecord.Run != runID {
			t.Fatalf("the merge record is %+v, want the read that ended the run", run.MergeRecord)
		}
		if run.Decision != nil {
			t.Fatal("a run its pull request ended still offers a decision")
		}
		if got := run.NextAction(); got != machine.OutcomePassed.NextAction() {
			t.Fatalf("the run says %q, want the passed outcome's action", got)
		}
		report := stageView(t, run, pipeline.StageCI).Report
		if report == nil || !strings.Contains(report.Summary, "checks_timeout") {
			t.Fatalf("the checks stage's report was rewritten: %+v", report)
		}
	})
	if c.provider.reads() != before+1 {
		t.Fatalf("recovery read the pull request %d times, want once", c.provider.reads()-before)
	}
	if bounded := c.provider.boundedReads(); !bounded[len(bounded)-1] {
		t.Fatal("recovery's read ran under no deadline, so a provider that never answers would hold Open")
	}
	hold, filed := readHold(t, c.home, runID)
	if !filed {
		t.Fatal("recovery ended the run without recording the hold it closed")
	}
	mustBeResolvedByTheProgram(t, hold, "merged")
	if record := readMergeRecord(t, c.home, runID); record == nil || record.End != store.PullRequestMerged {
		t.Fatalf("the store holds the merge record %+v, want the merge", record)
	}
}

// readMergeRecord reads a run's merge record off the store, nil when there is
// none.
func readMergeRecord(t *testing.T, h *home.Home, runID string) *store.MergeRecord {
	t.Helper()
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	record, found, err := records.MergeRecord(t.Context(), runID)
	if err != nil {
		t.Fatalf("reading the merge record of run %s: %v", runID, err)
	}
	if !found {
		return nil
	}
	return &record
}

// Answering the same hold re-reads the pull request before the answer is
// taken. A pull request read as closed without a merge closes the hold on the
// same terms and fails the run, with the reason the record carries, and the
// answer is answered with the run as it then stands rather than applied.
func TestRespondingToAnElapsedChecksHoldReReadsThePullRequestFirst(t *testing.T) {
	requiresIdentifiedPeer(t)
	c := newChecksHoldSubject(t)

	withServiceOptions(t, c.options(t, false), func(running serviceUnderTest) {
		runID := startAtChecksHold(t, c, running.client).Record.ID
		c.provider.set(forge.PullRequestStateClosed)

		run := answer(t, running.client, runID, string(pipeline.OutcomeApproved))
		if run.Outcome != machine.OutcomeFailed {
			t.Fatalf("answering the hold of a closed pull request answered %s, want failed: %+v", run.Outcome, run)
		}
		if run.Reason != "pull request closed without merging" {
			t.Fatalf("the run's reason is %q, want the one PRD section 5 fixes", run.Reason)
		}
		if run.MergeRecord == nil || run.MergeRecord.End != store.PullRequestClosed {
			t.Fatalf("the merge record is %+v, want the close", run.MergeRecord)
		}
		if !strings.Contains(run.AnswerNotApplied, "already closed") {
			t.Fatalf("the answer says %q about the answer it did not apply, want that the pull request had already closed", run.AnswerNotApplied)
		}
		if view := stageView(t, run, pipeline.StageCI); view.Outcome != pipeline.OutcomeHeld {
			t.Fatalf("the answer was applied to the checks stage, which now reads %s", view.Outcome)
		}
		hold, filed := readHold(t, c.home, runID)
		if !filed {
			t.Fatal("the run was ended without recording the hold that was closed")
		}
		mustBeResolvedByTheProgram(t, hold, "closed")
	})
}

// Only a positively read merged or closed state closes the hold. An open
// pull request and one in a state this build does not recognize leave the
// hold to the person: the answer is applied, the run goes on past the checks
// stage, and no hold is recorded as closed by the program. The pull request
// is still read first, which the count shows.
func TestOnlyAMergedOrClosedPullRequestClosesTheChecksHold(t *testing.T) {
	requiresIdentifiedPeer(t)
	for _, tc := range []struct {
		name  string
		state forge.PullRequestState
	}{
		{"open", forge.PullRequestStateOpen},
		{"unrecognized", forge.PullRequestStateUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newChecksHoldSubject(t)
			withServiceOptions(t, c.options(t, false), func(running serviceUnderTest) {
				runID := startAtChecksHold(t, c, running.client).Record.ID
				c.provider.set(tc.state)
				before := c.provider.reads()

				run := answer(t, running.client, runID, string(pipeline.OutcomeApproved))
				if c.provider.reads() != before+1 {
					t.Fatalf("answering read the pull request %d times, want once before the answer", c.provider.reads()-before)
				}
				if run.Outcome != machine.OutcomeChecksPassed {
					t.Fatalf("a %s pull request answered %s, want the answer applied and the run checks-passed", tc.name, run.Outcome)
				}
				if run.MergeRecord != nil || run.AnswerNotApplied != "" {
					t.Fatalf("the run carries a merge record %+v or an unapplied answer %q", run.MergeRecord, run.AnswerNotApplied)
				}
				if _, filed := readHold(t, c.home, runID); filed {
					t.Fatal("a hold was recorded as closed by the program for a pull request that did not end the run")
				}
			})
		})
	}
}

// A hold on a person's decision about a finding is never re-read, by
// recovery or by an answer: the pull request's state must not answer a
// question a person was asked. The run here holds at the first stage with its
// pull request already merged, and neither a restart nor the answer reads the
// pull request until the checks body itself does, once the answer has carried
// the run there; that read is the live wait's, which ends the run on PRD
// section 5's terms, and that is what makes the count after the answer a
// control rather than an absence.
func TestAHoldOnAPersonsDecisionIsNeverReRead(t *testing.T) {
	requiresIdentifiedPeer(t)
	c := newChecksHoldSubject(t)
	c.provider.set(forge.PullRequestStateMerged)

	var runID string
	withServiceOptions(t, c.options(t, true), func(running serviceUnderTest) {
		run := startRun(t, running.client, c.subject)
		holdingAt(t, run, pipeline.StageIntent)
		runID = run.Record.ID
	})
	if c.provider.reads() != 0 {
		t.Fatalf("the pull request was read %d times before anything reached the checks stage", c.provider.reads())
	}

	withServiceOptions(t, c.options(t, true), func(running serviceUnderTest) {
		if c.provider.reads() != 0 {
			t.Fatalf("recovery read the pull request %d times for a run held on a person's decision", c.provider.reads())
		}
		held := runByID(t, running.client, runID)
		if held.Outcome != machine.OutcomeDecision || held.Record.Status != store.RunHeld {
			t.Fatalf("recovery moved a run held on a person's decision: %s, %s", held.Outcome, held.Record.Status)
		}

		run := answer(t, running.client, runID, string(pipeline.OutcomeApproved))
		if _, filed := readHold(t, c.home, runID); filed {
			t.Fatal("the hold on a person's decision was recorded as closed by the program")
		}
		// The control: the answer carried the run to the checks stage, whose
		// own first read found the merge and ended the wait.
		if run.Outcome != machine.OutcomePassed || c.provider.reads() != 1 {
			t.Fatalf("after the answer the run reads %s with the pull request read %d times, want passed after the "+
				"checks stage's one read", run.Outcome, c.provider.reads())
		}
		if view := stageView(t, run, pipeline.StageCI); view.Report == nil || !strings.Contains(view.Report.Summary, "merged") {
			t.Fatalf("the checks stage does not report the merge it read: %+v", view.Report)
		}
	})
}

// A pull request that could not be read leaves the hold to the person, by
// recovery and by an answer alike, and a person's answer is then taken: a
// host that cannot open a provider, a provider command line that cannot be
// run, and a provider that refuses the read are each a typed answer the
// service logs and treats as not read, never an error that stops a person
// answering their own hold over a code host that is down.
// The control is the merged state those same runs would otherwise have been
// ended on: once the host answers again, the next look ends the run.
func TestAPullRequestThatCannotBeReadLeavesTheHoldToThePerson(t *testing.T) {
	requiresIdentifiedPeer(t)
	for _, tc := range []struct {
		name string
		fail func(c checksHoldSubject, failing bool)
	}{
		{"the host cannot open a provider", func(c checksHoldSubject, failing bool) { c.setFailOpen(failing) }},
		{"the provider command line cannot be run", func(c checksHoldSubject, failing bool) {
			c.provider.mu.Lock()
			defer c.provider.mu.Unlock()
			c.provider.unavailable = failing
		}},
		{"the provider refuses the read", func(c checksHoldSubject, failing bool) {
			c.provider.mu.Lock()
			defer c.provider.mu.Unlock()
			c.provider.refuse = failing
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newChecksHoldSubject(t)
			var runID string
			withServiceOptions(t, c.options(t, false), func(running serviceUnderTest) {
				runID = startAtChecksHold(t, c, running.client).Record.ID
			})
			c.provider.set(forge.PullRequestStateMerged)
			tc.fail(c, true)

			withServiceOptions(t, c.options(t, false), func(running serviceUnderTest) {
				held := runByID(t, running.client, runID)
				if held.Outcome != machine.OutcomeDecision {
					t.Fatalf("after recovery with %s the run reads %s, want the hold left standing", tc.name, held.Outcome)
				}
				if _, filed := readHold(t, c.home, runID); filed {
					t.Fatal("a hold was recorded as closed by the program on a pull request that was not read")
				}
				// Answering is not blocked by the failure: the answer is
				// taken and the run goes on.
				run := answer(t, running.client, runID, string(pipeline.OutcomeApproved))
				if run.Outcome != machine.OutcomeChecksPassed || run.AnswerNotApplied != "" {
					t.Fatalf("answering while %s answered %s with %q, want the answer applied", tc.name, run.Outcome, run.AnswerNotApplied)
				}
			})

			// The control: the same failure lifted ends a fresh run's hold.
			tc.fail(c, false)
			c.provider.set(forge.PullRequestStateOpen)
			other := newChecksHoldSubject(t)
			other.provider, other.failOpen = c.provider, c.failOpen
			var otherID string
			withServiceOptions(t, other.options(t, false), func(running serviceUnderTest) {
				otherID = startAtChecksHold(t, other, running.client).Record.ID
			})
			c.provider.set(forge.PullRequestStateMerged)
			withServiceOptions(t, other.options(t, false), func(running serviceUnderTest) {
				if run := runByID(t, running.client, otherID); run.Outcome != machine.OutcomePassed {
					t.Fatalf("with the host answering again the run reads %s, want passed", run.Outcome)
				}
			})
		})
	}
}

// The resolution and the run's move are one transaction. A hold already
// resolved by the program while the run is still held is left as it stands
// and the move still happens, which repairs a record left that way; and a
// move refused because a person answered the hold between the read and the
// write rolls the resolution back with it, so no hold row says the program
// resolved a hold on a run a person resumed.
func TestTheHoldClosureAndTheMoveAreOneTransaction(t *testing.T) {
	requiresIdentifiedPeer(t)
	t.Run("a hold already resolved is left and the run still moves", func(t *testing.T) {
		c := newChecksHoldSubject(t)
		var runID string
		withServiceOptions(t, c.options(t, false), func(running serviceUnderTest) {
			runID = startAtChecksHold(t, c, running.client).Record.ID
		})
		records := openRecords(t, c.home)
		key := service.ChecksHoldKey(runID)
		if _, err := records.RegisterHold(t.Context(), store.Hold{Key: key, RunID: store.Known(runID), Subject: "left behind"}); err != nil {
			t.Fatalf("RegisterHold: %v", err)
		}
		if _, err := records.ResolveHold(t.Context(), key, "an earlier reading", store.ResolvedByProgram()); err != nil {
			t.Fatalf("ResolveHold: %v", err)
		}
		if err := records.Close(); err != nil {
			t.Fatalf("closing the store: %v", err)
		}
		c.provider.set(forge.PullRequestStateMerged)

		withServiceOptions(t, c.options(t, false), func(running serviceUnderTest) {
			if run := runByID(t, running.client, runID); run.Outcome != machine.OutcomePassed {
				t.Fatalf("the run reads %s, want passed: the move was not made because the hold was already resolved", run.Outcome)
			}
		})
		hold, _ := readHold(t, c.home, runID)
		if resolution, _ := hold.Resolution.Get(); resolution != "an earlier reading" {
			t.Fatalf("the earlier resolution was rewritten: %+v", hold)
		}
	})

	t.Run("a move refused by a person's answer rolls the resolution back", func(t *testing.T) {
		c := newChecksHoldSubject(t)
		withServiceOptions(t, c.options(t, false), func(running serviceUnderTest) {
			runID := startAtChecksHold(t, c, running.client).Record.ID
			c.provider.set(forge.PullRequestStateMerged)
			// While the first answer's read is in flight, a second answer
			// cancels the run; its own read, the second, finds the pull
			// request open and the cancel is taken. The first read then
			// answers merged, and the move it asks for finds the run no
			// longer held.
			type answered struct {
				run machine.Run
				err error
			}
			cancelled := make(chan answered, 1)
			second := dial(t, running.service)
			before := c.provider.reads()
			c.provider.onGet = func(reads int) {
				if reads != before+1 {
					return
				}
				c.provider.set(forge.PullRequestStateOpen)
				var run machine.Run
				err := second.Call(context.Background(), ipc.MethodRunRespond,
					machine.RespondRequest{Run: runID, Answer: string(pipeline.OutcomeCancelled)}, &run)
				cancelled <- answered{run, err}
				c.provider.set(forge.PullRequestStateMerged)
			}
			var first machine.Run
			err := running.client.Call(t.Context(), ipc.MethodRunRespond,
				machine.RespondRequest{Run: runID, Answer: string(pipeline.OutcomeApproved)}, &first)
			if got := <-cancelled; got.err != nil || got.run.Outcome != machine.OutcomeCancelled {
				t.Fatalf("the cancel answered %s, %v, want cancelled", got.run.Outcome, got.err)
			}
			if err == nil {
				t.Fatalf("the overtaken answer was answered %s, want a refusal: the run was cancelled under it", first.Outcome)
			}
			if c.provider.reads() != before+2 {
				t.Fatalf("the pull request was read %d times, want the two answers' reads", c.provider.reads()-before)
			}
			if _, filed := readHold(t, c.home, runID); filed {
				t.Fatal("the refused move left a hold saying the program resolved it on a run a person ended")
			}
			if record := readMergeRecord(t, c.home, runID); record != nil {
				t.Fatalf("the refused move left a merge record: %+v", record)
			}
		})
	})
}
