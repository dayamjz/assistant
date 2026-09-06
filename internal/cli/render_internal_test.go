package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// A finding's text is whatever a stage's agent wrote, and a terminal reads an
// escape sequence in it as an instruction rather than as text: clearing the
// screen, rewriting the lines above it, or setting the clipboard. The
// structured rendering escapes every control character on its way through
// machine.Encoder, and the rendering a person reads has to be as safe.
func TestAControlCharacterInAFindingDoesNotReachTheTerminal(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	readOut(&out, machine.Run{
		Record:  store.Run{ID: "abc", Branch: "work", Status: store.RunHeld},
		Outcome: machine.OutcomeDecision,
		Decision: &machine.Decision{
			Decision: graph.Decision{
				Node:     "intent.hold",
				Question: "clear the screen \x1b[2J and ask",
				Options:  []string{"approved\x1b[1m", "cancelled"},
			},
			Stage: "intent",
			Findings: []findings.Finding{{
				ID:       "finding\x1b[2J",
				Severity: findings.SeverityWarning,
				Action:   findings.ActionAsk,
				Location: findings.Location{Path: "pkg/\x1bfile.go", Line: 12},
				Description: "the first line\n" +
					"a second line carrying \x1b]52;c;cGF5bG9hZA==\x07 an escape",
			}},
		},
	})
	rendered := out.String()

	if strings.ContainsRune(rendered, 0x1b) {
		t.Fatalf("an escape character reached the terminal unescaped:\n%q", rendered)
	}
	if strings.ContainsRune(rendered, 0x07) {
		t.Fatalf("a bell character reached the terminal unescaped:\n%q", rendered)
	}
	if !strings.Contains(rendered, `\x1b`) {
		t.Fatalf("the control characters were dropped rather than shown:\n%q", rendered)
	}
	// The text either side of them is still there, unsummarized, and the
	// description is still two lines.
	for _, want := range []string{"the first line", "a second line carrying ", "an escape", "pkg/", "12"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the rendering lost %q:\n%s", want, rendered)
		}
	}
	if !strings.Contains(rendered, "the first line\n") {
		t.Fatalf("the description's own line breaks did not survive:\n%q", rendered)
	}
}

// Text with nothing in it to escape is written as it stands, so the protection
// costs the ordinary case nothing.
func TestOrdinaryFindingTextIsWrittenAsItStands(t *testing.T) {
	t.Parallel()
	const text = "a finding with nothing to escape in it"
	if got := printable(text); got != text {
		t.Fatalf("printable rewrote ordinary text to %q", got)
	}
}

// A fix round's summary is text a fixer agent wrote, so it is escaped on the
// way to the terminal exactly as a finding's description is. It is written on
// one line, so a line break in it is escaped too rather than breaking the line
// it is part of.
func TestAControlCharacterInAFixSummaryDoesNotReachTheTerminal(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	readOut(&out, machine.Run{
		Record:  store.Run{ID: "abc", Branch: "work", Status: store.RunRunning},
		Outcome: machine.OutcomeDecision,
		Stages: []machine.Stage{{
			Stage:   "review",
			Outcome: pipeline.OutcomePassed,
			Ran:     true,
			Fix:     "rewrote the guard\x1b[2J and\nre-ran the tests",
		}},
	})
	rendered := out.String()

	if strings.ContainsRune(rendered, 0x1b) {
		t.Fatalf("an escape character in a fix summary reached the terminal unescaped:\n%q", rendered)
	}
	if !strings.Contains(rendered, `\x1b`) {
		t.Fatalf("the control character was dropped rather than shown:\n%q", rendered)
	}
	if !strings.Contains(rendered, "rewrote the guard") || !strings.Contains(rendered, "re-ran the tests") {
		t.Fatalf("the rendering lost the summary's own words:\n%s", rendered)
	}
	// The summary is one line, so its own line break may not split it.
	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if strings.Contains(line, "re-ran the tests") && !strings.Contains(line, "rewrote the guard") {
			t.Fatalf("a line break in the summary broke the line it is part of:\n%s", rendered)
		}
	}
}
