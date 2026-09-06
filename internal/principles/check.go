package principles

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrScan reports that the scan for citations could not have found any. A
// root with no test file under it is refused rather than reported as a
// repository whose tests claim nothing, because those two answers look the
// same and only one of them is a finding.
var ErrScan = errors.New("the scan found no test file to read citations from")

// ErrDeclaration reports a row in the declared gaps that says nothing about
// the gap. A row is read in review, so a row nobody has to write a sentence
// for would be a way to account for a principle without looking at it.
var ErrDeclaration = errors.New("a declared gap gives no reason")

// Coverage is the answer to one question: which principles does the PRD list
// that no test in this repository claims, and which claims here name nothing
// the PRD lists.
//
// It says nothing about whether a principle holds. A principle with a hundred
// citations is a principle a hundred tests say they check.
type Coverage struct {
	// Listed is the principles the PRD lists, in its order.
	Listed []Principle
	// Cited is every citation found, by principle.
	Cited map[Principle][]Citation
	// Unclaimed is the declared gaps, as unclaimed.go states them.
	Unclaimed map[Principle]string
	// Files is how many _test.go files the scan read.
	Files int

	// Missing is listed, uncited, and undeclared: the case this check exists
	// for.
	Missing []Principle
	// Stale is declared unclaimed and cited anyway, so the declaration is out
	// of date.
	Stale []Principle
	// Unnamed is listed by the PRD and has no constant in this package, so no
	// test could cite it.
	Unnamed []Principle
	// Unlisted is named here, by a constant, a citation, or a declared gap,
	// and the PRD does not list it.
	Unlisted []Principle
}

// OK reports whether every principle the PRD lists is either cited by a test
// or declared unclaimed, and nothing here names a principle the PRD does not.
func (c Coverage) OK() bool {
	return len(c.Missing) == 0 && len(c.Stale) == 0 && len(c.Unnamed) == 0 && len(c.Unlisted) == 0
}

// Report describes everything OK is false for and says what closes each. It
// is empty when OK is true.
func (c Coverage) Report() string {
	var b strings.Builder
	for _, p := range c.Missing {
		fmt.Fprintf(&b, "%s: the PRD lists it, no test cites it, and %s does not declare the gap.\n", p, unclaimedFile)
		fmt.Fprintf(&b, "  Add principles.Cite(t, principles.%s) to a test that checks it, or add a row to %s saying nothing here does.\n", p, unclaimedFile)
	}
	for _, p := range c.Stale {
		fmt.Fprintf(&b, "%s: %s declares that nothing claims it, but %s.\n", p, unclaimedFile, citedBy(c.Cited[p]))
		fmt.Fprintf(&b, "  Remove its row from %s.\n", unclaimedFile)
	}
	for _, p := range c.Unnamed {
		fmt.Fprintf(&b, "%s: the PRD lists it and this package has no constant for it, so no test can cite it.\n", p)
		b.WriteString("  Add it to the constant block in principle.go and move last to it.\n")
	}
	for _, p := range c.Unlisted {
		fmt.Fprintf(&b, "%s: named here, and the PRD's principles section does not list it.\n", p)
		b.WriteString("  The PRD owns the list. Reconcile with it rather than around it.\n")
	}
	return b.String()
}

// unclaimedFile is where a declared gap is written down, named here so the
// report can point at it.
const unclaimedFile = "internal/principles/unclaimed.go"

// citedBy renders who claims a principle, for a report that has to be read by
// somebody deciding whether a declaration is out of date.
func citedBy(cites []Citation) string {
	names := make([]string, 0, len(cites))
	for _, c := range cites {
		names = append(names, c.String())
	}
	return "it is cited by " + strings.Join(names, ", ")
}

// Check reads the PRD under root, scans root for citations, and reports what
// the two do not account for between them. Root is the module root: the PRD
// is read from PRDPath under it and every _test.go file under it is scanned.
func Check(root string) (Coverage, error) {
	return check(root, unclaimed)
}

// check is Check with the declared gaps supplied, so this package's own tests
// can drive it over a tree that is not this repository.
func check(root string, declared map[Principle]string) (Coverage, error) {
	html, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(PRDPath)))
	if err != nil {
		return Coverage{}, fmt.Errorf("%w: %w", ErrPRD, err)
	}
	listed, err := FromPRD(html)
	if err != nil {
		return Coverage{}, err
	}
	files, cites, err := scan(root)
	if err != nil {
		return Coverage{}, err
	}
	if files == 0 {
		return Coverage{}, fmt.Errorf("%w: nothing named *_test.go under %s", ErrScan, root)
	}

	c := Coverage{
		Listed:    listed,
		Cited:     make(map[Principle][]Citation),
		Unclaimed: make(map[Principle]string, len(declared)),
		Files:     files,
	}
	for _, cite := range cites {
		c.Cited[cite.Principle] = append(c.Cited[cite.Principle], cite)
	}
	for p, why := range declared {
		if strings.TrimSpace(why) == "" {
			return Coverage{}, fmt.Errorf("%w: %s", ErrDeclaration, p)
		}
		c.Unclaimed[p] = why
	}

	for _, p := range listed {
		if !p.known() {
			c.Unnamed = append(c.Unnamed, p)
			continue
		}
		if _, declaredGap := c.Unclaimed[p]; len(c.Cited[p]) == 0 && !declaredGap {
			c.Missing = append(c.Missing, p)
		}
	}
	for p := range c.Unclaimed {
		if len(c.Cited[p]) > 0 {
			c.Stale = append(c.Stale, p)
		}
	}
	named := append(All(), slices.Collect(maps.Keys(c.Cited))...)
	named = append(named, slices.Collect(maps.Keys(c.Unclaimed))...)
	for _, p := range named {
		if !slices.Contains(listed, p) && !slices.Contains(c.Unlisted, p) {
			c.Unlisted = append(c.Unlisted, p)
		}
	}
	slices.Sort(c.Stale)
	slices.Sort(c.Unlisted)
	return c, nil
}
