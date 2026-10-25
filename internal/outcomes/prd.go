package outcomes

import (
	"errors"
	"fmt"
	"regexp"

	xhtml "golang.org/x/net/html"

	"github.com/dayamjz/assistant/internal/principles"
)

// ErrPRD reports that the PRD's outcome set could not be read. Every refusal
// below wraps it. A set this package cannot read is a refusal and never a
// short list, because a short list would let the comparison pass while
// comparing against less than the document says.
var ErrPRD = errors.New("the PRD's outcome set could not be read")

// rowID is the id of the anchor inside the row that states the set, and
// rowAnchor is how the PRD spells that anchor. The row says it is where the
// set is stated, so the row and not the section is what is read: the section
// discusses these values in prose as well, and a rule that took the section
// would let the set grow somewhere the document says the set is not. The row
// is the rendered table row that holds the element carrying this id.
const (
	rowID     = "outcome-set"
	rowAnchor = `<a id="outcome-set">`
)

// sectionHeading names the section the row has to sit inside, by its heading
// and never by its number, because a section inserted earlier in the document
// renumbers it while leaving the heading, the row and the set exactly where
// they were. internal/principles' PRD.Section owns where that section begins
// and ends.
//
// This is a different question from where the markers are read. The markers
// are read from the row and refused everywhere else in the document; this is
// only about where the row itself lives, so that a row moved into another
// section is a refusal rather than a pin that stays green while the section
// the mechanism is documented against no longer states the set.
const sectionHeading = "Surfaces"

// markerAttr is the attribute the PRD puts an outcome behind. A declaration
// is a rendered code element carrying it, whose text, and nothing but text, is
// the value that travels on the wire and whose attribute value is the group
// the row puts it in. That is the whole extraction rule, and it is meant to be
// one a reader can apply by eye to the page. It is not tied to any other
// attribute on the element, which would be styling.
//
// Every rendered element carrying it is counted. Inside the row, one that is
// not a declaration - on another element, or on a code element holding markup
// rather than a bare value - is a refusal rather than an outcome silently left
// out of the set. Outside the row any at all is a refusal, because the row
// owning the set is what the document claims and a marker elsewhere would
// quietly make that false.
const markerAttr = "data-outcome"

// wireValue is the shape of a value that travels. The outcomes are lower case
// words joined by single dashes, and a declaration whose text is not one is
// refused rather than compared, because a value the wire cannot carry says
// the marker was put on something that is not an outcome.
var wireValue = regexp.MustCompile(`^[a-z]+(-[a-z]+)*$`)

// The two group words the marker takes, which are the two groups the row
// divides the set into: those that say a run is finished with, and those that
// say it is not.
const (
	groupFinished   = "finished"
	groupUnfinished = "unfinished"
)

// Listed is one outcome as the PRD declares it: the value that travels, and
// whether the row puts it in the group that says a run is finished with.
//
// It carries nothing about what to do next. The row requires every outcome to
// carry a next action and does not say what any of them is, so the text is the
// build's and there is nothing here to compare it against.
type Listed struct {
	// Name is the value as it travels on the wire.
	Name string
	// Terminal is whether the PRD puts it in the group that says a run is
	// finished with.
	Terminal bool
}

// FromPRD returns the outcomes the PRD declares, in the order it declares
// them.
//
// It refuses rather than returning what it could read. The row must sit inside
// the document's one section headed sectionHeading, and the set must be
// two contiguous groups, each non-empty, with no repeated value and no group
// word this package does not know, and no marker anywhere in the document
// outside the row, so a marker that is not an outcome and an outcome the PRD
// stops declaring both fail loudly here instead of quietly changing what is
// being compared.
//
// Everything here is read from the document as it renders, through
// internal/principles' RenderPRD, so a row or a marker that does not render as
// one, in a code block or an HTML comment, is neither: a row moved into one
// leaves the document with no row, which is a refusal, and an example of a
// marker set in code declares nothing.
func FromPRD(doc []byte) ([]Listed, error) {
	prd, err := principles.RenderPRD(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPRD, err)
	}
	row, err := outcomeRow(prd)
	if err != nil {
		return nil, err
	}

	markers := prd.Find(func(n *xhtml.Node) bool { _, ok := principles.Attr(n, markerAttr); return ok })
	type found struct{ group, name string }
	var declared []found
	inRow := 0
	for _, m := range markers {
		if !inside(m, row) {
			continue
		}
		inRow++
		if m.Data != "code" || hasElementChild(m) {
			continue
		}
		group, _ := principles.Attr(m, markerAttr)
		declared = append(declared, found{group, principles.Text(m)})
	}
	if inRow != len(declared) {
		return nil, fmt.Errorf("%w: the row carries %d data-outcome markers and %d of them read as a declaration: a marker outside <code>value</code> is not one this rule can read", ErrPRD, inRow, len(declared))
	}
	if len(declared) == 0 {
		return nil, fmt.Errorf("%w: the row declares none", ErrPRD)
	}
	if all := len(markers); all != inRow {
		return nil, fmt.Errorf("%w: the document carries %d data-outcome markers and %d of them are in the row: the row is where the set is stated, so a marker outside it is not part of the set and is not read as one", ErrPRD, all, inRow)
	}

	var out []Listed
	seen := make(map[string]bool, len(declared))
	for _, d := range declared {
		group, name := d.group, d.name
		if !wireValue.MatchString(name) {
			return nil, fmt.Errorf("%w: %q is marked as an outcome and is not a value the wire carries", ErrPRD, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("%w: %s is declared more than once", ErrPRD, name)
		}
		seen[name] = true
		switch group {
		case groupFinished:
			out = append(out, Listed{Name: name, Terminal: true})
		case groupUnfinished:
			out = append(out, Listed{Name: name, Terminal: false})
		default:
			return nil, fmt.Errorf("%w: %s is marked %q, which is neither %q nor %q", ErrPRD, name, group, groupFinished, groupUnfinished)
		}
	}
	if err := contiguous(out); err != nil {
		return nil, err
	}
	return out, nil
}

// outcomeRow returns the rendered table row that states the set, refusing
// every document where that row is not the one a reader would take, and every
// document where the row does not sit inside the section that owns it.
func outcomeRow(prd *principles.PRD) (*xhtml.Node, error) {
	anchors := prd.Find(func(n *xhtml.Node) bool { id, ok := principles.Attr(n, "id"); return ok && id == rowID })
	switch len(anchors) {
	case 0:
		return nil, fmt.Errorf("%w: no %s in the document", ErrPRD, rowAnchor)
	case 1:
	default:
		return nil, fmt.Errorf("%w: %s appears more than once", ErrPRD, rowAnchor)
	}
	row := anchors[0].Parent
	for row != nil && (row.Type != xhtml.ElementNode || row.Data != "tr") {
		row = row.Parent
	}
	if row == nil {
		return nil, fmt.Errorf("%w: %s is not in a table row, so what it marks is not the row a reader takes it for", ErrPRD, rowAnchor)
	}
	section, err := prd.Section(2, sectionHeading)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPRD, err)
	}
	if !section.Holds(row) {
		return nil, fmt.Errorf("%w: %s is not inside the %q section, which is where this rule and everything documented against it say the set is stated", ErrPRD, rowAnchor, "## "+sectionHeading)
	}
	return row, nil
}

// inside reports whether n is ancestor or one of its descendants.
func inside(n, ancestor *xhtml.Node) bool {
	for ; n != nil; n = n.Parent {
		if n == ancestor {
			return true
		}
	}
	return false
}

// hasElementChild reports whether n holds markup rather than only text.
func hasElementChild(n *xhtml.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode {
			return true
		}
	}
	return false
}

// contiguous refuses a set that is not the two groups the row says it is. The
// row states the groups and their sizes in prose no rule here reads, and what
// makes that prose true of the markers is that each group is declared in one
// run. A set that alternates is one where the prose and the markers have come
// apart.
func contiguous(set []Listed) error {
	groups := 1
	for i := 1; i < len(set); i++ {
		if set[i].Terminal != set[i-1].Terminal {
			groups++
		}
	}
	if groups != 2 {
		return fmt.Errorf("%w: the declarations form %d group(s) rather than the two the row states, so each group is not declared in one run", ErrPRD, groups)
	}
	return nil
}
