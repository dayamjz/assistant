package agents

import "testing"

// The line a new session answers is the pane's process and the server's,
// and nothing else is a creation a handle is returned over: one number,
// which is what the command answered before the host was read, three, a
// word, and a zero are each refused before any identity is read.
func TestTheTerminalAnswerIsTwoProcessIdentifiers(t *testing.T) {
	t.Parallel()
	pane, server, err := parseTerminalAnswer(" 4312 4311\n")
	if err != nil || pane != 4312 || server != 4311 {
		t.Fatalf("parseTerminalAnswer = %d, %d, %v; want the pane 4312 and the server 4311", pane, server, err)
	}
	for _, answer := range []string{"", "4312", "4312\n", "4312 4311 4310", "4312 server", "0 4311", "4312 -1"} {
		if pane, server, err := parseTerminalAnswer(answer); err == nil {
			t.Errorf("parseTerminalAnswer(%q) = %d, %d and no error; want a refusal", answer, pane, server)
		}
	}
}
