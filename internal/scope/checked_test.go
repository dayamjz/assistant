package scope

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

// A trace a decision model did not confirm silences nothing, and the note its
// path gets quotes the reason and says the changes did not bear it out. The
// same traces with nothing unconfirmed are the control: they silence the path,
// so the note is the unconfirmed set's doing.
func TestAnUnconfirmedTraceSilencesNothingAndIsQuoted(t *testing.T) {
	c := change("internal/fetch/retry.go", "internal/log/format.go")
	traces := []Trace{
		{Path: "internal/fetch/retry.go", Reason: "this is the bounded retry the intent asks for"},
		{Path: "internal/log/format.go", Reason: "the retry logs each attempt"},
	}
	control, err := ObserveChecked(c, traces, nil)
	if err != nil || len(control) != 0 {
		t.Fatalf("every path traced and nothing unconfirmed drew %v (%v), want silence", control, err)
	}

	got, err := ObserveChecked(c, traces, []Trace{{Path: " internal/log/format.go ", Reason: "the retry logs each attempt "}})
	if err != nil {
		t.Fatalf("ObserveChecked: %v", err)
	}
	if len(got) != 1 || got[0].Location.Path != "internal/log/format.go" {
		t.Fatalf("an unconfirmed trace drew %v, want one note on its path", paths(got))
	}
	if got[0].Action != findings.ActionNote {
		t.Fatalf("an unconfirmed trace's note is %q, want note: the lens blocks nothing", got[0].Action)
	}
	for _, want := range []string{`"the retry logs each attempt"`, "did not bear it out", describe("internal/log/format.go", true)} {
		if !strings.Contains(got[0].Description, want) {
			t.Errorf("the note does not carry %q: %q", want, got[0].Description)
		}
	}
}

// A path the reviewer traced twice keeps silence on the strength of the trace
// that was confirmed, and an unconfirmed entry naming a trace the reviewer
// never gave is ignored rather than read as one. Both directions are only ever
// stricter than or equal to Observe on the same traces.
func TestUnconfirmedOnlyEverAddsToWhatObserveSays(t *testing.T) {
	c := change("a.go", "b.go")
	traces := []Trace{
		{Path: "a.go", Reason: "first reason"},
		{Path: "a.go", Reason: "second reason"},
	}
	plain, err := Observe(c, traces)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	for _, unconfirmed := range [][]Trace{
		nil,
		{{Path: "a.go", Reason: "first reason"}},
		{{Path: "b.go", Reason: "a reason nobody gave"}},
		{{Path: "a.go", Reason: "first reason"}, {Path: "a.go", Reason: "second reason"}},
	} {
		got, err := ObserveChecked(c, traces, unconfirmed)
		if err != nil {
			t.Fatalf("ObserveChecked: %v", err)
		}
		for _, note := range plain {
			if !containsPath(got, note.Location.Path) {
				t.Fatalf("with %v unconfirmed the note on %s went away", unconfirmed, note.Location.Path)
			}
		}
	}
	both, err := ObserveChecked(c, traces, []Trace{{Path: "a.go", Reason: "first reason"}, {Path: "a.go", Reason: "second reason"}})
	if err != nil {
		t.Fatalf("ObserveChecked: %v", err)
	}
	if !containsPath(both, "a.go") {
		t.Fatalf("with both of a.go's traces unconfirmed the lens said %v, want a note on a.go", paths(both))
	}
	one, err := ObserveChecked(c, traces, []Trace{{Path: "a.go", Reason: "first reason"}})
	if err != nil {
		t.Fatalf("ObserveChecked: %v", err)
	}
	if containsPath(one, "a.go") {
		t.Fatalf("a.go kept a confirmed trace and still drew a note: %v", paths(one))
	}
}

func containsPath(fs []findings.Finding, path string) bool {
	for _, f := range fs {
		if f.Location.Path == path {
			return true
		}
	}
	return false
}
