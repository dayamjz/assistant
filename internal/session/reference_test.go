package session_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/session"
)

// A reference round-trips through its one format, and everything that is
// not one is refused with ErrReference rather than read as a nearby one.
func TestReferenceEncodesAndDecodesOneClosedFormat(t *testing.T) {
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	ref := session.Reference{Home: session.HomeMarker(h), Task: id, PID: 4242, Start: 1234567890123}
	text := ref.String()
	if !strings.HasPrefix(text, "terminal1:") || strings.Count(text, ":") != 4 {
		t.Fatalf("encoded as %q, want the format name and five fields", text)
	}
	got, err := session.ParseReference(text)
	if err != nil || got != ref {
		t.Fatalf("ParseReference(%q) = %+v, %v; want %+v", text, got, err, ref)
	}
	if len(ref.Home) != 32 || strings.Trim(ref.Home, "0123456789abcdef") != "" {
		t.Errorf("the home marker is %q, want 32 lowercase hex characters", ref.Home)
	}
	other, _ := home.Open(t.TempDir())
	if session.HomeMarker(other) == ref.Home {
		t.Error("two homes share a marker")
	}

	fields := strings.Split(text, ":")
	bad := map[string]string{
		"another format":      "terminal2:" + strings.Join(fields[1:], ":"),
		"four fields":         strings.Join(fields[:4], ":"),
		"six fields":          text + ":1",
		"short home marker":   strings.Join([]string{fields[0], fields[1][:31], fields[2], fields[3], fields[4]}, ":"),
		"uppercase marker":    strings.Join([]string{fields[0], strings.ToUpper(fields[1]), fields[2], fields[3], fields[4]}, ":"),
		"bad task identifier": strings.Join([]string{fields[0], fields[1], "../x", fields[3], fields[4]}, ":"),
		"zero pid":            strings.Join([]string{fields[0], fields[1], fields[2], "0", fields[4]}, ":"),
		"negative pid":        strings.Join([]string{fields[0], fields[1], fields[2], "-1", fields[4]}, ":"),
		"padded pid":          strings.Join([]string{fields[0], fields[1], fields[2], "042", fields[4]}, ":"),
		"non-decimal start":   strings.Join([]string{fields[0], fields[1], fields[2], fields[3], "x"}, ":"),
		"negative start":      strings.Join([]string{fields[0], fields[1], fields[2], fields[3], "-5"}, ":"),
		"empty":               "",
	}
	for name, text := range bad {
		if _, err := session.ParseReference(text); !errors.Is(err, session.ErrReference) {
			t.Errorf("%s (%q) answered %v, want ErrReference", name, text, err)
		}
	}
}

// A tmux worker's reference names its server's socket in a sixth field under
// a format of its own, round-trips through it, and is refused with
// ErrReference for a socket name Tmux.Create could not have drawn; a
// subordinate's reference names none.
func TestATerminalReferenceNamesItsSocket(t *testing.T) {
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	ref := session.Reference{Home: session.HomeMarker(h), Task: id, PID: 4242, Start: 99, Terminal: "0123456789abcdef"}
	text := ref.String()
	if !strings.HasPrefix(text, "terminal2:") || !strings.HasSuffix(text, ":0123456789abcdef") || strings.Count(text, ":") != 5 {
		t.Fatalf("encoded as %q, want the terminal format with the socket name last", text)
	}
	if got, err := session.ParseReference(text); err != nil || got != ref {
		t.Fatalf("ParseReference(%q) = %+v, %v; want %+v", text, got, err, ref)
	}
	for name, text := range map[string]string{
		"short name":     strings.TrimSuffix(text, "f"),
		"uppercase name": strings.TrimSuffix(text, "abcdef") + "ABCDEF",
		"no name":        strings.TrimSuffix(text, ":0123456789abcdef"),
		"seven fields":   text + ":0123456789abcdef",
	} {
		if _, err := session.ParseReference(text); !errors.Is(err, session.ErrReference) {
			t.Errorf("%s (%q) answered %v, want ErrReference", name, text, err)
		}
	}
	bad := ref
	bad.Terminal = "not-hex"
	if _, err := bad.Encode(); !errors.Is(err, session.ErrReference) {
		t.Errorf("Encode of a bad socket name answered %v, want ErrReference", err)
	}
	subordinate := session.Reference{Home: ref.Home, Domain: "payments", PID: 1, Start: 1, Terminal: ref.Terminal}
	if _, err := subordinate.Encode(); !errors.Is(err, session.ErrReference) {
		t.Errorf("Encode of a subordinate naming a terminal answered %v, want ErrReference", err)
	}
}

// A home fits a terminal worker only when the socket Create would put under
// it is within the platform's local socket path limit, so the default
// provider can fall back to a pipe for a home whose root is too long rather
// than refuse every launch there.
func TestATerminalFitsOnlyAHomeShortEnoughForItsSocket(t *testing.T) {
	short, err := home.Open("/tmp/h")
	if err != nil {
		t.Fatal(err)
	}
	if !session.TerminalFits(short, "/usr/bin/tmux") {
		t.Error("a short home does not fit a terminal worker")
	}
	long, err := home.Open("/tmp/" + strings.Repeat("l", 100))
	if err != nil {
		t.Fatal(err)
	}
	if session.TerminalFits(long, "/usr/bin/tmux") {
		t.Error("a home past the socket path limit fits a terminal worker")
	}
	if session.TerminalFits(short, "tmux") {
		t.Error("a relative tmux fits a terminal worker")
	}
}
