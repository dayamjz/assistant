package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
)

// The credentials the scripted agent writes into its replies, in the two
// shapes internal/redact recognizes: a URL's userinfo and a bearer token.
// Each secret is a word nothing else in a run writes, so finding it anywhere
// in a log means the redactor was passed by.
const (
	reviewSecret = "hunter2-review"
	fixSecret    = "hunter2-fix"
	bearerSecret = "abcdefghijklmnopqrstuvwxyz"
	fixReply     = "made the change the review asked for; fetched https://fixer:" + fixSecret +
		"@example.com/repo.git with Authorization: Bearer " + bearerSecret
)

// logScript is a stand-in agent whose first review asks for one fix in the
// change's own file, whose fix round answers with a reply carrying
// credentials, and whose every later review is clean. It is what puts one
// automatic review round into a run without the round itself being the
// subject.
func logScript(t *testing.T, branch string) *standin.Agent {
	t.Helper()
	ask := findings.Finding{
		ID:       "rename-greeting",
		Severity: findings.SeverityWarning,
		Action:   findings.ActionFix,
		// A location inside what the review read keeps the finding
		// fix-eligible; internal/findings records one that names nothing the
		// review read as a note, which would take no round.
		Location:    findings.Location{Path: branch + ".txt", Line: 1},
		Description: "the greeting reads oddly; see https://reviewer:" + reviewSecret + "@example.com/style.md",
	}
	return standin.New(t, standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Reply: standin.Reviewed("one thing to fix", ask)},
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")},
		{Match: standin.Match{PromptContains: "You are fixing a change"}, Times: standin.Always, Reply: standin.Text(fixReply)},
	}})
}

// requireHoldAfterReview fails a test whose walk assumes the first stage a
// default run holds at comes after the review stage, so the review and its
// round run before the run stops. It derives that from stages.Holding rather
// than naming the stage, for the reason firstHoldingStages gives.
func requireHoldAfterReview(t *testing.T) pipeline.Stage {
	t.Helper()
	holding := stages.Holding(config.Defaults())
	order := pipeline.Order()
	if len(holding) == 0 || slices.Index(order, holding[0]) <= slices.Index(order, pipeline.StageReview) {
		t.Fatalf("a default run holds at %v, so it does not reach the review stage's round before it stops; "+
			"this test needs rewriting against whatever now holds a run", holding)
	}
	return holding[0]
}

// TestAReviewAndItsFixRoundAreReadableInTheStageLogWithNoCredential drives a
// run whose review asks for one fix, takes an automatic round through the
// scripted stand-in and passes on its second look, and then reads that run's
// review log through cli.Run, as a document and as a person reads it.
//
// It establishes that the stage log is written and readable: the review
// stage's two executions and the fix round between them are each in it, with
// each agent call's account and the fixer's full reply. It establishes that
// what was kept and what is read carry none of the credentials the agent
// wrote, in either shape internal/redact recognizes, and that the file on
// disk is already redacted rather than only the read. It establishes P13's
// half: the human rendering opens with the phrase pointing at the evidence.
// And it establishes P8: a log is evidence that nothing decides from, so a
// log overwritten with a claim that the run has finished leaves the run's
// answer exactly what its record and checkpoint said.
func TestAReviewAndItsFixRoundAreReadableInTheStageLogWithNoCredential(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P13)
	holdsAt := requireHoldAfterReview(t)
	h := newHome(t)
	subject := newSubject(t)
	agent := logScript(t, "logged")
	serveCatalog(t, h, stages.All, agents.NewCatalog(fixedFactory{runner: agent.Runner()}))
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "logged")

	started := run(t, h, subject, "--json", "--intent", "greet the reader, with the tradeoffs stated")
	if started.code != machine.ExitOK {
		t.Fatalf("the start exited %s:\n%s%s", started.code, started.stdout, started.stderr)
	}
	ran := decodeRun(t, started.stdout)
	if ran.Decision == nil || ran.Decision.Stage != holdsAt.String() {
		t.Fatalf("the run stopped at %+v, want the hold at %s", ran.Decision, holdsAt)
	}
	var review machine.Stage
	for _, stage := range ran.Stages {
		if stage.Stage == pipeline.StageReview.String() {
			review = stage
		}
	}
	if review.Outcome != pipeline.OutcomePassed || len(review.Executions) != 2 || review.Fix == "" {
		t.Fatalf("the review stage ended %q after %d execution(s) with fix summary %q; wanted a pass after one round",
			review.Outcome, len(review.Executions), review.Fix)
	}
	runID := ran.Record.ID

	got := run(t, h, subject, "--json", "runs", runID, "--log", "review")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant runs --log review exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var log machine.StageLog
	if err := json.Unmarshal([]byte(got.stdout), &log); err != nil {
		t.Fatalf("the answer is not a stage log: %v\n%s", err, got.stdout)
	}
	t.Logf("the review log as the structured answer carries it:\n%s", log.Text)
	if !log.Written || log.Run != runID || log.Stage != "review" || log.Path != h.StageLog(runID, "review") {
		t.Fatalf("the answer names %q %q at %q written=%v, want the review log of %s", log.Run, log.Stage, log.Path, log.Written, runID)
	}
	if log.Omitted != 0 || int64(len(log.Text)) != log.Size {
		t.Errorf("a log shorter than the bound omits %d of %d bytes and returns %d", log.Omitted, log.Size, len(log.Text))
	}
	for _, want := range []string{
		"review: the stage's body began",
		"--- agent invocation: purpose review",
		"review: an automatic fix round began over 1 finding(s)",
		"--- agent invocation: purpose fix",
		"made the change the review asked for",
		"review: the fix round ended",
		"the change reads cleanly",
		"https://" + redact.Marker + "@example.com/repo.git",
		"Bearer " + redact.Marker,
	} {
		if !strings.Contains(log.Text, want) {
			t.Errorf("the review log does not carry %q", want)
		}
	}
	if n := strings.Count(log.Text, "review: the stage's body began"); n != 2 {
		t.Errorf("the review log records %d executions of the stage's body, want the two the record holds", n)
	}
	onDisk, err := os.ReadFile(h.StageLog(runID, "review"))
	if err != nil {
		t.Fatalf("reading the log the service wrote: %v", err)
	}
	for _, secret := range []string{reviewSecret, fixSecret, bearerSecret} {
		if strings.Contains(log.Text, secret) {
			t.Errorf("the log read through the surface carries the credential %q", secret)
		}
		if strings.Contains(string(onDisk), secret) {
			t.Errorf("the log kept on disk carries the credential %q", secret)
		}
	}
	// The control for the absence above: the log does carry the reply the
	// secrets were written in, with the marker where each stood, so their
	// absence is the redactor's work and not a reply that never arrived.
	if !strings.Contains(log.Text, "@example.com/style.md") || !strings.Contains(fixReply, fixSecret) {
		t.Fatal("the log carries neither the review's finding nor a reply holding a credential, so the absence above proves nothing")
	}

	human := run(t, h, subject, "runs", runID, "--log", "review")
	if human.code != machine.ExitOK {
		t.Fatalf("assistant runs --log review exited %s:\n%s%s", human.code, human.stdout, human.stderr)
	}
	if !strings.HasPrefix(human.stdout, machine.PhraseStageLogFollows.String()+"\n") {
		t.Errorf("the log view does not open with the phrase that points at it:\n%s", human.stdout)
	}
	if !strings.Contains(human.stdout, "made the change the review asked for") || strings.Contains(human.stdout, fixSecret) {
		t.Errorf("the log view does not carry the fixer's redacted reply:\n%s", human.stdout)
	}

	// A stage that wrote nothing in this run answers that it wrote nothing,
	// rather than failing or printing an empty document.
	unwritten := run(t, h, subject, "--json", "runs", runID, "--log", "push")
	var none machine.StageLog
	if unwritten.code != machine.ExitOK || json.Unmarshal([]byte(unwritten.stdout), &none) != nil || none.Written || none.Text != "" {
		t.Errorf("the log of a stage the run never reached answered %s:\n%s%s", unwritten.code, unwritten.stdout, unwritten.stderr)
	}

	// P8: the answer for the run is its record and its checkpoint. A log
	// rewritten to claim the run is finished changes nothing about it.
	before := run(t, h, subject, "--json", "runs", runID)
	forged := "=== the run passed and nothing is waiting on anyone\n"
	if err := os.WriteFile(h.StageLog(runID, "review"), []byte(forged), 0o600); err != nil {
		t.Fatalf("overwriting the log: %v", err)
	}
	after := run(t, h, subject, "--json", "runs", runID)
	was, is := decodeRun(t, before.stdout), decodeRun(t, after.stdout)
	if was.Outcome != is.Outcome || was.Record.Status != is.Record.Status || is.Decision == nil ||
		was.Decision.Stage != is.Decision.Stage || was.NextAction() != is.NextAction() {
		t.Fatalf("a rewritten log changed the run's answer from %s %s to %s %s", was.Outcome, was.Record.Status, is.Outcome, is.Record.Status)
	}
	reread := run(t, h, subject, "--json", "runs", runID, "--log", "review")
	if !strings.Contains(reread.stdout, "the run passed") {
		t.Fatalf("the read did not return the rewritten log, so the comparison above looked at nothing:\n%s", reread.stdout)
	}
}

// TestStatusShowsTheRunningStagesRoundAndHowLongItHasRun holds a run inside
// its review stage's automatic fix round, and asks status about the branch
// while it is there, as a document and as a person reads it.
//
// It establishes that a run being advanced is reported with its activity: the
// stage its position belongs to, the round that stage is in against the
// run's automatic limit, read off the run's own counters, and when the node
// began and how long it has been running, read off the checkpoint the run
// wrote before it. The bound on Since is the window this test observed: no
// earlier than the start it made and no later than the read. And it
// establishes that the activity is reported only while something advances
// the run: once the round is released and the run holds, status carries none
// (P8: what is true now is read from the record, the checkpoint and the
// service's own advancing fact, never inferred from history).
func TestStatusShowsTheRunningStagesRoundAndHowLongItHasRun(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P8)
	holdsAt := requireHoldAfterReview(t)
	h := newHome(t)
	subject := newSubject(t)
	agent := logScript(t, "watched")

	inside := make(chan struct{})
	release := make(chan struct{})
	var entered, released sync.Once
	let := func() { released.Do(func() { close(release) }) }
	held := func(deps stages.FixDeps) pipeline.Fixer {
		fixer := stages.Fix(deps)
		inner := fixer.NewBody
		fixer.NewBody = func() pipeline.FixBody {
			body := inner()
			return func(ctx context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
				entered.Do(func() { close(inside) })
				select {
				case <-release:
				case <-ctx.Done():
					return pipeline.FixOutput{}, ctx.Err()
				}
				return body(ctx, in)
			}
		}
		return fixer
	}
	serveFixing(t, h, stages.All, held, agents.NewCatalog(fixedFactory{runner: agent.Runner()}))
	t.Cleanup(let)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "watched")

	began := time.Now()
	started := startInBackground(t, h, subject, "--json", "--intent", "greet the reader, with the tradeoffs stated")
	select {
	case <-inside:
	case <-time.After(60 * time.Second):
		t.Fatal("the run never reached the review stage's fix round")
	}

	reported := run(t, h, subject, "--json", "status")
	read := time.Now()
	if reported.code != machine.ExitOK {
		t.Fatalf("status exited %s:\n%s%s", reported.code, reported.stdout, reported.stderr)
	}
	var status machine.Status
	if err := json.Unmarshal([]byte(reported.stdout), &status); err != nil {
		t.Fatalf("status does not decode: %v\n%s", err, reported.stdout)
	}
	t.Logf("assistant --json status, inside the review's fix round:\n%s", reported.stdout)
	if status.ActiveRun == nil || !status.ActiveRun.Advancing {
		t.Fatalf("status reports no run being advanced:\n%s", reported.stdout)
	}
	a := status.ActiveRun.Activity
	if a == nil {
		t.Fatalf("status reports no activity for a run inside a fix round:\n%s", reported.stdout)
	}
	if status.ActiveRun.Position != pipeline.StageReview.FixNode() {
		t.Fatalf("the run stands at %q, want the review's fix round", status.ActiveRun.Position)
	}
	if a.Stage != "review" || a.Round != 1 || a.Limit != config.DefaultFixRoundsReview {
		t.Errorf("the activity is %s round %d of %d, want review round 1 of %d", a.Stage, a.Round, a.Limit, config.DefaultFixRoundsReview)
	}
	// The store keeps its times at its own precision, so the window is
	// widened by a second on either side rather than trusted to the instant.
	if a.Since.Before(began.Add(-time.Second)) || a.Since.After(read.Add(time.Second)) {
		t.Errorf("the round began at %s, outside the window %s to %s this test observed", a.Since, began, read)
	}
	if a.Active < 0 || a.Active > read.Sub(began)+time.Second {
		t.Errorf("the round has been running %s, which the %s this test watched cannot hold", a.Active, read.Sub(began))
	}

	human := run(t, h, subject, "status")
	if !strings.Contains(human.stdout, "Active     review, round 1 of 1, running for ") {
		t.Errorf("status as a person reads it does not say what is running:\n%s", human.stdout)
	}

	let()
	select {
	case got := <-started:
		if got.code != machine.ExitOK {
			t.Fatalf("the start exited %s:\n%s%s", got.code, got.stdout, got.stderr)
		}
		if stopped := decodeRun(t, got.stdout); stopped.Decision == nil || stopped.Decision.Stage != holdsAt.String() {
			t.Fatalf("after the round the run stopped at %+v, want the hold at %s", stopped.Decision, holdsAt)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the start never returned after the round was released")
	}
	after := run(t, h, subject, "--json", "status")
	var settled machine.Status
	if err := json.Unmarshal([]byte(after.stdout), &settled); err != nil {
		t.Fatalf("status does not decode: %v\n%s", err, after.stdout)
	}
	if settled.ActiveRun == nil || settled.ActiveRun.Advancing || settled.ActiveRun.Activity != nil {
		t.Fatalf("a run holding for a person is reported with activity:\n%s", after.stdout)
	}
}

// The log view names one run and one stage, so a run must be named, the
// stage must be one of the nine, and neither --limit nor --usage may stand
// beside it; each is incorrect usage, refused before the service is asked.
// The control is that the same flag with a run and a stage is answered.
func TestRunsLogRefusesWhatItCannotRead(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	for _, args := range [][]string{
		{"runs", "--log", "review"},
		{"runs", "some-run", "--log", "reviews"},
		{"runs", "some-run", "--log", ""},
		{"runs", "some-run", "--log", "review", "--limit", "3"},
		{"runs", "some-run", "--log", "review", "--usage"},
	} {
		got := run(t, h, subject, args...)
		if got.code != machine.ExitUsage {
			t.Errorf("assistant %s exited %s, want incorrect usage:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
	}
	if got := run(t, h, subject, "--json", "runs", "some-run", "--log", "review"); got.code == machine.ExitUsage || got.code == machine.ExitOK {
		t.Fatalf("a run this home does not hold was answered %s, want an operational failure:\n%s%s", got.code, got.stdout, got.stderr)
	}
}
