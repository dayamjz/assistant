package principles

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The tests in this file drive the check over trees built for the purpose, so
// that what it does when a principle is unclaimed is demonstrated here rather
// than only in the repository it is aimed at. A coverage check that cannot
// fail is worse than none, because it turns an area nobody examined into a
// green tick.

// prdHTML writes a document shaped the way FromPRD reads one: a principles
// section with n badges in it. It also puts a badge that reads like a
// principle outside that section, because the real PRD does the same in its
// stage table and its configuration table.
func prdHTML(n int) string {
	var b strings.Builder
	b.WriteString("<html><body>\n")
	b.WriteString(`<section id="stages"><span class="prd-num">P3</span></section>` + "\n")
	b.WriteString(`<section id="principles" class="prd-anchor">` + "\n")
	for i := 1; i <= n; i++ {
		b.WriteString(`  <div class="collapse-title"><span class="badge prd-num">P` + strconv.Itoa(i) + `</span> a principle</div>` + "\n")
	}
	b.WriteString("</section>\n</body></html>\n")
	return b.String()
}

// tree builds a root with a PRD listing every principle this package has a
// constant for, plus the files given, and returns it.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, filepath.FromSlash(PRDPath), prdHTML(len(All())))
	for name, body := range files {
		write(t, root, filepath.FromSlash(name), body)
	}
	return root
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", full, err)
	}
}

// declaredExcept is a gap declaration for every principle but the ones named,
// so a case can be about those and nothing else.
func declaredExcept(except ...Principle) map[Principle]string {
	out := map[Principle]string{}
	for _, p := range All() {
		skip := false
		for _, e := range except {
			if p == e {
				skip = true
			}
		}
		if !skip {
			out[p] = "not what this case is about"
		}
	}
	return out
}

// source wraps a test body in a file that imports this package the way a test
// in this repository does.
func source(body string) string {
	return `package x

import (
	"testing"

	"example.test/repo/internal/principles"
)
` + body
}

func names(ps []Principle) string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return "[" + strings.Join(out, " ") + "]"
}

func mustCheck(t *testing.T, root string, declared map[Principle]string) Coverage {
	t.Helper()
	c, err := check(root, declared)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	return c
}

// TestAPrincipleNoTestCitesIsReportedMissing is the guard itself: the case the
// whole package exists to turn red.
func TestAPrincipleNoTestCitesIsReportedMissing(t *testing.T) {
	root := tree(t, map[string]string{"a/a_test.go": source(`
func TestSomething(t *testing.T) {
	principles.Cite(t, principles.P1)
}
`)})
	c := mustCheck(t, root, declaredExcept(P1, P2))

	if got := names(c.Missing); got != "[P2]" {
		t.Fatalf("Missing = %s, want [P2]", got)
	}
	if c.OK() {
		t.Fatal("OK is true with a principle nothing accounts for")
	}
	if !strings.Contains(c.Report(), "P2") {
		t.Fatalf("the report does not name P2:\n%s", c.Report())
	}
	if len(c.Cited[P1]) != 1 {
		t.Fatalf("Cited[P1] = %v, want the one citation", c.Cited[P1])
	}
	if cite := c.Cited[P1][0]; cite.File != "a/a_test.go" || cite.Test != "TestSomething" {
		t.Fatalf("citation %+v, want it attributed to TestSomething in a/a_test.go", cite)
	}
}

// TestACommentNamingAPrincipleIsNotACitation is the vacuous form this rule is
// chosen against. Every citation in this repository was a comment before it
// was a call, so a matcher that accepted comments would have reported the
// whole set covered on the day it was written.
func TestACommentNamingAPrincipleIsNotACitation(t *testing.T) {
	root := tree(t, map[string]string{"a/a_test.go": source(`
// TestSomething is P2 in this package: the comment says so and nothing else
// does. principles.Cite(t, principles.P2) appears here in prose too.
func TestSomething(t *testing.T) {
	principles.Cite(t, principles.P1)
	_ = "principles.Cite(t, principles.P2)"
}
`)})
	c := mustCheck(t, root, declaredExcept(P1, P2))

	if got := names(c.Missing); got != "[P2]" {
		t.Fatalf("Missing = %s, want [P2]: a comment and a string are not citations", got)
	}
}

// TestATestNamedAfterAPrincipleIsNotACitation covers the other rule that was
// considered. A name is not compiled against anything either.
func TestATestNamedAfterAPrincipleIsNotACitation(t *testing.T) {
	root := tree(t, map[string]string{"a/a_test.go": source(`
func TestP2HoldsUnderPressure(t *testing.T) {
	principles.Cite(t, principles.P1)
}
`)})
	c := mustCheck(t, root, declaredExcept(P1, P2))

	if got := names(c.Missing); got != "[P2]" {
		t.Fatalf("Missing = %s, want [P2]", got)
	}
}

// TestACitationInAHelperDoesNotCountForItsCallers keeps one helper from
// claiming a principle on behalf of every test that calls it.
func TestACitationInAHelperDoesNotCountForItsCallers(t *testing.T) {
	root := tree(t, map[string]string{"a/a_test.go": source(`
func helper(t *testing.T) {
	principles.Cite(t, principles.P2)
}

func TestSomething(t *testing.T) {
	principles.Cite(t, principles.P1)
	helper(t)
}
`)})
	c := mustCheck(t, root, declaredExcept(P1, P2))

	if got := names(c.Missing); got != "[P2]" {
		t.Fatalf("Missing = %s, want [P2]", got)
	}
}

// TestACitationOutsideATestFileDoesNotCount keeps a citation out of code no
// test has to reach.
func TestACitationOutsideATestFileDoesNotCount(t *testing.T) {
	root := tree(t, map[string]string{
		"a/a.go": source(`
func TestSomething(t *testing.T) {
	principles.Cite(t, principles.P2)
}
`),
		"a/a_test.go": source(`
func TestSomethingElse(t *testing.T) {
	principles.Cite(t, principles.P1)
}
`)})
	c := mustCheck(t, root, declaredExcept(P1, P2))

	if got := names(c.Missing); got != "[P2]" {
		t.Fatalf("Missing = %s, want [P2]", got)
	}
}

// TestACitationInASubtestOfTheTestCounts is the other side of the helper rule:
// a closure the test body declares is inside the test.
func TestACitationInASubtestOfTheTestCounts(t *testing.T) {
	root := tree(t, map[string]string{"a/a_test.go": source(`
func TestSomething(t *testing.T) {
	t.Run("one", func(t *testing.T) {
		principles.Cite(t, principles.P1, principles.P2)
	})
}
`)})
	c := mustCheck(t, root, declaredExcept(P1, P2))

	if len(c.Missing) != 0 {
		t.Fatalf("Missing = %s, want none", names(c.Missing))
	}
	if len(c.Cited[P1]) != 1 || len(c.Cited[P2]) != 1 {
		t.Fatalf("Cited = %v, want both principles of the one call", c.Cited)
	}
}

// TestAnAliasedImportStillCites keeps the rule about the import rather than
// about the spelling of one name.
func TestAnAliasedImportStillCites(t *testing.T) {
	root := tree(t, map[string]string{"a/a_test.go": `package x

import (
	"testing"

	prd "example.test/repo/internal/principles"
)

func TestSomething(t *testing.T) {
	prd.Cite(t, prd.P1, prd.P2)
}
`})
	c := mustCheck(t, root, declaredExcept(P1, P2))

	if len(c.Missing) != 0 {
		t.Fatalf("Missing = %s, want none", names(c.Missing))
	}
}

// TestADeclaredGapAccountsForAPrincipleAndNothingMore is the ledger working
// and the ledger going stale, which are the same row on two days.
func TestADeclaredGapAccountsForAPrincipleAndNothingMore(t *testing.T) {
	root := tree(t, map[string]string{"a/a_test.go": source(`
func TestSomething(t *testing.T) {
	principles.Cite(t, principles.P1)
}
`)})

	c := mustCheck(t, root, declaredExcept(P1))
	if !c.OK() {
		t.Fatalf("a declared gap did not account for the uncited principles:\n%s", c.Report())
	}

	stale := mustCheck(t, root, declaredExcept())
	if got := names(stale.Stale); got != "[P1]" {
		t.Fatalf("Stale = %s, want [P1]: a row claiming nothing cites P1 while a test does", got)
	}
	if !strings.Contains(stale.Report(), "TestSomething") {
		t.Fatalf("the report does not say who cites it:\n%s", stale.Report())
	}
}

// TestAPrincipleWithNoConstantHereIsReported is what a principle added to the
// PRD does before anything else: there is no identifier to cite it with, and
// that is a failure rather than a principle nobody has to account for.
func TestAPrincipleWithNoConstantHereIsReported(t *testing.T) {
	root := t.TempDir()
	write(t, root, filepath.FromSlash(PRDPath), prdHTML(len(All())+1))
	write(t, root, "a_test.go", source(`
func TestSomething(t *testing.T) {
	principles.Cite(t, principles.P1)
}
`))
	c := mustCheck(t, root, declaredExcept(P1))

	next := Principle(len(All()) + 1)
	if got := names(c.Unnamed); got != "["+next.String()+"]" {
		t.Fatalf("Unnamed = %s, want [%s]", got, next)
	}
	if c.OK() {
		t.Fatal("OK is true with a principle no constant here names")
	}
}

// TestAPrincipleTheDocumentNoLongerListsIsReported is the same reconciliation
// from the other side: the PRD owns the list, so a name here that it does not
// list is a finding rather than an extra.
func TestAPrincipleTheDocumentNoLongerListsIsReported(t *testing.T) {
	root := t.TempDir()
	write(t, root, filepath.FromSlash(PRDPath), prdHTML(1))
	write(t, root, "a_test.go", source(`
func TestSomething(t *testing.T) {
	principles.Cite(t, principles.P1)
}
`))
	c := mustCheck(t, root, nil)

	if len(c.Unlisted) != len(All())-1 {
		t.Fatalf("Unlisted = %s, want every constant the one-principle document does not list", names(c.Unlisted))
	}
	if c.OK() {
		t.Fatal("OK is true while this package names principles the PRD does not")
	}
}

// TestACitationNobodyCanReadIsRefused keeps an unreadable claim from passing
// as no claim at all.
func TestACitationNobodyCanReadIsRefused(t *testing.T) {
	for _, body := range []string{`
func TestSomething(t *testing.T) {
	p := principles.P1
	principles.Cite(t, p)
}
`, `
func TestSomething(t *testing.T) {
	principles.Cite(t)
}
`} {
		root := tree(t, map[string]string{"a/a_test.go": source(body)})
		if _, err := check(root, nil); !errors.Is(err, ErrCitation) {
			t.Fatalf("check(%s) = %v, want an ErrCitation", body, err)
		}
	}
}

// TestAScanThatCouldNotHaveFoundACitationIsRefused is the guard on the guard:
// a root with no test file under it reports nothing, and reporting nothing
// must not read as reporting no problem.
func TestAScanThatCouldNotHaveFoundACitationIsRefused(t *testing.T) {
	root := t.TempDir()
	write(t, root, filepath.FromSlash(PRDPath), prdHTML(len(All())))
	if _, err := check(root, declaredExcept()); !errors.Is(err, ErrScan) {
		t.Fatalf("check of a root with no test file = %v, want an ErrScan", err)
	}
}

// TestADocumentThatCannotBeReadIsRefused keeps a missing or unreadable list
// from becoming an empty one.
func TestADocumentThatCannotBeReadIsRefused(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a_test.go", source(`
func TestSomething(t *testing.T) {
	principles.Cite(t, principles.P1)
}
`))
	if _, err := check(root, nil); !errors.Is(err, ErrPRD) {
		t.Fatalf("check of a root with no PRD = %v, want an ErrPRD", err)
	}
}

// TestTestdataAndDotDirectoriesAreNotScanned states where the walk stops, so
// the rule is checked rather than only written down.
func TestTestdataAndDotDirectoriesAreNotScanned(t *testing.T) {
	cited := source(`
func TestSomething(t *testing.T) {
	principles.Cite(t, principles.P2)
}
`)
	root := tree(t, map[string]string{
		"a/testdata/a_test.go": cited,
		".hidden/a_test.go":    cited,
		"a/a_test.go": source(`
func TestSomethingElse(t *testing.T) {
	principles.Cite(t, principles.P1)
}
`)})
	c := mustCheck(t, root, declaredExcept(P1, P2))

	if got := names(c.Missing); got != "[P2]" {
		t.Fatalf("Missing = %s, want [P2]", got)
	}
	if c.Files != 1 {
		t.Fatalf("the scan read %d test files, want the one outside testdata and the dot directory", c.Files)
	}
}

// TestADeclaredGapWithNothingWrittenInItIsRefused keeps a row from accounting
// for a principle without anybody having to say anything about the gap.
func TestADeclaredGapWithNothingWrittenInItIsRefused(t *testing.T) {
	root := tree(t, map[string]string{"a/a_test.go": source(`
func TestSomething(t *testing.T) {
	principles.Cite(t, principles.P1)
}
`)})
	declared := declaredExcept(P1)
	declared[P2] = "  "
	if _, err := check(root, declared); !errors.Is(err, ErrDeclaration) {
		t.Fatalf("check with an empty reason = %v, want an ErrDeclaration", err)
	}
}

// TestCitationsReportsWhereEachClaimWasMade drives the scan on its own, since
// it is the rule the rest of this package is built on and a caller reading it
// gets the citations rather than the verdict.
func TestCitationsReportsWhereEachClaimWasMade(t *testing.T) {
	root := tree(t, map[string]string{"a/a_test.go": source(`
func TestSomething(t *testing.T) {
	principles.Cite(t, principles.P1)
}
`)})
	got, err := Citations(root)
	if err != nil {
		t.Fatalf("Citations: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Citations = %v, want the one call", got)
	}
	want := "P1 in TestSomething (a/a_test.go:10)"
	if got[0].String() != want {
		t.Fatalf("Citations = %q, want %q", got[0].String(), want)
	}
}
