package service

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/custody"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Custody of a person's branch: PRD section 11, served.
//
// internal/custody decides and acts; this file reads the facts it decides
// from out of the records that own them, which only this service holds
// together. The run is the newest run of the branch, read from the store
// and reconciled against its checkpoint first, as every operation on a run
// is, so a run a dead service left recorded as running is read as where its
// checkpoint says it is. Its position is the last checkpoint's
// pipeline.KeyHead and pipeline.KeyPushed, through internal/checkpoints, and
// its copy is read where internal/home places it. The gate repository is the
// one the run's commits are in.
//
// # Why sync runs in the service
//
// PRD section 8 has one service own every mutation, and section 11 one
// module own every mutation of a person's branch; serving it here puts both
// under the home's lock. It also puts it under the branch's exclusion, the
// same one a start and a rerun take: a confirmed sync holds it from the
// classification to the last step, so a run cannot be created for the
// branch between deciding the pipeline no longer holds it and moving it. A
// sync that is only a plan takes no exclusion, because it writes nothing.
//
// The method is restricted, so a process inside a validation stage cannot
// reach it, and the handler refuses a task's worker as well: a worker
// reconciles nobody's branch, and the branch a working copy names is the
// person's.
//
// # Status reads the same classification and never writes
//
// assistant status reports the classification through custodyStatus, which
// is Classify and nothing else: no fetch, no reference written, nothing in
// the working copy changed. The reconciliation status already makes of an
// active run is the one write a status causes, and it is to the run's
// record, not to the branch.

// syncBranch answers assistant sync: the plan, and with Confirm the plan
// carried out. Every refusal custody makes is part of the answer rather than
// an error, because it is a typed result a person reads and a driving agent
// decides on; an error is a record or a repository that could not be read.
func (s *Service) syncBranch(ctx context.Context, req ipc.Request, r machine.SyncRequest) (machine.Plan, error) {
	if _, err := s.refuseSubordinate(req); err != nil {
		return machine.Plan{}, err
	}
	repository, err := s.repositoryAt(ctx, r.WorkingPath)
	if err != nil {
		return machine.Plan{}, err
	}
	working, err := vcs.OpenWorktree(ctx, repository.WorkingPath, s.git()...)
	if err != nil {
		return machine.Plan{}, err
	}
	branch, _ := working.HeadBranch(ctx)
	if r.Confirm && branch != "" {
		release, err := s.holdBranch(ctx, branchKey{repository: repository.ID, branch: branch})
		if err != nil {
			return machine.Plan{}, err
		}
		defer release()
	}
	read, err := s.custodyRead(ctx, repository, working)
	if err != nil {
		return machine.Plan{}, err
	}
	if read.refusal != nil {
		return machine.Plan{Refusal: read.refusal.Reason, Detail: read.refusal.Error()}, nil
	}
	c := read.classification
	steps := custody.Steps(c, read.gatePath, repository.PushURL())
	plan := machine.Plan{Classification: &c, Steps: steps, Detail: c.Detail}
	if !r.Confirm || !c.Class.Acts() {
		return plan, nil
	}
	result, err := custody.Apply(ctx, working, c, read.gatePath, repository.PushURL())
	plan.Done = result.Done
	if err != nil {
		var refusal *custody.Refusal
		if !errors.As(err, &refusal) {
			return machine.Plan{}, err
		}
		s.log.Printf("sync of %s in %s was refused: %v", c.Branch, repository.WorkingPath, err)
		plan.Refusal, plan.Detail = refusal.Reason, refusal.Error()
		return plan, nil
	}
	plan.Applied = true
	s.log.Printf("sync of %s in %s carried out %s against run %s", c.Branch, repository.WorkingPath, c.Class, c.Run)
	return plan, nil
}

// custodyStatus is the classification assistant status reports, which
// reads and writes nothing in the working copy. A record or a repository
// that could not be read is reported as unverifiable rather than failing the
// status, which reports everything it can.
func (s *Service) custodyStatus(ctx context.Context, repository store.Repository) *machine.Custody {
	working, err := vcs.OpenWorktree(ctx, repository.WorkingPath, s.git()...)
	if err == nil {
		var read custodyRead
		if read, err = s.custodyRead(ctx, repository, working); err == nil {
			if read.refusal != nil {
				return &machine.Custody{Refusal: read.refusal.Reason, Detail: read.refusal.Error()}
			}
			c := read.classification
			return &machine.Custody{Classification: &c, Detail: c.Detail}
		}
	}
	return &machine.Custody{Refusal: custody.ReasonUnverifiable, Detail: err.Error()}
}

// custodyRead is a classification, or custody's refusal to make one, and the
// gate repository the run's commits are fetched from.
type custodyRead struct {
	classification custody.Classification
	refusal        *custody.Refusal
	gatePath       string
}

// custodyRead reads the facts and classifies them. A refusal custody made
// is in the result; the error is a record or a repository that could not be
// read at all.
func (s *Service) custodyRead(ctx context.Context, repository store.Repository, working *vcs.Repository) (custodyRead, error) {
	facts, gate, err := s.custodyFacts(ctx, repository, working)
	if err != nil {
		return custodyRead{}, err
	}
	var read custodyRead
	if gate != nil {
		read.gatePath = gate.Path()
	}
	var pipelineRepo custody.Pipeline = gate
	if gate == nil {
		// No run, so nothing the pipeline holds is asked about; the
		// classification refuses before reaching the gate.
		pipelineRepo = working
	}
	c, err := custody.Classify(ctx, facts, pipelineRepo, working)
	if err != nil {
		var refusal *custody.Refusal
		if !errors.As(err, &refusal) {
			return custodyRead{}, err
		}
		read.refusal = refusal
		return read, nil
	}
	read.classification = c
	return read, nil
}

// custodyFacts reads what Classify decides from, each from its owner, and
// the gate repository of the run when there is one.
func (s *Service) custodyFacts(ctx context.Context, repository store.Repository, working *vcs.Repository) (custody.Facts, *vcs.Repository, error) {
	var facts custody.Facts
	branch, err := working.HeadBranch(ctx)
	switch {
	case errors.Is(err, vcs.ErrDetachedHead):
		return facts, nil, nil
	case err != nil:
		return facts, nil, fmt.Errorf("service: reading the branch of %s: %w", repository.WorkingPath, err)
	}
	head, err := working.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return facts, nil, fmt.Errorf("service: reading the head of %s: %w", repository.WorkingPath, err)
	}
	facts.Branch, facts.Head = branch, head
	facts.Target = custody.TargetOf(repository.WorkingPath, branch)
	records, err := s.store.RunsForRepository(ctx, repository.ID)
	if err != nil {
		return facts, nil, err
	}
	newest := -1
	for i, record := range records {
		if record.Branch == branch {
			newest = i
			break
		}
	}
	if newest < 0 {
		return facts, nil, nil
	}
	latest := records[newest]
	record, err := s.reconcile(ctx, latest.ID)
	if err != nil {
		return facts, nil, err
	}
	facts.Run = &record
	gate, err := s.gateRepositoryOf(ctx, repository, record.ID)
	if err != nil {
		return facts, nil, err
	}
	if !record.Status.Recognized() || !runs.Finished(record.Status) {
		return facts, gate, nil
	}
	facts.Position, err = s.positionOf(ctx, record.ID)
	if err != nil {
		return facts, nil, err
	}
	facts.Copy, err = s.copyOf(ctx, record)
	if err != nil {
		return facts, nil, err
	}
	return facts, gate, nil
}

// positionOf is where a run's last checkpoint says its work stood. A run
// with no checkpoint has no recorded position, which custody refuses on.
func (s *Service) positionOf(ctx context.Context, runID string) (custody.Position, error) {
	latest, err := s.checkpoints.Latest(ctx, runID)
	if errors.Is(err, graph.ErrNoSuchRun) {
		return custody.Position{}, nil
	}
	if err != nil {
		return custody.Position{}, fmt.Errorf("service: reading the position of run %s: %w", runID, err)
	}
	text := func(key pipeline.Key) string {
		if v, ok := latest.State.Get(string(key)); ok {
			t, _ := v.Text()
			return t
		}
		return ""
	}
	head := text(pipeline.KeyHead)
	return custody.Position{Recorded: head != "", Head: head, Pushed: text(pipeline.KeyPushed)}, nil
}

// copyOf reads a run's isolated copy as it stands, where internal/home
// places it.
func (s *Service) copyOf(ctx context.Context, record store.Run) (custody.Copy, error) {
	path := s.home.Worktree(record.RepositoryID, record.ID)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return custody.Copy{Path: path}, nil
	} else if err != nil {
		return custody.Copy{}, fmt.Errorf("service: reading the isolated copy of run %s: %w", record.ID, err)
	}
	copyRepo, err := vcs.OpenWorktree(ctx, path, s.git()...)
	if err != nil {
		return custody.Copy{}, fmt.Errorf("service: opening the isolated copy of run %s: %w", record.ID, err)
	}
	inv, err := copyRepo.Inventory(ctx)
	if err != nil {
		return custody.Copy{}, fmt.Errorf("service: reading the isolated copy of run %s: %w", record.ID, err)
	}
	return custody.Copy{Standing: true, Path: path, Head: inv.Head.Commit, Dirty: inv.Dirty, Untracked: inv.Untracked}, nil
}
