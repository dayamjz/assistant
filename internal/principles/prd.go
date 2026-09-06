package principles

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// PRDPath is where the PRD lives, relative to the module root, in slash form.
const PRDPath = "docs/prd.html"

// ErrPRD reports that the PRD's principle list could not be read. Every
// refusal below wraps it. A list this package cannot read is a refusal and
// never an empty list, because an empty list would let every other check here
// pass while checking nothing.
var ErrPRD = errors.New("the PRD's principle list could not be read")

// sectionStart is the opening tag of the section that lists the principles.
// The PRD gives it a stable anchor, and section 4 is the only place a
// principle is declared; a badge that reads like one anywhere else in the
// document is prose about a principle rather than the principle itself.
const sectionStart = `<section id="principles"`

// badge matches the marker the PRD puts a principle behind: a span whose
// entire text is the principle's name. That is the whole extraction rule, and
// it is meant to be one a reader can apply by eye to the HTML. It is not tied
// to the classes on the badge, which are styling and change with the styling.
var badge = regexp.MustCompile(`<span\b[^>]*>\s*P([0-9]+)\s*</span>`)

// FromPRD returns the principles the PRD lists, in the order it lists them.
//
// It refuses rather than returning a short list. The numbering must run from
// P1 with no gap, no repeat, and no step backwards, so a badge this rule
// picks up that is not a principle, or a principle the PRD stops listing,
// fails loudly here instead of quietly changing what the rest of this package
// is measuring.
func FromPRD(html []byte) ([]Principle, error) {
	start := bytes.Index(html, []byte(sectionStart))
	if start < 0 {
		return nil, fmt.Errorf("%w: no %s in the document", ErrPRD, sectionStart)
	}
	if bytes.Contains(html[start+len(sectionStart):], []byte(sectionStart)) {
		return nil, fmt.Errorf("%w: %s appears more than once", ErrPRD, sectionStart)
	}
	rest := html[start+len(sectionStart):]
	end := bytes.Index(rest, []byte("</section>"))
	if end < 0 {
		return nil, fmt.Errorf("%w: the principles section is never closed", ErrPRD)
	}
	section := rest[:end]
	if bytes.Contains(section, []byte("<section")) {
		return nil, fmt.Errorf("%w: the principles section has a section nested in it, so its end cannot be found by the first closing tag", ErrPRD)
	}

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
