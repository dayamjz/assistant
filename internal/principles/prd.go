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

// badge matches the marker the PRD puts a principle behind: a kbd element
// whose entire text is the principle's name. That is the whole extraction
// rule, and it is meant to be one a reader can apply by eye to the Markdown.
// It is not tied to any attribute on the element, which would be styling.
var badge = regexp.MustCompile(`<kbd\b[^>]*>\s*P([0-9]+)\s*</kbd>`)

// FromPRD returns the principles the PRD lists, in the order it lists them.
//
// A badge is read only outside fenced code blocks, by PRDProse's rule: a
// badge in a fenced block renders as code, as an example of one, and is never
// a principle the PRD declares.
//
// It refuses rather than returning a short list. The numbering must run from
// P1 with no gap, no repeat, and no step backwards, so a badge this rule
// picks up that is not a principle, or a principle the PRD stops listing,
// fails loudly here instead of quietly changing what the rest of this package
// is measuring.
func FromPRD(doc []byte) ([]Principle, error) {
	prose, err := PRDProse(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPRD, err)
	}
	from, to, err := PRDSection(doc, 2, principlesHeading)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPRD, err)
	}
	section := prose[from:to]

	var out []Principle
	for _, m := range badge.FindAllSubmatch(section, -1) {
		n, err := strconv.Atoi(string(m[1]))
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
