package principles

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
)

// This file holds the two rules every check that reads docs/prd.md reads it
// through: which bytes are prose rather than code (PRDProse), and where a
// section begins and ends (PRDSection). They live with PRDPath because this
// package already owns where the document is; internal/outcomes and
// internal/journey read through them and keep no rule of their own.

// ErrPRDProse reports that PRDProse could not tell the document's prose from
// its code. Every refusal PRDProse makes wraps it.
var ErrPRDProse = errors.New("the PRD's prose could not be told from its code")

// ErrPRDSection reports that PRDSection could not delimit the section it was
// asked for. Every refusal PRDSection makes wraps it.
var ErrPRDSection = errors.New("the PRD section could not be delimited")

// PRDProse returns a copy of doc in which every fenced code block, its fence
// lines included, is blanked to spaces. Every newline is kept, so an offset or
// a line number in the copy is the same offset or line in doc, and a range
// PRDSection returns for doc indexes the copy too.
//
// A fenced block renders as code, so nothing in it is a heading or a
// declaration, however it is spelled: a check that reads its declarations from
// this copy cannot be satisfied by an example of one. A fence opens on a line
// of three or more backticks or tildes indented at most three spaces, and
// closes on a line of the same character, at least as many, and nothing else,
// as CommonMark has it. A fence indented four or more spaces, as inside a list
// item, is not recognized; the PRD writes none.
//
// It refuses a document whose fenced block is never closed, because a fence
// left open turns everything after it into code.
func PRDProse(doc []byte) ([]byte, error) {
	out := bytes.Clone(doc)
	var fence []byte
	fenceLine, fenceAt := 0, 0
	err := eachLine(doc, func(line, at, next int, text []byte) error {
		switch {
		case fence != nil:
			if closesFence(text, fence) {
				fence = nil
				blank(out[fenceAt:next])
			}
		default:
			if f := opensFence(text); f != nil {
				fence, fenceLine, fenceAt = f, line, at
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if fence != nil {
		return nil, fmt.Errorf("%w: the fenced block opened at line %d is never closed", ErrPRDProse, fenceLine)
	}
	return out, nil
}

// PRDSection returns the byte range doc[from:to] of the body of the PRD
// section whose heading is at the given level and reads heading: every line
// after that heading up to the next heading of the same level or a higher
// one, or to the end of the document. A section is named by its heading and
// never by its number, so a section inserted earlier, which renumbers every
// section after it, moves nothing a reader of this range depends on.
//
// It is the one rule for where a PRD section begins and ends, and every check
// that reads a section of the PRD reads it through this.
//
// Headings are read from PRDProse's copy, so a heading-shaped line in a fenced
// block is never one. A heading reads as its text with the optional closing
// run of hashes removed, so "## Principles" and "## Principles ##" are the
// same heading. A heading written as a line of text underlined with = or - is
// refused rather than read, wherever it is in the document: this rule does
// not delimit by it, so a document that uses one would have a section this
// rule could not see.
//
// It refuses rather than guessing: a document with no such heading, one in
// which a heading with that text appears more than once at any level, one
// with a fenced block that is never closed, and one with an underlined
// heading.
func PRDSection(doc []byte, level int, heading string) (from, to int, err error) {
	if level < 1 || level > 6 {
		return 0, 0, fmt.Errorf("%w: level %d is not a heading level", ErrPRDSection, level)
	}
	prose, err := PRDProse(doc)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %w", ErrPRDSection, err)
	}
	want := fmt.Sprintf("%s %s", bytes.Repeat([]byte("#"), level), heading)
	from, to = -1, len(doc)
	var previous []byte
	err = eachLine(prose, func(line, at, next int, text []byte) error {
		defer func() { previous = text }()
		if underline.Match(text) && len(bytes.TrimSpace(previous)) > 0 && headingLevel(previous) == 0 {
			return fmt.Errorf("%w: line %d underlines the line before it, which makes that line a heading this rule does not read; write it with leading hashes", ErrPRDSection, line)
		}
		n, title := headingText(text)
		if n == 0 {
			return nil
		}
		if title == heading {
			if from >= 0 {
				return fmt.Errorf("%w: a heading reading %q appears more than once", ErrPRDSection, heading)
			}
			if n != level {
				// The same words at another level are the same section to a
				// reader, so they are refused below unless the level matches.
				return fmt.Errorf("%w: the heading %q is at level %d, not %d", ErrPRDSection, heading, n, level)
			}
			from, to = next, len(doc)
			return nil
		}
		if from >= 0 && to == len(doc) && n <= level {
			to = at
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	if from < 0 {
		return 0, 0, fmt.Errorf("%w: no %q heading in the document", ErrPRDSection, want)
	}
	return from, to, nil
}

// underline matches a line that, directly under a line of text, would make
// that text a heading: only = or only -, indented at most three spaces.
var underline = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)

// eachLine calls f with every line of doc: its 1-based number, its offset,
// the offset of the line after it, and its text without the newline or
// trailing whitespace. It stops at the first error f returns.
func eachLine(doc []byte, f func(line, at, next int, text []byte) error) error {
	for at, line := 0, 1; at < len(doc); line++ {
		end := bytes.IndexByte(doc[at:], '\n')
		next := len(doc)
		if end >= 0 {
			next = at + end + 1
		} else {
			end = len(doc) - at
		}
		if err := f(line, at, next, bytes.TrimRight(doc[at:at+end], " \t\r")); err != nil {
			return err
		}
		at = next
	}
	return nil
}

// blank turns every byte of b but a newline into a space.
func blank(b []byte) {
	for i, c := range b {
		if c != '\n' {
			b[i] = ' '
		}
	}
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
	n, _ := headingText(line)
	return n
}

// headingText returns the level and the text of a heading written with
// leading hashes, and zero for any other line. The text is what a reader
// sees: the optional closing run of hashes, which CommonMark lets a heading
// end with when a space or tab precedes it, is not part of it.
func headingText(line []byte) (int, string) {
	trimmed := bytes.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return 0, ""
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || (n < len(trimmed) && trimmed[n] != ' ' && trimmed[n] != '\t') {
		return 0, ""
	}
	text := bytes.Trim(trimmed[n:], " \t")
	if closing := bytes.TrimRight(text, "#"); len(closing) == 0 {
		text = closing
	} else if len(closing) < len(text) && (closing[len(closing)-1] == ' ' || closing[len(closing)-1] == '\t') {
		text = bytes.TrimRight(closing, " \t")
	}
	return n, string(text)
}
