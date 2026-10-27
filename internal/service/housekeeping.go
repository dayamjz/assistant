package service

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/store"
)

// PRD section 6's housekeeping, the program's part of it as this service
// carries it: the trusted scope a pass's intake reads. The service creates
// no pass; the coordinator does, through task.create with the housekeeping
// mark, and internal/tasks' housekeeping.go is the intake.

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
