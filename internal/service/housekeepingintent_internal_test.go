package service

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// PRD section 6's housekeeping, "instructions": both parts of a pass's
// instructions, the program's and the coordinator's notes, "are the intent
// read off the task, so they are the acceptance criteria of the run that a
// promoted pass's push starts". A pass promoted with a delivery intent of
// its own pushes runs whose intent carries its project, that delivery
// intent and its instructions as intake wrote them, and not the sections
// the program appends for the worker. The positive control is an ordinary
// promoted task, whose intent is its project and its delivery intent alone.
// A pass whose instructions cannot be read is refused its intent, so a run
// is never held to less than them.
func TestAPromotedPassesRunIntentCarriesItsInstructions(t *testing.T) {
	t.Parallel()
	sub := newDispatchSubjectOf(t, func(id, copyPath string) (store.Task, store.Disposition) {
		return store.Task{
			ID: id, Shape: store.TaskInvestigation, Project: "agent context", Mode: "interactive", WorktreePath: copyPath,
			RepositoryID: store.Known("subject"), Role: config.RoleHousekeeping, Housekeeping: true,
		}, store.Disposition{Delivered: taskstate.Delivered}
	})
	ctx := t.Context()
	path, err := sub.service.home.TaskInstructions(sub.task)
	if err != nil {
		t.Fatal(err)
	}
	const program = "Never add, remove or reword a rule. The scope is `AGENTS.md`."
	const notes = "The person renamed the fixer role on Friday."
	if err := os.WriteFile(path, []byte(program+"\n\n"+notes+"\n\n"+secondCheckHeading+"\n\nthe program's words to the worker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := sub.records.TaskEvents(ctx, sub.task, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	promoted, _, err := sub.records.TransitionTaskShape(ctx, sub.task, store.TaskInvestigation, store.TaskDelivery, "subject",
		"housekeeping/agent-context", config.DeliveryGated, "Correct the README command.", events[len(events)-1].Sequence, "promoted",
		store.ResolvedByMachineInterface(), nil)
	if err != nil {
		t.Fatalf("promoting the pass: %v", err)
	}
	intent, err := sub.service.taskIntent(promoted)
	if err != nil {
		t.Fatalf("composing the promoted pass's intent: %v", err)
	}
	for _, want := range []string{"agent context", "Correct the README command.", program, notes} {
		if !strings.Contains(intent, want) {
			t.Errorf("the promoted pass's run intent does not carry %q:\n%s", want, intent)
		}
	}
	if strings.Contains(intent, "the program's words to the worker") {
		t.Errorf("the promoted pass's run intent carries a section the program appended for the worker:\n%s", intent)
	}

	plain := store.Task{ID: "taskhkplaina", Project: "plain", DeliveryIntent: store.Known("Deliver it.")}
	if got, err := sub.service.taskIntent(plain); err != nil || got != "plain\n\nDeliver it." {
		t.Fatalf("an ordinary promoted task's intent is %q, %v", got, err)
	}

	// With its instructions gone (the review of #685 at b73ff76), the pass's
	// intent is refused rather than composed without its scope, rules and
	// notes, so no run its push starts is held to less. An ordinary task
	// whose instructions are gone keeps its project and delivery intent.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := sub.service.taskIntent(promoted); !errors.Is(err, errPassIntentUnread) {
		t.Fatalf("the promoted pass's intent with its instructions gone = %q, %v; want errPassIntentUnread", got, err)
	}
	if got, err := sub.service.taskIntent(plain); err != nil || got != "plain\n\nDeliver it." {
		t.Fatalf("an ordinary promoted task's intent with no instructions file is %q, %v", got, err)
	}
}
