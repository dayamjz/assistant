package journey_test

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// ceilingWalk is what three runs of one home under an operator's usage
// ceiling came to, read off the answers the binary gave and off the calls the
// stand-in recorded.
type ceilingWalk struct {
	// ceiling is the operator's ceiling on input tokens, which is the
	// stand-in's reported cost of one review.
	ceiling int64

	// liftAnswered is whether starting the run of a branch whose own
	// configuration document names the ceiling's key exited successfully,
	// liftRefusal is the failure it answered with, and liftStatus is that
	// run's record as the listing reports it afterwards.
	liftAnswered bool
	liftRefusal  string
	liftStatus   store.RunStatus
	// callsAfterLift is how many agent invocations the stand-in had answered
	// once the refused run's start returned.
	callsAfterLift int

	// control is the run started before anything was recorded, under the
	// ceiling, and held is the run started after its review was recorded.
	control, held machine.Run
	// heldLines is what assistant runs RUN printed for the held run without
	// --json, as a person reads it.
	heldLines []string
	// calls is how many agent invocations the stand-in answered in all.
	calls int
	// usage is the usage view over the same day, as runs --usage answers it.
	usage machine.Usage
}

// ceilingAsk is the usage ceiling's finding on a decision, and false when the
// decision carries none.
func ceilingAsk(d *machine.Decision) (findings.Finding, bool) {
	if d == nil {
		return findings.Finding{}, false
	}
	return heldWith(d.Findings, machine.UsageCeilingFindingID)
}

// withHeldAsk returns a copy of w whose held run's ceiling finding is
// changed, so a counterfeit never writes through the decision the real
// observation shares.
func (w ceilingWalk) withHeldAsk(change func(*findings.Finding)) ceilingWalk {
	if w.held.Decision == nil {
		return w
	}
	decision := *w.held.Decision
	decision.Findings = slices.Clone(decision.Findings)
	for i := range decision.Findings {
		if decision.Findings[i].ID == machine.UsageCeilingFindingID {
			change(&decision.Findings[i])
		}
	}
	w.held.Decision = &decision
	return w
}

// figures is the line of exact figures the hold and the usage view both
// carry for the one count the ceiling is set on, composed from the usage
// view's own reading.
func (w ceilingWalk) figures() (string, error) {
	if len(w.usage.Ceiling.Counts) != 1 {
		return "", fmt.Errorf("the usage view weighs %d counts against the ceiling, want the one set", len(w.usage.Ceiling.Counts))
	}
	count := w.usage.Ceiling.Counts[0]
	recorded, ok := count.Recorded.Total.Get()
	if !ok {
		return "", errors.New("the usage view records no input-token total")
	}
	return fmt.Sprintf("%s %d of %d (%s)", count.Count, recorded, count.Ceiling, count.Standing), nil
}

// TestARunPastTheUsageCeilingHoldsThroughTheBinary drives the operator's
// usage ceiling (#372) end to end: the home's own configuration sets
// usage_ceiling.input_tokens to what the stand-in reports one review costs,
// so the service's own recorder, reading the adapter's counts off the wire,
// is what brings the record to the ceiling.
//
// Three runs, each on a branch of its own. The first carries a configuration
// document naming the ceiling's key, which only the operator's file may set,
// and is refused before any agent launches, its record ended (P7). The second
// is the control: its review starts under the ceiling, runs, and passes, and
// the run holds later where any run here holds. The third holds at the
// review stage on the ceiling's ask, which is for a person and has its remedy
// outside the change, without the stand-in being asked; its detail, as a
// person reads it through the binary, relays the ceiling's row and the exact
// figures the usage view reports.
//
// It cites P7 for the refused branch. The rendering is P13's subject and is
// recorded in that principle's Coverage row without a citation, on the terms
// that row gives for the other walk through a human rendering.
func TestARunPastTheUsageCeilingHoldsThroughTheBinary(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P7)
	scenario, path := cloned(t)
	cost, ok := standin.DefaultUsage().InputTokens.Value()
	if !ok || cost <= 0 {
		t.Fatalf("the stand-in reports no input-token cost for a review, so nothing could reach a ceiling")
	}
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly"),
	}}})
	j := open(t, scenario, func(o *journey.Options) { o.Dir = path; o.AgentArguments = agent.Arguments() })
	if err := j.WriteConfiguration(map[string]any{"usage_ceiling": map[string]any{
		"window": "24h", "input_tokens": cost,
	}}); err != nil {
		t.Fatalf("writing the home's configuration: %v", err)
	}
	succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
	serve(t, j)
	run := func(args ...string) string {
		t.Helper()
		out, err := journey.Git(scenario, path, args...)
		if err != nil {
			t.Fatalf("%v", err)
		}
		return out
	}
	commit := func(branch, from, file, content string) {
		t.Helper()
		run("checkout", "--quiet", "-b", branch, from)
		if err := writeInto(path, file, content); err != nil {
			t.Fatalf("%v", err)
		}
		run("add", file)
		run("commit", "--quiet", "-m", "work on "+branch)
	}
	original := run("rev-parse", "--abbrev-ref", "HEAD")
	observed := ceilingWalk{ceiling: cost}

	const liftBranch = "lift-the-ceiling"
	commit(liftBranch, original, config.RepositoryDocument, `{"usage_ceiling": {"input_tokens": 1000000000}}`+"\n")
	lift := j.Command("--intent", "lift the operator's ceiling from the branch")
	observed.liftAnswered = lift.Code == machine.ExitOK
	if failure, ok := lift.Failure(); ok {
		observed.liftRefusal = failure.Error
	}
	observed.callsAfterLift = len(agent.Calls())
	var listed machine.Runs
	if err := succeeds(t, j.Command("runs")).Decode(&listed); err != nil {
		t.Fatalf("reading the run listing: %v", err)
	}
	for _, record := range listed.Runs {
		if record.Branch == liftBranch {
			observed.liftStatus = record.Status
		}
	}

	run("checkout", "--quiet", original)
	observed.control = startRun(t, j, "--intent", "a run under the ceiling")

	commit("past-the-ceiling", original, "past.md", "A change made after the ceiling was reached.\n")
	started := startRun(t, j, "--intent", "a run past the ceiling")
	observed.held = decodeRun(t, succeeds(t, j.Command("runs", started.Record.ID)))
	detail := j.CommandPlainBounded(j.Dir(), 0, "runs", started.Record.ID)
	if detail.Code != machine.ExitOK {
		t.Fatalf("assistant runs exited %s:\n%s", detail.Code, detail)
	}
	t.Logf("assistant runs %s:\n%s", started.Record.ID, detail.Stdout)
	observed.heldLines = lines(detail.Stdout)
	observed.calls = len(agent.Calls())
	if err := succeeds(t, j.Command("runs", "--usage=1d")).Decode(&observed.usage); err != nil {
		t.Fatalf("reading the usage view: %v", err)
	}

	reachedRow := machine.CeilingHoldPhrase(machine.CeilingReached).String()
	check := journey.Check[ceilingWalk]{
		What: "P7 and the rendering: runs past the operator's usage ceiling",
		Clauses: []journey.Clause[ceilingWalk]{
			{
				States: "a branch whose configuration document names usage_ceiling.input_tokens is refused, naming the pushed copy and the key",
				Holds: func(w ceilingWalk) error {
					if w.liftAnswered {
						return errors.New("the run of a branch naming the ceiling's key was started rather than refused")
					}
					for _, want := range []string{"pushed configuration", string(config.KeyUsageCeilingInputTokens), "global-only"} {
						if !strings.Contains(w.liftRefusal, want) {
							return fmt.Errorf("the refusal %q does not carry %q", w.liftRefusal, want)
						}
					}
					return nil
				},
			},
			{
				States: "the refused run's record is ended, so the branch's next start is a fresh run",
				Holds: func(w ceilingWalk) error {
					if w.liftStatus != store.RunTerminated {
						return fmt.Errorf("the refused run is recorded %q, want %q", w.liftStatus, store.RunTerminated)
					}
					return nil
				},
			},
			{
				States:  "the refused run launched no agent",
				Absence: true,
				Possible: func(w ceilingWalk) error {
					if w.calls == 0 {
						return errors.New("no agent was launched by any run here, so none could have been by the refused one")
					}
					return nil
				},
				Holds: func(w ceilingWalk) error {
					if w.callsAfterLift != 0 {
						return fmt.Errorf("the stand-in had answered %d invocation(s) once the refused run returned", w.callsAfterLift)
					}
					return nil
				},
			},
			{
				States: "the run under the ceiling ran its review, which passed, and held later on no ceiling ask",
				Holds: func(w ceilingWalk) error {
					review, err := stageOf(w.control, pipeline.StageReview)
					if err != nil {
						return err
					}
					if !review.Ran || review.Outcome != pipeline.OutcomePassed {
						return fmt.Errorf("the review under the ceiling ran=%v and ended %q, want it run and passed", review.Ran, review.Outcome)
					}
					if w.control.Decision == nil || w.control.Decision.Stage == pipeline.StageReview.String() {
						return fmt.Errorf("the run under the ceiling stands at %+v, want a hold after review", w.control.Decision)
					}
					if _, ok := ceilingAsk(w.control.Decision); ok {
						return errors.New("the run under the ceiling holds on the ceiling's ask")
					}
					return nil
				},
			},
			{
				States: "the run past the ceiling is recorded held at review, on an ask for a person whose remedy is outside the change",
				Holds: func(w ceilingWalk) error {
					if w.held.Record.Status != store.RunHeld || w.held.Outcome != machine.OutcomeDecision {
						return fmt.Errorf("the run past the ceiling is recorded %q with outcome %q, want held on a decision",
							w.held.Record.Status, w.held.Outcome)
					}
					if w.held.Decision.Stage != pipeline.StageReview.String() {
						return fmt.Errorf("the run past the ceiling holds at %q, want the review stage", w.held.Decision.Stage)
					}
					ask, ok := ceilingAsk(w.held.Decision)
					if !ok {
						return fmt.Errorf("the hold carries %+v and not the ceiling's finding", w.held.Decision.Findings)
					}
					if !ask.Holds() || ask.Action != findings.ActionAsk || !ask.RemedyOutsideChange {
						return fmt.Errorf("the ceiling's finding is %+v, want an ask for a person with its remedy outside the change", ask)
					}
					return nil
				},
			},
			{
				States: "the stand-in was asked once, for the control's review, so the ceiling refused the second review's start",
				Holds: func(w ceilingWalk) error {
					if w.calls != 1 {
						return fmt.Errorf("the stand-in answered %d invocation(s), want the control's one review", w.calls)
					}
					return nil
				},
			},
			{
				States: "the usage view weighs the ceiling reached at the ceiling, and the hold's finding carries its row and those figures",
				Holds: func(w ceilingWalk) error {
					if w.usage.Ceiling.Standing != machine.CeilingReached {
						return fmt.Errorf("the usage view weighs the ceiling %q, want %q", w.usage.Ceiling.Standing, machine.CeilingReached)
					}
					figures, err := w.figures()
					if err != nil {
						return err
					}
					if want := fmt.Sprintf("input_tokens %d of %d (reached)", w.ceiling, w.ceiling); figures != want {
						return fmt.Errorf("the usage view reads %q, want %q", figures, want)
					}
					ask, ok := ceilingAsk(w.held.Decision)
					if !ok {
						return errors.New("the held run carries no ceiling finding to compare")
					}
					if !strings.Contains(ask.Description, reachedRow) || !strings.Contains(ask.Description, figures) {
						return fmt.Errorf("the hold's finding %q does not carry the reached row and %q", ask.Description, figures)
					}
					return nil
				},
			},
			{
				States: "assistant runs RUN, as a person reads it, says the run waits on them and relays the ceiling's row with the figures",
				Holds: func(w ceilingWalk) error {
					text := strings.Join(w.heldLines, "\n")
					if !strings.Contains(text, machine.PhraseFindingsFollow.String()) {
						return errors.New("the detail does not point at the findings holding the decision")
					}
					figures, err := w.figures()
					if err != nil {
						return err
					}
					for _, line := range w.heldLines {
						if strings.Contains(line, reachedRow) && strings.Contains(line, figures) {
							return nil
						}
					}
					return fmt.Errorf("no line of the detail carries the reached row with %q", figures)
				},
			},
		},
		Counterfeits: []journey.Counterfeit[ceilingWalk]{
			{Named: "the branch lifted the ceiling and its run started", Break: func(w ceilingWalk) ceilingWalk {
				w.liftAnswered, w.liftRefusal = true, ""
				return w
			}},
			{Named: "the branch's run was refused for some other reason", Break: func(w ceilingWalk) ceilingWalk {
				w.liftRefusal = "run.start: the service is shutting down"
				return w
			}},
			{Named: "the refused run was left pending", Break: func(w ceilingWalk) ceilingWalk {
				w.liftStatus = store.RunPending
				return w
			}},
			{Named: "the refused run launched the reviewer", Break: func(w ceilingWalk) ceilingWalk {
				w.callsAfterLift = 1
				return w
			}},
			{Named: "the run under the ceiling held at review on the ceiling", Break: func(w ceilingWalk) ceilingWalk {
				decision := *w.held.Decision
				w.control.Decision = &decision
				return w
			}},
			{Named: "the review under the ceiling never ran", Break: func(w ceilingWalk) ceilingWalk {
				w.control = cloneRun(w.control)
				for i := range w.control.Stages {
					if w.control.Stages[i].Stage == pipeline.StageReview.String() {
						w.control.Stages[i].Ran, w.control.Stages[i].Outcome = false, pipeline.OutcomeSkipped
					}
				}
				return w
			}},
			{Named: "the run past the ceiling went on running", Break: func(w ceilingWalk) ceilingWalk {
				w.held.Record.Status, w.held.Outcome = store.RunRunning, machine.OutcomeExecuting
				return w
			}},
			{Named: "the run past the ceiling held at a later stage", Break: func(w ceilingWalk) ceilingWalk {
				decision := *w.held.Decision
				decision.Stage = pipeline.StageTest.String()
				w.held.Decision = &decision
				return w
			}},
			{Named: "the ceiling's ask read as a remedy inside the change", Break: func(w ceilingWalk) ceilingWalk {
				return w.withHeldAsk(func(f *findings.Finding) { f.RemedyOutsideChange = false })
			}},
			{Named: "the ceiling's finding was one a fixer may take", Break: func(w ceilingWalk) ceilingWalk {
				return w.withHeldAsk(func(f *findings.Finding) { f.Action = findings.ActionFix })
			}},
			{Named: "the ceiling refused nothing and the reviewer was asked twice", Break: func(w ceilingWalk) ceilingWalk {
				w.calls = 2
				return w
			}},
			{Named: "the usage view weighed the ceiling under", Break: func(w ceilingWalk) ceilingWalk {
				w.usage.Ceiling.Standing = machine.CeilingUnder
				return w
			}},
			{Named: "the hold's figures disagree with the usage view", Break: func(w ceilingWalk) ceilingWalk {
				return w.withHeldAsk(func(f *findings.Finding) {
					f.Description = strings.ReplaceAll(f.Description, strconv.FormatInt(w.ceiling, 10)+" of", "0 of")
				})
			}},
			{Named: "the detail dropped the ceiling's row", Break: func(w ceilingWalk) ceilingWalk {
				var kept []string
				for _, line := range w.heldLines {
					kept = append(kept, strings.ReplaceAll(line, reachedRow, ""))
				}
				w.heldLines = kept
				return w
			}},
			{Named: "the detail did not relay the findings", Break: func(w ceilingWalk) ceilingWalk {
				var kept []string
				for _, line := range w.heldLines {
					if !strings.Contains(line, machine.PhraseFindingsFollow.String()) {
						kept = append(kept, line)
					}
				}
				w.heldLines = kept
				return w
			}},
		},
	}
	if err := check.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}

// stageOf is one stage's view on a run's answer.
func stageOf(run machine.Run, stage pipeline.Stage) (machine.Stage, error) {
	for _, v := range run.Stages {
		if v.Stage == stage.String() {
			return v, nil
		}
	}
	return machine.Stage{}, fmt.Errorf("the run reports no %s stage", stage)
}
