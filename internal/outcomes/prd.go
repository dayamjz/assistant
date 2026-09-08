package outcomes

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrPRD reports that the PRD's outcome set could not be read. Every refusal
// below wraps it. A set this package cannot read is a refusal and never a
// short list, because a short list would let the comparison pass while
// comparing against less than the document says.
var ErrPRD = errors.New("the PRD's outcome set could not be read")

// rowAnchor is the opening tag of the row that states the set. The row says
// it is where the set is stated, so the row and not the section is what is
// read: the section discusses these values in prose as well, and a rule that
// took the section would let the set grow somewhere the document says the set
// is not.
const rowAnchor = `<tr id="outcome-set"`

// sectionAnchor is the opening tag of the section the row has to sit inside.
// The section is named by its id and never by its number, because a section
// inserted earlier in the document renumbers it while leaving the id, the row
// and the set exactly where they were.
//
// This is a different question from where the markers are read. The markers
// are read from the row and refused everywhere else in the document; this is
// only about where the row itself lives, so that a row moved into another
// section is a refusal rather than a pin that stays green while the section
// the mechanism is documented against no longer states the set.
const sectionAnchor = `<section id="surfaces"`

// declaration matches the marker the PRD puts an outcome behind: a code
// element carrying a data-outcome attribute, whose text is the value that
// travels on the wire and whose attribute is the group the row puts it in.
// That is the whole extraction rule, and it is meant to be one a reader can
// apply by eye to the HTML. It is not tied to the classes on the element,
// which are styling and change with the styling.
var declaration = regexp.MustCompile(`<code\b[^>]*\bdata-outcome="([^"]*)"[^>]*>([^<]*)</code>`)

// marker matches every use of the attribute, whatever it is written on. Two
// counts are taken with it. Inside the row it is compared against the
// declarations found, so a marker this package cannot read as one - on another
// element, or on a code element holding markup rather than a bare value - is a
// refusal rather than an outcome silently left out of the set. Outside the row
// any use at all is a refusal, because the row owning the set is what the
// document claims and a marker elsewhere would quietly make that false.
var marker = regexp.MustCompile(`\bdata-outcome\s*=`)

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
// the document's one section that carries sectionAnchor, and the set must be
// two contiguous groups, each non-empty, with no repeated value and no group
// word this package does not know, and no marker anywhere in the document
// outside the row, so a marker that is not an outcome and an outcome the PRD
// stops declaring both fail loudly here instead of quietly changing what is
// being compared.
func FromPRD(html []byte) ([]Listed, error) {
	row, err := outcomeRow(html)
	if err != nil {
		return nil, err
	}

	found := declaration.FindAllSubmatch(row, -1)
	if inRow := len(marker.FindAll(row, -1)); inRow != len(found) {
		return nil, fmt.Errorf("%w: the row carries %d data-outcome markers and %d of them read as a declaration: a marker outside <code>value</code> is not one this rule can read", ErrPRD, inRow, len(found))
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("%w: the row declares none", ErrPRD)
	}
	if all, inRow := len(marker.FindAll(html, -1)), len(found); all != inRow {
		return nil, fmt.Errorf("%w: the document carries %d data-outcome markers and %d of them are in the row: the row is where the set is stated, so a marker outside it is not part of the set and is not read as one", ErrPRD, all, inRow)
	}

	var out []Listed
	seen := make(map[string]bool, len(found))
	for _, m := range found {
		group, name := string(m[1]), strings.TrimSpace(string(m[2]))
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

// outcomeRow returns the body of the row that states the set, refusing every
// document where the boundaries of that row are not the ones a reader would
// draw, and every document where the row does not sit inside the section that
// owns it.
func outcomeRow(html []byte) ([]byte, error) {
	sectionFrom, sectionTo, err := ownerSection(html)
	if err != nil {
		return nil, err
	}
	start := bytes.Index(html, []byte(rowAnchor))
	if start < 0 {
		return nil, fmt.Errorf("%w: no %s in the document", ErrPRD, rowAnchor)
	}
	if bytes.Contains(html[start+len(rowAnchor):], []byte(rowAnchor)) {
		return nil, fmt.Errorf("%w: %s appears more than once", ErrPRD, rowAnchor)
	}
	rest := html[start+len(rowAnchor):]
	end := bytes.Index(rest, []byte("</tr>"))
	if end < 0 {
		return nil, fmt.Errorf("%w: the outcome row is never closed", ErrPRD)
	}
	row := rest[:end]
	if bytes.Contains(row, []byte("<tr")) {
		return nil, fmt.Errorf("%w: the outcome row has a row nested in it, so its end cannot be found by the first closing tag", ErrPRD)
	}
	if start < sectionFrom || start+len(rowAnchor)+end+len("</tr>") > sectionTo {
		return nil, fmt.Errorf("%w: %s is not inside the %s section, which is where this rule and everything documented against it say the set is stated", ErrPRD, rowAnchor, sectionAnchor)
	}
	return row, nil
}

// ownerSection returns the bounds of the body of the section the row has to
// sit inside, refusing every document where those bounds are not the ones a
// reader would draw. A document whose section this rule cannot delimit is a
// refusal and never a containment check quietly skipped.
func ownerSection(html []byte) (from, to int, err error) {
	start := bytes.Index(html, []byte(sectionAnchor))
	if start < 0 {
		return 0, 0, fmt.Errorf("%w: no %s in the document", ErrPRD, sectionAnchor)
	}
	rest := html[start+len(sectionAnchor):]
	if bytes.Contains(rest, []byte(sectionAnchor)) {
		return 0, 0, fmt.Errorf("%w: %s appears more than once", ErrPRD, sectionAnchor)
	}
	end := bytes.Index(rest, []byte("</section>"))
	if end < 0 {
		return 0, 0, fmt.Errorf("%w: the %s section is never closed", ErrPRD, sectionAnchor)
	}
	if bytes.Contains(rest[:end], []byte("<section")) {
		return 0, 0, fmt.Errorf("%w: the %s section has a section nested in it, so its end cannot be found by the first closing tag", ErrPRD, sectionAnchor)
	}
	return start + len(sectionAnchor), start + len(sectionAnchor) + end, nil
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
