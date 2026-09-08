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

// prdStageRow matches one row of that table: the number, then the stage's name
// in bold. The table is the PRD's own rendering and this reads it as written.
var prdStageRow = regexp.MustCompile(`<td class="prd-num">(\d+)</td>\s*<td><b>([^<]+)</b></td>`)

// prdStagesHeading is where the read of section 5's stage table begins. It
// fixes where reading starts and buys nothing else today: this document holds
// no numbered row before it, so slicing there excludes nothing, and the offset
// could only ever exclude a table that came earlier.
//
// What tells a stage row from any other numbered row is the shape prdStageRow
// requires, where the bold name is the whole of its cell. The PRD's other
// numbered table writes prose around its bold runs, so none of its rows
// matches. That is how that table happens to be written rather than anything
// enforced here, and StagesFromPRD names what it leaves open.
const prdStagesHeading = "The nine stages"

// StagesFromPRD returns the stages PRD section 5's table names, in the order it
// numbers them.
//
// The PRD owns the list. This reads it rather than restating it, so a stage
// added, removed or reordered there is a change this harness sees rather than
// one it silently agrees with, which is the difference between measuring the
// product and following it.
//
// It refuses a document whose table it cannot find, whose numbering is not one
// through however many rows it holds, or which holds no rows at all.
//
// The residual gap is that nothing here bounds the read to one table. A row
// elsewhere in the document that matched prdStageRow's shape would be read as
// a further row of this one, and what catches it is the numbering rather than
// the position: such a row restarts at one where the sequence wants the next
// number, so the document is refused rather than quietly read long.
func StagesFromPRD(html []byte) ([]string, error) {
	at := strings.Index(string(html), prdStagesHeading)
	if at < 0 {
		return nil, fmt.Errorf("%w: no %q in the document", ErrPRDStages, prdStagesHeading)
	}
	rows := prdStageRow.FindAllStringSubmatch(string(html)[at:], -1)
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: the table under %q has no rows this reads", ErrPRDStages, prdStagesHeading)
	}
	named := make([]string, 0, len(rows))
	for i, row := range rows {
		number, err := strconv.Atoi(row[1])
		if err != nil {
			return nil, fmt.Errorf("%w: row %d is numbered %q", ErrPRDStages, i+1, row[1])
		}
		if number != i+1 {
			// A gap or a repeat means the rows this read are not the whole
			// table in its own order, and a list that is nearly the PRD's is
			// worse than none: everything below would compare against it.
			return nil, fmt.Errorf("%w: row %d is numbered %d, so the rows read are not the table in order",
				ErrPRDStages, i+1, number)
		}
		name := strings.TrimSpace(row[2])
		if name == "" {
			return nil, fmt.Errorf("%w: row %d names no stage", ErrPRDStages, i+1)
		}
		named = append(named, name)
	}
	return named, nil
}

// PRDStages reads the stage list out of the PRD under a module root.
func PRDStages(root string) ([]string, error) {
	path := filepath.Join(root, filepath.FromSlash(principles.PRDPath))
	html, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", ErrPRDStages, path, err)
	}
	return StagesFromPRD(html)
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
// for, named one at a time.
//
// Naming them is the whole point. Subtracting what the build implements from
// the stage list would make this harness agree with the build about which
// stages it may skip, so a build that quietly stopped implementing a stage
// would be followed rather than caught; that is how this harness became an
// eight-boundary harness in the middle of a run and reported success. A name
// here has to be written by somebody, and DeclaresEveryStage goes red when the
// names and the build stop matching in either direction.
var bodyless = []pipeline.Stage{
	pipeline.StageRebase,
	pipeline.StageReview,
	pipeline.StageTest,
	pipeline.StageDocument,
	pipeline.StageLint,
	pipeline.StagePush,
	pipeline.StagePR,
	pipeline.StageCI,
}

// StagesWithoutABody returns the stages declared above, in the order a run
// takes them. It is a declaration, never a measurement.
func StagesWithoutABody() []pipeline.Stage {
	out := slices.Clone(bodyless)
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
