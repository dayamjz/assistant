package agents_test

import (
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// TestACursorReadCallAnswersInAFreshDirectoryAndCannotEdit runs a review,
// which is GrantRead, through the production Cursor adapter against the
// stand-in in a fresh directory, as a run's copy is. cursor-agent
// 2026.10.19-6bbf2c4 refuses a one-shot there unless its command line tells
// it to trust the directory, and the stand-in models that refusal from the
// recording, so before the read grant carried --trust this call came back a
// failure with the CLI's "Workspace Trust Required" on standard error
// (#511). The grant still runs the CLI in ask mode, which it documents as
// read-only, and never with --force, which would auto-approve commands.
//
// A fix round, GrantEdit, is asked the same way to show its --force already
// answers there without --trust, so the change is confined to the read grant.
func TestACursorReadCallAnswersInAFreshDirectoryAndCannotEdit(t *testing.T) {
	t.Parallel()
	agent := standin.NewCursor(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text("ok")}}})

	review, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, nil))
	if err != nil {
		t.Fatalf("a read-only Cursor call in a fresh directory came back %v, want its answer", err)
	}
	if review.Text != "ok" {
		t.Errorf("the read-only call answered %q, want ok", review.Text)
	}
	fix, err := agent.Runner().Run(t.Context(), agents.PurposeFix, invocation(t, agents.ShapeText, nil))
	if err != nil {
		t.Fatalf("a Cursor fix round in a fresh directory came back %v, want its answer", err)
	}
	if fix.Text != "ok" {
		t.Errorf("the fix round answered %q, want ok", fix.Text)
	}

	calls := agent.Calls()
	if len(calls) != 2 || !calls[0].Answered() || !calls[1].Answered() {
		t.Fatalf("the stand-in saw %v, want two answered calls", calls)
	}
	read, edit := options(calls[0].Args), options(calls[1].Args)
	if !slices.Contains(read, "--trust") {
		t.Errorf("the read grant does not tell the CLI to trust its directory: %q", read)
	}
	if at := slices.Index(read, "--mode"); at < 0 || at+1 >= len(read) || read[at+1] != "ask" {
		t.Errorf("the read grant does not run the CLI in ask mode: %q", read)
	}
	for _, widening := range []string{"--force", "-f", "--yolo"} {
		if slices.Contains(read, widening) {
			t.Errorf("the read grant carries %s, which auto-approves commands: %q", widening, read)
		}
	}
	if !slices.Contains(edit, "--force") || slices.Contains(edit, "--trust") || slices.Contains(edit, "--mode") {
		t.Errorf("the edit grant is %q, want --force alone of the grant flags", edit)
	}
}

// options is a command line's words before the "--" that ends its options,
// so a flag spelled in the prompt is not read as one.
func options(args []string) []string {
	if at := slices.Index(args, "--"); at >= 0 {
		return args[:at]
	}
	return args
}
