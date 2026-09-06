package principles

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestFromPRDReadsTheBadgesInTheSectionAndNothingElse pins the extraction rule
// to a document shaped like the real one, including the badges outside the
// section that read like principles.
func TestFromPRDReadsTheBadgesInTheSectionAndNothingElse(t *testing.T) {
	got, err := FromPRD([]byte(prdHTML(3)))
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
	full := prdHTML(3)
	cases := []struct {
		name string
		html string
	}{
		{"no principles section", strings.Replace(full, `<section id="principles"`, `<section id="other"`, 1)},
		{"the section is never closed", strings.Replace(full, "</section>\n</body>", "\n</body>", 1)},
		{"two principles sections", full + full},
		{"a section nested in it", strings.Replace(full, `<span class="badge prd-num">P2</span>`, `<section><span class="badge prd-num">P2</span></section>`, 1)},
		{"no badge in it", strings.Replace(full, `<span class="badge prd-num">P1</span>`, "", 1)},
		{"a gap in the numbering", strings.Replace(full, `>P2<`, `>P4<`, 1)},
		{"a repeated number", strings.Replace(full, `<span class="badge prd-num">P3</span>`, `<span class="badge prd-num">P2</span>`, 1)},
		{"a step backwards", strings.Replace(full, `>P1<`, `>P3<`, 1)},
		{"a number too large to hold", strings.Replace(full, `>P1<`, `>P9001<`, 1)},
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

// tb records what Cite does to the test it is given, because what Cite does
// when it is asked to record a principle that does not exist is the only part
// of it that has to hold.
type tb struct {
	testing.TB
	fatal []string
	logs  []string
}

func (r *tb) Helper()                         {}
func (r *tb) Logf(format string, args ...any) { r.logs = append(r.logs, fmt.Sprintf(format, args...)) }
func (r *tb) Fatalf(format string, args ...any) {
	r.fatal = append(r.fatal, fmt.Sprintf(format, args...))
}

// TestCiteRefusesAPrincipleThisPackageDoesNotHave keeps a citation from naming
// a value that is not a principle at all.
func TestCiteRefusesAPrincipleThisPackageDoesNotHave(t *testing.T) {
	rec := &tb{}
	Cite(rec, Principle(0), Principle(len(All())+1))
	if len(rec.fatal) != 2 {
		t.Fatalf("Cite failed the test %d times, want once for each value: %v", len(rec.fatal), rec.fatal)
	}

	ok := &tb{}
	Cite(ok, P1)
	if len(ok.fatal) != 0 {
		t.Fatalf("Cite failed the test on a principle it has: %v", ok.fatal)
	}
	if len(ok.logs) != 1 || !strings.Contains(ok.logs[0], "P1") {
		t.Fatalf("Cite logged %v, want the claim it recorded", ok.logs)
	}
}
