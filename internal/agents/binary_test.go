package agents_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

func TestLookPathFirstPrefersTheEarliestCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("planting bare executable names needs a portable script")
	}
	dir := t.TempDir()
	for _, name := range []string{"second", "first"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	got, err := agents.LookPathFirst("missing", "first", "second")
	if err != nil {
		t.Fatalf("LookPathFirst: %v", err)
	}
	if !strings.HasSuffix(got, string(filepath.Separator)+"first") {
		t.Errorf("resolved %q, want …/first", got)
	}
}

func TestWithoutEnvNamesDropsOnlyTheNamedVariables(t *testing.T) {
	got := agents.WithoutEnvNames(
		[]string{"KEEP=1", "CURSOR_API_KEY=secret", "XAI_API_KEY=other", "ALSO=2"},
		agents.CursorKeyVariable, agents.GrokKeyVariable,
	)
	want := []string{"KEEP=1", "ALSO=2"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCursorResolvesCursorAgentWhenThatIsWhatIsOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("planting bare executable names needs a portable script")
	}
	dir := t.TempDir()
	// cursor-agent wins over historical "cursor"; bare "agent" is not a candidate.
	for _, name := range []string{"cursor", "cursor-agent"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	got, err := agents.LookPathFirst(agents.CursorBinaries...)
	if err != nil {
		t.Fatalf("LookPathFirst: %v", err)
	}
	if !strings.HasSuffix(got, string(filepath.Separator)+"cursor-agent") {
		t.Errorf("resolved %q, want …/cursor-agent ahead of cursor", got)
	}
	if _, err := agents.CursorFactory().New(t.Context(), nil); err != nil {
		t.Fatalf("CursorFactory.New: %v", err)
	}
}

func TestCursorDoesNotResolveBareAgentAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("planting bare executable names needs a portable script")
	}
	dir := t.TempDir()
	// Grok's installer links "agent"; that must not satisfy CursorBinaries.
	if err := os.WriteFile(filepath.Join(dir, "agent"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, err := agents.LookPathFirst(agents.CursorBinaries...); err == nil {
		t.Fatal("LookPathFirst resolved bare agent; CursorBinaries must omit it")
	}
	if _, err := agents.CursorFactory().New(t.Context(), nil); err == nil {
		t.Fatal("CursorFactory.New succeeded with only bare agent on PATH")
	}
	if slices.Contains(agents.CursorBinaries, "agent") {
		t.Fatal(`CursorBinaries still lists "agent"`)
	}
}

func TestCursorStripsAPIKeyAndWritesGrant(t *testing.T) {
	t.Parallel()
	factory := agents.CursorFactory(
		agents.WithCursorBinary(helperBinary(t)),
		agents.WithCursorBaseEnvironment([]string{
			helperModeVar + "=call",
			agents.CursorKeyVariable + "=should-not-reach-agent",
			"KEEP=yes",
		}),
	)
	runner, err := factory.New(t.Context(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	review, err := runner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, nil))
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	reviewCall := decodeCall(t, review.Text)
	reviewArgs := reviewCall.Args
	if !slices.Contains(reviewArgs, "--mode") || !slices.Contains(reviewArgs, "ask") {
		t.Errorf("review grant missing --mode ask: %q", reviewArgs)
	}
	if slices.Contains(reviewArgs, "--force") {
		t.Errorf("review must not carry --force: %q", reviewArgs)
	}
	if reviewCall.Stdin != "" {
		t.Errorf("cursor must not send the prompt on stdin: %q", reviewCall.Stdin)
	}
	if !slices.Contains(reviewArgs, "--") || !slices.Contains(reviewArgs, "review this change") {
		t.Errorf("cursor must pass the prompt as a trailing argv operand after --: %q", reviewArgs)
	}

	fix, err := runner.Run(t.Context(), agents.PurposeFix, invocation(t, agents.ShapeText, nil))
	if err != nil {
		t.Fatalf("fix: %v", err)
	}
	fixArgs := decodeCall(t, fix.Text).Args
	if !slices.Contains(fixArgs, "--force") {
		t.Errorf("fix grant missing --force: %q", fixArgs)
	}

	envFactory := agents.CursorFactory(
		agents.WithCursorBinary(helperBinary(t)),
		agents.WithCursorBaseEnvironment([]string{
			helperModeVar + "=environment",
			agents.CursorKeyVariable + "=should-not-reach-agent",
			"KEEP=yes",
		}),
	)
	envRunner, err := envFactory.New(t.Context(), nil)
	if err != nil {
		t.Fatalf("env New: %v", err)
	}
	envRes, err := envRunner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, nil))
	if err != nil {
		t.Fatalf("environment run: %v", err)
	}
	var environ []string
	if err := json.Unmarshal([]byte(envRes.Text), &environ); err != nil {
		t.Fatalf("decoding environment: %v\n%s", err, envRes.Text)
	}
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if name == agents.CursorKeyVariable {
			t.Fatalf("CURSOR_API_KEY reached the agent: %q", entry)
		}
	}
	if !slices.Contains(environ, "KEEP=yes") {
		t.Errorf("KEEP was stripped: %v", environ)
	}
}

func TestGrokUsesPromptFileStripsAPIKeyAndWritesGrant(t *testing.T) {
	t.Parallel()
	factory := agents.GrokFactory(
		agents.WithGrokBinary(helperBinary(t)),
		agents.WithGrokBaseEnvironment([]string{
			helperModeVar + "=call",
			agents.GrokKeyVariable + "=should-not-reach-agent",
		}),
	)
	runner, err := factory.New(t.Context(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	review, err := runner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, nil))
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	call := decodeCall(t, review.Text)
	if !slices.Contains(call.Args, "--prompt-file") {
		t.Fatalf("grok did not pass --prompt-file: %q", call.Args)
	}
	if slices.Contains(call.Args, "--print") {
		t.Errorf("grok must not pass Claude's --print: %q", call.Args)
	}
	if call.Stdin != "review this change" {
		t.Fatalf("prompt via --prompt-file = %q, want the invocation prompt", call.Stdin)
	}
	if !slices.Contains(call.Args, "--permission-mode") || !slices.Contains(call.Args, "plan") {
		t.Errorf("review grant missing --permission-mode plan: %q", call.Args)
	}

	fix, err := runner.Run(t.Context(), agents.PurposeFix, invocation(t, agents.ShapeText, nil))
	if err != nil {
		t.Fatalf("fix: %v", err)
	}
	fixArgs := decodeCall(t, fix.Text).Args
	if !slices.Contains(fixArgs, "acceptEdits") {
		t.Errorf("fix grant missing acceptEdits: %q", fixArgs)
	}

	envFactory := agents.GrokFactory(
		agents.WithGrokBinary(helperBinary(t)),
		agents.WithGrokBaseEnvironment([]string{
			helperModeVar + "=environment",
			agents.GrokKeyVariable + "=should-not-reach-agent",
			"KEEP=yes",
		}),
	)
	envRunner, err := envFactory.New(t.Context(), nil)
	if err != nil {
		t.Fatalf("env New: %v", err)
	}
	envRes, err := envRunner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, nil))
	if err != nil {
		t.Fatalf("environment run: %v", err)
	}
	var environ []string
	if err := json.Unmarshal([]byte(envRes.Text), &environ); err != nil {
		t.Fatalf("decoding environment: %v\n%s", err, envRes.Text)
	}
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if name == agents.GrokKeyVariable {
			t.Fatalf("XAI_API_KEY reached the agent: %q", entry)
		}
	}
}
