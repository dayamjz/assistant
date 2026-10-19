package service

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// PRD section 9's catching up: the one read assistant watch starts with, and
// the mark that moves what it filters.
//
// # Where each part is read from
//
// Every part is resolved from the records that own its facts on this read
// (P8), and none is read off the event log or the stream:
//
//   - Waiting on you is every open hold record, oldest first, from
//     store.OpenHolds, and every run whose record says held, which is a
//     run's hold in this build: nothing registers a hold row for a halt, so
//     the run's checkpoint decision is the decision and the run's answer is
//     its options. Each carries the existing answer path.
//   - Landed is the records internal/taskstate's terminal tier derived a
//     task's state from on this read: the merge record of the run its
//     resolution names as landed, and for a task it resolved Complete, its
//     cleanup record. It reads the resolution's own basis rather than
//     choosing among the records again, so the tier and this part cannot
//     name different records. What the seen mark holds is subtracted, and
//     machine.ShowLanded orders and bounds what is left.
//   - Under way is every task internal/supervisor's TaskInFlight holds in
//     flight, which is section 6's definition and the one the pull guard
//     counts, and every run this service is advancing, read off the slot
//     the way report reads it.
//   - Stopped without a decision is every task resolved Failed or Unknown.
//
// Only Landed is filtered. The other three are never filtered by anything
// but what they are, so an open decision is shown until it is answered.
// The digest is not composed here: machine.CatchUp computes it from the
// parts this function fills, so nothing in this file can make it disagree.
//
// Two consequences of reading the PRD's rows as written are left as they
// are rather than settled here. A task resolved Unknown is in flight under
// section 6 and stopped without a decision under section 9, so it is listed
// in both parts. A run attributed to a task under way is listed beside the
// task when a segment is advancing it, because the run is being advanced
// and the task is in flight, which are the two parts' own definitions.
//
// # The mark
//
// The mark names records and is never computed: markSeen writes exactly
// what the request names, through store.MarkSeen, which refuses anything
// that is not a landed record. The read names what it showed through
// machine.CatchUp.Shown, so a record recorded after the read is not in the
// mark and is still unseen. A read writes nothing to the seen mark. It is
// kept apart from the wake queue: acknowledging a wake is the coordinator's
// record that it handled one, and nothing here reads or writes the queue
// beyond the count the in-flight rule takes.

// catchUp answers catchup.read on the terms above.
func (s *Service) catchUp(ctx context.Context) (machine.CatchUp, error) {
	waiting, advancing, err := s.catchUpRuns(ctx)
	if err != nil {
		return machine.CatchUp{}, err
	}
	observed, err := s.tasks().Observe(ctx)
	if err != nil {
		return machine.CatchUp{}, err
	}
	unacknowledged, err := s.store.UnacknowledgedWakes(ctx)
	if err != nil {
		return machine.CatchUp{}, err
	}
	holds, err := s.store.OpenHolds(ctx)
	if err != nil {
		return machine.CatchUp{}, err
	}
	marks, err := s.store.SeenMarks(ctx)
	if err != nil {
		return machine.CatchUp{}, err
	}
	seen := make(map[store.SeenRecord]bool, len(marks))
	for _, m := range marks {
		seen[m.SeenRecord] = true
	}

	out := machine.CatchUp{
		Waiting:  []machine.Waiting{},
		Landed:   []machine.Landed{},
		UnderWay: machine.UnderWay{Tasks: []machine.Task{}, Runs: advancing},
		Stopped:  []machine.Task{},
	}
	byTask := make(map[string]machine.Task, len(observed))
	var unseen []machine.Landed
	for _, o := range observed {
		task := s.taskView(ctx, o)
		byTask[task.Record.ID] = task
		inFlight, decided := supervisor.TaskInFlight(o.State.State, unacknowledged[task.Record.ID])
		if inFlight || !decided {
			out.UnderWay.Tasks = append(out.UnderWay.Tasks, task)
		}
		if o.State.State == taskstate.Failed || o.State.State == taskstate.Unknown {
			out.Stopped = append(out.Stopped, task)
		}
		for _, l := range landedOf(o, task) {
			if !seen[l.Record()] {
				unseen = append(unseen, l)
			}
		}
	}
	out.Landed, out.MoreLanded = machine.ShowLanded(unseen)
	if out.Landed == nil {
		out.Landed = []machine.Landed{}
	}

	type dated struct {
		waiting machine.Waiting
		at      waitingSince
	}
	var all []dated
	for _, h := range holds {
		hold := h
		w := machine.Waiting{Hold: &hold}
		if id, ok := h.TaskID.Get(); ok {
			if task, found := byTask[id]; found {
				w.Task = &task
			}
		}
		answer, err := s.answeringHold(ctx, h)
		if err != nil {
			return machine.CatchUp{}, err
		}
		w.Answer = answer
		all = append(all, dated{waiting: w, at: waitingSince{at: h.OpenedAt.UnixNano(), key: h.Key}})
	}
	for _, r := range waiting {
		run := r.run
		all = append(all, dated{
			waiting: machine.Waiting{Run: &run, Answer: machine.RunAnswering(r.workingPath, run.Record.Branch)},
			at:      waitingSince{at: run.Record.UpdatedAt.UnixNano(), key: run.Record.ID},
		})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at.before(all[j].at) })
	for _, d := range all {
		out.Waiting = append(out.Waiting, d.waiting)
	}

	supervision := s.supervision(ctx)
	out.Supervision = &supervision
	return out, nil
}

// waitingSince orders the waiting part oldest first: a hold by when it was
// registered and a run by its record's last change, with the key breaking a
// tie. The run's is an approximation: the record keeps no time of the move
// to held as such, so a column written on the run after that move would
// date the run later than it held.
type waitingSince struct {
	at  int64
	key string
}

func (w waitingSince) before(o waitingSince) bool {
	if w.at != o.at {
		return w.at < o.at
	}
	return w.key < o.key
}

// heldRun is a run waiting on a person, with the checkout its answer is
// given from.
type heldRun struct {
	run         machine.Run
	workingPath string
}

// catchUpRuns reads every unfinished run of the home, reconciled against its
// checkpoint first as every operation on a run is, and returns the ones
// held at a decision and the ones a segment is advancing, each as report
// answers for it.
func (s *Service) catchUpRuns(ctx context.Context) ([]heldRun, []machine.Run, error) {
	active, err := s.activeRuns(ctx)
	if err != nil {
		return nil, nil, err
	}
	var held []heldRun
	advancing := []machine.Run{}
	for _, record := range active {
		reconciled, err := s.reconcile(ctx, record.ID)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case reconciled.Status == store.RunHeld:
			view, err := s.view(ctx, record.ID)
			if err != nil {
				return nil, nil, err
			}
			repository, err := s.store.Repository(ctx, reconciled.RepositoryID)
			if err != nil {
				return nil, nil, err
			}
			held = append(held, heldRun{run: view, workingPath: repository.WorkingPath})
		case s.isAdvancing(record.ID):
			view, err := s.view(ctx, record.ID)
			if err != nil {
				return nil, nil, err
			}
			advancing = append(advancing, view)
		}
	}
	return held, advancing, nil
}

// answeringHold is how one open hold record is answered: given the task for
// a hold naming a task and no run, which is what task.respond closes, and
// from the run's checkout otherwise. A hold naming a run that no longer has
// a record is answered given its task when it names one, and with --answer
// alone otherwise, because no checkout is left to name.
func (s *Service) answeringHold(ctx context.Context, h store.Hold) (machine.Answering, error) {
	task, named := h.TaskID.Get()
	run, ofRun := h.RunID.Get()
	if !ofRun {
		if named {
			return machine.TaskAnswering(task), nil
		}
		return machine.Answering{Args: []string{"--answer"}}, nil
	}
	record, err := s.store.Run(ctx, run)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if named {
			return machine.TaskAnswering(task), nil
		}
		return machine.Answering{Args: []string{"--answer"}}, nil
	case err != nil:
		return machine.Answering{}, err
	}
	repository, err := s.store.Repository(ctx, record.RepositoryID)
	if err != nil {
		return machine.Answering{}, err
	}
	return machine.RunAnswering(repository.WorkingPath, record.Branch), nil
}

// landedOf is the landed records the terminal tier derived o's state from:
// the merge record of the run the resolution names as landed, and for a
// task resolved Complete, the cleanup record that made it so.
func landedOf(o taskstate.Observed, task machine.Task) []machine.Landed {
	var out []machine.Landed
	if run := o.State.Basis.LandedRun; run != "" {
		for _, rec := range o.Snapshot.MergeRecords {
			if rec.Run == run && rec.End == store.PullRequestMerged {
				merge := rec
				out = append(out, machine.Landed{Task: task, Merge: &merge})
				break
			}
		}
	}
	if o.State.State == taskstate.Complete {
		if cleanup, ok := o.Snapshot.Cleanup.Get(); ok && cleanup.RemovedAt.IsKnown() {
			out = append(out, machine.Landed{Task: task, Cleanup: &cleanup})
		}
	}
	return out
}

// markSeen answers catchup.mark: it marks seen exactly the records the
// request names, through store.MarkSeen, and nothing else. A worker is
// refused, an empty request is ipc.ErrInvalidRequest, and a record that is
// not a landed one refuses the whole batch as ipc.ErrInvalidRequest naming
// it, with nothing written.
func (s *Service) markSeen(ctx context.Context, req ipc.Request, r machine.MarkSeenRequest) (machine.Marked, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.Marked{}, err
	}
	if len(r.Records) == 0 {
		return machine.Marked{}, fmt.Errorf("%w: a mark names no records", ipc.ErrInvalidRequest)
	}
	marks, err := s.store.MarkSeen(ctx, r.Records)
	if err != nil {
		var refused *store.SeenRecordError
		if errors.As(err, &refused) {
			return machine.Marked{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
		}
		return machine.Marked{}, err
	}
	return machine.Marked{Marks: marks}, nil
}
