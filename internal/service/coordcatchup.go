package service

import (
	"context"
	"math"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// coordinatorCatchUp answers coordinator.catchup. It is the first
// coordinator's alone (ipc.ErrWorker, ErrUnplaced, ErrSubordinate before
// anything is read: this build does not scope it to a domain). Each part is
// its usual owner's: supervision.assert's feed.VerifyArm, one catchUp read
// for the decisions and every task, and wakes.peek's reading of the queue,
// which hands out nothing. It answers no hold and moves no seen mark; the one
// write under it is catchUpRuns' reconciliation, as every read of a run makes.
func (s *Service) coordinatorCatchUp(ctx context.Context, req ipc.Request) (machine.CoordinatorCatchUp, error) {
	p, err := s.refuseSubordinate(req)
	if err != nil {
		return machine.CoordinatorCatchUp{}, err
	}
	arm, err := feed.VerifyArm(ctx, s.home, s.cfg.SupervisionBeaconInterval, time.Now)
	if err != nil {
		return machine.CoordinatorCatchUp{}, err
	}
	var tasks []machine.Task
	read, err := s.catchUp(ctx, func(t machine.Task) { tasks = append(tasks, t) })
	if err != nil {
		return machine.CoordinatorCatchUp{}, err
	}
	// Every queued row, so the count of the rows left out is exact.
	queued, err := s.queuedWakes(ctx, p, math.MaxInt32)
	if err != nil {
		return machine.CoordinatorCatchUp{}, err
	}
	return machine.CoordinatorCatchUpOf(arm, queued, read.Waiting, tasks, read.Kept), nil
}
