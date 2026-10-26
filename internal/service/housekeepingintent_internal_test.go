package service

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// PRD section 6's housekeeping, "instructions": both parts of a pass's
// instructions, the program's and the coordinator's notes, "are the intent
// read off the task, so they are the acceptance criteria of the run that a
// promoted pass's push starts". A pass created through intake and promoted
// with a delivery intent of its own pushes runs whose intent carries the
// delivery intent and both parts: the rule against rewording a rule, the
// scope, and the notes.
func TestAPromotedPassesRunIntentCarriesBothPartsOfItsInstructions(t *testing.T) {
	t.Parallel()
	sub := openHousekeeping(t, housekeepingOff, nil)
	ctx := t.Context()
	created, err := sub.service.queue.Create(ctx, tasks.Request{Project: "agent context", Repository: hkRepository,
		Housekeeping: true, Instructions: "The person renamed the fixer role on Friday."})
	if err != nil {
		t.Fatalf("creating the pass: %v", err)
	}
	records := sub.records(t)
	events, err := records.TaskEvents(ctx, created.Task.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	promoted, _, err := records.TransitionTaskShape(ctx, created.Task.ID, store.TaskInvestigation, store.TaskDelivery, hkRepository,
		hkBranch, config.DeliveryGated, "Correct the README command.", events[len(events)-1].Sequence, "promoted")
	if err != nil {
		t.Fatalf("promoting the pass: %v", err)
	}
	intent := sub.service.taskIntent(promoted)
	for _, want := range []string{"agent context", "Correct the README command.", "Never add, remove or reword a rule",
		"`AGENTS.md`", "renamed the fixer role"} {
		if !strings.Contains(intent, want) {
			t.Errorf("the promoted pass's run intent does not carry %q:\n%s", want, intent)
		}
	}

	// The positive control: an ordinary promoted task's intent is its
	// project and its delivery intent alone, as before.
	plain := store.Task{ID: "taskhkplaina", Project: "plain", DeliveryIntent: store.Known("Deliver it.")}
	if got := sub.service.taskIntent(plain); got != "plain\n\nDeliver it." {
		t.Fatalf("an ordinary promoted task's intent is %q", got)
	}
}
