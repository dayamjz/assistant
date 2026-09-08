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

	// Positive control one: a stage removed from the PRD's table must go red,
	// and for being a PRD list of the wrong length rather than for anything
	// about the order it is compared against. A control that went red for the
	// other reason would establish what control two establishes.
	t.Run("a stage deleted from the PRD table is caught", func(t *testing.T) {
		short := slices.Delete(slices.Clone(prd), 4, 5)
		err := journey.AgreesWithPRD(short, pipeline.Order())
		if err == nil {
			t.Fatal("a PRD list missing a stage was accepted, so this harness would follow a PRD that " +
				"lost one rather than reporting it")
		}
		if !strings.Contains(err.Error(), "the PRD names 8 stages") {
			t.Fatalf("caught, but not for the PRD's own list being short: %v", err)
		}
		t.Logf("control 1 red as required: %v", err)
	})

	// Positive control two: a stage removed from the product's order must go
	// red. The order is a compiled table, so the disagreement is handed to the
	// comparison rather than compiled in; what is being controlled is whether
	// the comparison catches it.
	t.Run("a stage deleted from pipeline.Order is caught", func(t *testing.T) {
		short := slices.Delete(slices.Clone(pipeline.Order()), 4, 5)
		err := journey.AgreesWithPRD(prd, short)
		if err == nil {
			t.Fatal("a stage order missing a stage was accepted against the full PRD list, which is the " +
				"defect this construction exists for")
		}
		if !strings.Contains(err.Error(), "the stage order carries 8 stages") {
			t.Fatalf("caught, but not for the order being short: %v", err)
		}
		t.Logf("control 2 red as required: %v", err)
	})

	// Positive control three: a stage that becomes body-less, or gains a body,
	// must require the declaration to be updated rather than being absorbed.
	t.Run("an undeclared change in which stages have bodies is caught", func(t *testing.T) {
		gained := append(slices.Clone(journey.Implemented()), journey.StagesWithoutABody()[0])
		err := journey.DeclaresEveryStage(gained)
		if err == nil {
			t.Fatal("a stage that gained a body while still declared body-less was accepted")
		}
		if !errors.Is(err, journey.ErrUndeclaredStage) {
			t.Fatalf("caught for the wrong reason: %v", err)
		}
		if !strings.Contains(err.Error(), "has an implementation and is declared body-less here") {
			t.Fatalf("caught, but not for the direction this control names: %v", err)
		}
		t.Logf("control 3a red as required: %v", err)

		lost := slices.Clone(journey.Implemented())
		if len(lost) == 0 {
			t.Skip("this build implements no stage, so none can be taken away")
		}
		err = journey.DeclaresEveryStage(lost[:len(lost)-1])
		if err == nil {
			t.Fatal("a stage that lost its body without being declared was accepted, which is the silent " +
				"subtraction this construction exists to stop")
		}
		if !errors.Is(err, journey.ErrUndeclaredStage) {
			t.Fatalf("caught for the wrong reason: %v", err)
		}
		if !strings.Contains(err.Error(), "has no implementation and is not declared body-less here") {
			t.Fatalf("caught, but not for the direction this control names: %v", err)
		}
		t.Logf("control 3b red as required: %v", err)
	})

	// And the reader itself: a PRD it cannot read has to refuse rather than
	// yield a short list, on internal/principles' own terms.
	//
	// One document per way it refuses, and each says what it must be refused
	// with rather than only that the sentinel came back. The sentinel is one
	// value on every path, so a document that missed the branch it was written
	// for would still be refused and the case would still pass while controlling
	// nothing: the empty-name case did exactly that, matching no row at all
	// because the row pattern needs a character between the tags, so it was
	// refused for holding no rows and the empty-name branch had no control.
	t.Run("an unreadable PRD refuses rather than yielding nothing", func(t *testing.T) {
		for _, c := range []struct{ what, html, says string }{
			{"no stage table at all", "<html><body><p>nothing here</p></body></html>",
				`no "The nine stages" in the document`},
			{"a heading with no rows under it", "The nine stages<p>prose and no table</p>",
				"has no rows this reads"},
			{"a row numbered past what a number holds",
				`The nine stages<td class="prd-num">99999999999999999999</td><td><b>Intent</b></td>`,
				`row 1 is numbered "99999999999999999999"`},
			{"a table whose numbering skips", `The nine stages<td class="prd-num">1</td><td><b>Intent</b></td>` +
				`<td class="prd-num">3</td><td><b>Review</b></td>`,
				"row 2 is numbered 3"},
			{"a row naming no stage", `The nine stages<td class="prd-num">1</td><td><b> </b></td>`,
				"row 1 names no stage"},
		} {
			_, err := journey.StagesFromPRD([]byte(c.html))
			if err == nil {
				t.Fatalf("%s was read as a stage list", c.what)
			}
			if !errors.Is(err, journey.ErrPRDStages) {
				t.Fatalf("%s failed for the wrong reason: %v", c.what, err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Fatalf("%s was refused, but not by the branch it is written for; wanted a refusal saying "+
					"%q and got: %v", c.what, c.says, err)
			}
			t.Logf("%s refused as required: %v", c.what, err)
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
