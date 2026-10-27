package service

import (
	"context"
	"fmt"
	"slices"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/store"
)

// PRD section 6's housekeeping, the program's part of it as this service
// carries it: the trusted scope a pass's intake reads, and passRun, which
// marks a run the records attribute to a pass for the review's scope ask.
// The service creates no pass; the coordinator does, through task.create
// with the housekeeping mark, and internal/tasks' housekeeping.go is the
// intake.

// housekeepingScope is internal/tasks' HousekeepingScope: the repository's
// housekeeping.paths read from its trusted copy at a fresh fetch of the
// default branch, on projectPosture's terms, so a pushed copy has no say.
func (s *Service) housekeepingScope(ctx context.Context, repository store.Repository, taskID string) (config.PatternSet, error) {
	trusted, err := s.trustedTaskLayer(ctx, repository, housekeepingReadNamespace, taskID, "housekeeping pass "+taskID)
	if err != nil {
		return nil, err
	}
	resolution, err := config.ResolveRun(s.global, trusted, config.Absent(config.OriginPushed))
	if err != nil {
		return nil, fmt.Errorf("%w: resolving the configuration of housekeeping pass %s: %w", resolutionRefusal(err), taskID, err)
	}
	return resolution.Config.HousekeepingPaths, nil
}

// passRun reports whether a run is a housekeeping pass's, which begin
// records as pipeline.Start.HousekeepingPass so the review stage holds the
// change to the pass's scope (internal/stages' housekeeping.go). It is
// decided from the task records and never from the push: the run's tasks
// are the tasks whose snapshot attributes the run to them
// (store.TasksOfRun), so a pass's own push, a rerun of its run, and a
// person's push of its branch while the pass holds the branch are all the
// pass's and held to the scope, which is wider than PRD section 6's "the
// run that a pass's push starts" and never narrower; a run of the branch
// after another task's attempt took it, as an ordinary delivery may once
// the pass is retired, is that task's and not held. A run attributed to a
// pass and to another task too is held, the stricter reading. A read that
// fails refuses the begin rather than starting a pass's run without the
// rule.
func (s *Service) passRun(ctx context.Context, record store.Run) (bool, error) {
	owners, err := s.store.TasksOfRun(ctx, record)
	if err != nil {
		return false, fmt.Errorf("service: whether run %s is a housekeeping pass's could not be read: %w", record.ID, err)
	}
	return slices.ContainsFunc(owners, func(t store.Task) bool { return t.Housekeeping }), nil
}
