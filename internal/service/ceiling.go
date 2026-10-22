package service

import (
	"context"
	"errors"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// The usage ceiling: the operator's bound on recorded agent use, and where
// this service holds a run to it.
//
// # What kind of bound it is
//
// The agent invocation record is the one owner of what was spent, and
// machine.UsageOf is the one total of it; a ceiling is a bound compared
// against that total. It is the home's, not a run's. It counts every run's
// calls together over a window measured back from each comparison, so what
// it is compared against is the record and nothing a run's checkpoint
// carries, and it is not one of internal/graph's three loop bounds: it parks
// nothing, a resume needs nothing from it, and no run's counters change it.
// It is read from the operator's own configuration alone, s.cfg, because its
// keys are global-only (P7): no repository copy can raise or lift it.
//
// # Where it is enforced
//
// admitInvocation is asked before every agent call containRunner's wrappers
// start, a stage body's and a fix round's alike, before the process is
// registered or anything is launched, and a refusal is the typed
// *machine.UsageCeilingError. A refusal is three standings, never one: the
// ceiling reached (at or over it), a total the record does not establish
// because some call in the window left a set count unreported, and a record
// that could not be read. Missing information is never read as room.
//
// A refusal reaching a stage is not a failure of the run and does not strand
// it. observe.go turns it into a hold: the stage reports
// machine.CeilingHeldReport, an ask for a person whose remedy lies outside
// the change, so standing consent leaves it alone, and a fix round the
// ceiling refused leaves that same ask as the round's hold. The person reads
// the hold row and the exact figures the usage view also reports, because
// both come from machine.CeilingOf over the record.
//
// # What it does not cover
//
//   - It bounds starts, not spend. A call is absent from the record until its
//     row is written after it returns, so a start under the ceiling can carry
//     the total past it by that call's whole cost, and every call already
//     running when the ceiling is reached goes on to its end. Two starts that
//     both read the total under it both run.
//   - It counts what the record holds. A row the recorder dropped is in no
//     total, so the ceiling compares a total that understates; invocation.go
//     owns that drop. A worker's own turns and any price are not in the
//     record, so not in the ceiling either.
//   - A call that reported no count the ceiling is set on refuses every
//     start while it stays in the window. That is the fail-closed reading of
//     an unknown, and the remedy is the person's: a ceiling on counts their
//     agents report, or the window passing.
//   - The ceiling is read when the service opens. A changed ceiling takes
//     effect on the next start of the service, as every global key does.
//   - Approving the hold carries the run past a stage whose agent call did
//     not run, as approving any hold of a stage that could not establish
//     anything does; the row says so.
//   - Retrying is the hold's answer for when there is room again, PRD
//     section 5's Retry: the finding is marked as a call that never started,
//     findings.Finding.NotStarted, which is what offers the retry, and
//     internal/pipeline's retry.go owns the cycle and its bound. It runs the
//     refused call again. A stage body whose call was refused starts over.
//     An automatic fix round the ceiling refused is run by the retry itself,
//     as the round the stage's round limit already counted, and the stage
//     then runs from the top; a retry with no room therefore spends no round
//     and does not trip convergence. Two cases are not run again. A fix
//     round a person asked for is not, because a retry does not carry the
//     request; the stage holds on that round's hold and they can ask again.
//     And when the stage's own call was refused too after the refused round,
//     the stage starts over with no round in front of it, so the refused
//     round stays counted against the round limit, and a stage with no
//     automatic round left parks in front of its fixer rather than taking
//     one. checkRetry in runs.go takes no reading of its own: the retried
//     call is admitted or refused here like any other, so a retry with no
//     room holds on the same question again.
//   - Only calls through containRunner are held to it. The decision-model
//     checks reach their provider through internal/judge, not through an
//     agent, and a task's worker is not wrapped here at all.

// admitInvocation refuses to start another agent call while the operator's
// usage ceiling refuses one. A ceiling with no count set reads nothing and
// admits, so a home that never set one starts what it started before. A
// context already ended is that ending, never a ceiling refusal.
func (s *Service) admitInvocation(ctx context.Context) error {
	if !s.cfg.UsageCeiling.Set() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	standing := s.ceilingStanding(ctx)
	if standing.Standing.Refuses() {
		return &machine.UsageCeilingError{Ceiling: standing}
	}
	return nil
}

// errNoInvocationRecord is why a set ceiling reads as unread on a service with
// no store to read the record from.
var errNoInvocationRecord = errors.New("the agent invocation record is not open")

// ceilingStanding compares the operator's ceiling against the record as it
// stands now. A read that fails is the unread standing, carrying why.
func (s *Service) ceilingStanding(ctx context.Context) machine.UsageCeiling {
	readAt := time.Now().UTC()
	ceiling := s.cfg.UsageCeiling
	if s.store == nil {
		return machine.CeilingUnreadable(ceiling, readAt, errNoInvocationRecord)
	}
	rows, err := s.store.AgentInvocationsSince(ctx, machine.CeilingWindow(ceiling).Since(readAt))
	if err != nil {
		return machine.CeilingUnreadable(ceiling, readAt, err)
	}
	return machine.CeilingOf(ceiling, readAt, rows)
}

// ceilingRefusal reports the comparison a refusal carries, when err is one.
func ceilingRefusal(err error) (machine.UsageCeiling, bool) {
	var refused *machine.UsageCeilingError
	if !errors.As(err, &refused) {
		return machine.UsageCeiling{}, false
	}
	return refused.Ceiling, true
}

// heldAtCeiling is what a stage body that returned a ceiling refusal reports
// instead of failing: the hold, with no writes, since the body did not get
// as far as anything it would have written.
func heldAtCeiling(out pipeline.Output, err error) (pipeline.Output, error) {
	standing, ok := ceilingRefusal(err)
	if !ok {
		return out, err
	}
	return pipeline.Output{Report: machine.CeilingHeldReport(standing)}, nil
}

// roundHeldAtCeiling is what a fix round the ceiling refused returns instead
// of failing: no change, and the hold, which the stage's next execution
// reports with its own findings so the stage holds for a person.
func roundHeldAtCeiling(stage pipeline.Stage, out pipeline.FixOutput, err error) (pipeline.FixOutput, error) {
	standing, ok := ceilingRefusal(err)
	if !ok {
		return out, err
	}
	return pipeline.FixOutput{
		Summary: "No fix round ran for the " + stage.String() + " stage, because the usage ceiling refused its agent call.",
		Holds:   []findings.Finding{machine.CeilingHeldFinding(standing)},
	}, nil
}
