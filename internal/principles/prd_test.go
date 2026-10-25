package principles

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestFromPRDReadsTheBadgesInTheSectionAndNothingElse pins the extraction rule
// to a document shaped like the real one, including the badges outside the
// section that read like principles.
func TestFromPRDReadsTheBadgesInTheSectionAndNothingElse(t *testing.T) {
	got, err := FromPRD([]byte(prdMarkdown(3)))
	if err != nil {
		t.Fatalf("FromPRD: %v", err)
	}
	if names(got) != "[P1 P2 P3]" {
		t.Fatalf("FromPRD = %s, want [P1 P2 P3]", names(got))
	}
}

// TestFromPRDRefusesAListItCannotTrust is why a short list is never an answer:
// each of these is a document the rule reads differently from a reader, and a
// silent short list would quietly shrink what everything else here measures.
func TestFromPRDRefusesAListItCannotTrust(t *testing.T) {
	full := prdMarkdown(3)
	cases := []struct {
		name string
		doc  string
	}{
		{"no principles section", strings.Replace(full, "## Principles\n", "## Other\n", 1)},
		{"the heading one level down", strings.Replace(full, "## Principles\n", "### Principles\n", 1)},
		{"the heading inside a fenced block", strings.Replace(full, "## Principles\n", "```\n## Principles\n```\n", 1)},
		{"two principles sections", full + full},
		{"a fenced block never closed", strings.Replace(full, "<details>", "```\n<details>", 1)},
		{"no badge in it", strings.NewReplacer("<kbd>P1</kbd>", "", "<kbd>P2</kbd>", "", "<kbd>P3</kbd>", "").Replace(
			full[strings.Index(full, "## Principles"):strings.Index(full, "## Decisions")])},
		{"a gap in the numbering", strings.Replace(full, ">P2<", ">P4<", 1)},
		{"a repeated number", strings.Replace(full, "<kbd>P3</kbd> a principle", "<kbd>P2</kbd> a principle", 1)},
		{"a step backwards", strings.Replace(full, "<kbd>P1</kbd> a principle", "<kbd>P3</kbd> a principle", 1)},
		{"a number too large to hold", strings.Replace(full, "<kbd>P1</kbd> a principle", "<kbd>P9001</kbd> a principle", 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.doc == full {
				t.Fatal("this case did not change the fixture, so it would pass without the rule it is named for")
			}
			got, err := FromPRD([]byte(c.doc))
			if !errors.Is(err, ErrPRD) {
				t.Fatalf("FromPRD = %s, %v, want an ErrPRD", names(got), err)
			}
		})
	}
}

// TestPRDSectionEndsWhereAReaderSeesTheNextSection pins where a section's
// body begins and ends: after its own heading, at the next heading of level
// one or two, never at a deeper heading or at a heading-shaped line inside a
// fenced block, and at the end of the document when nothing follows.
func TestPRDSectionEndsWhereAReaderSeesTheNextSection(t *testing.T) {
	doc := "# Title\n\n## A\n\nfirst\n\n### A sub\n\n```text\n## not a heading\n```\n\n" +
		"~~~~\n# nor this\n~~~\n~~~~\nlast of A\n\n## B\n\nb body\n\n# Another top\n\n## C\n\nc body\n"
	for _, c := range []struct{ heading, want string }{
		{"A", "\nfirst\n\n### A sub\n\n```text\n## not a heading\n```\n\n~~~~\n# nor this\n~~~\n~~~~\nlast of A\n\n"},
		{"B", "\nb body\n\n"},
		{"C", "\nc body\n"},
	} {
		from, to, err := PRDSection([]byte(doc), c.heading)
		if err != nil {
			t.Fatalf("PRDSection(%q): %v", c.heading, err)
		}
		if got := doc[from:to]; got != c.want {
			t.Fatalf("PRDSection(%q) = %q, want %q", c.heading, got, c.want)
		}
	}
	for _, c := range []struct{ what, doc, heading, says string }{
		{"no such heading", doc, "D", `no "## D" heading`},
		{"a deeper heading is not one", doc, "A sub", `no "## A sub" heading`},
		{"a heading-shaped line in a fence is not one", doc, "not a heading", `no "## not a heading" heading`},
		{"the heading twice", doc + "\n## B\n", "B", "appears more than once"},
		{"the heading twice, once indented", doc + "\n   ## B\n", "B", "appears more than once"},
		{"a fence never closed", doc + "\n```\n## D\n", "B", "never closed"},
	} {
		_, _, err := PRDSection([]byte(c.doc), c.heading)
		if !errors.Is(err, ErrPRDSection) || !strings.Contains(err.Error(), c.says) {
			t.Fatalf("%s: PRDSection(%q) = %v, want an ErrPRDSection saying %q", c.what, c.heading, err, c.says)
		}
	}
}

// tb records what Cite does to the test it is given, because what Cite does
// when it is asked to record a principle that does not exist is the only part
// of it that has to hold.
//
// Its Fatalf ends the call the way testing's does, by calling runtime.Goexit,
// so Cite cannot run on past a refusal here in a way it never could against a
// real *testing.T. That is why cite drives it on a goroutine of its own.
type tb struct {
	testing.TB
	fatal []string
	logs  []string
}

func (r *tb) Helper()                         {}
func (r *tb) Logf(format string, args ...any) { r.logs = append(r.logs, fmt.Sprintf(format, args...)) }
func (r *tb) Fatalf(format string, args ...any) {
	r.fatal = append(r.fatal, fmt.Sprintf(format, args...))
	runtime.Goexit()
}

// cite calls Cite against a recorder and returns once the call is over,
// however it ended.
func cite(rec *tb, p Principle, more ...Principle) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		Cite(rec, p, more...)
	}()
	wg.Wait()
}

// TestCiteRefusesAPrincipleThisPackageDoesNotHave keeps a citation from naming
// a value that is not a principle at all, and pins that the refusal ends the
// call: a real Fatalf does not return, so the first bad value is the last
// thing Cite reports.
func TestCiteRefusesAPrincipleThisPackageDoesNotHave(t *testing.T) {
	rec := &tb{}
	first, second := Principle(0), Principle(len(All())+1)
	cite(rec, first, second)
	if len(rec.fatal) != 1 {
		t.Fatalf("Cite failed the test %d times, want once, on the first bad value: %v", len(rec.fatal), rec.fatal)
	}
	if !strings.Contains(rec.fatal[0], first.String()) {
		t.Fatalf("Cite failed with %q, want it to name %s", rec.fatal[0], first)
	}
	if strings.Contains(rec.fatal[0], second.String()) {
		t.Fatalf("Cite failed with %q, want it to stop at %s rather than reach %s", rec.fatal[0], first, second)
	}
	if len(rec.logs) != 0 {
		t.Fatalf("Cite logged %v after refusing %s, want the refusal to end the call", rec.logs, first)
	}

	ok := &tb{}
	cite(ok, P1)
	if len(ok.fatal) != 0 {
		t.Fatalf("Cite failed the test on a principle it has: %v", ok.fatal)
	}
	if len(ok.logs) != 1 || !strings.Contains(ok.logs[0], "P1") {
		t.Fatalf("Cite logged %v, want the claim it recorded", ok.logs)
	}
}
