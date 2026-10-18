package agents_test

import (
	"context"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/principles"
)

func TestFallbackOnlyAfterAnAvailabilityFailure(t *testing.T) {
	for _, test := range []struct {
		name     string
		reply    standin.Reply
		fallback bool
	}{
		{"quota", standin.Failed("quota exceeded"), true},
		{"unavailable", standin.Failed("service unavailable"), true},
		{"bad-output", standin.Malformed("quota exceeded"), false},
		{"ordinary-error", standin.Failed("could not read repository"), false},
		{"successful-mention", standin.Text("quota exceeded is an example"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			primary := standin.New(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: test.reply}}})
			backup := standin.New(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text("backup")}}})
			opened := 0
			runner := agents.WithFallback(primary.Runner(), func(context.Context) (agents.Runner, error) { opened++; return backup.Runner(), nil })
			inv := invocation(t, agents.ShapeText, nil)
			result, err := runner.Run(t.Context(), agents.PurposeReview, inv)
			if test.fallback {
				if err != nil || result.Text != "backup" || opened != 1 {
					t.Fatalf("fallback = %q, %v, opens %d", result.Text, err, opened)
				}
			} else if opened != 0 || len(backup.Calls()) != 0 {
				t.Fatal("unexpected fallback")
			}
		})
	}
}

// P4: the backup fix session survives reopen, but a review remains fresh.
func TestFallbackFixerKeepsTheBackupSessionAcrossReopen(t *testing.T) {
	principles.Cite(t, principles.P4)
	primary := standin.New(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Failed("quota exceeded")}}})
	backup := standin.NewCursor(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text("fixed")}}})
	runner := agents.WithFallback(primary.Runner(), func(context.Context) (agents.Runner, error) { return backup.Runner(), nil })
	inv := invocation(t, agents.ShapeText, nil)
	fixer, err := agents.OpenFixer(t.Context(), runner, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixer.Apply(t.Context(), inv); err != nil {
		t.Fatal(err)
	}
	reference := fixer.Reference()
	if agents.SessionAgent(runner, reference) != "cursor" {
		t.Fatal("backup session owner was lost")
	}
	if reference == "" {
		t.Fatal("backup reference missing")
	}
	reopened, err := agents.OpenFixer(t.Context(), runner, reference)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Apply(t.Context(), inv); err != nil {
		t.Fatal(err)
	}
	if len(primary.Calls()) != 1 {
		t.Fatalf("primary retried after handoff: %v", primary.Calls())
	}
	calls := backup.Calls()
	if len(calls) != 2 || calls[0].Session() != "" || calls[1].Session() == "" {
		t.Fatalf("backup sessions: %v", calls)
	}
	if _, err = runner.Run(t.Context(), agents.PurposeReview, inv); err != nil {
		t.Fatal(err)
	}
	calls = backup.Calls()
	if calls[len(calls)-1].Session() != "" {
		t.Fatal("review resumed a fix session")
	}
}
