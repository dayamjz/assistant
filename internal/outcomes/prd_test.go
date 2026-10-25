package outcomes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// prdMarkdown is a document shaped like the real one: an earlier section
// that discusses an outcome, the machine interface section around the row,
// another row in the same table that sets an outcome's value in a code span
// without declaring it, and prose after the table that does the same.
func prdMarkdown() string {
	return "# A PRD\n\n## Execution model\n\n" +
		"A checkpoint is written once an `executing` segment claims the run.\n\n" +
		"## Surfaces\n\n| Contract | Rule |\n|---|---|\n" +
		"| **Blocking calls** | That one call returns `executing` instead. |\n" +
		`| <a id="outcome-set"></a>**Outcomes** | Four say a run is finished with: ` +
		`<code data-outcome="finished">checks-passed</code>, ` +
		`<code class="whatever" data-outcome="finished">passed</code>, ` +
		`<code data-outcome="finished">failed</code> and ` +
		`<code data-outcome="finished">cancelled</code>. ` +
		`Two say it is not: <code data-outcome="unfinished">decision</code> and ` +
		`<code data-outcome="unfinished">executing</code>. |` + "\n\n" +
		"A caller never answers `executing`, because there is nothing to answer.\n\n" +
		"## Configuration\n\nNothing here.\n"
}

// wantSet is what the fixture declares, in the order it declares it.
func wantSet() []Listed {
	return []Listed{
		{"checks-passed", true}, {"passed", true}, {"failed", true}, {"cancelled", true},
		{"decision", false}, {"executing", false},
	}
}

// movedRow returns two documents that differ in one thing only: which section
// the outcome row sits in.
//
// In outside, the row is lifted out of the surfaces section and put into the
// section before it, whole and carrying every marker it had, under a table
// header of its own. In inside, that same document has the two sections'
// headings exchanged, so the row's new home is the one the rule looks in.
// Reading inside is what makes outside a control: without it a refusal could
// come from the move having broken the document, and the case would pass for
// the wrong reason.
func movedRow(t *testing.T) (outside, inside string) {
	t.Helper()
	full := prdMarkdown()
	const host, owner = "## Execution model\n", "## " + sectionHeading + "\n"
	if strings.Count(full, host) != 1 || strings.Count(full, owner) != 1 {
		t.Fatalf("the fixture does not hold exactly one %q to move the row into and one %q to move it out of", host, owner)
	}
	from := strings.Index(full, "| "+rowAnchor)
	if from < 0 {
		t.Fatalf("the fixture holds no row opening with %s", rowAnchor)
	}
	width := strings.Index(full[from:], "\n")
	if width < 0 {
		t.Fatal("the fixture's outcome row is the last line, with nothing after it")
	}
	row := full[from : from+width+1]

	outside = strings.Replace(full[:from]+full[from+width+1:],
		host, host+"\n| Contract | Rule |\n|---|---|\n"+row+"\n", 1)
	if !strings.Contains(outside, host+"\n| Contract | Rule |\n|---|---|\n"+row) {
		t.Fatalf("the row was not moved into %q, so the document is unchanged in the way that matters", host)
	}

	inside = strings.Replace(outside, owner, "## Machine\n", 1)
	inside = strings.Replace(inside, host, owner, 1)
	if strings.Count(inside, owner) != 1 {
		t.Fatalf("exchanging the two headings left %d %q in the document", strings.Count(inside, owner), owner)
	}
	return outside, inside
}

// names renders a read set the way a failure reads best.
func names(set []Listed) string {
	var b strings.Builder
	b.WriteString("[")
	for i, l := range set {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s(finished=%v)", l.Name, l.Terminal)
	}
	b.WriteString("]")
	return b.String()
}

// TestFromPRDReadsTheMarkersInTheRowAndNothingElse pins the extraction rule to
// a document shaped like the real one, including the code elements elsewhere
// in the same section that hold an outcome's value without declaring one.
func TestFromPRDReadsTheMarkersInTheRowAndNothingElse(t *testing.T) {
	assertReads(t, prdMarkdown())
}

// assertReads fails unless FromPRD reads the fixture's six declarations back
// in the fixture's order and groups.
func assertReads(t *testing.T, doc string) {
	t.Helper()
	got, err := FromPRD([]byte(doc))
	if err != nil {
		t.Fatalf("FromPRD: %v", err)
	}
	want := wantSet()
	if len(got) != len(want) {
		t.Fatalf("FromPRD = %s, want %s", names(got), names(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FromPRD = %s, want %s", names(got), names(want))
		}
	}
}

// fenceLine returns doc with the one line that starts with prefix set in a
// closed fenced code block of its own, so it renders as code.
func fenceLine(t *testing.T, doc, prefix string) string {
	t.Helper()
	at := strings.Index(doc, prefix)
	if at < 0 || strings.Count(doc, prefix) != 1 {
		t.Fatalf("the document does not hold exactly one line starting %q to fence", prefix)
	}
	end := at + strings.Index(doc[at:], "\n")
	if end < at {
		t.Fatalf("the line starting %q is the last line, with no newline to close the fence after", prefix)
	}
	return doc[:at] + "\n```text\n" + doc[at:end] + "\n```\n" + doc[end:]
}

// TestFromPRDReadsNoMarkerInAFence is the other half of the fence rule: a
// fenced marker is not refused, it is not read. An example of a declaration
// set in code elsewhere in the section leaves the set what the row declares.
func TestFromPRDReadsNoMarkerInAFence(t *testing.T) {
	assertReads(t, strings.Replace(prdMarkdown(), "## Configuration",
		"```html\n<code data-outcome=\"finished\">merged</code>\n```\n\n## Configuration", 1))
}

// TestFromPRDRefusesTheRealRowFenced derives its control from the real
// docs/prd.md: the outcome row, fenced as code, is no row at all, so the
// document is refused rather than read. The unmodified document is the
// positive control, and it has to read.
func TestFromPRDRefusesTheRealRowFenced(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(PRDPath)))
	if err != nil {
		t.Fatalf("reading the PRD: %v", err)
	}
	if got, err := FromPRD(body); err != nil || len(got) == 0 {
		t.Fatalf("the real PRD reads as %s, %v, want its outcome set", names(got), err)
	}
	fenced := fenceLine(t, string(body), "| "+rowAnchor)
	got, err := FromPRD([]byte(fenced))
	if !errors.Is(err, ErrPRD) || !strings.Contains(err.Error(), "no "+rowAnchor) {
		t.Fatalf("the real PRD with its outcome row fenced reads as %s, %v, want an ErrPRD saying there is no row", names(got), err)
	}
}

// TestFromPRDPinsTheRowToTheSectionThatOwnsIt is the pair the containment rule
// rests on. The two documents differ in nothing but which section the row is
// inside: the one where it sits outside is refused, and the one where the same
// move lands it inside the section the rule looks in reads back the whole set.
// Without the second half, the first would pass for a document the move had
// simply broken.
func TestFromPRDPinsTheRowToTheSectionThatOwnsIt(t *testing.T) {
	outside, inside := movedRow(t)
	if got, err := FromPRD([]byte(outside)); !errors.Is(err, ErrPRD) {
		t.Fatalf("a row outside the section that owns it reads as %s, %v, want an ErrPRD", names(got), err)
	}
	assertReads(t, inside)
}

// TestFromPRDRefusesASetItCannotTrust is why a short set is never an answer:
// each of these is a document the rule reads differently from a reader, and a
// silent short set would quietly shrink what the comparison is over.
func TestFromPRDRefusesASetItCannotTrust(t *testing.T) {
	full := prdMarkdown()
	outsideTheSection, _ := movedRow(t)
	owner := "## " + sectionHeading + "\n"
	cases := []struct {
		name string
		doc  string
	}{
		{"the row is outside the section that owns it", outsideTheSection},
		{"no section that owns the row", strings.Replace(full, owner, "## Elsewhere\n", 1)},
		{"two sections that own the row", strings.Replace(full, owner, owner+"\n"+owner, 1)},
		{"a fenced block never closed", strings.Replace(full, owner, owner+"\n```\n", 1)},
		{"no outcome row", strings.Replace(full, rowAnchor, `<a id="other">`, 1)},
		{"two outcome rows", strings.Replace(full, rowAnchor, rowAnchor+rowAnchor, 1)},
		{"the anchor not on a table row", strings.Replace(full, "| "+rowAnchor, rowAnchor, 1)},
		{"no marker in it", strings.NewReplacer(
			`data-outcome="finished"`, "", `data-outcome="unfinished"`, "").Replace(full)},
		{"a marker outside the row, in the same section", strings.Replace(full,
			"A caller never answers `executing`",
			`A caller never answers <code data-outcome="unfinished">executing</code>`, 1)},
		{"a marker outside the row, in another section", strings.Replace(full,
			"`executing` segment claims the run.",
			`<code data-outcome="finished">merged</code> once an `+"`executing`"+` segment claims the run.`, 1)},
		{"a marker on something that is not a code element", strings.Replace(full,
			`<code data-outcome="finished">failed</code>`, `<b data-outcome="finished">failed</b>`, 1)},
		{"a marker on a code element holding markup", strings.Replace(full,
			`<code data-outcome="finished">failed</code>`, `<code data-outcome="finished"><b>failed</b></code>`, 1)},
		{"a group word this rule does not know", strings.Replace(full, `data-outcome="unfinished">decision`, `data-outcome="open">decision`, 1)},
		{"a repeated value", strings.Replace(full, `>cancelled<`, `>passed<`, 1)},
		{"a value the wire does not carry", strings.Replace(full, `>cancelled<`, `>Cancelled Run<`, 1)},
		{"an empty value", strings.Replace(full, `>cancelled<`, `><`, 1)},
		{"one group", strings.ReplaceAll(full, `data-outcome="unfinished"`, `data-outcome="finished"`)},
		{"the row in a closed fence", fenceLine(t, full, "| "+rowAnchor)},
		{"the groups interleaved", strings.Replace(full, `data-outcome="finished">passed`, `data-outcome="unfinished">passed`, 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.doc == full {
				t.Fatal("this case did not change the fixture, so it is the readable document under another name and would pass without the rule it is named for")
			}
			got, err := FromPRD([]byte(c.doc))
			if !errors.Is(err, ErrPRD) {
				t.Fatalf("FromPRD = %s, %v, want an ErrPRD", names(got), err)
			}
		})
	}
}
