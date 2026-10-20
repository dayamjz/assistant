package service

import (
	"context"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
)

// The per-run usage budget: the operator's bound on what one run's own agent
// calls may report generating, and where this service hands it to the graph.
//
// # What kind of bound it is
//
// It is a run's, not the home's, and internal/graph owns it beside the step
// budget: the run records config.Config.RunUsageBudget when it starts, its
// checkpoint's counters carry it, the executor compares the run against it
// before every node and before it halts a run or lets an answer run one, and
// a run that reaches it parks and its record is terminated with a
// store.ParkUsageBudget park. A resume under another budget is refused with
// graph.ErrUsageBudgetChanged, and graph.Executor.AdoptBudget is the one way
// a budget is moved onto a live run; nothing in this build calls it, as
// nothing calls it for the step budget. The usage ceiling in ceiling.go is a
// different thing and stays one: it counts every run's calls over a window,
// refuses the next call, and holds for a person.
//
// # What it is compared against
//
// runUsage is the graph.UsageMeter, and it reads the run's own rows of the
// agent invocation record at each comparison, through
// store.AgentInvocationsForRun, totalled by machine.RunSpendOf: the output
// tokens of the calls containRunner stamped with the run, a call that
// reported none counted as unreported. No total is kept anywhere else, so a
// resume or a restart reads the same record the run's earlier segments
// wrote and counts on from what they spent. A read
// that fails is graph.ErrUsageUnread, which ends the segment as an error
// rather than parking or advancing on it, so the run is left standing at the
// checkpoint of its last completed node, or halted at the halt point that
// node led to, and the caller is told, as a failed step leaves it (carryOn's
// dispositionReported); graph.ErrUsageUnread says which.
//
// # What it does not cover
//
//   - It bounds starts, not spend. A call is in the record only once its row
//     is written after it returns, and the comparison is made before a node
//     starts, so a node that starts under the budget runs to its end however
//     many calls it makes, and the run parks in front of the next node.
//   - It counts what the record holds. A row the recorder dropped is in no
//     total (invocation.go), and a call this service did not stamp with the
//     run is in no run's total: a task's worker, the decision-model checks,
//     and anything a stage runs other than through containRunner.
//   - A call that reported no output token count parks every run it belongs
//     to at the next comparison, whatever the others reported. That is the
//     fail-closed reading of an unknown, as the ceiling's is, and the park's
//     action names it.
//   - The record is keyed by run, so a fork of a run would be metered from
//     nothing under its own name. Nothing in this build forks a run.
//   - The budget is the operator's configuration as the service read it when
//     it opened, like every global key, and a run already started under
//     another value is refused on resume until someone adopts the new one.

// runUsage is the usage meter every executor this service builds is given:
// what run's own agent calls have reported generating, read from the record
// as it stands.
func (s *Service) runUsage(ctx context.Context, run string) (graph.Usage, error) {
	if s.store == nil {
		return graph.Usage{}, errNoInvocationRecord
	}
	rows, err := s.store.AgentInvocationsForRun(ctx, run)
	if err != nil {
		return graph.Usage{}, err
	}
	spent := machine.RunSpendOf(run, rows)
	return graph.Usage{Spent: spent.Total.Or(0), Unreported: spent.Unreported}, nil
}
