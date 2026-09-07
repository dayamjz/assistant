package outcomes

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// prdHTML is a document shaped like the real one: an earlier section that
// discusses an outcome, the machine interface section around the row, another
// row in the same table that sets an outcome's value in a code element without
// declaring it, and prose after the table that does the same.
func prdHTML() string {
	return `<html><body>
<section id="execution" class="prd-anchor">
  <p>A checkpoint is written once an <code>executing</code> segment claims the run.</p>
</section>
<section id="surfaces" class="prd-anchor">
  <table><tbody>
    <tr><td><b>Blocking calls</b></td><td>That one call returns <code>executing</code> instead.</td></tr>
    <tr id="outcome-set"><td><b>Outcomes</b></td><td>Four say a run is finished with:
      <code data-outcome="finished">checks-passed</code>,
      <code class="whatever" data-outcome="finished">passed</code>,
      <code data-outcome="finished">failed</code> and
      <code data-outcome="finished">cancelled</code>.
      Two say it is not: <code data-outcome="unfinished">decision</code> and
      <code data-outcome="unfinished">executing</code>.</td></tr>
  </tbody></table>
  <p>A caller never answers <code>executing</code>, because there is nothing to answer.</p>
</section>
</body></html>`
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
// section before it, whole and carrying every marker it had. In inside, that
// same document has the two sections' ids exchanged, so the row's new home is
// the one the rule looks in. Reading inside is what makes outside a control:
// without it a refusal could come from the move having broken the document,
// and the case would pass for the wrong reason.
func movedRow(t *testing.T) (outside, inside string) {
	t.Helper()
	full := prdHTML()
	const host = `<section id="execution" class="prd-anchor">`
	if strings.Count(full, host) != 1 {
		t.Fatalf("the fixture does not hold exactly one %s to move the row into", host)
	}
	from := strings.Index(full, rowAnchor)
	if from < 0 {
		t.Fatalf("the fixture holds no %s", rowAnchor)
	}
	width := strings.Index(full[from:], "</tr>")
	if width < 0 {
		t.Fatal("the fixture's outcome row is never closed")
	}
	row := full[from : from+width+len("</tr>")]

	outside = strings.Replace(full[:from]+full[from+width+len("</tr>"):],
		host, host+"<table><tbody>"+row+"</tbody></table>", 1)
	if !strings.Contains(outside, host+"<table><tbody>"+rowAnchor) {
		t.Fatalf("the row was not moved into %s, so the document is unchanged in the way that matters", host)
	}

	inside = strings.Replace(outside, sectionAnchor, `<section id="machine"`, 1)
	inside = strings.Replace(inside, `<section id="execution"`, `<section id="surfaces"`, 1)
	if strings.Count(inside, sectionAnchor) != 1 {
		t.Fatalf("exchanging the two ids left %d %s in the document", strings.Count(inside, sectionAnchor), sectionAnchor)
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
	assertReads(t, prdHTML())
}

// assertReads fails unless FromPRD reads the fixture's six declarations back
// in the fixture's order and groups.
func assertReads(t *testing.T, html string) {
	t.Helper()
	got, err := FromPRD([]byte(html))
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
	full := prdHTML()
	outsideTheSection, _ := movedRow(t)
	cases := []struct {
		name string
		html string
	}{
		{"the row is outside the section that owns it", outsideTheSection},
		{"no section that owns the row", strings.Replace(full, sectionAnchor, `<section id="elsewhere"`, 1)},
		{"two sections that own the row", strings.Replace(full, sectionAnchor, `<section id="surfaces"></section>`+sectionAnchor, 1)},
		{"the section is never closed", strings.Replace(full, "</section>\n</body>", "\n</body>", 1)},
		{"a section nested in it", strings.Replace(full,
			`<section id="surfaces" class="prd-anchor">`,
			`<section id="surfaces" class="prd-anchor"><section id="inner"><p>x</p></section>`, 1)},
		{"no outcome row", strings.Replace(full, `<tr id="outcome-set"`, `<tr id="other"`, 1)},
		{"the row is never closed", strings.Replace(full, "</td></tr>\n  </tbody>", "\n  </tbody>", 1)},
		{"two outcome rows", strings.Replace(full, `<tr id="outcome-set"`, `<tr id="outcome-set"></tr><tr id="outcome-set"`, 1)},
		{"a row nested in it", strings.Replace(full, `<code data-outcome="unfinished">decision</code>`, `<tr><code data-outcome="unfinished">decision</code></tr>`, 1)},
		{"no marker in it", strings.NewReplacer(
			`data-outcome="finished"`, "", `data-outcome="unfinished"`, "").Replace(full)},
		{"a marker outside the row, in the same section", strings.Replace(full,
			`<p>A caller never answers <code>executing</code>`,
			`<p>A caller never answers <code data-outcome="unfinished">executing</code>`, 1)},
		{"a marker outside the row, in another section", strings.Replace(full,
			`<code>executing</code> segment claims the run.`,
			`<code data-outcome="finished">merged</code> once an <code>executing</code> segment claims the run.`, 1)},
		{"a marker on something that is not a code element", strings.Replace(full,
			`<code data-outcome="finished">failed</code>`, `<b data-outcome="finished">failed</b>`, 1)},
		{"a marker on a code element holding markup", strings.Replace(full,
			`<code data-outcome="finished">failed</code>`, `<code data-outcome="finished"><b>failed</b></code>`, 1)},
		{"a group word this rule does not know", strings.Replace(full, `data-outcome="unfinished">decision`, `data-outcome="open">decision`, 1)},
		{"a repeated value", strings.Replace(full, `>cancelled<`, `>passed<`, 1)},
		{"a value the wire does not carry", strings.Replace(full, `>cancelled<`, `>Cancelled Run<`, 1)},
		{"an empty value", strings.Replace(full, `>cancelled<`, `><`, 1)},
		{"one group", strings.ReplaceAll(full, `data-outcome="unfinished"`, `data-outcome="finished"`)},
		{"the groups interleaved", strings.Replace(full, `data-outcome="finished">passed`, `data-outcome="unfinished">passed`, 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.html == full {
				t.Fatal("this case did not change the fixture, so it is the readable document under another name and would pass without the rule it is named for")
			}
			got, err := FromPRD([]byte(c.html))
			if !errors.Is(err, ErrPRD) {
				t.Fatalf("FromPRD = %s, %v, want an ErrPRD", names(got), err)
			}
		})
	}
}
