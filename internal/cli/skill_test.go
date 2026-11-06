package cli_test

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	xhtml "golang.org/x/net/html"
)

// skillDir is the coordinator skill's directory, relative to this package.
var skillDir = filepath.Join("..", "..", "skills", "coordinator")

// skillCoreLines is the most lines the coordinator skill's core may hold.
// The core is what a coordinating agent holds every turn; everything else
// is a reference file it reads when the core's trigger for it applies.
const skillCoreLines = 150

// readSkillCore reads the core and refuses a file that is not the
// coordinator skill, so a moved or emptied file fails rather than measuring
// nothing.
func readSkillCore(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("reading the coordinator skill: %v", err)
	}
	core := string(body)
	if !strings.HasPrefix(core, "---\nname: coordinator\n") {
		t.Fatalf("skills/coordinator/SKILL.md does not open with the coordinator skill's frontmatter:\n%.200s", core)
	}
	return core
}

// TestTheCoordinatorSkillCoreFitsInOneHundredFiftyLines holds the core the
// agent holds every turn to skillCoreLines lines, counted as wc -l counts
// them plus a last line with no newline. A core that grew past it is
// split: a rule moves to the reference file whose trigger it belongs to,
// or, when the program enforces it, shrinks to the refusal that does.
func TestTheCoordinatorSkillCoreFitsInOneHundredFiftyLines(t *testing.T) {
	core := readSkillCore(t)
	lines := strings.Count(core, "\n")
	if !strings.HasSuffix(core, "\n") {
		lines++
	}
	if lines > skillCoreLines {
		t.Fatalf("skills/coordinator/SKILL.md is %d lines, over the core's %d; move what only some turns need to a reference file", lines, skillCoreLines)
	}
}

// TestEveryCoordinatorSkillReferenceIsNamedFromTheCoreWithATrigger holds
// the core to naming every file under references/ in a table row that says
// when to read it, and to naming no file that is not there. The program
// loads no skill: a reference reaches the agent only when the agent reads
// it because the core told it to, so a file the core does not name is a
// rule no agent is told, and a name with no file is a trigger that leads
// nowhere. The table is read as the core renders, so a row inside a code
// block or an HTML comment names nothing.
func TestEveryCoordinatorSkillReferenceIsNamedFromTheCoreWithATrigger(t *testing.T) {
	core := readSkillCore(t)
	files, err := filepath.Glob(filepath.Join(skillDir, "references", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	var present []string
	for _, f := range files {
		present = append(present, "references/"+filepath.Base(f))
	}
	if len(present) == 0 {
		t.Fatal("skills/coordinator/references holds no reference file, so the core carries every rule itself")
	}
	rendered, err := principles.RenderPRD([]byte(core))
	if err != nil {
		t.Fatal(err)
	}
	triggers := map[string]string{}
	for _, row := range rendered.Elements("tr") {
		var cells []*xhtml.Node
		for c := row.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == xhtml.ElementNode && c.Data == "td" {
				cells = append(cells, c)
			}
		}
		if len(cells) < 2 {
			continue
		}
		for _, link := range rendered.Find(func(n *xhtml.Node) bool { return n.Data == "a" && within(n, cells[0]) }) {
			href, _ := principles.Attr(link, "href")
			if strings.HasPrefix(href, "references/") {
				triggers[path.Clean(href)] = principles.Text(cells[1])
			}
		}
	}
	for _, name := range present {
		trigger, named := triggers[name]
		switch {
		case !named:
			t.Errorf("the core names %s in no table row, so no agent is told to read it", name)
		case trigger == "":
			t.Errorf("the core names %s with no trigger beside it", name)
		}
	}
	for name := range triggers {
		if !slices.Contains(present, name) {
			t.Errorf("the core names %s, which is not a file in skills/coordinator/references", name)
		}
	}
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
