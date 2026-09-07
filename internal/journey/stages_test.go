package journey_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
)

// TestTheStageListComesFromThePRDRatherThanFromTheBuild is the check that makes
// every other check here capable of failing when the build is wrong.
//
// A harness that derived the stages from the build could only ever fail when
// the build disagreed with itself. This one reads PRD section 5's table, which
// is the owner of the list, and holds internal/pipeline's order to it; and it
// requires a stage with no implementation to be named here rather than
// subtracted from the list, because a silent subtraction is how this harness
// became an eight-boundary harness in the middle of a run and reported success.
//
// The three positive controls are part of the test rather than something run
// once by hand: each hands the comparison a disagreement it is supposed to
// catch and fails if it is accepted.
func TestTheStageListComesFromThePRDRatherThanFromTheBuild(t *testing.T) {
	principles.Cite(t, principles.P2)

	root := moduleRoot(t)
	prd, err := journey.PRDStages(root)
	if err != nil {
		t.Fatalf("reading the stage list out of the PRD: %v", err)
	}
	if len(prd) == 0 {
		t.Fatal("the PRD yielded no stages, and an empty list would let every check built on it pass")
	}
	t.Logf("PRD section 5 names %d stages: %s", len(prd), strings.Join(prd, ", "))

	if err := journey.AgreesWithPRD(prd, pipeline.Order()); err != nil {
		t.Fatalf("the product's stage order and the PRD's list disagree: %v", err)
	}
	if err := journey.DeclaresEveryStage(journey.Implemented()); err != nil {
		t.Fatalf("%v", err)
	}

	// Positive control one: a stage removed from the PRD's table must go red.
	t.Run("a stage deleted from the PRD table is caught", func(t *testing.T) {
		short := slices.Delete(slices.Clone(prd), 4, 5)
		if err := journey.AgreesWithPRD(short, pipeline.Order()); err == nil {
			t.Fatal("a PRD list missing a stage was accepted, so this harness would follow a PRD that " +
				"lost one rather than reporting it")
		} else {
			t.Logf("control 1 red as required: %v", err)
		}
	})

	// Positive control two: a stage removed from the product's order must go
	// red. The order is a compiled table, so the disagreement is handed to the
	// comparison rather than compiled in; what is being controlled is whether
	// the comparison catches it.
	t.Run("a stage deleted from pipeline.Order is caught", func(t *testing.T) {
		short := slices.Delete(slices.Clone(pipeline.Order()), 4, 5)
		if err := journey.AgreesWithPRD(prd, short); err == nil {
			t.Fatal("a stage order missing a stage was accepted against the full PRD list, which is the " +
				"defect this construction exists for")
		} else {
			t.Logf("control 2 red as required: %v", err)
		}
	})

	// Positive control three: a stage that becomes body-less, or gains a body,
	// must require the declaration to be updated rather than being absorbed.
	t.Run("an undeclared change in which stages have bodies is caught", func(t *testing.T) {
		gained := append(slices.Clone(journey.Implemented()), journey.StagesWithoutABody()[0])
		if err := journey.DeclaresEveryStage(gained); err == nil {
			t.Fatal("a stage that gained a body while still declared body-less was accepted")
		} else if !errors.Is(err, journey.ErrUndeclaredStage) {
			t.Fatalf("caught for the wrong reason: %v", err)
		} else {
			t.Logf("control 3a red as required: %v", err)
		}

		lost := slices.Clone(journey.Implemented())
		if len(lost) == 0 {
			t.Skip("this build implements no stage, so none can be taken away")
		}
		if err := journey.DeclaresEveryStage(lost[:len(lost)-1]); err == nil {
			t.Fatal("a stage that lost its body without being declared was accepted, which is the silent " +
				"subtraction this construction exists to stop")
		} else if !errors.Is(err, journey.ErrUndeclaredStage) {
			t.Fatalf("caught for the wrong reason: %v", err)
		} else {
			t.Logf("control 3b red as required: %v", err)
		}
	})

	// And the reader itself: a PRD it cannot read has to refuse rather than
	// yield a short list, on internal/principles' own terms.
	t.Run("an unreadable PRD refuses rather than yielding nothing", func(t *testing.T) {
		for _, c := range []struct{ what, html string }{
			{"no stage table at all", "<html><body><p>nothing here</p></body></html>"},
			{"a table whose numbering skips", `The nine stages<td class="prd-num">1</td><td><b>Intent</b></td>` +
				`<td class="prd-num">3</td><td><b>Review</b></td>`},
			{"a row naming no stage", `The nine stages<td class="prd-num">1</td><td><b></b></td>`},
		} {
			if _, err := journey.StagesFromPRD([]byte(c.html)); err == nil {
				t.Fatalf("%s was read as a stage list", c.what)
			} else if !errors.Is(err, journey.ErrPRDStages) {
				t.Fatalf("%s failed for the wrong reason: %v", c.what, err)
			}
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(principles.PRDPath)))
		if err != nil {
			t.Fatalf("reading the PRD: %v", err)
		}
		if _, err := journey.StagesFromPRD(body); err != nil {
			t.Fatalf("the real PRD was refused: %v", err)
		}
	})
}
