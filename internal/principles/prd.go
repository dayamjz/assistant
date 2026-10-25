package principles

import (
	"bytes"
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

// ErrPRDSection reports that PRDSection could not delimit the section it was
// asked for. Every refusal PRDSection makes wraps it.
var ErrPRDSection = errors.New("the PRD section could not be delimited")

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
// It refuses rather than returning a short list. The numbering must run from
// P1 with no gap, no repeat, and no step backwards, so a badge this rule
// picks up that is not a principle, or a principle the PRD stops listing,
// fails loudly here instead of quietly changing what the rest of this package
// is measuring.
func FromPRD(doc []byte) ([]Principle, error) {
	from, to, err := PRDSection(doc, principlesHeading)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPRD, err)
	}
	section := doc[from:to]

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

// PRDSection returns the byte range doc[from:to] of the body of the PRD
// section whose heading is "## " followed by heading, exactly: every line
// after that heading up to the next heading of level one or two, or to the
// end of the document. A section is named by its heading and never by its
// number, so a section inserted earlier, which renumbers every section after
// it, moves nothing a reader of this range depends on.
//
// It is the one rule for where a PRD section begins and ends, and every check
// that reads a section of the PRD reads it through this.
//
// A line inside a fenced code block is never a heading, since it renders as
// code. The rule reads headings written with leading hashes and nothing else:
// a heading written as a line of text underlined with = or - is not a
// boundary here, and the PRD writes none.
//
// It refuses rather than guessing: a document with no such heading, one with
// it more than once, and one whose fenced code block is never closed, because
// a fence left open hides every heading after it.
func PRDSection(doc []byte, heading string) (from, to int, err error) {
	want := "## " + heading
	from, to = -1, len(doc)
	var fence []byte
	fenceLine := 0
	for at, line := 0, 1; at < len(doc); line++ {
		end := bytes.IndexByte(doc[at:], '\n')
		next := len(doc)
		if end >= 0 {
			next = at + end + 1
		} else {
			end = len(doc) - at
		}
		text := bytes.TrimRight(doc[at:at+end], " \t\r")
		if fence != nil {
			if closesFence(text, fence) {
				fence = nil
			}
		} else if f := opensFence(text); f != nil {
			fence, fenceLine = f, line
		} else if level := headingLevel(text); level == 1 || level == 2 {
			named := string(bytes.TrimLeft(text, " ")) == want
			switch {
			case named && from >= 0:
				return 0, 0, fmt.Errorf("%w: %q appears more than once", ErrPRDSection, want)
			case named:
				from, to = next, len(doc)
			case from >= 0 && to == len(doc):
				to = at
			}
		}
		at = next
	}
	if fence != nil {
		return 0, 0, fmt.Errorf("%w: the fenced block opened at line %d is never closed", ErrPRDSection, fenceLine)
	}
	if from < 0 {
		return 0, 0, fmt.Errorf("%w: no %q heading in the document", ErrPRDSection, want)
	}
	return from, to, nil
}

// opensFence returns the run of backticks or tildes that opens a fenced code
// block on this line, or nil when the line opens none. Up to three spaces of
// indentation are allowed, as CommonMark allows them.
func opensFence(line []byte) []byte {
	trimmed := bytes.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return nil
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == trimmed[0] {
		n++
	}
	if n < 3 || (trimmed[0] == '`' && bytes.IndexByte(trimmed[n:], '`') >= 0) {
		return nil
	}
	return trimmed[:n]
}

// closesFence reports whether line closes a block that fence opened: the same
// character, at least as many of it, and nothing after.
func closesFence(line, fence []byte) bool {
	trimmed := bytes.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < len(fence) {
		return false
	}
	for _, c := range trimmed {
		if c != fence[0] {
			return false
		}
	}
	return true
}

// headingLevel returns the level of a heading written with leading hashes,
// and zero for any other line.
func headingLevel(line []byte) int {
	trimmed := bytes.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return 0
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || (n < len(trimmed) && trimmed[n] != ' ' && trimmed[n] != '\t') {
		return 0
	}
	return n
}
