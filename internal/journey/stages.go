package journey

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"

	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// ErrPRDStages reports that PRD section 5's stage table could not be read, so
// no stage list could be built from it.
//
// It is a refusal rather than a short list for the reason internal/principles
// gives for its own: a list nobody could read comes back empty, and an empty
// list lets every check built on it pass while checking nothing. That is the
// failure this whole construction exists against, so it may not be the way the
// construction itself fails.
var ErrPRDStages = errors.New("journey: PRD section 5's stage table could not be read")

// prdStageNumber is the shape of a stage row's first cell.
var prdStageNumber = regexp.MustCompile(`^[0-9]+$`)

// prdStagesHeading is the heading of the subsection of PRD section 5 that
// holds the stage table, read at level three through internal/principles'
// PRD.Section, which owns where a section of the PRD begins and ends.
const prdStagesHeading = "The nine stages"

// StagesFromPRD returns the stages PRD section 5's table names, in the order it
// numbers them.
//
// The PRD owns the list. This reads it rather than restating it, so a stage
// added, removed or reordered there is a change this harness sees rather than
// one it silently agrees with, which is the difference between measuring the
// product and following it.
//
// The table is read as it renders, through internal/principles' RenderPRD: the
// one table the subsection headed "The nine stages" holds, every body row of
// it, and each row's cells as the renderer built them. So whatever Markdown
// allows a row to be spelled as, a row is read exactly when it renders as one,
// and a table that does not render, as one whose delimiter row does not match
// its header, is no table. Every body row has to be a stage row: its first
// cell a number, and its second cell holding the stage's name in bold and
// nothing else. A row that is not one is a refusal, never a row left out,
// because a list one row short of the table is a list that agrees with a build
// the table no longer describes.
//
// It refuses a document whose subsection it cannot delimit, one with no table
// or more than one in that subsection, a table with no body rows, a row it
// cannot read, and numbering that is not one through however many rows the
// table holds.
//
// What it leaves open is what RenderPRD leaves open, which its comment states,
// and the names themselves: a name is taken as written, and what holds it to
// the build is AgreesWithPRD and its mapping table.
func StagesFromPRD(doc []byte) ([]string, error) {
	prd, err := principles.RenderPRD(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPRDStages, err)
	}
	section, err := prd.Section(3, prdStagesHeading)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPRDStages, err)
	}
	tables := section.Elements("table")
	switch len(tables) {
	case 0:
		return nil, fmt.Errorf("%w: the %q subsection holds no table", ErrPRDStages, prdStagesHeading)
	case 1:
	default:
		return nil, fmt.Errorf("%w: the %q subsection holds %d tables, and this reads one", ErrPRDStages, prdStagesHeading, len(tables))
	}
	var rows []*xhtml.Node
	for _, tr := range section.Elements("tr") {
		if inHead(tr) {
			continue
		}
		rows = append(rows, tr)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: the table under %q has no rows this reads", ErrPRDStages, prdStagesHeading)
	}
	named := make([]string, 0, len(rows))
	for i, tr := range rows {
		cells := children(tr, "td")
		if len(cells) < 2 {
			return nil, fmt.Errorf("%w: row %d renders %d cell(s), so it has no stage name to read: %s",
				ErrPRDStages, i+1, len(cells), principles.Text(tr))
		}
		numberText := principles.Text(cells[0])
		if !prdStageNumber.MatchString(numberText) {
			return nil, fmt.Errorf("%w: row %d is not a stage row, a number then the stage's name in bold as the whole cell: %s",
				ErrPRDStages, i+1, principles.Text(tr))
		}
		number, err := strconv.Atoi(numberText)
		if err != nil {
			return nil, fmt.Errorf("%w: row %d is numbered %q", ErrPRDStages, i+1, numberText)
		}
		if number != i+1 {
			// A gap or a repeat means the rows this read are not the whole
			// table in its own order, and a list that is nearly the PRD's is
			// worse than none: everything below would compare against it.
			return nil, fmt.Errorf("%w: row %d is numbered %d, so the rows read are not the table in order",
				ErrPRDStages, i+1, number)
		}
		bold, ok := soleStrong(cells[1])
		if !ok {
			return nil, fmt.Errorf("%w: row %d is not a stage row, a number then the stage's name in bold as the whole cell: %s",
				ErrPRDStages, i+1, principles.Text(tr))
		}
		name := principles.Text(bold)
		if name == "" {
			return nil, fmt.Errorf("%w: row %d names no stage", ErrPRDStages, i+1)
		}
		named = append(named, name)
	}
	return named, nil
}

// inHead reports whether a table row is in its table's header.
func inHead(tr *xhtml.Node) bool {
	for n := tr.Parent; n != nil; n = n.Parent {
		if n.Type == xhtml.ElementNode {
			switch n.Data {
			case "thead":
				return true
			case "table":
				return false
			}
		}
	}
	return false
}

// children returns n's element children named tag.
func children(n *xhtml.Node, tag string) []*xhtml.Node {
	var out []*xhtml.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode && c.Data == tag {
			out = append(out, c)
		}
	}
	return out
}

// soleStrong returns the one strong element a cell holds when it holds that
// and nothing else but whitespace, which is what bold as the whole cell
// renders to.
func soleStrong(cell *xhtml.Node) (*xhtml.Node, bool) {
	var bold *xhtml.Node
	for c := cell.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == xhtml.TextNode && strings.TrimSpace(c.Data) == "":
		case c.Type == xhtml.ElementNode && c.Data == "strong" && bold == nil:
			bold = c
		default:
			return nil, false
		}
	}
	return bold, bold != nil
}

// PRDStages reads the stage list out of the PRD under a module root.
func PRDStages(root string) ([]string, error) {
	path := filepath.Join(root, filepath.FromSlash(principles.PRDPath))
	doc, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", ErrPRDStages, path, err)
	}
	return StagesFromPRD(doc)
}

// spelling is one stage under both the name PRD section 5 gives it and the name
// internal/pipeline gives it.
//
// Two of the nine differ, which internal/pipeline/doc.go states and owns: the
// PRD calls them "Pull request" and "Checks" and the short names are what
// appear in node names, state keys and reports. The table is total rather than
// holding only those two, so a rename on either side is caught here instead of
// silently matching through a fallback.
type spelling struct {
	prd   string
	stage pipeline.Stage
}

// spellings is the whole mapping, in the order both sides put the stages in.
var spellings = []spelling{
	{"Intent", pipeline.StageIntent},
	{"Rebase", pipeline.StageRebase},
	{"Review", pipeline.StageReview},
	{"Test", pipeline.StageTest},
	{"Document", pipeline.StageDocument},
	{"Lint", pipeline.StageLint},
	{"Push", pipeline.StagePush},
	{"Pull request", pipeline.StagePR},
	{"Checks", pipeline.StageCI},
}

// AgreesWithPRD reports how a stage order disagrees with the list PRD section 5
// names, and nil when they are the same stages in the same order.
//
// Both directions are compared against the mapping table above, so a stage the
// PRD names that no order carries, a stage an order carries that the PRD does
// not name, and the same stages in another order are all reported. It takes
// both lists as arguments rather than reading either, so the disagreements it
// is supposed to catch can be handed to it and watched being caught.
func AgreesWithPRD(prd []string, order []pipeline.Stage) error {
	if len(prd) != len(spellings) {
		return fmt.Errorf("the PRD names %d stages and this harness maps %d; the PRD owns the list, so "+
			"reconcile the mapping with it: %v", len(prd), len(spellings), prd)
	}
	if len(order) != len(spellings) {
		return fmt.Errorf("the stage order carries %d stages and the PRD names %d: %v",
			len(order), len(prd), order)
	}
	for i, want := range spellings {
		if prd[i] != want.prd {
			return fmt.Errorf("the PRD's stage %d is %q and this harness maps %q there",
				i+1, prd[i], want.prd)
		}
		if order[i] != want.stage {
			return fmt.Errorf("the stage order's position %d is %q and the PRD names %q there, which this "+
				"harness maps to %q", i+1, order[i], prd[i], want.stage)
		}
	}
	return nil
}

// ErrUndeclaredStage reports that the stages this build implements and the
// stages this harness declares body-less do not account for each other.
var ErrUndeclaredStage = errors.New("journey: a stage's implementation status is not declared here")

// bodyless is the stages this harness declares this build has no implementation
// for, named one at a time. This build implements all nine, so the list is
// empty; it stays a declaration rather than being deleted, because a build
// that quietly stopped implementing a stage is exactly what an absent
// declaration could not catch.
//
// Naming them is the whole point. Subtracting what the build implements from
// the stage list would make this harness agree with the build about which
// stages it may skip, so a build that quietly stopped implementing a stage
// would be followed rather than caught; that is how this harness became an
// eight-boundary harness in the middle of a run and reported success. A name
// here has to be written by somebody, and DeclaresEveryStage goes red when the
// names and the build stop matching in either direction.
var bodyless = []pipeline.Stage{}

// StagesWithoutABody returns the stages declared above, in the order a run
// takes them. It is a declaration, never a measurement.
func StagesWithoutABody() []pipeline.Stage {
	out := slices.Clone(bodyless)
	slices.SortFunc(out, func(a, b pipeline.Stage) int {
		return slices.Index(pipeline.Order(), a) - slices.Index(pipeline.Order(), b)
	})
	return out
}

// stops is the stages this harness declares a run it drives stops at, named
// one at a time for the reason bodyless is: an expectation subtracted from
// what the build does would follow the build wherever it went.
//
// It is not the bodyless list under another name, because having a body and
// holding are different questions, in both directions. The test, document and
// lint stages hold here with bodies: PRD section 10 makes each of their
// commands empty by default, no run this harness drives is given one, and the
// branch's own is never read, so each body holds for a person rather than
// reporting a pass it did not establish. The push stage holds with a body
// because every run here that reaches it skipped the review stage, but for
// the three walks that take it to the push stage's decision, and a push whose
// run carries no completed review is an ask rather than a push. The review
// stage has a body and is not a stop, because every run this harness walks
// but those three skips it, for the reason walkableRun states: its body
// launches the resolved agent over the change, and a harness run may not
// reach whatever agent the machine happens to have. The one run a task's
// worker starts by pushing takes the review stage with the stand-in scripted
// clean, and no answer walks it past the first hold it reaches, so it is not
// walked against this list; nor are the usage ceiling walk's runs, whose
// control takes the review stage the same way and whose held run stops at
// review on the ceiling. The pull request and checks stages are off the
// list because a local upstream sets no_code_host at begin (#283), so both
// skip after push rather than holding: a run that asked to skip only the
// pull request would still never stop at checks here.
//
// What holds these names to the build is the checks that walk a real run
// against them: the kill-at-every-boundary check and the classification walk
// compare the stops a run actually made with this list's length, and the
// contention check refuses a list whose stops do not run consecutively. A
// name here a run does not stop at, or a stop no name here covers, is a red
// check rather than a quiet drift.
var stops = []pipeline.Stage{
	pipeline.StageTest,
	pipeline.StageDocument,
	pipeline.StageLint,
	pipeline.StagePush,
}

// StagesARunStopsAt returns the stages declared above, in the order a run
// takes them. It is a declaration, never a measurement.
func StagesARunStopsAt() []pipeline.Stage {
	out := slices.Clone(stops)
	slices.SortFunc(out, func(a, b pipeline.Stage) int {
		return slices.Index(pipeline.Order(), a) - slices.Index(pipeline.Order(), b)
	})
	return out
}

// DeclaresEveryStage reports how a body-less declaration and the build
// disagree, and nil when every stage is accounted for exactly once.
//
// Both lists are arguments for one reason: a disagreement of either kind can
// then be handed to this and watched being caught, which is what keeps a
// branch here from being one nobody has shown can fire. implemented is what the
// build says it has bodies for and declared is the declaration above, so a
// caller answering for this build passes Implemented and StagesWithoutABody;
// passing anything else for the second compares the build against a list
// nobody wrote down.
func DeclaresEveryStage(implemented, declared []pipeline.Stage) error {
	for _, stage := range pipeline.Order() {
		hasBody := slices.Contains(implemented, stage)
		isDeclared := slices.Contains(declared, stage)
		switch {
		case hasBody && isDeclared:
			return fmt.Errorf("%w: %s has an implementation and is declared body-less here; a stage that "+
				"gained a body is one this harness has to be told about, because what it skips for a "+
				"body-less stage is no longer right for it", ErrUndeclaredStage, stage)
		case !hasBody && !isDeclared:
			return fmt.Errorf("%w: %s has no implementation and is not declared body-less here; naming it "+
				"is what stops this harness quietly following the build", ErrUndeclaredStage, stage)
		}
	}
	for _, stage := range declared {
		if !slices.Contains(pipeline.Order(), stage) {
			return fmt.Errorf("%w: %s is declared body-less here and is not one of the stages",
				ErrUndeclaredStage, stage)
		}
	}
	return nil
}

// Implemented is what the build says it has stage bodies for. It is a thin read
// of internal/stages, here so a test can name the real answer and a counterfeit
// of it in the same terms.
func Implemented() []pipeline.Stage { return stages.Implemented() }
