package service

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The two state publishers beside runs.go's publishRunState and workers.go's
// publishTaskEvent: a task's resolved state and a hold opening or closing.
// Both are ipc.ClassState, so a stream that overflows collapses them into its
// gap marker rather than dropping them, and both carry a revision from the
// one delivery counter every state event of this service's open stream draws
// on, so a consumer orders the four kinds against each other.
//
// # task.state has one reader
//
// A task's state is resolved, never stored, so "it changed" is a comparison
// between two resolutions. The supervisor loop's pass already resolves every
// task on every pass, so it is the one reader that publishes: after the pass
// has observed the fleet and before it decides anything, publishTaskChanges
// compares each task's Basis with the one the previous pass published and
// publishes exactly the tasks whose basis moved. Basis equality is what
// internal/taskstate offers as the cheap question of whether an answer could
// have changed, and the comparison is made here rather than on the state
// word, because a task whose state word stayed Running while its run or its
// newest entry changed is a change a consumer reconciling a view has to see.
// A task not published before is a change, so the first pass of a service
// publishes every task once. The memory is this service's and nothing
// durable, so a restart publishes every task again, which is right: every
// subscriber attached fresh too.
//
// What that does not promise is a publication the instant a record moves:
// the pass runs at the beacon interval, so a change is published within one
// interval of being made, and two changes inside one interval publish once,
// as the later basis. The task surface answers the current state at any
// time, and a consumer that needs the instant reads it there.
//
// # hold.state is published where a hold is written
//
// Every site in this service that writes a hold publishes it as the store
// returned it: storeTaskReport, where an investigation's report registers
// one per ask finding; the checks-hold closure, where the one closure PRD
// section 8 lets the program make resolves the hold in the same transaction
// as the run's move; consent.go's registration of a standing-consent hold,
// published open before its answer is given, and its resolution; local.go's
// approval hold, the landing that closes it and its other answers; steer.go's
// stop hold; taskholds.go's three registrations (the
// relaunch bound, the relaunch with no sweep and the dispatch with no sweep)
// and its answer to a task's hold; workerfallback.go's hold; and
// subordinates.go's delegation hold, its answer, and the holds an ended
// delegation closes. The payload is the store.Hold record, on machine's rule
// that a record travels as its owner's type.

// published is what this service has published about each task: the basis of
// the last task.state it sent, keyed by task. It is guarded by its own mutex
// rather than the service's, because the loop holds no service lock across a
// pass and must not take one to publish.
type published struct {
	mu    sync.Mutex
	bases map[string]taskstate.Basis
}

// publishTaskChanges publishes task.state for every observed task whose basis
// differs from the one last published, and remembers the basis it published.
// It is called once per pass with the whole fleet.
func (s *Service) publishTaskChanges(observed []taskstate.Observed) {
	s.published.mu.Lock()
	defer s.published.mu.Unlock()
	if s.published.bases == nil {
		s.published.bases = make(map[string]taskstate.Basis)
	}
	for _, o := range observed {
		id := o.Snapshot.Task.ID
		if previous, ok := s.published.bases[id]; ok && sameBasis(previous, o.State.Basis) {
			continue
		}
		s.published.bases[id] = o.State.Basis
		s.publishTaskState(s.taskView(context.Background(), o))
	}
}

// sameBasis is Basis equality with the one time-valued field compared as an
// instant, so two reads of the same record decoded to the same instant
// compare equal whatever location each carries.
func sameBasis(a, b taskstate.Basis) bool {
	return a.RunID == b.RunID && a.RunStatus == b.RunStatus && a.RunUpdatedAt.Equal(b.RunUpdatedAt) &&
		a.LandedRun == b.LandedRun && a.NothingToDeliver == b.NothingToDeliver && a.LandedLocally == b.LandedLocally && a.Hold == b.Hold && a.CleanupRecorded == b.CleanupRecorded && a.CleanupRemoved == b.CleanupRemoved &&
		a.ReportStored == b.ReportStored && a.Relayed == b.Relayed &&
		a.EventSequence == b.EventSequence && a.EventKind == b.EventKind && a.Process == b.Process && a.Advancing == b.Advancing
}

// publishTaskState tells subscribers that a task's resolved state moved. A
// payload that cannot be encoded or published is logged and nothing else: a
// publication is not a record, and the pass that made it owes the record
// nothing further. A service with no publisher, which Open never builds and
// this package's internal tests build by hand, publishes nothing; the same
// holds for publishHoldState.
func (s *Service) publishTaskState(task machine.Task) {
	if s.events == nil {
		return
	}
	payload, err := json.Marshal(task)
	if err != nil {
		s.log.Printf("could not encode the state of task %s: %v", task.Record.ID, err)
		return
	}
	if err := s.events.Publish(ipc.Event{
		Type:     ipc.TypeTaskState,
		Revision: s.revision.Add(1),
		Payload:  payload,
	}); err != nil {
		s.log.Printf("could not publish the state of task %s: %v", task.Record.ID, err)
	}
}

// publishHoldState tells subscribers that a hold opened or closed, as the
// record stands. It is called with the hold the write returned, so what
// travels is what the store holds and not what the caller meant to write.
func (s *Service) publishHoldState(h store.Hold) {
	if s.events == nil {
		return
	}
	payload, err := json.Marshal(h)
	if err != nil {
		s.log.Printf("could not encode the state of hold %s: %v", h.Key, err)
		return
	}
	if err := s.events.Publish(ipc.Event{
		Type:     ipc.TypeHoldState,
		Revision: s.revision.Add(1),
		Payload:  payload,
	}); err != nil {
		s.log.Printf("could not publish the state of hold %s: %v", h.Key, err)
	}
}

// publishHoldByKey reads a hold back by its key and publishes it, for a site
// whose write returned the run and not the hold. A hold that cannot be read
// is logged and not published.
func (s *Service) publishHoldByKey(ctx context.Context, key string) {
	h, err := s.store.Hold(ctx, key)
	if err != nil {
		s.log.Printf("could not read hold %s to publish it: %v", key, err)
		return
	}
	s.publishHoldState(h)
}

// taskView is the one composition of a task's answer from an observation: the
// record, the state, what a paused worker said it is waiting on, and the
// investigation's stored report when one has been filed. The surface and the
// publisher both build a task through it, so the two cannot disagree about
// what a task carries.
//
// Waiting is filled exactly when the state is Paused, which only the event
// tier resolves, from the detail of the history entry the basis names by its
// sequence. The entry is read out of the snapshot the state was resolved
// from, so this is not a second read of the log and not the log's tail: the
// resolver decided which entry decided, and this reads that entry's words.
//
// Report is filled when the snapshot says a task_report row exists, from that
// row and the evidence copy's path, so a coordinator reading task.get sees
// the same body the store holds (P8) rather than inferring it from history.
// A row that cannot be decoded is omitted rather than inventing an empty
// report; Basis.ReportStored still says one was filed.
//
// Policy is taskPolicy's answer.
func (s *Service) taskView(ctx context.Context, o taskstate.Observed) machine.Task {
	t := machine.Task{Record: o.Snapshot.Task, State: o.State}
	if o.State.State == taskstate.Paused {
		for _, e := range o.Snapshot.Events {
			if e.Sequence == o.State.Basis.EventSequence {
				t.Waiting = e.Detail
				break
			}
		}
	}
	if _, ok := o.Snapshot.ReportStoredAt.Get(); ok {
		t.Report = s.investigationReport(ctx, o.Snapshot.Task.ID)
	}
	if o.Snapshot.Task.Domain != "" {
		// The decisions the domain's subordinate handed up about the task,
		// read from the escalation record; a read that fails is logged and
		// the answer carries none, which Basis says nothing about either
		// way, so nothing here invents an empty list as a fact.
		escalations, err := s.store.TaskEscalations(ctx, o.Snapshot.Task.ID)
		if err != nil {
			s.log.Printf("the escalations about task %s could not be read for its answer: %v", o.Snapshot.Task.ID, err)
		}
		t.Escalations = escalations
	}
	t.Policy = s.taskPolicy(ctx, o.Snapshot.Task)
	return t
}

// taskPolicy is the policy a task's answer states: the row of the posture
// the task's record holds, with its landing authority taken from codeHostOf,
// the decision a run of the task's repository starts from, so the answer and
// the run cannot disagree about where the change lands. It is nil, and the
// answer states no policy, whenever either fact is not known: the record
// holds no posture (an investigation, or a delivery task recorded before
// intake wrote one, whose push localPosture follows as the gate anyway), the
// task names no repository, the repository row cannot be read, or codeHostOf
// refuses the repository, so no run of it can start. A recorded posture
// outside the closed set cannot reach here, because the store refuses to
// read one back.
func (s *Service) taskPolicy(ctx context.Context, task store.Task) *config.Policy {
	posture, ok := task.Delivery.Get()
	if !ok {
		return nil
	}
	repositoryID, ok := task.RepositoryID.Get()
	if !ok {
		return nil
	}
	repository, err := s.store.Repository(ctx, repositoryID)
	if err != nil {
		s.log.Printf("task %s states no policy: its repository %s could not be read: %v", task.ID, repositoryID, err)
		return nil
	}
	host, err := codeHostOf(repository)
	if err != nil {
		return nil
	}
	policy, ok := posture.PolicyForNoCodeHost(host.none)
	if !ok {
		return nil
	}
	return &policy
}

// investigationReport reads one task's stored report into the shape task.get
// carries. It returns nil when the row is missing or its parsed bytes cannot
// be read as a findings.Report, so a caller treating absence as "no report
// on this answer" stays honest when the store could not answer.
func (s *Service) investigationReport(ctx context.Context, taskID string) *machine.InvestigationReport {
	stored, err := s.store.TaskReport(ctx, taskID)
	if err != nil {
		return nil
	}
	var report findings.Report
	if err := json.Unmarshal(stored.Report, &report); err != nil {
		s.log.Printf("could not decode the stored report of task %s: %v", taskID, err)
		return nil
	}
	out := &machine.InvestigationReport{
		Summary:       report.Summary,
		Findings:      report.Findings,
		Evidence:      report.Evidence,
		Uncertainties: report.Uncertainties,
		FollowUps:     report.FollowUps,
		RecordedAt:    stored.RecordedAt,
	}
	if path, err := s.home.TaskReport(taskID); err == nil {
		out.EvidenceFile = path
	}
	return out
}
