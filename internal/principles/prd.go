package principles

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// PRDPath is where the PRD lives, relative to the module root, in slash form.
const PRDPath = "docs/prd.md"

// ErrPRD reports that the PRD's principle list could not be read. Every
// refusal below wraps it. A list this package cannot read is a refusal and
// never an empty list, because an empty list would let every other check here
// pass while checking nothing.
var ErrPRD = errors.New("the PRD's principle list could not be read")

// principlesHeading is the heading of the section that lists the principles.
// Section 4 is the only place a principle is declared; a badge that reads like
// one anywhere else in the document is prose about a principle rather than
// the principle itself.
const principlesHeading = "Principles"

// badge matches the text of the marker the PRD puts a principle behind: a
// rendered kbd element whose entire visible text is the principle's name.
// That is the whole extraction rule, and it is meant to be one a reader can
// apply by eye to the page. It is not tied to any attribute on the element,
// which would be styling.
var badge = regexp.MustCompile(`^P([0-9]+)$`)

// FromPRD returns the principles the PRD lists, in the order it lists them.
//
// The badges are read from the document as it renders (RenderPRD), in the
// section headed "Principles" at level two. A badge that does not render as
// one, in a code block or an HTML comment, is not read, and neither is
// anything that renders the same as a badge from some other spelling.
//
// It refuses rather than returning a short list. The numbering must run from
// P1 with no gap, no repeat, and no step backwards, so a badge this rule
// picks up that is not a principle, or a principle the PRD stops listing,
// fails loudly here instead of quietly changing what the rest of this package
// is measuring.
func FromPRD(doc []byte) ([]Principle, error) {
	prd, err := RenderPRD(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPRD, err)
	}
	section, err := prd.Section(2, principlesHeading)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPRD, err)
	}

	var out []Principle
	for _, kbd := range section.Elements("kbd") {
		m := badge.FindStringSubmatch(Text(kbd))
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n > int(^Principle(0)) {
			return nil, fmt.Errorf("%w: badge P%s is not a number this package can hold", ErrPRD, m[1])
		}
		if want := len(out) + 1; n != want {
			return nil, fmt.Errorf("%w: badge %d in the section is P%d, want P%d: the list must run from P1 with no gap, no repeat, and no step backwards", ErrPRD, want, n, want)
		}
		out = append(out, Principle(n))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: the principles section lists none", ErrPRD)
	}
	return out, nil
}
