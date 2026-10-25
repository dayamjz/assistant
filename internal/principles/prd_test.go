package principles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
		{"every badge in a closed fence", strings.Replace(strings.Replace(full,
			"## Principles\n\n", "## Principles\n\n```text\n", 1), "## Decisions", "```\n\n## Decisions", 1)},
		{"a second heading with closing hashes", full + "\n## Principles ##\n\n<kbd>P4</kbd> a principle\n"},
		{"a second heading at another level", full + "\n### Principles\n\n<kbd>P4</kbd> a principle\n"},
		{"a second heading underlined", full + "\nPrinciples\n----------\n\n<kbd>P4</kbd> a principle\n"},
		{"a second heading in bold", full + "\n## **Principles**\n\n<kbd>P4</kbd> a principle\n"},
		{"a second heading spelled with a character reference", full + "\n## Princip&#108;es\n\n<kbd>P4</kbd> a principle\n"},
		{"every badge indented as code", strings.ReplaceAll(full, "<details>\n<summary>", "    <details>\n    <summary>")},
		{"a principle hidden in an HTML comment", hide(t, full, "<kbd>P2</kbd>")},
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

// TestFromPRDReadsNoBadgeInAFence is the other half of the fence rule: a
// fenced badge is not refused, it is not read. An example of a badge set in
// code inside the section leaves the list exactly what the prose declares.
func TestFromPRDReadsNoBadgeInAFence(t *testing.T) {
	doc := strings.Replace(prdMarkdown(3), "## Decisions",
		"```markdown\n<summary><kbd>P4</kbd> an example</summary>\n```\n\n## Decisions", 1)
	got, err := FromPRD([]byte(doc))
	if err != nil {
		t.Fatalf("FromPRD: %v", err)
	}
	if names(got) != "[P1 P2 P3]" {
		t.Fatalf("FromPRD = %s, want [P1 P2 P3]: the fenced P4 is code, not a principle", names(got))
	}
}

// TestFromPRDRefusesTheRealDocumentMisread derives each control from the real
// docs/prd.md rather than from a fixture, so what is shown to refuse is a
// misreading of the document this package actually reads: its principles
// rendered as code or hidden in a comment, and a second principles section
// spelled each way a heading can be spelled to read the same. The unmodified
// document is the positive control, and it has to read as every principle
// this package names.
func TestFromPRDRefusesTheRealDocumentMisread(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(PRDPath)))
	if err != nil {
		t.Fatalf("reading the PRD: %v", err)
	}
	real := string(body)
	got, err := FromPRD(body)
	if err != nil || len(got) != len(All()) {
		t.Fatalf("the real PRD reads as %s, %v, want all %d principles", names(got), err, len(All()))
	}

	const heading = "\n## Principles\n"
	at := strings.Index(real, heading)
	if at < 0 {
		t.Fatalf("the real PRD has no %q line to build the controls from", heading)
	}
	start := at + len(heading)
	end := start + strings.Index(real[start:], "\n## ")
	if end < start {
		t.Fatal("the real PRD's principles section is the last section, so there is no end to fence it to")
	}
	extra := "<kbd>P" + strconv.Itoa(len(All())+1) + "</kbd> a principle nobody counted\n"
	for _, c := range []struct{ name, doc, says string }{
		{"the principles fenced as code", real[:start] + "\n```text\n" + real[start:end] + "\n```\n" + real[end:],
			"lists none"},
		{"a second section, closing hashes", real + "\n## Principles ##\n\n" + extra, "appears more than once"},
		{"a second section, underlined", real + "\nPrinciples\n----------\n\n" + extra, "appears more than once"},
		{"a second section, in bold", real + "\n## **Principles**\n\n" + extra, "appears more than once"},
		{"a second section, a character reference", real + "\n## Princip&#108;es\n\n" + extra, "appears more than once"},
		{"the principles indented as code", real[:start] + indent(real[start:end]) + real[end:], "lists none"},
		{"P7 hidden in an HTML comment", hide(t, real, "<kbd>P7</kbd>"), "is P8, want P7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := FromPRD([]byte(c.doc))
			if !errors.Is(err, ErrPRD) || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("FromPRD = %s, %v, want an ErrPRD saying %q", names(got), err, c.says)
			}
		})
	}
}

// indent sets every non-empty line of s four spaces in, which renders it as
// an indented code block.
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = "    " + l
		}
	}
	return strings.Join(lines, "\n")
}

// hide wraps the one <details> block whose summary carries badge in an HTML
// comment, so the principle no longer renders at all.
func hide(t *testing.T, doc, badge string) string {
	t.Helper()
	at := strings.Index(doc, "<summary>"+badge)
	open := strings.LastIndex(doc[:at], "<details")
	end := strings.Index(doc[at:], "</details>")
	if at < 0 || open < 0 || end < 0 {
		t.Fatalf("the document has no <details> block for %s to hide", badge)
	}
	end += at + len("</details>")
	return doc[:open] + "<!--\n" + doc[open:end] + "\n-->" + doc[end:]
}

// TestPRDSectionEndsWhereAReaderSeesTheNextSection pins where a section's
// body begins and ends in the rendered document: after its own heading, at the
// next heading of its level or a higher one, never at a deeper heading or at a
// heading-shaped line a code block holds, and at the end of the document when
// nothing follows.
func TestPRDSectionEndsWhereAReaderSeesTheNextSection(t *testing.T) {
	doc := "# Title\n\n## A\n\nfirst\n\n### A sub\n\n```text\n## not a heading\n```\n\n" +
		"    # nor this\n\nlast of A\n\n## B\n\nb body\n\n# Another top\n\n## C\n\nc body\n"
	prd, err := RenderPRD([]byte(doc))
	if err != nil {
		t.Fatalf("RenderPRD: %v", err)
	}
	for _, c := range []struct {
		heading string
		want    []string
	}{
		{"A", []string{"first", "last of A"}},
		{"B", []string{"b body"}},
		{"C", []string{"c body"}},
	} {
		section, err := prd.Section(2, c.heading)
		if err != nil {
			t.Fatalf("Section(%q): %v", c.heading, err)
		}
		var got []string
		for _, p := range section.Elements("p") {
			got = append(got, Text(p))
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Fatalf("Section(%q) holds paragraphs %q, want %q", c.heading, got, c.want)
		}
	}
	for _, c := range []struct{ what, doc, heading, says string }{
		{"no such heading", doc, "D", `no heading reading "D"`},
		{"a deeper heading is not one", doc, "A sub", "is at level 3, not 2"},
		{"a heading-shaped line in a fence is not one", doc, "not a heading", `no heading reading "not a heading"`},
		{"a heading-shaped line indented as code is not one", doc, "nor this", `no heading reading "nor this"`},
		{"the heading twice", doc + "\n## B\n", "B", "appears more than once"},
		{"the heading again, indented", doc + "\n   ## B\n", "B", "appears more than once"},
		{"the heading again with closing hashes", doc + "\n## B ##\n", "B", "appears more than once"},
		{"the heading again at another level", doc + "\n#### B\n", "B", "appears more than once"},
		{"the heading again, underlined", doc + "\nB\n===\n", "B", "appears more than once"},
		{"the heading again in emphasis", doc + "\n## *B*\n", "B", "appears more than once"},
		{"the heading again as a character reference", doc + "\n## &#66;\n", "B", "appears more than once"},
		{"the heading again with an inline anchor", doc + "\n## <a id=\"b\"></a>B\n", "B", "appears more than once"},
		{"a fence never closed hides what follows", strings.Replace(doc, "## B", "```\n## B", 1), "B", `no heading reading "B"`},
	} {
		prd, err := RenderPRD([]byte(c.doc))
		if err != nil {
			t.Fatalf("%s: RenderPRD: %v", c.what, err)
		}
		_, err = prd.Section(2, c.heading)
		if !errors.Is(err, ErrPRDSection) || !strings.Contains(err.Error(), c.says) {
			t.Fatalf("%s: Section(%q) = %v, want an ErrPRDSection saying %q", c.what, c.heading, err, c.says)
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
