package service

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// The reduced path. The nine-stage gate stays the default. A run takes the
// reduced path only when it asks and the trusted configuration names one
// (config.ReducedPath record or publish). A pushed copy cannot name it (P7).
// A person's push and a rerun do not ask, so they stay on the nine stages.
//
// What each value requires, what it may publish, and who may merge are
// config.DeliveryModes. This file does not add a merge authority. settle
// does not treat the run as checks-passed, and mergeUnderAutonomy refuses
// the record before it opens a code host, so autonomy.merge's live read is
// unchanged and still does not see this run.
//
// The stages that run are intent, rebase, and an anchored push, plus the
// pull request stage when the mode may publish. Every other stage in
// pipeline.Order is skipped, so a stage added later is skipped until someone
// puts it in that keep set. The push stage still requires the anchor (P6)
// and does not launch a review.
//
// The fact is the run row's validation_not_performed column, written once
// before the run is recorded as started, and pipeline.KeyValidationNotPerformed
// so the push stage can read it without opening the database.
//
// Residual gaps:
//   - A pull request this path publishes is not polled for a person's merge.
//     That read is for checks-passed runs, and this path's passed record
//     reads as pushed.
//   - The operator's file can name the path, because the key's trust class
//     admits it. A trusted copy that sets the key wins over that file.
//   - On a local upstream the pull request stage also skips because there is
//     no code host. The skip list is what distinguishes record from publish.
//   - The copy side of a crash between the flag write and the first
//     checkpoint is the pending run begin terminates when NewState fails.
//     A crash after Start is a run the next open resumes with the flag
//     already on the row and in the checkpoint.

// ErrReducedPathNotAllowed reports a request for the reduced path on a run
// whose trusted configuration does not name one. It travels as
// ipc.ErrInvalidRequest, because the remedy is the caller's: a value on the
// default branch, or the request without the flag.
var ErrReducedPathNotAllowed = fmt.Errorf("service: the reduced path was asked for and this repository's trusted configuration does not name one")

func reducedPathRefused(repository store.Repository) error {
	return fmt.Errorf("%w: %w: set %q to %q or %q in %s on %s, the branch the trusted copy is read from, "+
		"or ask without it; a pushed branch cannot set it, so the branch under validation carrying it does not count",
		ipc.ErrInvalidRequest, ErrReducedPathNotAllowed,
		string(config.KeyReducedPath), string(config.ReducedPathRecord), string(config.ReducedPathPublish),
		config.RepositoryDocument, repository.DefaultBranch)
}

func reducedTrustedRef(check string) string {
	return "refs/assistant/trusted/reduced/" + check
}

// trustedReducedPath reads the reduced path the repository's trusted copy
// names, at a fresh fetch of the default branch, resolved with the operator's
// layer and no pushed layer. A trusted copy that cannot be read is an error,
// never an answer either way.
func (s *Service) trustedReducedPath(ctx context.Context, repository store.Repository) (config.ReducedPath, error) {
	check, err := newRunID()
	if err != nil {
		return "", err
	}
	forWhat := "the reduced path for " + repository.ID
	bare, err := s.gateRepositoryOf(ctx, repository, forWhat)
	if err != nil {
		return "", err
	}
	ref := reducedTrustedRef(check)
	defer func() { _ = bare.DeleteRef(context.WithoutCancel(ctx), ref) }()
	trusted, _, _, err := s.trustedLayerAt(ctx, bare, repository, ref, forWhat)
	if err != nil {
		return "", err
	}
	resolution, err := config.ResolveRun(s.global, trusted, config.Absent(config.OriginPushed))
	if err != nil {
		return "", fmt.Errorf("service: resolving whether %s names a reduced path: %w", repository.ID, err)
	}
	return resolution.Config.ReducedPath, nil
}

// takeReducedPath applies a request for the reduced path after the run's own
// resolution, or refuses it and ends the pending run. A request that did not
// ask returns nil and changes nothing.
func (s *Service) takeReducedPath(ctx context.Context, built *driver, record store.Run, start *pipeline.Start, resolved runResolution) error {
	if !start.Reduced {
		return nil
	}
	mode, ok := resolved.cfg.ReducedPath.Mode()
	if !ok || !mode.Path.Named() {
		s.endPending(ctx, built, record.ID, "the reduced path was not named")
		repository, err := s.store.Repository(ctx, record.RepositoryID)
		if err != nil {
			return err
		}
		return reducedPathRefused(repository)
	}
	if err := s.store.SetRunValidationNotPerformed(ctx, record.ID); err != nil {
		s.endPending(ctx, built, record.ID, "its validation flag could not be written")
		return err
	}
	start.Skip = appendSkips(start.Skip, reducedSkips(mode))
	start.ValidationNotPerformed = true
	return nil
}

func (s *Service) endPending(ctx context.Context, built *driver, id, why string) {
	if _, err := built.runs.Terminate(ctx, id); err != nil {
		s.log.Printf("run %s could not be ended after %s: %v", id, why, err)
	}
}

// reducedSkips is the stages a named reduced path does not run, derived from
// pipeline.Order. Intent, rebase, and push stay. The pull request stage stays
// only when the mode may publish. Every other stage is skipped, including one
// added to the order later.
func reducedSkips(mode config.DeliveryMode) []pipeline.Stage {
	if !mode.Path.Named() {
		return nil
	}
	var skip []pipeline.Stage
	for _, stage := range pipeline.Order() {
		switch stage {
		case pipeline.StageIntent, pipeline.StageRebase, pipeline.StagePush:
			continue
		case pipeline.StagePR:
			if mode.MayPublish() {
				continue
			}
		}
		skip = append(skip, stage)
	}
	return skip
}

func appendSkips(have, add []pipeline.Stage) []pipeline.Stage {
	seen := map[pipeline.Stage]bool{}
	for _, stage := range have {
		seen[stage] = true
	}
	for _, stage := range add {
		if !seen[stage] {
			have = append(have, stage)
			seen[stage] = true
		}
	}
	return have
}

// validationNotPerformed reports whether the run record says validation was
// not performed. Unknown is false: a row that predates the column did not
// take the path.
func validationNotPerformed(record store.Run) bool {
	flag, ok := record.ValidationNotPerformed.Get()
	return ok && flag
}
