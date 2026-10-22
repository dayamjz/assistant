package standin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheModelledTrustRefusalIsTheRecordedOne holds the refusal the stand-in
// prints for a Cursor one-shot that does not tell the CLI to trust its
// directory to what cursor-agent 2026.10.19-6bbf2c4 printed, recorded in
// internal/agents/testdata. The recorded directory is read from the
// recording, the one line that is an absolute path, so the comparison covers
// every other byte.
func TestTheModelledTrustRefusalIsTheRecordedOne(t *testing.T) {
	t.Parallel()
	recorded, err := os.ReadFile(filepath.Join("..", "testdata", "cursor-print-untrusted-2026.10.19.txt"))
	if err != nil {
		t.Fatalf("reading the recording: %v", err)
	}
	var dirs []string
	for line := range strings.Lines(string(recorded)) {
		if trimmed := strings.TrimSpace(line); filepath.IsAbs(trimmed) {
			dirs = append(dirs, trimmed)
		}
	}
	if len(dirs) != 1 {
		t.Fatalf("the recording names directories %q, want exactly one", dirs)
	}
	if got := cursorUntrusted(dirs[0]); got != string(recorded) {
		t.Errorf("the model refuses with\n%q\nand the recording is\n%q", got, recorded)
	}
}

// TestOnlyATrustFlagLetsACursorOneShotRun asks the stand-in's trust predicate
// of the command lines the adapter writes and of the flags the recorded
// refusal and the CLI's help name, so a flag added to or dropped from the
// model fails here.
func TestOnlyATrustFlagLetsACursorOneShotRun(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args    []string
		trusted bool
	}{
		{[]string{"--mode", "ask", "--print", "--output-format", "json", "--", "p"}, false},
		{[]string{"--mode", "ask", "--trust", "--print", "--output-format", "json", "--", "p"}, true},
		{[]string{"--force", "--print", "--output-format", "json", "--", "p"}, true},
		{[]string{"-f", "--print", "--", "p"}, true},
		{[]string{"--yolo", "--print", "--", "p"}, true},
		// A trust flag in the prompt is not an option.
		{[]string{"--mode", "ask", "--print", "--", "--trust"}, false},
	} {
		if got := cursorTrusted(c.args); got != c.trusted {
			t.Errorf("cursorTrusted(%q) = %v, want %v", c.args, got, c.trusted)
		}
	}
}
