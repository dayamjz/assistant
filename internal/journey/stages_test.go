package journey_test

import (
	"errors"
	"fmt"
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
// The positive controls are part of the test rather than something run once by
// hand: each hands a comparison a disagreement it is supposed to catch, fails
// if it is accepted, and says which disagreement came back, so no two of them
// establish the same thing. Between them they reach every disagreement
// AgreesWithPRD and DeclaresEveryStage can report, because a comparison with a
// branch nobody has shown can fire is the defect this package refuses in the
// checks it runs and may not ship in the construction underneath them.
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
	if err := journey.DeclaresEveryStage(journey.Implemented(), journey.StagesWithoutABody()); err != nil {
		t.Fatalf("%v", err)
	}
	if err := journey.DeclaresEveryHold(journey.Implemented(), journey.StagesWithoutABody(),
		journey.StagesARunHoldsAt()); err != nil {
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
		says := fmt.Sprintf("the PRD names %d stages", len(short))
		if !strings.Contains(err.Error(), says) {
			t.Fatalf("caught, but not for the PRD's own list being short; wanted a refusal saying %q "+
				"and got: %v", says, err)
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
		says := fmt.Sprintf("the stage order carries %d stages", len(short))
		if !strings.Contains(err.Error(), says) {
			t.Fatalf("caught, but not for the order being short; wanted a refusal saying %q and got: %v",
				says, err)
		}
		t.Logf("control 2 red as required: %v", err)
	})

	// Positive controls three and four are the third disagreement AgreesWithPRD
	// promises, once from each side. Neither changes a length, so a comparison
	// that only counted would accept both, and the two are separate controls
	// because the position the PRD names and the position the order carries are
	// separate branches: one control reaching either would leave the other a
	// branch nobody has shown can fire.
	t.Run("two stages swapped in the PRD table is caught", func(t *testing.T) {
		const at = 0
		swapped := slices.Clone(prd)
		swapped[at], swapped[at+1] = swapped[at+1], swapped[at]
		err := journey.AgreesWithPRD(swapped, pipeline.Order())
		if err == nil {
			t.Fatal("the PRD's own stages in another order were accepted, so this harness would follow a " +
				"PRD that reordered them rather than reporting it")
		}
		says := fmt.Sprintf("the PRD's stage %d is %q and this harness maps %q there",
			at+1, swapped[at], prd[at])
		if !strings.Contains(err.Error(), says) {
			t.Fatalf("caught, but not for the PRD naming another stage at that position; wanted a refusal "+
				"saying %q and got: %v", says, err)
		}
		t.Logf("control 3 red as required: %v", err)
	})

	t.Run("two stages swapped in pipeline.Order is caught", func(t *testing.T) {
		const at = 0
		swapped := slices.Clone(pipeline.Order())
		swapped[at], swapped[at+1] = swapped[at+1], swapped[at]
		err := journey.AgreesWithPRD(prd, swapped)
		if err == nil {
			t.Fatal("a stage order carrying the PRD's stages in another order was accepted, which is P2's " +
				"whole content")
		}
		says := fmt.Sprintf("the stage order's position %d is %q and the PRD names %q there",
			at+1, swapped[at], prd[at])
		if !strings.Contains(err.Error(), says) {
			t.Fatalf("caught, but not for the order carrying another stage at that position; wanted a "+
				"refusal saying %q and got: %v", says, err)
		}
		t.Logf("control 4 red as required: %v", err)
	})

	// Positive control five: a stage that becomes body-less, or gains a body,
	// must require the declaration to be updated rather than being absorbed; and
	// a declaration naming something that is not a stage must be refused. The
	// third is why DeclaresEveryStage takes the declaration rather than reading
	// it: a branch over a list only the package can see is one no control can
	// reach.
	declared := journey.StagesWithoutABody()
	t.Run("an undeclared change in which stages have bodies is caught", func(t *testing.T) {
		gained := append(slices.Clone(journey.Implemented()), declared[0])
		err := journey.DeclaresEveryStage(gained, declared)
		if err == nil {
			t.Fatal("a stage that gained a body while still declared body-less was accepted")
		}
		if !errors.Is(err, journey.ErrUndeclaredStage) {
			t.Fatalf("caught for the wrong reason: %v", err)
		}
		says := fmt.Sprintf("%s has an implementation and is declared body-less here", declared[0])
		if !strings.Contains(err.Error(), says) {
			t.Fatalf("caught, but not for the direction this control names; wanted a refusal saying %q "+
				"and got: %v", says, err)
		}
		t.Logf("control 5a red as required: %v", err)

		lost := slices.Clone(journey.Implemented())
		if len(lost) == 0 {
			t.Skip("this build implements no stage, so none can be taken away")
		}
		taken := lost[len(lost)-1]
		err = journey.DeclaresEveryStage(lost[:len(lost)-1], declared)
		if err == nil {
			t.Fatal("a stage that lost its body without being declared was accepted, which is the silent " +
				"subtraction this construction exists to stop")
		}
		if !errors.Is(err, journey.ErrUndeclaredStage) {
			t.Fatalf("caught for the wrong reason: %v", err)
		}
		says = fmt.Sprintf("%s has no implementation and is not declared body-less here", taken)
		if !strings.Contains(err.Error(), says) {
			t.Fatalf("caught, but not for the direction this control names; wanted a refusal saying %q "+
				"and got: %v", says, err)
		}
		t.Logf("control 5b red as required: %v", err)
	})

	t.Run("a declaration naming something that is not a stage is caught", func(t *testing.T) {
		notAStage := pipeline.StageInvalid
		if slices.Contains(pipeline.Order(), notAStage) {
			t.Fatalf("%s is one of the stages now, so handing it to the declaration controls nothing",
				notAStage)
		}
		err := journey.DeclaresEveryStage(journey.Implemented(), append(slices.Clone(declared), notAStage))
		if err == nil {
			t.Fatal("a declaration naming something that is not a stage was accepted, so a name no run " +
				"could ever reach would sit in the list looking like coverage")
		}
		if !errors.Is(err, journey.ErrUndeclaredStage) {
			t.Fatalf("caught for the wrong reason: %v", err)
		}
		says := fmt.Sprintf("%s is declared body-less here and is not one of the stages", notAStage)
		if !strings.Contains(err.Error(), says) {
			t.Fatalf("caught, but not for the name being no stage; wanted a refusal saying %q and got: %v",
				says, err)
		}
		t.Logf("control 5c red as required: %v", err)
	})

	// Positive controls six are DeclaresEveryHold's three branches, each handed
	// the disagreement it exists to catch. What no control here can reach is an
	// implemented stage declared holding whose body does not hold, because that
	// is a fact about the build rather than about the lists; the walking checks
	// are what go red on it, by comparing the holds a run reaches against the
	// declaration.
	holds := journey.StagesARunHoldsAt()
	t.Run("a body-less stage missing from the holds declaration is caught", func(t *testing.T) {
		short := slices.DeleteFunc(slices.Clone(holds), func(s pipeline.Stage) bool { return s == declared[0] })
		err := journey.DeclaresEveryHold(journey.Implemented(), declared, short)
		if err == nil {
			t.Fatal("a holds declaration missing a body-less stage was accepted, and every body-less " +
				"stage holds, so a walk held to it would pass one stop short")
		}
		if !errors.Is(err, journey.ErrUndeclaredHold) {
			t.Fatalf("caught for the wrong reason: %v", err)
		}
		says := fmt.Sprintf("%s has no body, so a run holds at it", declared[0])
		if !strings.Contains(err.Error(), says) {
			t.Fatalf("caught, but not for the direction this control names; wanted a refusal saying %q "+
				"and got: %v", says, err)
		}
		t.Logf("control 6a red as required: %v", err)
	})

	t.Run("a holds declaration naming something that is not a stage is caught", func(t *testing.T) {
		notAStage := pipeline.StageInvalid
		err := journey.DeclaresEveryHold(journey.Implemented(), declared, append(slices.Clone(holds), notAStage))
		if err == nil {
			t.Fatal("a holds declaration naming something that is not a stage was accepted")
		}
		if !errors.Is(err, journey.ErrUndeclaredHold) {
			t.Fatalf("caught for the wrong reason: %v", err)
		}
		says := fmt.Sprintf("%s is declared holding here and is not one of the stages", notAStage)
		if !strings.Contains(err.Error(), says) {
			t.Fatalf("caught, but not for the name being no stage; wanted a refusal saying %q and got: %v",
				says, err)
		}
		t.Logf("control 6b red as required: %v", err)
	})

	t.Run("a held stage the build neither implements nor leaves body-less is caught", func(t *testing.T) {
		implemented := journey.Implemented()
		if len(implemented) == 0 {
			t.Skip("this build implements no stage, so no held stage can lose its implementation")
		}
		heldWithBody := implemented[len(implemented)-1]
		if !slices.Contains(holds, heldWithBody) {
			t.Skipf("%s is implemented and not declared holding, so taking its body away controls nothing here",
				heldWithBody)
		}
		err := journey.DeclaresEveryHold(implemented[:len(implemented)-1], declared, holds)
		if err == nil {
			t.Fatal("a held stage that lost its body without either declaration moving was accepted, so " +
				"the holds list would keep naming a stage no list accounts for")
		}
		if !errors.Is(err, journey.ErrUndeclaredHold) {
			t.Fatalf("caught for the wrong reason: %v", err)
		}
		says := fmt.Sprintf("%s is declared holding here, is not declared body-less", heldWithBody)
		if !strings.Contains(err.Error(), says) {
			t.Fatalf("caught, but not for the direction this control names; wanted a refusal saying %q "+
				"and got: %v", says, err)
		}
		t.Logf("control 6c red as required: %v", err)
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
