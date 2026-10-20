package service

import (
	"context"
	"errors"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
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

// errUsageReadFault is the failure a reading returns while a test has asked
// for one through usageReadFaults.
var errUsageReadFault = errors.New("the agent invocation record could not be read")

// runUsage is the usage meter every executor this service builds is given:
// what run's own agent calls have reported generating, read from the record
// as it stands.
func (s *Service) runUsage(ctx context.Context, run string) (graph.Usage, error) {
	if s.store == nil {
		return graph.Usage{}, errNoInvocationRecord
	}
	if s.usageReadFaults.Load() > 0 && s.usageReadFaults.Add(-1) >= 0 {
		return graph.Usage{}, errUsageReadFault
	}
	rows, err := s.store.AgentInvocationsForRun(ctx, run)
	if err != nil {
		return graph.Usage{}, err
	}
	spent := machine.RunSpendOf(run, rows)
	return graph.Usage{Spent: spent.Total.Or(0), Unreported: spent.Unreported}, nil
}

// parkHeldPastUsage parks a held run that is past its usage budget, and
// reports whether it acted. It is the one place this service decides that,
// and the two operations that meet such a run call it: attach, for a run a
// restart or a lost caller left held, and respond, when an answer is refused
// with graph.ErrUsageSpent.
//
// A decision can be open on a run already past its usage budget, because
// internal/graph records a run halted at its halt point when the reading
// before it fails, so the node before it is kept; graph.Executor.Resume is
// where that comparison is finished and the park recorded. Nothing else in
// this service resumes a held run, so without this the run would stay held
// with every answer refused and its park never recorded. It is not in
// reconcile, because reconcile moves a record to match its checkpoint and
// never advances a run.
//
// It resumes only a run whose latest checkpoint is halted with steps left
// under a usage budget, and only when graph.Usage.Exhausts says the reading
// leaves no room, so a held run with room is reported as it stands rather
// than resumed, and the step budget's refusal stays what it is. A reading
// that cannot be made decides nothing and is logged; the run stays held.
func (s *Service) parkHeldPastUsage(ctx context.Context, record store.Run) (machine.Run, bool, error) {
	latest, err := s.checkpoints.Latest(ctx, record.ID)
	if err != nil || latest.Status != graph.StatusHalted || latest.Counters.UsageBudget == 0 ||
		latest.Counters.Steps >= latest.Counters.Budget {
		return machine.Run{}, false, nil
	}
	reading, err := s.runUsage(ctx, record.ID)
	if err != nil {
		s.log.Printf("run %s is held under a usage budget and its spend could not be read: %v", record.ID, err)
		return machine.Run{}, false, nil
	}
	if !reading.Exhausts(latest.Counters.UsageBudget) {
		return machine.Run{}, false, nil
	}
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.Run{}, true, err
	}
	resolved, err := s.resolveRunConfig(ctx, record)
	if err != nil {
		return machine.Run{}, true, err
	}
	topo, err := s.topologyFor(built, resolved)
	if err != nil {
		return machine.Run{}, true, err
	}
	view, err := s.advance(ctx, record.ID, topo, func(ctx context.Context) (graph.Result, error) {
		return topo.executor.Resume(ctx, record.ID)
	})
	if errors.Is(err, ErrRunAdvancing) || errors.Is(err, ErrRunEnding) {
		view, err = s.view(ctx, record.ID)
	}
	return view, true, err
}
