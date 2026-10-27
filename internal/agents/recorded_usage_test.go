package agents_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// TestEachAdapterReadsTheCountsItsCLIPrinted plays what each real CLI printed
// for one call, recorded in testdata, through the stand-in to the production
// adapter, and asks the record for the four token counts the recording
// carries. Each CLI reported all four, so an adapter recording any of them as
// unreported is reading keys its CLI does not write: a ceiling set on that
// count would then refuse every start after one call of that agent, and the
// usage view would show its spend as unknown (#510).
//
// The figures are the recording's own, so a count read from the wrong key
// fails even where the stand-in's model happens to agree with the adapter.
func TestEachAdapterReadsTheCountsItsCLIPrinted(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		recording string
		build     func(testing.TB, standin.Script) *standin.Agent
		want      [4]int64 // input, output, cache read, cache creation
	}{
		{"claude 2.1.306", "claude-print-2.1.306.json",
			func(tb testing.TB, s standin.Script) *standin.Agent { return standin.New(tb, s) },
			[4]int64{2, 4, 10835, 18808}},
		{"cursor-agent 2026.10.19", "cursor-print-2026.10.19.json",
			func(tb testing.TB, s standin.Script) *standin.Agent { return standin.NewCursor(tb, s) },
			[4]int64{8502, 88, 6016, 0}},
		{"codex 0.163.0", "codex-exec-0.163.0.jsonl",
			func(tb testing.TB, s standin.Script) *standin.Agent { return standin.NewCodex(tb, s) },
			[4]int64{22733, 5, 12672, 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			recorded, err := os.ReadFile(filepath.Join("testdata", c.recording))
			if err != nil {
				t.Fatalf("reading the recording: %v", err)
			}
			agent := c.build(t, standin.Script{Steps: []standin.Step{{Reply: standin.Reply{Stdout: string(recorded)}}}})
			result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, nil))
			if err != nil {
				t.Fatalf("the recorded call came back %v, want its result", err)
			}
			if result.Text != "ok" {
				t.Errorf("the recorded call answered %q, want the recording's ok", result.Text)
			}
			usage := result.Record.Usage
			for i, count := range []struct {
				name  string
				count agents.Count
			}{
				{"input", usage.InputTokens}, {"output", usage.OutputTokens},
				{"cache read", usage.CacheReadTokens}, {"cache creation", usage.CacheCreationTokens},
			} {
				if n, reported := count.count.Value(); !reported || n != c.want[i] {
					t.Errorf("the %s count reads %s, want the recorded %d", count.name, count.count, c.want[i])
				}
			}
		})
	}
}
