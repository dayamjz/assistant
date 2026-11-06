package principles

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	xhtml "golang.org/x/net/html"
)

// proposedTableHeading is the PRD subsection, at level three, whose table
// lists every proposed feature and what the build does for it.
const proposedTableHeading = "Proposed, not yet decided"

// isProposedMarker reports whether a kbd element is a proposed marker: its
// visible text is "proposed", or begins "proposed," as a decision row's
// "proposed, not decided" does.
func isProposedMarker(kbd *xhtml.Node) bool {
	text := strings.ToLower(Text(kbd))
	return text == "proposed" || strings.HasPrefix(text, "proposed,")
}

// proposedMismatches is every way doc's proposed-features table and its
// proposed markers disagree, as the document renders:
//
//   - a feature is a heading carrying a marker, named by the anchor id the
//     heading holds;
//   - the table's rows are the features, each named by the link in its
//     first cell, with what the build does for it in its last cell;
//   - every other marker belongs to a feature, by lying inside the
//     feature's subsection or by a link to the feature in its own block,
//     the nearest paragraph, list item or table row around it.
//
// It refuses rather than answering empty when the document has no such
// table or no feature at all, so a document it cannot read is never one it
// passes.
func proposedMismatches(doc []byte) ([]string, error) {
	prd, err := RenderPRD(doc)
	if err != nil {
		return nil, err
	}
	table, err := prd.Section(3, proposedTableHeading)
	if err != nil {
		return nil, err
	}
	var problems []string

	features := map[string]Section{}
	var order []string
	marked := map[*xhtml.Node]bool{}
	for _, h := range prd.heads {
		var marker *xhtml.Node
		for _, kbd := range prd.Elements("kbd") {
			if within(kbd, h.node) && isProposedMarker(kbd) {
				marker = kbd
			}
		}
		if marker == nil {
			continue
		}
		marked[marker] = true
		id := anchorOf(h.node)
		if id == "" {
			problems = append(problems, fmt.Sprintf("the proposed heading %q has no anchor id for the table to link to", h.text))
			continue
		}
		section, err := prd.Section(h.level, h.text)
		if err != nil {
			return nil, err
		}
		features[id] = section
		order = append(order, id)
	}
	if len(features) == 0 {
		return nil, fmt.Errorf("the document marks no heading proposed")
	}

	rows := map[string]bool{}
	for _, tr := range table.Elements("tr") {
		cells := children(tr, "td")
		if len(cells) == 0 {
			continue
		}
		var ids []string
		for _, a := range prd.Find(func(n *xhtml.Node) bool { return n.Data == "a" && within(n, cells[0]) }) {
			if href, _ := Attr(a, "href"); strings.HasPrefix(href, "#") {
				ids = append(ids, strings.TrimPrefix(href, "#"))
			}
		}
		if len(ids) != 1 {
			problems = append(problems, fmt.Sprintf("the table row %q names %d features in its first cell, want one", Text(tr), len(ids)))
			continue
		}
		id := ids[0]
		switch {
		case rows[id]:
			problems = append(problems, fmt.Sprintf("the table lists #%s twice", id))
		case features[id].prd == nil:
			problems = append(problems, fmt.Sprintf("the table lists #%s, which is no heading marked proposed", id))
		case len(cells) < 2 || Text(cells[len(cells)-1]) == "":
			problems = append(problems, fmt.Sprintf("the table row for #%s does not say what this build does", id))
		}
		rows[id] = true
	}
	for _, id := range order {
		if !rows[id] {
			problems = append(problems, fmt.Sprintf("the heading #%s is marked proposed and the table has no row for it", id))
		}
	}

	for _, kbd := range prd.Elements("kbd") {
		if !isProposedMarker(kbd) || marked[kbd] {
			continue
		}
		if slices.ContainsFunc(order, func(id string) bool { return features[id].Holds(kbd) }) {
			continue
		}
		block := enclosing(kbd, "p", "li", "tr")
		if block == nil {
			problems = append(problems, "a proposed marker stands in no paragraph, list item or table row")
			continue
		}
		linked := prd.Find(func(n *xhtml.Node) bool {
			if n.Data != "a" || !within(n, block) {
				return false
			}
			href, _ := Attr(n, "href")
			return features[strings.TrimPrefix(href, "#")].prd != nil && strings.HasPrefix(href, "#")
		})
		if len(linked) == 0 {
			text := Text(block)
			if len(text) > 120 {
				text = text[:120] + "..."
			}
			problems = append(problems, fmt.Sprintf("a proposed marker links to no proposed feature: %q", text))
		}
	}
	return problems, nil
}

// anchorOf is the id of the first element inside heading that carries one.
func anchorOf(heading *xhtml.Node) string {
	var id string
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if id != "" {
			return
		}
		if n.Type == xhtml.ElementNode {
			if v, ok := Attr(n, "id"); ok && v != "" {
				id = v
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(heading)
	return id
}

// children are n's child elements named tag.
func children(n *xhtml.Node, tag string) []*xhtml.Node {
	var out []*xhtml.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode && c.Data == tag {
			out = append(out, c)
		}
	}
	return out
}

// within reports whether n lies inside ancestor.
func within(n, ancestor *xhtml.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p == ancestor {
			return true
		}
	}
	return false
}

// enclosing is the nearest element around n named one of tags.
func enclosing(n *xhtml.Node, tags ...string) *xhtml.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == xhtml.ElementNode && slices.Contains(tags, p.Data) {
			return p
		}
	}
	return nil
}

// readRealPRD is docs/prd.md as it stands in this tree.
func readRealPRD(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(PRDPath)))
	if err != nil {
		t.Fatalf("reading the PRD: %v", err)
	}
	return string(body)
}

// TestTheProposedFeaturesTableMatchesTheMarkers holds the PRD's table of
// proposed features to the proposed markers in the document as it renders:
// one row for every heading marked proposed and none for anything else,
// each saying what this build does for it, and every other marker
// belonging to one of those features by where it stands or what it links
// to. A reader then learns from the table what the build does for every
// marker without scanning the document for them.
func TestTheProposedFeaturesTableMatchesTheMarkers(t *testing.T) {
	problems, err := proposedMismatches([]byte(readRealPRD(t)))
	if err != nil {
		t.Fatalf("reading the PRD's proposed features: %v", err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// TestTheProposedFeaturesCheckFailsOnEachDrift is the positive control for
// the check above: each change below to the real PRD is drift the check
// exists to catch, and each is reported, so the check cannot pass by
// reading nothing. Cutting the table's first row, marking a new heading
// proposed with no row, putting a marker in a paragraph that links to no
// feature, and listing a row for a heading that is not proposed are each
// named; a document with no table is refused outright.
func TestTheProposedFeaturesCheckFailsOnEachDrift(t *testing.T) {
	real := readRealPRD(t)
	at := strings.Index(real, "\n### "+proposedTableHeading+"\n")
	if at < 0 {
		t.Fatalf("the PRD has no %q subsection to alter", proposedTableHeading)
	}
	tableStart := at + strings.Index(real[at:], "\n| ---")
	firstRow := tableStart + 1 + strings.Index(real[tableStart+1:], "\n") + 1
	rowEnd := firstRow + strings.Index(real[firstRow:], "\n") + 1
	if tableStart < at || firstRow <= tableStart || rowEnd <= firstRow {
		t.Fatal("the proposed features table has no first row to cut")
	}
	cut := real[:firstRow] + real[rowEnd:]
	strayHeading := real + "\n### <a id=\"stray\"></a>A stray proposal <kbd>proposed</kbd>\n\nText.\n"
	strayMarker := real + "\n<kbd>proposed</kbd> A sentence pointing at nothing.\n"
	notProposed := real[:rowEnd] + "| [Principles](#principles) | none | Nothing. |\n" + real[rowEnd:]
	for _, c := range []struct{ name, doc, says string }{
		{"a row cut", cut, "the table has no row for it"},
		{"a proposed heading with no row", strayHeading, "#stray is marked proposed and the table has no row"},
		{"a marker linking nowhere", strayMarker, "links to no proposed feature"},
		{"a row for a heading not proposed", notProposed, "#principles, which is no heading marked proposed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			problems, err := proposedMismatches([]byte(c.doc))
			if err != nil {
				t.Fatalf("the check refused the altered document: %v", err)
			}
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, c.says) }) {
				t.Fatalf("the check reported %q, want one saying %q", problems, c.says)
			}
		})
	}
	t.Run("no table", func(t *testing.T) {
		if _, err := proposedMismatches([]byte(strings.Replace(real, "### "+proposedTableHeading, "### Something else", 1))); err == nil {
			t.Fatal("the check answered a document with no table instead of refusing it")
		}
	})
}
