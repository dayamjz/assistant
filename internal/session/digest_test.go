package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/session"
)

// Normalize applies exactly the rules its doc states, and leaves alone what
// it says it leaves alone.
func TestNormalizeRules(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"a carriage return keeps what follows it", "progress [=  ]\rprogress [== ]\rprogress [===]\n", "progress [===]"},
		{"trailing spaces and tabs go", "done  \t\nnext\n", "done\nnext"},
		{"a final newline is not significant", "a\nb", "a\nb"},
		{"two final newlines keep one blank line", "a\n\n", "a\n"},
		{"leading whitespace stays", "  indented\n", "  indented"},
		{"escape sequences pass through", "\x1b[32mok\x1b[0m\n", "\x1b[32mok\x1b[0m"},
		{"line order stays", "b\na\n", "b\na"},
		{"empty is empty", "", ""},
	}
	for _, tc := range cases {
		if got := session.Normalize(tc.in); got != tc.want {
			t.Errorf("%s: Normalize(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	if session.Digest("a\rb\n") != session.Digest("b") || session.Digest("a") == session.Digest("b") {
		t.Error("Digest does not hash the normalized text")
	}
}

// A redraw on the stream, a line overwritten with carriage returns the way a
// progress line is, digests the same as the line it ended on, read off two
// real workers whose raw captures differ; a line appended afterwards
// changes the digest. The comparison is the test's: the provider hands back
// bytes and a digest and no verdict.
func TestARedrawDigestsUnchangedAndAnAppendedLineChangesIt(t *testing.T) {
	script := standin.Script{Worker: &standin.WorkerScript{
		Greeting: "worker ready",
		Exchanges: []standin.Exchange{
			{Contains: "redraw", Reply: "progress [=  ]\rprogress [== ]\rprogress [===]"},
			{Contains: "plain", Reply: "progress [===]"},
			{Contains: "more", Reply: "and then a new line"},
		},
	}}
	sub := newSubject(t)
	p, _ := provider(t, sub.home, script)
	redrawn := create(t, p, sub.spec("begin"))
	plain := create(t, p, sub.spec("begin"))
	for _, s := range []*session.Session{redrawn, plain} {
		waitCapture(t, p, s, "worker ready")
	}
	if err := p.Send(context.Background(), redrawn, "redraw"); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(context.Background(), plain, "plain"); err != nil {
		t.Fatal(err)
	}
	a := waitCapture(t, p, redrawn, "progress [===]")
	b := waitCapture(t, p, plain, "progress [===]")
	if !strings.Contains(a.Text, "\r") || strings.Contains(b.Text, "\r") {
		t.Fatalf("the raw captures do not differ by a redraw:\n%q\n%q", a.Text, b.Text)
	}
	if a.Digest != b.Digest {
		t.Errorf("a redrawn line digests differently from the line it ended on:\n%q\n%q", session.Normalize(a.Text), session.Normalize(b.Text))
	}
	if err := p.Send(context.Background(), redrawn, "more"); err != nil {
		t.Fatal(err)
	}
	c := waitCapture(t, p, redrawn, "and then a new line")
	if c.Digest == a.Digest {
		t.Error("an appended line left the digest unchanged")
	}
}
