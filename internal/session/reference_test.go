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
