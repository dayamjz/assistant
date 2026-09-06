package scope

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

// change returns a change with a supplied intent, which is the framing most
// tests here do not care about.
func change(touched ...string) Change {
	return Change{
		Intent:   "Add a bounded retry to the fetch path.",
		Supplied: true,
		Touched:  touched,
	}
}

// paths returns the location path of each finding, in order.
func paths(fs []findings.Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Location.Path
	}
	return out
}

func observe(t *testing.T, c Change, traces ...Trace) []findings.Finding {
	t.Helper()
	got, err := Observe(c, traces)
	if err != nil {
		t.Fatalf("Observe: unexpected error: %v", err)
	}
	return got
}

// The lens has to fire in one direction and stay silent in the other. A lens
// that fires on everything is as useless as one that fires on nothing, so both
// directions are evidence and neither alone is.
func TestObserveFiresOnAnUnrequestedChange(t *testing.T) {
	c := change("internal/fetch/retry.go", "internal/log/format.go")
	got := observe(t, c, Trace{
		Path:   "internal/fetch/retry.go",
		Reason: "this is the bounded retry the intent asks for",
	})

	if want := []string{"internal/log/format.go"}; len(got) != 1 || paths(got)[0] != want[0] {
		t.Fatalf("Observe located %v, want one note on %v", paths(got), want)
	}
	if !strings.Contains(got[0].Description, "internal/log/format.go") {
		t.Errorf("the note does not name the path it is about: %q", got[0].Description)
	}
}

func TestObserveIsSilentWhenEveryPathTraces(t *testing.T) {
	c := change("internal/fetch/retry.go", "internal/fetch/retry_test.go")
	got := observe(t, c,
		Trace{Path: "internal/fetch/retry.go", Reason: "the retry itself"},
		Trace{Path: "internal/fetch/retry_test.go", Reason: "its bound, tested"},
	)
	if len(got) != 0 {
		t.Fatalf("Observe reported %v on a change that traces entirely to the intent", paths(got))
	}
}

// Silence is bought with an affirmative claim, so the shapes that are not one
// do not buy it. Each of these is a way a reviewer's answer can fail to
// account for a path, and every one of them has to leave the note standing.
func TestObserveOnlySilencedByATraceOfThatPath(t *testing.T) {
	const path = "internal/log/format.go"
	for _, tc := range []struct {
		name  string
		trace Trace
	}{
		{"no trace at all", Trace{}},
		{"a path with no reason", Trace{Path: path}},
		{"a reason that is only space", Trace{Path: path, Reason: "  \n "}},
		{"a trace of some other path", Trace{Path: "internal/fetch/retry.go", Reason: "the retry"}},
		{"a path differing in letter case", Trace{Path: "internal/log/Format.go", Reason: "the retry"}},
		{"a path differing in spelling", Trace{Path: "./internal/log/format.go", Reason: "the retry"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := observe(t, change(path), tc.trace)
			if len(got) != 1 || got[0].Location.Path != path {
				t.Fatalf("Observe reported %v, want one note on %q", paths(got), path)
			}
		})
	}
}

// Surrounding space is the one difference a trace is allowed, on both sides.
func TestObserveTrimsPathsOnBothSides(t *testing.T) {
	c := change("  internal/fetch/retry.go\n")
	got := observe(t, c, Trace{Path: " internal/fetch/retry.go ", Reason: "the retry"})
	if len(got) != 0 {
		t.Fatalf("Observe reported %v; surrounding space should not defeat a trace", paths(got))
	}
}

func TestObserveCollapsesRepeatedPaths(t *testing.T) {
	got := observe(t, change("a.go", "a.go", "a.go"))
	if len(got) != 1 {
		t.Fatalf("Observe reported %d notes for one repeated path, want 1: %v", len(got), paths(got))
	}
}

func TestObserveReportsNothingForATraceOfAnUntouchedPath(t *testing.T) {
	got := observe(t, change("a.go"),
		Trace{Path: "a.go", Reason: "asked for"},
		Trace{Path: "never/touched.go", Reason: "a claim about nothing"},
	)
	if len(got) != 0 {
		t.Fatalf("Observe reported %v; a trace of an untouched path is not a finding", paths(got))
	}
}

// A scope observation is a note and can be nothing else. This is the property
// the lens's default rests on, so it is checked over every finding the lens
// can produce rather than on one sample.
func TestObserveProducesOnlyNotes(t *testing.T) {
	for _, supplied := range []bool{true, false} {
		got := observe(t, Change{
			Intent:   "Add a bounded retry.",
			Supplied: supplied,
			Touched:  []string{"a.go", "b.go", "c.go"},
		})
		if len(got) == 0 {
			t.Fatalf("supplied=%v: nothing to check; the lens should have fired", supplied)
		}
		for _, f := range got {
			if f.Action != findings.ActionNote {
				t.Errorf("supplied=%v: %q carries action %q, want %q",
					supplied, f.Location.Path, f.Action, findings.ActionNote)
			}
			if f.FixEligible() {
				t.Errorf("supplied=%v: %q is fix-eligible", supplied, f.Location.Path)
			}
			if f.Holds() {
				t.Errorf("supplied=%v: %q holds the run for a decision", supplied, f.Location.Path)
			}
		}
		if n := len(findings.Fixable(got)); n != 0 {
			t.Errorf("supplied=%v: %d of the lens's findings feed the fix loop", supplied, n)
		}
		if n := len(findings.Held(got)); n != 0 {
			t.Errorf("supplied=%v: %d of the lens's findings hold for a decision", supplied, n)
		}
	}
}

// The lens's findings are the review stage's findings, so they have to survive
// the path every report takes: normalization, identifier assignment, and
// validation. A report holding only these is approved as it stands.
func TestObserveFindingsAreUsableInAReport(t *testing.T) {
	got := observe(t, change("a.go", "b.go"))
	report := findings.Report{
		Summary:  "reviewed the change against the recorded intent",
		Findings: got,
	}.Normalize()

	if err := report.Validate(); err != nil {
		t.Fatalf("a report of the lens's findings does not validate: %v", err)
	}
	if !report.AllNotes() {
		t.Error("a report holding only scope observations is not all notes")
	}
	if report.HasHeld() {
		t.Error("scope observations hold the report")
	}
	seen := make(map[string]struct{}, len(report.Findings))
	for _, f := range report.Findings {
		if f.ID == "" {
			t.Errorf("%q was assigned no identifier", f.Location.Path)
		}
		if _, dup := seen[f.ID]; dup {
			t.Errorf("identifier %q was assigned twice", f.ID)
		}
		seen[f.ID] = struct{}{}
	}
}

// The intent's source does not decide whether the lens fires, only how the
// observation reads. Both halves matter: an inferred intent must not silence
// the lens, and it must not be described as acceptance criteria either.
func TestObserveDescribesTheIntentsSource(t *testing.T) {
	c := change("a.go")
	supplied := observe(t, c)

	c.Supplied = false
	inferred := observe(t, c)

	if len(supplied) != 1 || len(inferred) != 1 {
		t.Fatalf("the lens fired %d times on a supplied intent and %d on an inferred one, want 1 each",
			len(supplied), len(inferred))
	}
	if supplied[0].Description == inferred[0].Description {
		t.Fatal("a supplied intent and an inferred one produce the same note")
	}
	if !strings.Contains(supplied[0].Description, "supplied") {
		t.Errorf("the supplied-intent note does not say so: %q", supplied[0].Description)
	}
	if !strings.Contains(inferred[0].Description, "inferred") {
		t.Errorf("the inferred-intent note does not say so: %q", inferred[0].Description)
	}
}

func TestNoIntentIsRefused(t *testing.T) {
	for _, intent := range []string{"", "   \n\t "} {
		c := Change{Intent: intent, Touched: []string{"a.go"}}
		if _, err := Observe(c, nil); !errors.Is(err, ErrNoIntent) {
			t.Errorf("Observe(%q): got %v, want ErrNoIntent", intent, err)
		}
		if _, err := Guidance(c); !errors.Is(err, ErrNoIntent) {
			t.Errorf("Guidance(%q): got %v, want ErrNoIntent", intent, err)
		}
	}
}

// A lens asked about no path could only ever return silence, which is a check
// that passes without checking anything. Both entry points refuse instead.
func TestNoTouchedPathIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		touched []string
	}{
		{"no paths at all", nil},
		{"an empty list", []string{}},
		{"paths that are empty once trimmed", []string{"", "  \n\t "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Change{
				Intent:   "Add a bounded retry to the fetch path.",
				Supplied: true,
				Touched:  tc.touched,
			}
			got, err := Observe(c, nil)
			if !errors.Is(err, ErrNoTouched) {
				t.Errorf("Observe: got %v, want ErrNoTouched", err)
			}
			if len(got) != 0 {
				t.Errorf("Observe returned %v alongside a refusal", paths(got))
			}
			text, err := Guidance(c)
			if !errors.Is(err, ErrNoTouched) {
				t.Errorf("Guidance: got %v, want ErrNoTouched", err)
			}
			if text != "" {
				t.Errorf("Guidance returned text alongside a refusal:\n%s", text)
			}
		})
	}
}

// The intent is the yardstick, so a call missing both reports the missing
// yardstick rather than the missing question.
func TestNeitherAnIntentNorATouchedPathReportsTheIntent(t *testing.T) {
	if _, err := Observe(Change{}, nil); !errors.Is(err, ErrNoIntent) {
		t.Errorf("Observe: got %v, want ErrNoIntent", err)
	}
	if _, err := Guidance(Change{}); !errors.Is(err, ErrNoIntent) {
		t.Errorf("Guidance: got %v, want ErrNoIntent", err)
	}
}

// The reviewer is asked about each path by name, because that is what makes an
// answer that omits one visible as an omission rather than as agreement.
func TestGuidanceNamesEveryTouchedPath(t *testing.T) {
	c := change("internal/fetch/retry.go", " internal/log/format.go ", "internal/fetch/retry.go")
	text, err := Guidance(c)
	if err != nil {
		t.Fatalf("Guidance: %v", err)
	}
	for _, path := range []string{"internal/fetch/retry.go", "internal/log/format.go"} {
		if !strings.Contains(text, path) {
			t.Errorf("guidance does not name %q:\n%s", path, text)
		}
	}
	if n := strings.Count(text, "internal/fetch/retry.go"); n != 1 {
		t.Errorf("a repeated path is asked about %d times, want 1", n)
	}
	if !strings.Contains(text, strings.TrimSpace(c.Intent)) {
		t.Errorf("guidance does not carry the intent:\n%s", text)
	}
}

// listedPaths returns the paths the guidance asks about, read back off the
// emitted text the way a reviewer reads them.
func listedPaths(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if path, ok := strings.CutPrefix(line, "  - "); ok {
			out = append(out, path)
		}
	}
	return out
}

// Observe matches a trace to a touched path by exact string equality, so the
// reviewer has to be told to copy the path, and told it where it meets the
// list rather than after it. The reason stays prose.
func TestGuidanceAsksForEachPathExactlyAsListed(t *testing.T) {
	const path = "internal/log/format.go"
	text, err := Guidance(change(path))
	if err != nil {
		t.Fatalf("Guidance: %v", err)
	}
	instruction := strings.Index(text, "exactly as it is listed")
	if instruction < 0 {
		t.Fatalf("guidance does not ask for each path exactly as listed:\n%s", text)
	}
	list := strings.Index(text, "  - "+path)
	if list < 0 {
		t.Fatalf("guidance does not list %q:\n%s", path, text)
	}
	if instruction > list {
		t.Errorf("the instruction trails the path list, where a reviewer has already answered:\n%s", text)
	}
	if !strings.Contains(text, "in your own words") {
		t.Errorf("guidance no longer asks for the reason in the reviewer's own words:\n%s", text)
	}
}

// The instruction is only worth giving if following it works, so the paths the
// guidance lists have to be exactly the strings a trace is matched against: a
// reviewer that copies every one of them back leaves no note standing.
func TestATraceCopiedFromTheGuidanceSilencesItsPath(t *testing.T) {
	c := change("  internal/fetch/retry.go\n", "internal/log/format.go", "internal/fetch/retry.go")
	text, err := Guidance(c)
	if err != nil {
		t.Fatalf("Guidance: %v", err)
	}
	listed := listedPaths(text)
	if len(listed) != 2 {
		t.Fatalf("guidance lists %v, want the two distinct touched paths", listed)
	}
	traces := make([]Trace, len(listed))
	for i, path := range listed {
		traces[i] = Trace{Path: path, Reason: "the bounded retry the intent asks for"}
	}
	if got := observe(t, c, traces...); len(got) != 0 {
		t.Fatalf("Observe reported %v after every listed path was copied back verbatim", paths(got))
	}
}

func TestGuidanceFramesTheIntentBySource(t *testing.T) {
	c := change("a.go")
	supplied, err := Guidance(c)
	if err != nil {
		t.Fatalf("Guidance: %v", err)
	}
	c.Supplied = false
	inferred, err := Guidance(c)
	if err != nil {
		t.Fatalf("Guidance: %v", err)
	}
	if supplied == inferred {
		t.Fatal("guidance frames a supplied intent and an inferred one identically")
	}
	if !strings.Contains(inferred, "low-confidence") {
		t.Errorf("guidance for an inferred intent does not say it is a weak signal:\n%s", inferred)
	}
}
