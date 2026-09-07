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
	got, err := FromPRD([]byte(prdHTML()))
	if err != nil {
		t.Fatalf("FromPRD: %v", err)
	}
	want := []Listed{
		{"checks-passed", true}, {"passed", true}, {"failed", true}, {"cancelled", true},
		{"decision", false}, {"executing", false},
	}
	if len(got) != len(want) {
		t.Fatalf("FromPRD = %s, want %s", names(got), names(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FromPRD = %s, want %s", names(got), names(want))
		}
	}
}

// TestFromPRDRefusesASetItCannotTrust is why a short set is never an answer:
// each of these is a document the rule reads differently from a reader, and a
// silent short set would quietly shrink what the comparison is over.
func TestFromPRDRefusesASetItCannotTrust(t *testing.T) {
	full := prdHTML()
	cases := []struct {
		name string
		html string
	}{
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
			got, err := FromPRD([]byte(c.html))
			if !errors.Is(err, ErrPRD) {
				t.Fatalf("FromPRD = %s, %v, want an ErrPRD", names(got), err)
			}
		})
	}
}
