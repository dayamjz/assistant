package principles

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
	xhtml "golang.org/x/net/html"
)

// This file holds the rule every check that reads docs/prd.md reads it
// through: the document is read as it renders, never as its source is
// spelled. It lives with PRDPath because this package already owns where the
// document is; internal/outcomes and internal/journey read through it and keep
// no reader of their own.
//
// Reading the rendering rather than the source is the point. A rule written
// against the source has to recognize every spelling Markdown allows for the
// same thing, and a spelling it misses is a declaration that renders and is
// not read, or one that is read and does not render: a badge in a fenced or
// indented code block or an HTML comment, a heading written with emphasis or
// a character reference, a table row without its leading pipe, a table whose
// delimiter row does not match its header. Each of those was a silent pass
// against a source-reading rule. Rendered, each is what a reader sees: code is
// text inside pre and code elements, a comment is not an element at all, a
// heading is its visible text, and a table is whatever cells the renderer
// built.

// ErrPRDSection reports that a section of the PRD could not be delimited.
// Every refusal Section makes wraps it.
var ErrPRDSection = errors.New("the PRD section could not be delimited")

// PRD is docs/prd.md as it renders: the HTML that goldmark, with its GitHub
// Flavored Markdown extensions and raw HTML passed through, produces from the
// document, parsed into a tree.
//
// What this establishes is how goldmark renders the document, a CommonMark
// and GFM implementation. It is not GitHub's renderer, so a construct the two
// render differently is outside what any check built on this establishes. The
// raw HTML is kept as written, attributes included, where GitHub would remove
// some of it; that is what lets a declaration carry a marker attribute at all.
type PRD struct {
	root  *xhtml.Node
	order map[*xhtml.Node]int
	last  map[*xhtml.Node]int
	heads []head
}

// head is one rendered heading: its node, level, and visible text.
type head struct {
	node  *xhtml.Node
	level int
	text  string
}

// RenderPRD renders doc and parses the result. It fails only when rendering or
// parsing does, which for a byte slice in memory is a defect rather than a
// property of the document: every byte sequence is some Markdown.
func RenderPRD(doc []byte) (*PRD, error) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
	var out bytes.Buffer
	if err := md.Convert(doc, &out); err != nil {
		return nil, fmt.Errorf("rendering the PRD: %w", err)
	}
	root, err := xhtml.Parse(&out)
	if err != nil {
		return nil, fmt.Errorf("parsing the rendered PRD: %w", err)
	}
	p := &PRD{root: root, order: map[*xhtml.Node]int{}, last: map[*xhtml.Node]int{}}
	n := 0
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		p.order[node] = n
		n++
		if node.Type == xhtml.ElementNode {
			if level := headingTag(node.Data); level > 0 {
				p.heads = append(p.heads, head{node, level, Text(node)})
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		p.last[node] = n - 1
	}
	walk(root)
	return p, nil
}

// Section is the run of a PRD between one heading and the next heading of the
// same level or a higher one, in document order. A section is named by its
// heading and never by its number, so a section inserted earlier, which
// renumbers every section after it, moves nothing a reader of this range
// depends on.
type Section struct {
	prd      *PRD
	from, to int
}

// Section returns the section whose heading is at the given level and whose
// visible text is heading. The text is what a reader sees: emphasis, a
// character reference, an inline anchor, and a closing run of hashes all leave
// it the same, and a heading written as text underlined with = or - is a
// heading like any other.
//
// It refuses rather than guessing: a document with no such heading, and one in
// which a heading with that visible text appears more than once, at any level,
// because a reader cannot tell which of two sections with the same name is the
// one meant.
func (p *PRD) Section(level int, heading string) (Section, error) {
	if level < 1 || level > 6 {
		return Section{}, fmt.Errorf("%w: level %d is not a heading level", ErrPRDSection, level)
	}
	found := -1
	for i, h := range p.heads {
		if h.text != heading {
			continue
		}
		if found >= 0 {
			return Section{}, fmt.Errorf("%w: a heading reading %q appears more than once", ErrPRDSection, heading)
		}
		found = i
	}
	if found < 0 {
		return Section{}, fmt.Errorf("%w: no heading reading %q in the document", ErrPRDSection, heading)
	}
	h := p.heads[found]
	if h.level != level {
		return Section{}, fmt.Errorf("%w: the heading %q is at level %d, not %d", ErrPRDSection, heading, h.level, level)
	}
	s := Section{prd: p, from: p.last[h.node] + 1, to: len(p.order)}
	for _, next := range p.heads[found+1:] {
		if next.level <= level {
			s.to = p.order[next.node]
			break
		}
	}
	return s, nil
}

// Holds reports whether n, and everything inside it, lies within the section.
func (s Section) Holds(n *xhtml.Node) bool {
	at, ok := s.prd.order[n]
	return ok && at >= s.from && s.prd.last[n] < s.to
}

// Elements returns every element named tag that the section holds whole, in
// document order.
func (s Section) Elements(tag string) []*xhtml.Node {
	var out []*xhtml.Node
	for _, n := range s.prd.Elements(tag) {
		if s.Holds(n) {
			out = append(out, n)
		}
	}
	return out
}

// Elements returns every element named tag in the rendered document, in
// document order.
func (p *PRD) Elements(tag string) []*xhtml.Node {
	return p.Find(func(n *xhtml.Node) bool { return n.Data == tag })
}

// Find returns every element in the rendered document that match accepts, in
// document order.
func (p *PRD) Find(match func(*xhtml.Node) bool) []*xhtml.Node {
	var out []*xhtml.Node
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && match(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(p.root)
	return out
}

// Text returns the visible text of n: every text node inside it, joined, with
// runs of whitespace collapsed to one space and the ends trimmed.
func Text(n *xhtml.Node) string {
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// Attr returns the value of n's attribute named key and whether n has it.
func Attr(n *xhtml.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// headingTag returns the level of a heading element's tag, and zero for any
// other tag.
func headingTag(tag string) int {
	if len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6' {
		return int(tag[1] - '0')
	}
	return 0
}
