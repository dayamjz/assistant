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
// caller runs: it is a set of clauses plus the evidence that every one of them
// discriminates, and Verify runs both halves every time. A check that has been
// watched failing once, by hand, on a machine nobody else has, is not evidence
// anybody else can read.
//
// The predicate is a list of clauses rather than one function on purpose. A
// single predicate is answered as a whole, so a counterfeit only has to reach
// one of its assertions for the check to look alive; an assertion no
// counterfeit reaches is then invisible, and this package shipped three of
// them. Splitting the predicate is what lets Verify hold each assertion to the
// standard the check as a whole is held to.
//
// O is the observation: a typed model of what the product did, produced by
// driving the product rather than by describing it.
type Check[O any] struct {
	// What names the subject this check is about, and may not be the claim it
	// makes. It is a short label - "P2: a run through the binary" - and Verify
	// refuses one longer than AboutMax.
	//
	// The bound is the construction. This package shipped several checks whose
	// What promised more than their clauses asserted, because a free-text claim
	// beside a list of assertions is two statements of the same thing that
	// drift, and the prose is the one nothing checks. Claim renders what the
	// check actually establishes by reading the clauses, so the sentence a
	// reader sees is composed from the assertions rather than written beside
	// them.
	//
	// What the bound does not do is make a short claim impossible, only a long
	// one. A label with room for a paragraph of promises is what this removes;
	// a label that lies in eight words is still writable, and only review
	// catches that.
	What string
	// Clauses are the assertions this check makes, each answered on its own.
	// A check declaring none is refused, and so is one carrying a clause no
	// counterfeit reaches.
	Clauses []Clause[O]
	// Counterfeits are the ways the observation could have come out if the
	// mechanism had failed. Every one of them has to make some clause report
	// something, every clause has to be reported by some one of them, and a
	// Check declaring none is refused.
	Counterfeits []Counterfeit[O]
}

// Clause is one assertion a check makes about the observation.
//
// It is separate from its neighbours so that Verify can ask of it the question
// it asks of the check: has anything shown this can fail. A clause is written
// so that it answers over any observation of its type without panicking,
// including the counterfeits, because Verify runs every clause against every
// counterfeit rather than stopping at the first that reports.
type Clause[O any] struct {
	// States what this clause asserts, in the terms a failure should be read
	// in. It appears in the failure Verify reports for this clause and in the
	// refusal Verify reports when no counterfeit reaches it.
	States string
	// Absence declares that this clause asserts something is not there:
	// nothing fired, nothing was rejected, no run started, no session was
	// carried. Such a clause is the one shape a counterfeit cannot vouch for
	// on its own, because a counterfeit mutates a model and says nothing about
	// the world the model came from, so an absence clause carries Possible and
	// any other clause is refused for carrying it.
	Absence bool
	// Possible reports why the thing this clause asserts the absence of could
	// not have been present in the world the observation came from, and nil
	// when it could have been. It is run against the real observation alone,
	// never against a counterfeit, because what it establishes is a fact about
	// the subject rather than about the model.
	//
	// It is required of an absence clause and refused on any other.
	Possible func(O) error
	// Holds reports why the observation is not what this clause requires, and
	// nil when it is. It is written as a report rather than a boolean so that
	// a failure names the part of the observation that was wrong.
	Holds func(O) error
}

// Counterfeit is one way an observation could have come out if the mechanism
// under it had failed.
//
// It is a mutation of the real observation rather than an observation written
// from nothing, and that is the whole of why it is worth anything. A fake
// stated from nothing can state a shape the product could not produce, and a
// guard against such a shape looks alive while being dead; this repository has
// shipped that once already. A counterfeit derived from what the product
// actually produced starts from a real shape, so a clause that accepts a
// faithful mutation of it is a clause that would accept the product failing in
// that way.
//
// Starting from a real observation is the whole of what Verify enforces. Break
// is an unconstrained func(O) O, so whether the mutation lands on a shape the
// product could still have produced is the writer's discipline, and the rule
// is that it must: a counterfeit stating an impossible shape shows a clause
// failing against a failure the mechanism cannot reach, which establishes
// nothing about the clause. The residual gap is that nothing here can tell the
// two apart, because only the mechanism under the observation knows which
// shapes it can produce.
type Counterfeit[O any] struct {
	// Named says what went wrong in the counterfeit, in the terms a reader
	// asking "could this check have caught that?" would use.
	Named string
	// Break derives the counterfeit from the real observation. It must not
	// modify what it is given: an observation holding a map or a slice is
	// copied as far as the mutation reaches.
	Break func(O) O
}

// AboutMax bounds Check.What. It is small enough that a multi-clause promise
// does not fit and large enough for a subject a reader recognizes.
const AboutMax = 72

// Claim is what this check establishes, composed from its clauses rather than
// written beside them, so the sentence cannot promise more than the assertions
// deliver.
func (c Check[O]) Claim() string {
	var b strings.Builder
	b.WriteString(c.What)
	for i, clause := range c.Clauses {
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		b.WriteString(clause.States)
	}
	return b.String()
}

// Verify reports what is wrong, and nil when nothing is.
//
// It answers four questions in one call, and all four are failures of the same
// weight.
//
// The product did not do what the principle requires, which is the finding the
// check was written for. Or some clause accepted a counterfeit that no other
// clause caught either, which means the check would have reported the product
// correct while the product was wrong, and a check in that state is worse than
// no check: it is a green light nobody can read. Or a clause is reached by no
// counterfeit at all, which is the same failure one clause at a time: the
// check discriminates and that part of it does not, so what the check claims
// is wider than what it establishes. Or an absence clause holds over a world
// in which the thing it looked for could not have been there, which is a
// clause falsifiable in the model and vacuous in the subject.
//
// A check with no clause or no counterfeit is refused rather than run. Nothing
// about an observation makes a predicate over it able to fail, so a check that
// names no way of failing is one nobody has shown can.
func (c Check[O]) Verify(observed O) error {
	if err := c.declared(); err != nil {
		return err
	}
	for _, clause := range c.Clauses {
		if !clause.Absence {
			continue
		}
		if err := clause.Possible(observed); err != nil {
			return fmt.Errorf("%s: the clause %q asserts an absence, and %w; nothing could have made it "+
				"report over this observation, so it reads as coverage and establishes nothing",
				c.What, clause.States, err)
		}
	}
	for _, clause := range c.Clauses {
		if err := clause.Holds(observed); err != nil {
			return fmt.Errorf("%s: %s: %w", c.What, clause.States, err)
		}
	}
	reached := make([]bool, len(c.Clauses))
	for _, counterfeit := range c.Counterfeits {
		broken := counterfeit.Break(observed)
		caught := false
		for i, clause := range c.Clauses {
			if clause.Holds(broken) != nil {
				reached[i] = true
				caught = true
			}
		}
		if !caught {
			return fmt.Errorf("%s: this check accepts an observation in which %s, so it would report the "+
				"product correct while the product was wrong; the check proves nothing as written",
				c.Claim(), counterfeit.Named)
		}
	}
	for i, clause := range c.Clauses {
		if !reached[i] {
			return fmt.Errorf("%s: no counterfeit here makes the clause %q report anything, so nothing "+
				"shows that clause can fail; the check as a whole discriminates and this part of it "+
				"does not, which is how a clause that establishes nothing survives", c.Claim(), clause.States)
		}
	}
	return nil
}

// declared reports what is wrong with how the check is written, before any of
// it is run against an observation.
func (c Check[O]) declared() error {
	if len(c.What) > AboutMax {
		return fmt.Errorf("journey: %q is %d characters and a check's subject is bounded at %d; it names "+
			"what the check is about and the clauses say what it establishes, so a claim written here "+
			"would be a second statement of what they already say",
			c.What, len(c.What), AboutMax)
	}
	if strings.TrimSpace(c.What) == "" {
		return fmt.Errorf("journey: a check has to say what it establishes")
	}
	if len(c.Clauses) == 0 {
		return fmt.Errorf("journey: %s: the check asserts nothing, so there is nothing it could report", c.What)
	}
	for _, clause := range c.Clauses {
		if strings.TrimSpace(clause.States) == "" {
			return fmt.Errorf("journey: %s: a clause has to say what it asserts", c.What)
		}
		if clause.Holds == nil {
			return fmt.Errorf("journey: %s: the clause %q has no predicate", c.What, clause.States)
		}
		if clause.Absence && clause.Possible == nil {
			return fmt.Errorf("journey: %s: the clause %q asserts an absence and says nothing about how "+
				"the thing it looked for could have been present; a counterfeit mutates the model and "+
				"cannot establish that, so an absence clause carries the answer itself", c.What, clause.States)
		}
		if !clause.Absence && clause.Possible != nil {
			return fmt.Errorf("journey: %s: the clause %q carries the precondition only an absence clause "+
				"needs and does not declare itself one", c.What, clause.States)
		}
	}
	if len(c.Counterfeits) == 0 {
		return fmt.Errorf("journey: %s: the check names no counterfeit observation, so nothing shows it can fail; "+
			"a check that cannot fail reports green while proving nothing", c.What)
	}
	for _, counterfeit := range c.Counterfeits {
		if strings.TrimSpace(counterfeit.Named) == "" {
			return fmt.Errorf("journey: %s: a counterfeit has to say what went wrong in it", c.What)
		}
		if counterfeit.Break == nil {
			return fmt.Errorf("journey: %s: the counterfeit %q derives nothing", c.Claim(), counterfeit.Named)
		}
	}
	return nil
}
