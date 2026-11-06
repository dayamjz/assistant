package service

import (
	"context"
	"math"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// coordinatorCatchUp answers coordinator.catchup for the first coordinator
// alone (ErrWorker, ErrUnplaced, ErrSubordinate: it is not domain-scoped). Its
// parts are feed.VerifyArm, one catchUp read and wakes.peek's, so it hands out
// and answers nothing; its one write is catchUpRuns' run reconciliation.
func (s *Service) coordinatorCatchUp(ctx context.Context, req ipc.Request) (machine.CoordinatorCatchUp, error) {
	if _, err := s.refuseSubordinate(req); err != nil {
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
	queued, err := s.peekWakes(ctx, req, machine.WakesRequest{Limit: math.MaxInt32})
	if err != nil {
		return machine.CoordinatorCatchUp{}, err
	}
	return machine.CoordinatorCatchUpOf(arm, queued, read.Waiting, tasks, read.Kept), nil
}
