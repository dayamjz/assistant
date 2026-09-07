package journey

import (
	"fmt"
	"strings"
)

// Check is one principle assertion, together with the counterfeit observations
// it has to reject.
//
// It exists because a harness of checks that cannot fail reports green while
// proving nothing, which is the failure this product exists to prevent built
// into the thing meant to verify it. A Check is therefore not a predicate a
// caller runs: it is a predicate plus the evidence that the predicate
// discriminates, and Verify runs both halves every time. A check that has been
// watched failing once, by hand, on a machine nobody else has, is not evidence
// anybody else can read.
//
// O is the observation: a typed model of what the product did, produced by
// driving the product rather than by describing it.
type Check[O any] struct {
	// What states what this check establishes, in the terms a failure should
	// be read in. It appears in every failure Verify reports.
	What string
	// Holds reports why the observation is not what the principle requires,
	// and nil when it is. It is written as a report rather than a boolean so
	// that a failure names the part of the observation that was wrong.
	Holds func(O) error
	// Counterfeits are the ways the observation could have come out if the
	// mechanism had failed. Every one of them has to make Holds report
	// something, and a Check declaring none is refused.
	Counterfeits []Counterfeit[O]
}

// Counterfeit is one way an observation could have come out if the mechanism
// under it had failed.
//
// It is a mutation of the real observation rather than an observation written
// from nothing, and that is the whole of why it is worth anything. A fake
// stated from nothing can state a shape the product could not produce, and a
// guard against such a shape looks alive while being dead; this repository has
// shipped that once already. A counterfeit derived from what the product
// actually produced changes one fact about a real shape, so a predicate that
// accepts it is a predicate that would accept the product failing in that way.
type Counterfeit[O any] struct {
	// Named says what went wrong in the counterfeit, in the terms a reader
	// asking "could this check have caught that?" would use.
	Named string
	// Break derives the counterfeit from the real observation. It must not
	// modify what it is given: an observation holding a map or a slice is
	// copied as far as the mutation reaches.
	Break func(O) O
}

// Verify reports what is wrong, and nil when nothing is.
//
// It answers two questions in one call, and both are failures of the same
// weight. The product did not do what the principle requires, which is the
// finding the check was written for. Or the check accepted a counterfeit,
// which means it would have reported the product correct while the product was
// wrong, and a check in that state is worse than no check: it is a green light
// nobody can read.
//
// A check with no counterfeits is refused rather than run. Nothing about an
// observation makes a predicate over it able to fail, so a check that names no
// way of failing is one nobody has shown can.
func (c Check[O]) Verify(observed O) error {
	if strings.TrimSpace(c.What) == "" {
		return fmt.Errorf("journey: a check has to say what it establishes")
	}
	if c.Holds == nil {
		return fmt.Errorf("journey: %s: the check has no predicate", c.What)
	}
	if len(c.Counterfeits) == 0 {
		return fmt.Errorf("journey: %s: the check names no counterfeit observation, so nothing shows it can fail; "+
			"a check that cannot fail reports green while proving nothing", c.What)
	}
	if err := c.Holds(observed); err != nil {
		return fmt.Errorf("%s: %w", c.What, err)
	}
	for _, counterfeit := range c.Counterfeits {
		if strings.TrimSpace(counterfeit.Named) == "" {
			return fmt.Errorf("journey: %s: a counterfeit has to say what went wrong in it", c.What)
		}
		if counterfeit.Break == nil {
			return fmt.Errorf("journey: %s: the counterfeit %q derives nothing", c.What, counterfeit.Named)
		}
		if err := c.Holds(counterfeit.Break(observed)); err == nil {
			return fmt.Errorf("%s: this check accepts an observation in which %s, so it would report the "+
				"product correct while the product was wrong; the check proves nothing as written",
				c.What, counterfeit.Named)
		}
	}
	return nil
}
