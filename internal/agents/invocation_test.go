package agents_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

func TestInvocationValidateAcceptsAWellFormedInvocation(t *testing.T) {
	inv := agents.Invocation{
		Prompt: "do the thing",
		Shape:  agents.ShapeReport,
		Dir:    t.TempDir(),
		Env:    map[string]string{"ANTHROPIC_API_KEY": "secret"},
		Model:  "a-model",
	}
	if err := inv.Validate(); err != nil {
		t.Fatalf("a well-formed invocation was refused: %v", err)
	}
}

func TestInvocationValidateRefusals(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		inv     agents.Invocation
		mention string
	}{
		{
			name:    "empty prompt",
			inv:     agents.Invocation{Prompt: "   ", Dir: dir},
			mention: "Prompt",
		},
		{
			name:    "oversize prompt",
			inv:     agents.Invocation{Prompt: strings.Repeat("x", agents.MaxPromptBytes+1), Dir: dir},
			mention: "Prompt",
		},
		{
			name:    "unrecognized shape",
			inv:     agents.Invocation{Prompt: "p", Shape: agents.Shape(9), Dir: dir},
			mention: "Shape",
		},
		{
			name:    "no directory",
			inv:     agents.Invocation{Prompt: "p"},
			mention: "Dir",
		},
		{
			name:    "relative directory",
			inv:     agents.Invocation{Prompt: "p", Dir: filepath.Join("relative", "path")},
			mention: "Dir",
		},
		{
			name:    "empty environment name",
			inv:     agents.Invocation{Prompt: "p", Dir: dir, Env: map[string]string{"": "v"}},
			mention: "Env",
		},
		{
			name:    "environment name carrying an equals sign",
			inv:     agents.Invocation{Prompt: "p", Dir: dir, Env: map[string]string{"A=B": "v"}},
			mention: "Env",
		},
		{
			name:    "environment value carrying a NUL",
			inv:     agents.Invocation{Prompt: "p", Dir: dir, Env: map[string]string{"A": "v\x00w"}},
			mention: "Env",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.inv.Validate()
			if err == nil {
				t.Fatal("expected a refusal, got none")
			}
			if !errors.Is(err, agents.ErrInvalidInvocation) {
				t.Errorf("refusal does not match ErrInvalidInvocation: %v", err)
			}
			if !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("refusal does not name %s: %v", tc.mention, err)
			}
		})
	}
}

// An invalid invocation must be refused before a process starts, which is what
// makes the refusal free and keeps it out of the cost record.
func TestAnInvalidInvocationStartsNothingAndRecordsNothing(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))

	_, err := runner.Run(t.Context(), agents.PurposeReview, agents.Invocation{Prompt: ""})
	if !errors.Is(err, agents.ErrInvalidInvocation) {
		t.Fatalf("expected an invalid-invocation refusal, got %v", err)
	}
	if len(rec.records) != 0 {
		t.Fatalf("an invocation that never started produced %d records", len(rec.records))
	}
}

func TestRunRefusesAnUnrecognizedPurpose(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))

	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "envelope",
		helperResultVar: "fine",
	})
	if _, err := runner.Run(t.Context(), agents.Purpose("audit"), inv); !errors.Is(err, agents.ErrUnrecognizedPurpose) {
		t.Fatalf("expected ErrUnrecognizedPurpose, got %v", err)
	}
	if len(rec.records) != 0 {
		t.Fatalf("a refused purpose produced %d records", len(rec.records))
	}
	// The accepting side of the same rule is
	// TestEveryRunInvocationIsRecognizedAndSessionFree, which runs every
	// purpose this package defines.
}
