package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/custody"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// fixtureTask is a task whose record carries every word the old rendering
// printed and P13 forbids: an identifier in the generator's format, a copy
// path under a home, a state label and its source.
func fixtureTask() machine.Task {
	return machine.Task{
		Record: store.Task{
			ID: "abcdefghij23", Shape: store.TaskDelivery, Project: "the parser",
			Mode: "headless", WorktreePath: filepath.Join(vocabtest.SampleRoot(), "tasks", "abcdefghij23", "copy"),
			Branch: store.Known("feature/parser"),
		},
		State: taskstate.Resolution{
			TaskID: "abcdefghij23", State: taskstate.Paused, Source: taskstate.SourceEvent,
			Basis: taskstate.Basis{EventSequence: 3, EventKind: "waiting-external", Process: taskstate.Unobserved},
		},
		Waiting: "an upstream release of the parser",
	}
}

// fixtureRun is a run at a hold, with no position, no stage views and no
// decision, so what it renders is the phrased head alone; the details and the
// findings behind a decision are renderers that are not phrased.
func fixtureRun() machine.Run {
	return machine.Run{
		Record: store.Run{
			ID: "abcdefghij23", RepositoryID: "repo", Branch: "feature/parser", Status: store.RunHeld,
			ResolvedAgent: store.Known("claude"), CreatedAt: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC),
		},
		Outcome: machine.OutcomeDecision,
		Cleanup: &store.RunCleanup{Run: "abcdefghij23", Refusal: store.Known("unproven"), Detail: "the branch is not on any remote"},
		CopySweep: &store.RunCopySweep{
			Run: "abcdefghij23", Outcome: store.SweepNotCleared, Ended: store.Known(0), Remaining: store.Known(1),
			Occasion: store.SweptAtRecovery, Detail: "swept " + filepath.Join(vocabtest.SampleRoot(), "worktrees", "repo", "abcdefghij23") + ": 1 survived",
		},
	}
}

// fixtureStatus is a status carrying the home root, the socket, the
// repository's and the gate's identifiers and the head, with the fixture run
// active.
func fixtureStatus() machine.Status {
	root := vocabtest.SampleRoot()
	run := fixtureRun()
	return machine.Status{
		Home:       root,
		Service:    machine.Service{Running: true, Socket: filepath.Join(root, "socket")},
		Repository: &store.Repository{ID: "repo", WorkingPath: "/somewhere/parser", UpstreamURL: "https://example.test/parser.git", DefaultBranch: "main"},
		Gate:       &machine.Gate{Present: true, ID: "gate"},
		Branch:     &machine.Branch{WorkingPath: "/somewhere/parser", Name: "feature/parser", Head: "0123456789abcdef0123456789abcdef01234567"},
		ActiveRun:  &run,
	}
}

// sampleID is an identifier in the generator's format, which the derived
// forbidden set catches wherever it appears, so a fixture carrying it in
// every identifier field holds a renderer to leaving all of them out.
const sampleID = "abcdefghij23"

// fixtureDecision is a decision at the test stage's hold, the shape
// internal/pipeline's hold node declares, with a finding carrying a stage
// name, an identifier and a path, so a decision renderer that printed the
// question or the findings would be caught.
func fixtureDecision() machine.Decision {
	stage := pipeline.StageTest
	return machine.Decision{
		Decision: graph.Decision{
			Node:     stage.HoldNode(),
			Question: fmt.Sprintf("The %s stage is holding for your decision.", stage),
			Options:  []string{"approved", "skipped", "cancelled", string(pipeline.OutcomeFixRequested)},
			Detailed: []string{string(pipeline.OutcomeFixRequested)},
		},
		Stage: stage.String(),
		Findings: []findings.Finding{{
			ID: sampleID, Severity: findings.SeverityWarning, Action: findings.ActionAsk,
			Location:    findings.Location{Path: filepath.Join(vocabtest.SampleRoot(), "tasks", sampleID, "copy", "main.go"), Line: 3},
			Description: fmt.Sprintf("No %s command is configured.", stage),
		}},
	}
}

// fixtureCatchUp is a catch-up read with every part filled: a hold naming
// the fixture task and the fixture run held at the fixture decision, a merge
// and a cleanup record of the task with more past the bound, the task and a
// run under way, and the task stopped, each carrying identifiers, a copy
// path, a stage name and state labels its human rendering must leave out.
func fixtureCatchUp() machine.CatchUp {
	task, run := fixtureTask(), fixtureRun()
	decision := fixtureDecision()
	run.Decision = &decision
	advancing := fixtureRun()
	advancing.Record.Status, advancing.Outcome, advancing.Advancing = store.RunRunning, machine.OutcomeExecuting, true
	stopped := task
	stopped.State.State, stopped.State.Source, stopped.Waiting = taskstate.Failed, taskstate.SourceRun, ""
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	return machine.CatchUp{
		Waiting: []machine.Waiting{
			{Hold: &store.Hold{Key: "task/" + sampleID + "/relaunch-bound/2", TaskID: store.Known(sampleID),
				Subject: "whether to begin this work again", OpenedAt: at}, Task: &task, Answer: machine.TaskAnswering(sampleID)},
			{Run: &run, Answer: machine.RunAnswering("/somewhere/parser", run.Record.Branch)},
		},
		Landed: []machine.Landed{
			{Task: task, Cleanup: &store.TaskCleanup{TaskID: sampleID, Proof: store.CleanupProofRemoteBranch, RemovedAt: store.Known(at)}},
			{Task: task, Merge: &store.MergeRecord{Run: sampleID, PullRequest: 7, Head: "0123abc", End: store.PullRequestMerged, RecordedAt: at}},
		},
		MoreLanded: 2,
		UnderWay:   machine.UnderWay{Tasks: []machine.Task{task}, Runs: []machine.Run{advancing}},
		Stopped:    []machine.Task{stopped},
	}
}

// fixtureUsage is a usage view over two calls of the fixture run, one in its
// review stage and a fix round serving it, and one call from before the
// record kept a run or stage, so its evidence carries a run identifier, a
// stage name and an unknown group, with one count no call reported.
func fixtureUsage() machine.Usage {
	window, err := machine.ParseUsageWindow("7d")
	if err != nil {
		panic(err)
	}
	readAt := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	review := store.AgentInvocation{Purpose: "review", Agent: "claude", Model: "some-model", SessionUse: "none",
		Started: readAt.Add(-time.Hour), Duration: 90 * time.Second, InputTokens: store.Known[int64](1200),
		OutputTokens: store.Known[int64](300), Run: store.Known(sampleID), Stage: store.Known("review")}
	fix := store.AgentInvocation{Purpose: "fix", Agent: "claude", Model: "some-model", SessionUse: "resumed",
		Started: readAt.Add(-50 * time.Minute), Duration: 30 * time.Second, Failure: "timeout",
		InputTokens: store.Known[int64](80), Run: store.Known(sampleID), Stage: store.Known("review")}
	old := store.AgentInvocation{Purpose: "intent", Agent: "claude", SessionUse: "none",
		Started: readAt.Add(-40 * time.Minute), Duration: time.Second}
	return machine.UsageOf(window, readAt, []store.AgentInvocation{review, fix, old})
}

// fixturePlan is an adoption printed for confirmation: its classification
// and steps carry the run's identifier, the commits and the references the
// steps write, which the structured rendering keeps and the human one leaves
// out.
func fixturePlan() machine.Plan {
	c := custody.Classification{
		Class: custody.ClassDivergedEquivalent, Run: sampleID, Branch: "feature/parser",
		Head: "0123456789abcdef0123456789abcdef01234567", Submitted: "0123456789abcdef0123456789abcdef01234567",
		Work: "89abcdef0123456789abcdef0123456789abcdef", Pushed: true,
		Detail: "integrating the branch into run " + sampleID + "'s work reproduces its tree",
	}
	return machine.Plan{Classification: &c, Detail: c.Detail,
		Steps: custody.Steps(c, filepath.Join(vocabtest.SampleRoot(), "repos", sampleID+".git"), "https://example.test/parser.git")}
}

// fixtures is a value for every renderer, each carrying the words its old
// rendering printed: identifiers in the generator's format, paths under a
// sample home, stage names and state labels. The fallback's is a real answer
// shape this surface has no renderer for, a worker's appended entry, so the
// refusal is exercised on a value a service does produce.
func fixtures(t *testing.T) map[string]any {
	t.Helper()
	root := vocabtest.SampleRoot()
	task := fixtureTask()
	run := fixtureRun()
	status := fixtureStatus()
	active := run.Record
	active.ID, active.Status = sampleID, store.RunRunning
	withDetails := run
	halted := graph.StatusHalted
	withDetails.Progress, withDetails.Position = &halted, "hold:test"
	withDetails.Stages = []machine.Stage{{Stage: "test", Outcome: "passed", Ran: true}}
	return map[string]any{
		"status":  status,
		"run":     run,
		"runs":    machine.Runs{Runs: []store.Run{run.Record}},
		"tasks":   machine.Tasks{Tasks: []machine.Task{task}},
		"task":    task,
		"service": status.Service,
		"event":   mustEvent(t, ipc.TypeTaskState, task),
		// The stop guard blocked: the guard's reason and the detail are the
		// structured rendering's, and the message is what a person reads.
		"host stop": machine.HostStopOf(supervisor.StopVerdict{Reason: supervisor.StopLockFree}, false),
		// The installer refused a conflict, which prints all three lines of
		// machine text beside its row: the settings file, the line, and the
		// command the file already holds, each carrying a home path.
		"host install": machine.HostInstall{
			Settings: filepath.Join(os.TempDir(), "someone", ".claude", "settings.json"),
			Line:     `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/bin/assistant --home ` + root + ` host stop"}]}]}}`,
			Command:  "/bin/assistant --home " + root + " host stop",
			Result:   machine.HostInstallRefused, Refusal: machine.HostInstallConflict,
			Conflict: "/elsewhere/assistant --home " + filepath.Join(root, sampleID) + " host stop",
			Detail:   "a turn-end hook runs this program's stop guard with a different command line",
		},
		"decision": fixtureDecision(),
		"details":  withDetails,
		"findings": fixtureDecision(),
		"usage":    fixtureUsage(),
		"doctor": machine.Doctor{
			Checks: []machine.Check{
				{Name: machine.CheckHome, OK: true, Blocking: true, Detail: root},
				{Name: machine.CheckDatabase, OK: true, Blocking: true, Detail: filepath.Join(root, "state.db")},
				{Name: machine.CheckService, Blocking: true, Detail: "dial " + filepath.Join(root, "socket") + ": no such file"},
				{Name: machine.CheckStages, Detail: "3 of 9 stages have an implementation (intent, rebase, review); the rest hold for a decision"},
			},
			Detail: "a run cannot start: service",
		},
		"lifecycle": machine.Lifecycle{
			Active: []store.Run{active},
			Detail: "1 run(s) are active. Pass the force flag for this command to stop anyway.",
		},
		"init": machine.Init{
			Gate:                machine.Gate{Present: true, ID: sampleID, Repository: filepath.Join(root, "repos", sampleID+".git")},
			Repository:          store.Repository{ID: sampleID, WorkingPath: "/somewhere/parser", DefaultBranch: "main"},
			DefaultBranchSource: machine.DefaultBranchFromOrigin,
		},
		"plan":      fixturePlan(),
		"eject":     machine.Eject{Removed: true, Repository: sampleID, Runs: 2},
		"health":    machine.Health{Ready: true, Home: root, Build: store.Build{Version: "v1"}, Instance: sampleID},
		"admission": machine.Admission{Gate: sampleID, Refs: []string{"refs/heads/feature/parser"}},
		"notification": machine.Notification{
			Gate: sampleID,
			Started: []machine.Started{{Branch: "feature/parser", Head: "0123456789abcdef0123456789abcdef01234567",
				Run: sampleID, Superseded: sampleID}},
			Ignored: []machine.Ignored{{Ref: "refs/tags/v1", Reason: "not a branch, and the gate validates branches"}},
		},
		"task answer": machine.TaskAnswered{
			Hold: store.Hold{Key: "task/" + sampleID + "/relaunch-bound/2", TaskID: store.Known(sampleID),
				Subject: "whether to begin this work again", Resolution: store.Known(machine.AnswerRetry)},
			Effect:   machine.AnswerRelaunched,
			Relaunch: &machine.RelaunchBegun{Attempt: 3, Superseded: sampleID},
			Task:     task,
		},
		"catch up":    fixtureCatchUp(),
		"marked seen": machine.Marked{Marks: []store.SeenMark{{SeenRecord: store.SeenRecord{Kind: store.SeenMerge, Key: sampleID}}}},
		"fallback": machine.Appended{Event: store.TaskEvent{TaskID: sampleID, Sequence: 3, Kind: "waiting-external",
			Detail: "an upstream release of the parser"}},
	}
}

// rendered is what one renderer wrote for a value: the lines on standard
// output, and the text a person reads somewhere else when the renderer is
// read there instead.
type rendered struct {
	out       string
	elsewhere string
}

// renderNamed drives one renderer by its name in the enumeration, the way a
// person reaches it: a row with a type through readOut, a row reached only
// from another renderer through its own function, and the fallback through
// readOut over a value no row claims, which must come back as the refusal.
func renderNamed(t *testing.T, name string, value any) rendered {
	t.Helper()
	var out bytes.Buffer
	if name == fallback.name {
		err := readOut(&out, value)
		var refusal noReading
		if !errors.As(err, &refusal) {
			t.Fatalf("the fallback's fixture rendered with %v rather than being refused", err)
		}
		return rendered{out: out.String(), elsewhere: err.Error()}
	}
	for _, r := range renderings {
		if r.name != name {
			continue
		}
		if r.of == nil {
			r.render(&out, value)
		} else if err := readOut(&out, value); err != nil {
			t.Fatalf("the %s fixture was refused: %v", name, err)
		}
		if hostStop, ok := value.(machine.HostStop); ok {
			return rendered{out: out.String(), elsewhere: hostStop.Message}
		}
		return rendered{out: out.String()}
	}
	t.Fatalf("no renderer is named %q", name)
	return rendered{}
}

// TestThePhrasedRenderersSpeakTheTableAndTheJSONKeepsIdentifiers is P13
// for this surface's human renderers: every renderer the enumeration lists as
// phrased, over a fixture carrying the words it must not print, writes
// nothing from the derived forbidden set; the lines the old renderers wrote
// for the same fixtures are caught, which is the other direction; and the
// structured rendering of the same values keeps every identifier, because the
// machine interface keeps them by design. The enumeration is the renderer
// table's, so a renderer added there as phrased is held here without being
// named.
//
// The claim is bounded by that enumeration and no wider. The renderers it
// lists as evidence - a run's details and the findings holding a decision -
// keep this build's words, as P13 lets an evidence document, and
// TestTheMessagePointingAtEvidenceIsPhrased holds what points at them;
// TestTheUnphrasedRenderersPrintWhatTheyAreListedFor shows that rather than
// asserting it. The refusal and usage text the command
// surface writes on standard error is not a renderer and is not held here.
func TestThePhrasedRenderersSpeakTheTableAndTheJSONKeepsIdentifiers(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	values := fixtures(t)
	// A renderer whose answer a person reads somewhere other than standard
	// output is held to writing nothing there instead, and its text
	// elsewhere is held to the rule in place of the lines, so it is not
	// passed for looking at nothing: the stop guard's message goes to
	// standard error, where the host reads it, and so does the fallback's
	// refusal.
	elsewhere := map[string]bool{"host stop": true, fallback.name: true}
	// A renderer that prints machine text a person copies exactly beside its
	// row names those lines here: each has to be printed, exactly, after its
	// label, and only the lines left once they are taken out are held to the
	// rule, so the exemption cannot swallow the row itself.
	verbatim := map[string]func(any) []string{
		"host install": func(v any) []string {
			a := v.(machine.HostInstall)
			return []string{hostInstallFileLabel + " " + a.Settings, hostInstallLineLabel + " " + a.Line, hostInstallConflictLabel + " " + a.Conflict}
		},
	}
	phrased := 0
	for _, r := range Renderings() {
		if r.Voice != VoicePhrased {
			continue
		}
		phrased++
		value, ok := values[r.Name]
		if !ok {
			t.Fatalf("the renderer %q is phrased and this test has no fixture for it", r.Name)
		}
		got := renderNamed(t, r.Name, value)
		if elsewhere[r.Name] {
			if got.out != "" {
				t.Errorf("%s is read elsewhere and rendered %q", r.Name, got.out)
			}
			if got.elsewhere == "" {
				t.Errorf("%s is read elsewhere and its fixture carries nothing to read", r.Name)
			}
			vocabtest.Assert(t, got.elsewhere)
		} else if got.out == "" {
			t.Errorf("%s rendered nothing", r.Name)
		}
		exempt := map[string]bool{}
		if lines, ok := verbatim[r.Name]; ok {
			for _, line := range lines(value) {
				if !strings.Contains(got.out, line+"\n") {
					t.Errorf("%s did not print the machine text %q as it is", r.Name, line)
				}
				exempt[line] = true
			}
		}
		held := 0
		for _, line := range strings.Split(strings.TrimRight(got.out, "\n"), "\n") {
			if exempt[line] {
				continue
			}
			held++
			vocabtest.Assert(t, line)
		}
		if got.out != "" && held == 0 {
			t.Errorf("%s printed nothing but machine text, so its row was held to nothing", r.Name)
		}
		t.Logf("%s:\n%s%s", r.Name, got.out, got.elsewhere)

		var document bytes.Buffer
		if err := machine.NewEncoder(&document).Encode(value); err != nil {
			t.Fatalf("encoding the %s fixture: %v", r.Name, err)
		}
		if n := len(vocabtest.Violations(t, document.String())); n == 0 {
			t.Errorf("the structured rendering of %s carries no identifier, path or label, so this fixture holds the human rendering to nothing", r.Name)
		}
	}
	if phrased < 18 {
		t.Fatalf("%d renderers are phrased; the table claims eighteen", phrased)
	}

	// The other direction: the lines these renderers used to write, built
	// here from the same fixtures the way the old renderers built them, are
	// caught by the derivation.
	task, run, status := fixtureTask(), fixtureRun(), fixtureStatus()
	decision := values["decision"].(machine.Decision)
	doctor := values["doctor"].(machine.Doctor)
	lifecycle := values["lifecycle"].(machine.Lifecycle)
	initialized := values["init"].(machine.Init)
	ejected := values["eject"].(machine.Eject)
	health := values["health"].(machine.Health)
	admission := values["admission"].(machine.Admission)
	notification := values["notification"].(machine.Notification)
	plan := values["plan"].(machine.Plan)
	for name, old := range map[string]string{
		"task":         fmt.Sprintf("%s  %-10s %-12s %-8s %s", task.Record.ID, task.Record.Shape, task.State.State, task.State.Source, task.Record.Project),
		"run":          fmt.Sprintf("Run        %s on %s (%s)", run.Record.ID, run.Record.Branch, run.Record.Status),
		"runs":         fmt.Sprintf("%s  %-10s %-24s", run.Record.ID, run.Record.Status, run.Record.Branch),
		"status":       fmt.Sprintf("Home       %s", status.Home),
		"service":      fmt.Sprintf("Service    running on %s", status.Service.Socket),
		"event":        fmt.Sprintf("%s (revision %d) %s", ipc.TypeTaskState, 1, mustEvent(t, ipc.TypeTaskState, task).Payload),
		"decision":     fmt.Sprintf("Waiting on you: %s", decision.Question),
		"doctor":       fmt.Sprintf("%s  %-13s %s", "yes", doctor.Checks[0].Name, doctor.Checks[0].Detail),
		"lifecycle":    fmt.Sprintf("  %s  %-10s %s", lifecycle.Active[0].ID, lifecycle.Active[0].Status, lifecycle.Active[0].Branch),
		"init":         fmt.Sprintf("Created the gate %s at %s", initialized.Gate.ID, initialized.Gate.Repository),
		"eject":        fmt.Sprintf("Removed the gate %s and %d run record(s).", ejected.Repository, ejected.Runs),
		"health":       fmt.Sprintf("ready: %v, home %s, build %s", health.Ready, health.Home, health.Build),
		"admission":    fmt.Sprintf("Admitted to gate %s: %s", admission.Gate, strings.Join(admission.Refs, ", ")),
		"notification": fmt.Sprintf("Started run %s for %s at %s", notification.Started[0].Run, notification.Started[0].Branch, notification.Started[0].Head[:12]),
		"fallback":     fmt.Sprintf("%+v", values["fallback"]),
		"plan":         fmt.Sprintf("  %s %s from %s to %s", plan.Steps[2].Kind, plan.Steps[2].Ref, plan.Steps[2].From, plan.Steps[2].To),
	} {
		if len(vocabtest.Violations(t, old)) == 0 {
			t.Errorf("the old %s line %q is not caught, so passing the new one proves nothing", name, old)
		}
	}
}

// The renderers the enumeration lists as not phrased, the evidence documents,
// do print this build's own words over the same
// fixtures, which is what makes the listing honest in the other direction: a
// renderer listed as not phrased that printed nothing forbidden would be one
// this surface could claim and does not, and one listed as phrased that did
// is caught by the test above. The domain is the enumeration's, so a renderer
// added there as not phrased needs a fixture here before it passes.
func TestTheUnphrasedRenderersPrintWhatTheyAreListedFor(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	values := fixtures(t)
	unphrased := 0
	for _, r := range Renderings() {
		if r.Voice == VoicePhrased {
			continue
		}
		unphrased++
		value, ok := values[r.Name]
		if !ok {
			t.Fatalf("the renderer %q is not phrased and this test has no fixture for it", r.Name)
		}
		got := renderNamed(t, r.Name, value)
		if len(vocabtest.Violations(t, got.out)) == 0 {
			t.Errorf("%s is listed as not phrased and printed nothing the derivation forbids:\n%s", r.Name, got.out)
		}
	}
	if unphrased == 0 {
		t.Fatal("no renderer is listed as not phrased, so this test checked nothing; if that is now true, delete it")
	}
}

// TestTheMessagePointingAtEvidenceIsPhrased is P13 for the three evidence
// documents PRD section 9 names. P13 lets an evidence document keep exact
// identifiers and stage names and keeps the message that points at one
// phrased, so a run carrying the two a run carries is rendered and each
// document is taken out from behind its pointer; what is left - the
// pointers, the run's head, the notice that its starting inputs were not
// applied, and the decision - is held to the derived forbidden set line by
// line. The usage view is an answer of its own, so it is rendered the way
// runs --usage reaches it, through readOut, and held the same way.
//
// It is not passed for looking at nothing. Each evidence fixture is shown to
// carry a forbidden word on its own, so taking it out is what lets the rest
// pass; and the check is run over counterfeits of the same output it must
// reject: a pointer carrying the run's identifier and position, a document
// written with no pointer before it, the notice in the words the details
// renderer used to print it in, and the usage view with no pointer.
func TestTheMessagePointingAtEvidenceIsPhrased(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	values := fixtures(t)
	run := values["details"].(machine.Run)
	decision := values["findings"].(machine.Decision)
	run.Decision = &decision
	run.NotApplied = []string{"intent", "skip"}
	var out bytes.Buffer
	if err := readOut(&out, run); err != nil {
		t.Fatalf("the run was refused: %v", err)
	}
	rows := machine.TablePhrases()
	var blocks []evidenceBlock
	answers := map[string]evidenceBlock{}
	for _, r := range renderings {
		if r.voice != VoiceEvidence {
			if r.points != (machine.Phrase{}) {
				t.Errorf("%s is %s and carries a pointer, which only an evidence row has", r.name, r.voice)
			}
			continue
		}
		if !slices.Contains(rows, r.points) {
			t.Errorf("%s points at itself with %q, which is not a row of the phrase table", r.name, r.points)
		}
		var rendered bytes.Buffer
		r.render(&rendered, values[r.name])
		body := rendered.String()
		if len(vocabtest.Violations(t, body)) == 0 {
			t.Errorf("the %s fixture carries nothing the derivation forbids, so taking it out holds the rest to nothing:\n%s", r.name, body)
		}
		block := evidenceBlock{pointer: r.points.String(), body: body}
		if r.of != nil {
			answers[r.name] = block
			continue
		}
		blocks = append(blocks, block)
	}
	if len(blocks)+len(answers) != 3 {
		t.Fatalf("%d renderers are evidence; PRD section 9 names three evidence documents", len(blocks)+len(answers))
	}
	for name, block := range answers {
		var answered bytes.Buffer
		if err := readOut(&answered, values[name]); err != nil {
			t.Fatalf("the %s answer was refused: %v", name, err)
		}
		whole := answered.String()
		t.Logf("the %s answer as a person reads it:\n%s", name, whole)
		if !strings.HasPrefix(whole, block.pointer+"\n") {
			t.Errorf("the %s answer does not open with the phrase that points at it:\n%s", name, whole)
		}
		for _, problem := range pointedAt(t, whole, []evidenceBlock{block}) {
			t.Error(problem)
		}
		if len(pointedAt(t, strings.Replace(whole, block.pointer+"\n", "", 1), []evidenceBlock{block})) == 0 {
			t.Errorf("the check passed the %s answer with no pointer, so passing the real one proves nothing", name)
		}
	}
	got := out.String()
	t.Logf("the run as a person reads it:\n%s", got)
	if !strings.Contains(got, machine.PhraseInputsNotApplied.String()+"\n") {
		t.Errorf("the run does not say its starting inputs were not applied:\n%s", got)
	}
	for _, problem := range pointedAt(t, got, blocks) {
		t.Error(problem)
	}

	counterfeitPointer := "Details of run " + sampleID + " at " + run.Position
	misnamed := slices.Clone(blocks)
	misnamed[0].pointer = counterfeitPointer
	for name, c := range map[string]struct {
		out    string
		blocks []evidenceBlock
	}{
		"a pointer carrying the identifier and position": {strings.Replace(got, blocks[0].pointer, counterfeitPointer, 1), misnamed},
		"a document with no pointer":                     {strings.Replace(got, blocks[1].pointer+"\n", "", 1), blocks},
		"the notice in the old details words": {strings.Replace(got, machine.PhraseInputsNotApplied.String(),
			"Not applied "+strings.Join(run.NotApplied, ", ")+" - this branch already had a run, and these start one", 1), blocks},
	} {
		if len(pointedAt(t, c.out, c.blocks)) == 0 {
			t.Errorf("the check passed %s, so passing the real output proves nothing", name)
		}
	}
}

// evidenceBlock is one evidence document as a run renders it: the phrase
// that points at it and the document written after it.
type evidenceBlock struct{ pointer, body string }

// pointedAt reports what is wrong with a rendering against the evidence it
// should carry: a document not written directly after its pointer, and any
// line left once each document is taken out that carries a forbidden word.
func pointedAt(t *testing.T, out string, blocks []evidenceBlock) []string {
	t.Helper()
	var problems []string
	for _, b := range blocks {
		whole := b.pointer + "\n" + b.body
		if !strings.Contains(out, whole) {
			problems = append(problems, fmt.Sprintf("the document behind %q is not written directly after it", b.pointer))
			continue
		}
		out = strings.Replace(out, whole, b.pointer+"\n", 1)
	}
	for _, line := range strings.Split(out, "\n") {
		for _, v := range vocabtest.Violations(t, line) {
			problems = append(problems, fmt.Sprintf("%q points at evidence or stands beside it and carries %q, which is %s's word", line, v.Text, v.Owner))
		}
	}
	return problems
}

// An answer with no renderer is refused rather than printed as Go prints it:
// nothing on standard output, internal/machine's refusal row on standard
// error, and the operational failure code, so an answer type added without a
// renderer fails loudly. The same answer through --json is a document, which
// is the control that the refusal is the human rendering's and not the
// answer's.
func TestAnAnswerWithNoRendererIsRefusedRatherThanDumped(t *testing.T) {
	t.Parallel()
	value := fixtures(t)["fallback"]
	var stdout, stderr bytes.Buffer
	in := &invocation{env: Environment{Stdout: &stdout, Stderr: &stderr}}
	if code := render(in, value, nil); code != machine.ExitFailure {
		t.Fatalf("an answer with no renderer exited %s, want failure", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("an answer with no renderer printed %q", stdout.String())
	}
	if got, want := strings.TrimSpace(stderr.String()), machine.PhraseNoReading.String(); got != want {
		t.Fatalf("an answer with no renderer said %q, want the refusal %q", got, want)
	}

	stdout.Reset()
	in.json = true
	if code := render(in, value, nil); code != machine.ExitOK {
		t.Fatalf("the same answer as a document exited %s, want ok", code)
	}
	var decoded machine.Appended
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil || decoded.Event.TaskID != sampleID {
		t.Fatalf("the same answer as a document did not carry it: %v\n%s", err, stdout.String())
	}
}

// A task with no run shows its own state and no outcome word; a paused task
// shows what the worker said it is waiting on and never the word; and the
// task line carries neither the identifier nor the copy path.
func TestATaskLineIsItsPhraseAndNothingOfTheRecord(t *testing.T) {
	t.Parallel()
	task := fixtureTask()
	var out bytes.Buffer
	readOut(&out, task)
	line := out.String()
	for _, want := range []string{"the parser: ", task.Waiting} {
		if !strings.Contains(line, want) {
			t.Errorf("the task line %q does not carry %q", line, want)
		}
	}
	for _, forbidden := range []string{task.Record.ID, task.Record.WorktreePath, string(task.State.State), string(task.State.Source)} {
		if strings.Contains(line, forbidden) {
			t.Errorf("the task line %q carries %q", line, forbidden)
		}
	}
	for _, outcome := range machine.Outcomes() {
		if strings.Contains(" "+line+" ", " "+string(outcome)+" ") || strings.Contains(line, string(outcome)+".") {
			t.Errorf("the task line %q carries the outcome word %q, and a task with no run has no outcome", line, outcome)
		}
	}
	// The same task through --json keeps all of it. The document is decoded
	// rather than searched as text, because the encoder escapes a path's
	// separators on Windows and the fact under test is what a consumer reads
	// back, not the bytes on the wire.
	var document bytes.Buffer
	if err := machine.NewEncoder(&document).Encode(task); err != nil {
		t.Fatal(err)
	}
	var decoded machine.Task
	if err := json.Unmarshal(document.Bytes(), &decoded); err != nil {
		t.Fatalf("the structured rendering does not decode: %v\n%s", err, document.String())
	}
	for name, pair := range map[string][2]string{
		"identifier": {decoded.Record.ID, task.Record.ID},
		"copy path":  {decoded.Record.WorktreePath, task.Record.WorktreePath},
		"state":      {string(decoded.State.State), string(task.State.State)},
		"wait":       {decoded.Waiting, task.Waiting},
	} {
		if pair[0] != pair[1] {
			t.Errorf("the structured rendering lost the %s: read %q, want %q:\n%s", name, pair[0], pair[1], document.String())
		}
	}
}

// A run's head is its phrase, its branch, its agent, the kept-copy line
// from its cleanup refusal and the line for a copy sweep that did not clear;
// the details beneath it are an evidence document,
// and this says so by showing exactly which lines carry a stage name once
// stage views are added.
func TestARunsHeadIsPhrasedAndItsDetailsAreNot(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	run := fixtureRun()
	var out bytes.Buffer
	readOut(&out, run)
	head := out.String()
	kept, ok := machine.CleanupPhrase(*run.Cleanup)
	if !ok || !strings.Contains(head, kept.String()) {
		t.Fatalf("the head does not carry the kept-copy line:\n%s", head)
	}
	// The copy sweep that did not clear is the one phrased line, and its
	// account, which names the copy's path, stays the structured rendering's.
	unswept, ok := machine.CopySweepPhrase(*run.CopySweep)
	if !ok || !strings.Contains(head, unswept.String()) || strings.Contains(head, run.CopySweep.Detail) {
		t.Fatalf("the head does not carry the uncleared-sweep line alone:\n%s", head)
	}
	if !strings.Contains(head, run.Phrase().String()) || !strings.Contains(head, "Branch     feature/parser") || !strings.Contains(head, "Agent      claude") {
		t.Fatalf("the head is missing the phrase, the branch or the agent:\n%s", head)
	}
	for _, forbidden := range []string{run.Record.ID, string(run.Record.Status), "Outcome    ", "Next:"} {
		if strings.Contains(head, forbidden) {
			t.Errorf("the head carries %q:\n%s", forbidden, head)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(head, "\n"), "\n") {
		vocabtest.Assert(t, line)
	}

	// With a position and stage views, the details carry a node name and
	// stage names: that renderer is the one Renderings lists as evidence, and
	// this is the line that says so.
	halted := graph.StatusHalted
	withStages := run
	withStages.Progress, withStages.Position = &halted, "hold:test"
	withStages.Stages = []machine.Stage{{Stage: "test", Outcome: "passed", Ran: true}}
	out.Reset()
	readOut(&out, withStages)
	if !strings.Contains(out.String(), "Position   hold:test") || !strings.Contains(out.String(), "\nStages\n  test ") {
		t.Fatalf("the details do not carry the position and the stage:\n%s", out.String())
	}
	if len(vocabtest.Violations(t, "Position   hold:test after 0 of 0 steps")) == 0 || len(vocabtest.Violations(t, "  test      passed")) == 0 {
		t.Fatal("neither detail line carries a forbidden word, so the details renderer could be claimed phrased")
	}
	for _, r := range Renderings() {
		if r.Name == "details" && r.Voice != VoiceEvidence {
			t.Fatalf("the details renderer is listed as %s and prints stage names as only an evidence document may", r.Voice)
		}
	}
}

// Every event type this build classifies has a phrase, with the domain
// derived from internal/ipc's table: the three records read as their
// phrases, a worker's history entry as added and not relayed, the stream's
// two control events as theirs, activity as output that arrived, and the
// two state types nothing here produces, like a type this build does not
// know, as an update the view cannot describe. A payload
// that does not read as its type reads the same way rather than as nothing.
func TestEveryEventTypeHasAPhrase(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	task := fixtureTask()
	run := fixtureRun().Record
	hold := store.Hold{Key: "k", RunID: store.Known(run.ID), Subject: "whether the migration may drop the column"}
	payloads := map[ipc.Type]any{ipc.TypeRunState: run, ipc.TypeTaskState: task, ipc.TypeHoldState: hold}
	types := ipc.Types()
	if len(types) == 0 {
		t.Fatal("internal/ipc classifies no types")
	}
	for _, typ := range types {
		event := ipc.Event{Type: typ, Revision: 7}
		if payload, ok := payloads[typ]; ok {
			event = mustEvent(t, typ, payload)
		}
		line := eventLine(event)
		if line == "" {
			t.Errorf("%s renders as nothing", typ)
		}
		vocabtest.Assert(t, line)
		if strings.Contains(line, string(typ)) || strings.Contains(line, "revision") {
			t.Errorf("%s renders its own type or revision: %q", typ, line)
		}
		switch typ {
		case ipc.TypeRunState:
			if !strings.Contains(line, "feature/parser: ") || !strings.Contains(line, machine.RecordPhrase(run).String()) {
				t.Errorf("a run's state renders %q", line)
			}
		case ipc.TypeTaskState:
			if !strings.Contains(line, "the parser: ") || !strings.Contains(line, task.Waiting) {
				t.Errorf("a task's state renders %q", line)
			}
		case ipc.TypeHoldState:
			if !strings.Contains(line, hold.Subject) || !strings.Contains(line, "waiting on you") {
				t.Errorf("a hold renders %q", line)
			}
		case ipc.TypeTaskEvent:
			if line != machine.PhraseWorkerSpoke.String() {
				t.Errorf("a worker's history entry renders %q", line)
			}
		case ipc.TypeGap:
			if line != machine.PhraseUpdatesMissed.String() {
				t.Errorf("a gap renders %q", line)
			}
		case ipc.TypeServiceStopping:
			if line != machine.PhraseServiceStopping.String() {
				t.Errorf("the service stopping renders %q", line)
			}
		default:
			want := machine.PhraseUnfamiliarUpdate.String()
			if ipc.Classify(typ) == ipc.ClassActivity {
				want = machine.PhraseActivity.String()
			}
			if line != want {
				t.Errorf("%s renders %q, want %q", typ, line, want)
			}
		}
	}
	for _, typ := range []ipc.Type{ipc.TypeRunState, ipc.TypeTaskState, ipc.TypeHoldState} {
		if got := eventLine(ipc.Event{Type: typ, Revision: 1, Payload: json.RawMessage(`"not the record"`)}); got != machine.PhraseUnfamiliarUpdate.String() {
			t.Errorf("%s over a payload that is not its record renders %q", typ, got)
		}
	}
	if got := eventLine(ipc.Event{Type: "never.a.type", Revision: 1}); got != machine.PhraseUnfamiliarUpdate.String() {
		t.Errorf("a type this build does not know renders %q", got)
	}
}

// mustEvent builds an event carrying a record, the way a publisher does.
func mustEvent(t *testing.T, typ ipc.Type, payload any) ipc.Event {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encoding a %s payload: %v", typ, err)
	}
	return ipc.Event{Type: typ, Revision: 1, Payload: body}
}
