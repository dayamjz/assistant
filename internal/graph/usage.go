package graph

import (
	"context"
	"fmt"
)

// Usage is what one run's own work has reported spending, as a UsageMeter
// reads it. This package does not know what is being counted or where it is
// recorded: the meter is the caller's, and the one thing asked of it is that
// a reading is the record's as it stands, never a total kept beside it.
type Usage struct {
	// Spent is the sum of the count over the work that reported it.
	Spent int64 `json:"spent"`
	// Unreported is how many units of the run's work reported no count. A
	// reading with any is not an established total, and the usage budget
	// parks on it rather than taking the gap for room.
	Unreported int `json:"unreported,omitempty"`
}

// exhausts reports whether this reading leaves a run held to budget no room:
// spent at or past it, or not established at all. A budget of zero is no
// budget and nothing exhausts it.
func (u Usage) exhausts(budget int64) bool {
	return budget > 0 && (u.Unreported > 0 || u.Spent >= budget)
}

// clone returns an independent copy of the reading.
func (u *Usage) clone() *Usage {
	if u == nil {
		return nil
	}
	out := *u
	return &out
}

// UsageMeter reads what a run has spent against its usage budget. The
// executor calls it immediately before every node a run held to a nonzero
// usage budget would run, which is where the step budget is compared too,
// and before it halts such a run at a halt point or lets an answer run one.
// A run held to no usage budget is never metered.
//
// It is called with the run's name and must answer from the record that owns
// what the run spent, read at the call. An error is a reading nobody could
// make: the executor refuses with ErrUsageUnread rather than advancing or
// parking on it.
type UsageMeter func(ctx context.Context, run string) (Usage, error)

// usageExhausted reads the run's spend when its checkpoint holds it to a
// usage budget, and reports the reading and whether it leaves the run no
// room. A run held to none is not read.
func (e *Executor) usageExhausted(ctx context.Context, cp Checkpoint) (Usage, bool, error) {
	budget := cp.Counters.UsageBudget
	if budget == 0 {
		return Usage{}, false, nil
	}
	if e.meter == nil {
		// NewExecutor refuses a usage budget with no meter, and load refuses
		// a run whose budget is not this executor's, so a run held to one
		// reaches an executor without a meter only through AdoptBudget's
		// revival of a run that recorded none. That is still not room.
		return Usage{}, false, fmt.Errorf("%w: run %q is held to a usage budget of %d and this executor has no meter",
			ErrUsageUnread, cp.Run, budget)
	}
	u, err := e.meter(ctx, cp.Run)
	if err != nil {
		return Usage{}, false, fmt.Errorf("%w: run %q: %w", ErrUsageUnread, cp.Run, err)
	}
	if u.Spent < 0 || u.Unreported < 0 {
		return Usage{}, false, fmt.Errorf("%w: run %q: the meter answered %d spent and %d unreported, which no record holds",
			ErrUsageUnread, cp.Run, u.Spent, u.Unreported)
	}
	return u, u.exhausts(budget), nil
}

// parkOnUsage records that the run stopped in front of node because its
// usage budget left it no room on the reading u. It is the one owner of what
// that park says.
func parkOnUsage(cp *Checkpoint, node string, u Usage) {
	cp.Status = StatusUsageExhausted
	cp.ParkedUsage = &u
	if u.Unreported > 0 {
		cp.Reason = fmt.Sprintf("the usage budget of %d could not be weighed before node %q could run: "+
			"%d of the run's calls reported no count, so the %d the others reported is not what the run spent",
			cp.Counters.UsageBudget, node, u.Unreported, u.Spent)
		return
	}
	cp.Reason = fmt.Sprintf("the usage budget of %d was spent before node %q could run: the run's calls reported %d",
		cp.Counters.UsageBudget, node, u.Spent)
}

// parkOnRunBounds parks cp in front of node when either run-level bound
// leaves it no room, and reports whether it did. The step budget is compared
// first, because it needs no reading; the usage budget is read only for a
// run with steps left. This is the one place both are compared, so every
// route that stops a run in front of a node, a halt point included, applies
// the same two bounds in the same order.
func (e *Executor) parkOnRunBounds(ctx context.Context, cp *Checkpoint, node string) (bool, error) {
	if spent(cp.Counters) {
		parkOnBudget(cp, node)
		return true, nil
	}
	u, over, err := e.usageExhausted(ctx, *cp)
	if err != nil {
		return false, err
	}
	if over {
		parkOnUsage(cp, node, u)
		return true, nil
	}
	return false, nil
}

// validateParkedUsage holds a recorded usage reading to what the executor
// writes: only a usage park carries one, every usage park does, the run it
// describes was held to a usage budget, and the reading exhausts that budget,
// so a checkpoint cannot report a usage park the bound it names could not
// have made.
func (g *Graph) validateParkedUsage(c Checkpoint) error {
	u := c.ParkedUsage
	if c.Status != StatusUsageExhausted {
		if u != nil {
			return &CheckpointError{Field: "parked_usage", Detail: fmt.Sprintf(
				"is present on a run that is %s, and only a usage park has one", c.Status)}
		}
		return nil
	}
	if u == nil {
		return &CheckpointError{Field: "parked_usage", Detail: "is missing from a usage park"}
	}
	if u.Spent < 0 || u.Unreported < 0 {
		return &CheckpointError{Field: "parked_usage", Detail: fmt.Sprintf(
			"records %d spent and %d unreported, which no reading produces", u.Spent, u.Unreported)}
	}
	if !u.exhausts(c.Counters.UsageBudget) {
		return &CheckpointError{Field: "parked_usage", Detail: fmt.Sprintf(
			"records %d spent and %d unreported, which leaves room under a usage budget of %d, so it parked nothing",
			u.Spent, u.Unreported, c.Counters.UsageBudget)}
	}
	return nil
}
