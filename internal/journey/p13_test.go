package journey_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// spokenFleet is what the human rendering of the fleet said, through the
// binary, beside what the structured rendering of the same fleet carried.
type spokenFleet struct {
	// tasksLines is what assistant tasks printed, one line each, and
	// watchLines what assistant watch printed before it was interrupted.
	tasksLines []string
	watchLines []string
	// watchEnded is whether watch was stopped by the harness rather than
	// returning on its own.
	watchEnded bool
	// waiting is the wait the paused task's worker declared, and ids are
	// the identifiers the two planted tasks were given.
	waiting string
	ids     []string
	// structuredCarries is whether --json tasks over the same fleet carried
	// an identifier and a state label, which is what makes their absence
	// from the lines above discriminating.
	structuredCarries bool
	// violations is the derivation the lines are held to, from
	// internal/machine/vocabtest, closed over the test.
	violations func(string) []vocabtest.Term
}

// TestTheFleetIsSpokenInPhrasesThroughTheBinary drives the human rendering
// of assistant tasks and assistant watch, without --json, over a fleet whose
// records carry every word P13 keeps off a line: an identifier in the
// generator's format, a copy path, a state label and an event kind. Every
// line printed is held to the forbidden set vocabtest derives from the
// owners, the paused worker's declared wait is relayed and the state is
// never the word, and a task whose newest entry is a relaunch note is no
// word from the worker rather than a failure.
//
// It cites nothing. P13 is cited at package reach, by internal/machine for
// the phrases in its table and by internal/cli for the renderers it
// enumerates as phrased, and the row in Coverage says why the claim does not
// widen here and which renderers still print this build's words. What this
// walk adds is that the two fleet verbs, as a person drives them through the
// binary, print those phrases and nothing else; the row records the walk and
// the skip it takes.
func TestTheFleetIsSpokenInPhrasesThroughTheBinary(t *testing.T) {
	vocabtest.Control(t)
	j := inClone(t)
	written := records(t, j)

	const (
		paused     = "abcdefghij23"
		relaunched = "relaunchedbb"
		waiting    = "a sign-off from the security team"
	)
	for _, task := range []struct {
		id, project string
		events      [][2]string
	}{
		{paused, "the parser", [][2]string{{"waiting-external", waiting}}},
		{relaunched, "the indexer", [][2]string{{"relaunched", "attempt 2 in the same copy"}}},
	} {
		if _, err := written.CreateTask(t.Context(), store.Task{
			ID: task.id, Shape: store.TaskInvestigation, Project: task.project, Mode: "journey",
			WorktreePath: vocabtest.SampleRoot() + "/tasks/" + task.id + "/copy",
		}, store.Disposition{}); err != nil {
			t.Fatalf("recording task %s: %v", task.id, err)
		}
		for _, e := range task.events {
			if _, err := written.AppendTaskEvent(t.Context(), task.id, e[0], e[1]); err != nil {
				t.Fatalf("appending %s to %s: %v", e[0], task.id, err)
			}
		}
	}

	var fleet machine.Tasks
	if err := succeeds(t, j.Command("tasks")).Decode(&fleet); err != nil {
		t.Fatalf("reading the fleet as a document: %v", err)
	}
	structured := succeeds(t, j.Command("tasks")).Stdout
	plain := j.CommandPlainBounded(j.Dir(), 0, "tasks")
	if plain.Code != machine.ExitOK {
		t.Fatalf("assistant tasks exited %s:\n%s", plain.Code, plain)
	}
	watching := j.CommandPlainBounded(j.Dir(), 2*time.Second, "watch")
	t.Logf("assistant tasks:\n%s\nassistant watch:\n%s", plain.Stdout, watching.Stdout)

	observed := spokenFleet{
		tasksLines:        lines(plain.Stdout),
		watchLines:        lines(watching.Stdout),
		watchEnded:        watching.Ended,
		waiting:           waiting,
		ids:               []string{paused, relaunched},
		structuredCarries: strings.Contains(structured, paused) && strings.Contains(structured, string(taskstate.Paused)),
		violations:        func(text string) []vocabtest.Term { return vocabtest.Violations(t, text) },
	}

	spoken := journey.Check[spokenFleet]{
		What: "P13: the fleet as a person reads it",
		Clauses: []journey.Clause[spokenFleet]{
			{
				States: "assistant tasks printed a line for each planted task, and watch opened with the same lines in its catch-up read",
				Holds: func(f spokenFleet) error {
					if len(f.tasksLines) < len(f.ids) {
						return fmt.Errorf("tasks printed %d lines for %d tasks", len(f.tasksLines), len(f.ids))
					}
					for _, line := range f.tasksLines {
						if !slices.Contains(f.watchLines, line) {
							return fmt.Errorf("watch did not open with the listing line %q in its catch-up read", line)
						}
					}
					return nil
				},
			},
			{
				States:  "no line of either verb carries a word the owners forbid: a state label, an event kind, a stage name, a copy path or an identifier",
				Absence: true,
				Possible: func(f spokenFleet) error {
					if !f.structuredCarries {
						return errors.New("the structured rendering of the same fleet carried no identifier or label, " +
							"so nothing here shows the human rendering kept one out")
					}
					return nil
				},
				Holds: func(f spokenFleet) error {
					for _, line := range append(slices.Clone(f.tasksLines), f.watchLines...) {
						if found := f.violations(line); len(found) > 0 {
							return fmt.Errorf("the line %q carries %q, which is %s's word", line, found[0].Text, found[0].Owner)
						}
					}
					return nil
				},
			},
			{
				States:  "no line carries a planted identifier",
				Absence: true,
				Possible: func(f spokenFleet) error {
					if !f.structuredCarries {
						return errors.New("the structured rendering carried no identifier")
					}
					return nil
				},
				Holds: func(f spokenFleet) error {
					for _, line := range append(slices.Clone(f.tasksLines), f.watchLines...) {
						for _, id := range f.ids {
							if strings.Contains(line, id) {
								return fmt.Errorf("the line %q carries the identifier %s", line, id)
							}
						}
					}
					return nil
				},
			},
			{
				States: "the paused task's line relays what its worker said it is waiting on",
				Holds: func(f spokenFleet) error {
					for _, line := range f.tasksLines {
						if strings.Contains(line, f.waiting) {
							return nil
						}
					}
					return fmt.Errorf("no line carries the declared wait %q", f.waiting)
				},
			},
			{
				States: "the relaunched task's line says there is no word from the worker yet",
				Holds: func(f spokenFleet) error {
					for _, line := range f.tasksLines {
						if strings.Contains(line, machine.NoWordYet.Outcome) {
							return nil
						}
					}
					return fmt.Errorf("no line says %q", machine.NoWordYet.Outcome)
				},
			},
			{
				States: "watch followed the stream until it was stopped",
				Holds: func(f spokenFleet) error {
					if !f.watchEnded {
						return errors.New("watch returned on its own")
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[spokenFleet]{
			{Named: "a line carried the state as a word", Break: func(f spokenFleet) spokenFleet {
				f.tasksLines = append(slices.Clone(f.tasksLines), "the parser is "+string(taskstate.Paused))
				return f
			}},
			{Named: "a line carried the task's identifier", Break: func(f spokenFleet) spokenFleet {
				f.watchLines = append(slices.Clone(f.watchLines), f.ids[1]+"  investigation")
				return f
			}},
			{Named: "the wait was translated away", Break: func(f spokenFleet) spokenFleet {
				var kept []string
				for _, line := range f.tasksLines {
					kept = append(kept, strings.ReplaceAll(line, f.waiting, "something"))
				}
				f.tasksLines = kept
				return f
			}},
			{Named: "the relaunched task read as a failure", Break: func(f spokenFleet) spokenFleet {
				var kept []string
				for _, line := range f.tasksLines {
					kept = append(kept, strings.ReplaceAll(line, machine.NoWordYet.Outcome, "This work stopped."))
				}
				f.tasksLines = kept
				return f
			}},
			{Named: "tasks printed nothing", Break: func(f spokenFleet) spokenFleet {
				f.tasksLines = nil
				return f
			}},
			{Named: "watch opened without the listing", Break: func(f spokenFleet) spokenFleet {
				f.watchLines = nil
				return f
			}},
			{Named: "watch returned on its own", Break: func(f spokenFleet) spokenFleet {
				f.watchEnded = false
				return f
			}},
		},
	}
	if err := spoken.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}

// The P13 row names the renderers the phrase table covers, the evidence
// documents and the ones that remain, and all three lists are internal/cli's
// enumeration and not a list kept here: a renderer rerouted, reclassified,
// added or removed there fails this rather than leaving the note to say what
// used to be true.
func TestTheP13RowNamesTheRenderersAsTheSurfaceEnumeratesThem(t *testing.T) {
	covered, evidence, remaining := journey.P13Renderers()
	want := map[cli.Voice][]string{}
	for _, r := range cli.Renderings() {
		want[r.Voice] = append(want[r.Voice], r.Name)
	}
	for _, c := range []struct {
		voice cli.Voice
		got   []string
	}{{cli.VoicePhrased, covered}, {cli.VoiceEvidence, evidence}, {cli.VoiceUnsettled, remaining}} {
		if !slices.Equal(c.got, want[c.voice]) {
			t.Errorf("the row lists %v as %s; internal/cli enumerates %v", c.got, c.voice, want[c.voice])
		}
		delete(want, c.voice)
	}
	if len(want) != 0 {
		t.Errorf("internal/cli enumerates renderers in a voice the row has no list for: %v", want)
	}
	var note string
	for _, row := range journey.Coverage() {
		if row.Principle == principles.P13 {
			note = row.Note
		}
	}
	for _, name := range slices.Concat(covered, evidence, remaining) {
		if !strings.Contains(note, name) {
			t.Errorf("the P13 note does not name the renderer %q", name)
		}
	}
}

// lines splits what a verb printed into its non-empty lines.
func lines(stdout string) []string {
	var out []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
