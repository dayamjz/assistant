package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// handle serves one request. The method table is internal/ipc's and it is
// closed, so a method that reaches here is one this build serves and one this
// switch has a row for; a method added to that table without a row here
// reaches the default and is refused rather than answered with silence.
//
// Authority is decided before this is called. internal/ipc refuses a
// restricted method to a caller its Ancestry places inside an active
// validation stage, so nothing here asks whether a caller is contained. The
// one question a handler asks of a caller is the other relation, with its
// three answers: a task's worker, a domain's subordinate coordinator, or
// neither, which is the coordinator's own client or a person's. workers.go
// derives the worker half and subordinates.go's placeRequest composes the
// whole from the same credentials and the same parent chain, asking the
// worker sets first. It is asked for the methods whose answer turns on it:
// workers.go's six and wakewait.go's wakes.wait and wakes.subscribe, where
// a worker is refused and a subordinate is served on the route of its
// domain; coordinator.go's four, steer.go's three and peek.go's task.peek,
// where a worker is refused and a subordinate is served within its domain
// and refused ErrOutsideDomain past it, or refused ErrSubordinate outright
// for a decision it hands up; taskholds.go's task.respond, catchup.go's
// catchup.mark, custody.go's branch.sync, the two delegation methods and
// the stream, where a subordinate is refused as a worker is; and the run
// methods and the service's lifecycle below, where refuseSubordinateOnly
// refuses a subordinate and holds a worker to nothing new. tasks.list and
// task.get, open methods, ask placeOpen so a subordinate reads its domain
// alone. wakes.subscribe is a stream, so internal/ipc asks this only
// whether the caller may hold it, and an error here is its refusal.
func (s *Service) handle(ctx context.Context, req ipc.Request) (json.RawMessage, error) {
	switch req.Method {
	case ipc.MethodHealth:
		return answer(machine.Health{Ready: true, Home: s.home.Root(), Build: s.build, Instance: s.instance})
	case ipc.MethodStatus:
		return call(ctx, req, s.status)
	case ipc.MethodRunsList:
		return call(ctx, req, s.listRuns)
	case ipc.MethodRunGet:
		return call(ctx, req, func(ctx context.Context, r machine.RunRequest) (machine.Run, error) {
			if _, err := s.reconcile(ctx, r.Run); err != nil {
				return machine.Run{}, err
			}
			return s.view(ctx, r.Run)
		})
	case ipc.MethodRunStart:
		return call(ctx, req, func(ctx context.Context, r machine.StartRequest) (machine.Run, error) {
			if err := s.refuseSubordinateOnly(req); err != nil {
				return machine.Run{}, err
			}
			return s.startTo(ctx, req, r)
		})
	case ipc.MethodRunRerun:
		return call(ctx, req, func(ctx context.Context, r machine.RerunRequest) (machine.Run, error) {
			if err := s.refuseSubordinateOnly(req); err != nil {
				return machine.Run{}, err
			}
			return s.rerun(ctx, r)
		})
	case ipc.MethodRunRespond:
		return call(ctx, req, func(ctx context.Context, r machine.RespondRequest) (machine.Run, error) {
			if err := s.refuseSubordinateOnly(req); err != nil {
				return machine.Run{}, err
			}
			return s.respondTo(ctx, req, r)
		})
	case ipc.MethodRunCancel:
		return call(ctx, req, func(ctx context.Context, r machine.CancelRequest) (machine.Run, error) {
			if err := s.refuseSubordinateOnly(req); err != nil {
				return machine.Run{}, err
			}
			return s.cancel(ctx, r)
		})
	case ipc.MethodBranchSync:
		return call(ctx, req, func(ctx context.Context, r machine.SyncRequest) (machine.Plan, error) {
			return s.syncBranch(ctx, req, r)
		})
	case ipc.MethodGateAdmit:
		return call(ctx, req, func(ctx context.Context, r machine.GateRequest) (machine.Admission, error) {
			return s.admit(ctx, req, r)
		})
	case ipc.MethodGateNotify:
		return call(ctx, req, func(ctx context.Context, r machine.GateRequest) (machine.Notification, error) {
			return s.notify(ctx, req, r)
		})
	case ipc.MethodStageReport:
		return call(ctx, req, s.stageReport)
	case ipc.MethodTasksList:
		return call(ctx, req, func(ctx context.Context, _ struct{}) (machine.Tasks, error) {
			return s.listTasksFor(ctx, req)
		})
	case ipc.MethodTaskGet:
		return call(ctx, req, func(ctx context.Context, r machine.TaskRequest) (machine.Task, error) {
			return s.taskFor(ctx, req, r)
		})
	case ipc.MethodTaskEvent:
		return call(ctx, req, func(ctx context.Context, r machine.TaskEventRequest) (machine.Appended, error) {
			return s.taskEvent(ctx, req, r)
		})
	case ipc.MethodTaskRespond:
		return call(ctx, req, func(ctx context.Context, r machine.TaskRespondRequest) (machine.TaskAnswered, error) {
			return s.respondTask(ctx, req, r)
		})
	case ipc.MethodKeptWorkAnswer:
		return call(ctx, req, func(ctx context.Context, r machine.KeptWorkAnswerRequest) (machine.KeptWorkAnswered, error) {
			return s.answerKeptWork(ctx, req, r)
		})
	case ipc.MethodTaskCreate:
		return call(ctx, req, func(ctx context.Context, r machine.TaskCreateRequest) (machine.TaskCreated, error) {
			return s.createTask(ctx, req, r)
		})
	case ipc.MethodTaskPromote:
		return call(ctx, req, func(ctx context.Context, r machine.TaskPromoteRequest) (machine.TaskPromoted, error) {
			return s.promoteTask(ctx, req, r)
		})
	case ipc.MethodTaskRelaunch:
		return call(ctx, req, func(ctx context.Context, r machine.TaskRequest) (machine.TaskRelaunched, error) {
			return s.relaunchTaskFor(ctx, req, r)
		})
	case ipc.MethodTaskRelay:
		return call(ctx, req, func(ctx context.Context, r machine.TaskRequest) (machine.TaskRelayed, error) {
			return s.relayTask(ctx, req, r)
		})
	case ipc.MethodTaskMessage:
		return call(ctx, req, func(ctx context.Context, r machine.TaskMessageRequest) (machine.TaskMessaged, error) {
			return s.messageTask(ctx, req, r)
		})
	case ipc.MethodTaskStop:
		return call(ctx, req, func(ctx context.Context, r machine.TaskRequest) (machine.TaskStopped, error) {
			return s.stopTask(ctx, req, r)
		})
	case ipc.MethodTaskPeek:
		return call(ctx, req, func(ctx context.Context, r machine.TaskRequest) (machine.TaskPeek, error) {
			return s.peekTask(ctx, req, r)
		})
	case ipc.MethodTaskInterrupt:
		return call(ctx, req, func(ctx context.Context, r machine.TaskRequest) (struct{}, error) {
			return s.interruptTask(ctx, req, r)
		})
	case ipc.MethodWakesList:
		return call(ctx, req, func(ctx context.Context, r machine.WakesRequest) (machine.Wakes, error) {
			return s.listWakes(ctx, req, r)
		})
	case ipc.MethodWakesPeek:
		return call(ctx, req, func(ctx context.Context, r machine.WakesRequest) (machine.QueuedWakes, error) {
			return s.peekWakes(ctx, req, r)
		})
	case ipc.MethodWakesAcknowledge:
		return call(ctx, req, func(ctx context.Context, r machine.AcknowledgeRequest) (machine.Acknowledged, error) {
			return s.acknowledgeWakes(ctx, req, r)
		})
	case ipc.MethodWakesWait:
		return call(ctx, req, func(ctx context.Context, r machine.WakeWaitRequest) (machine.Wakes, error) {
			return s.waitWakes(ctx, req, r)
		})
	case ipc.MethodWakesSubscribe:
		// The wake stream is internal/ipc's to open and pump; what the
		// handler is asked is whether this caller may hold it, the answer
		// wakes.list's, and for a subordinate coordinator which stream it
		// holds, its own domain's, answered as an ipc.StreamChoice.
		choice, err := s.admitWakeStream(req)
		if err != nil || choice.Name == "" {
			return nil, err
		}
		return json.Marshal(choice)
	case ipc.MethodSupervisionAssert:
		return call(ctx, req, func(ctx context.Context, _ struct{}) (supervisor.Arm, error) {
			return s.assertSupervision(ctx, req)
		})
	case ipc.MethodCatchUpRead:
		return call(ctx, req, func(ctx context.Context, _ struct{}) (machine.CatchUp, error) {
			return s.catchUp(ctx)
		})
	case ipc.MethodCatchUpMark:
		return call(ctx, req, func(ctx context.Context, r machine.MarkSeenRequest) (machine.Marked, error) {
			return s.markSeen(ctx, req, r)
		})
	case ipc.MethodUsageRead:
		return call(ctx, req, func(ctx context.Context, r machine.UsageRequest) (machine.Usage, error) {
			return s.usage(ctx, r)
		})
	case ipc.MethodRunLog:
		return call(ctx, req, s.runLog)
	case ipc.MethodDomainDelegate:
		return call(ctx, req, func(ctx context.Context, r machine.DomainDelegateRequest) (machine.Delegated, error) {
			return s.delegateDomain(ctx, req, r)
		})
	case ipc.MethodDomainEnd:
		return call(ctx, req, func(ctx context.Context, r machine.DomainEndRequest) (machine.DelegationEnded, error) {
			return s.endDelegation(ctx, req, r)
		})
	case ipc.MethodServiceStop:
		return call(ctx, req, func(ctx context.Context, r machine.LifecycleRequest) (machine.Lifecycle, error) {
			if err := s.refuseSubordinateOnly(req); err != nil {
				return machine.Lifecycle{}, err
			}
			return s.lifecycle(ctx, req, r, false)
		})
	case ipc.MethodServiceRestart:
		return call(ctx, req, func(ctx context.Context, r machine.LifecycleRequest) (machine.Lifecycle, error) {
			if err := s.refuseSubordinateOnly(req); err != nil {
				return machine.Lifecycle{}, err
			}
			return s.lifecycle(ctx, req, r, true)
		})
	default:
		return nil, fmt.Errorf("%w: %s is in the method table and this service has no answer for it", ipc.ErrUnknownMethod, req.Method)
	}
}

// call decodes a request body and encodes what the handler answered. It exists
// so that every method decodes the same way and a body that does not decode is
// ErrInvalidRequest rather than a zero value the handler acts on.
//
// An empty body decodes to the zero request, because a method whose parameters
// are all optional is legitimately called with none.
func call[Req, Res any](ctx context.Context, req ipc.Request, handler func(context.Context, Req) (Res, error)) (json.RawMessage, error) {
	var params Req
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ipc.ErrInvalidRequest, req.Method, err)
		}
	}
	result, err := handler(ctx, params)
	if err != nil {
		return nil, err
	}
	return answer(result)
}

// answer encodes a result.
func answer(v any) (json.RawMessage, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding the answer: %w", ipc.ErrInternal, err)
	}
	return body, nil
}

// status reports repository, gate, service, active run, and local branch
// state, which is PRD section 9's list for that command.
//
// Every part it cannot report carries its own detail rather than failing the
// whole answer: a working copy with no gate is a legitimate thing to ask about
// and is exactly the state assistant init exists to change. The local branch
// state includes PRD section 11's classification, through custodyStatus,
// which fetches nothing and writes nothing in the working copy. Kept work
// is listed on keptWorkInStatus's terms, whether or not the working copy has
// a repository record.
func (s *Service) status(ctx context.Context, req machine.StatusRequest) (machine.Status, error) {
	build := s.build
	out := machine.Status{
		Home: s.home.Root(),
		Service: machine.Service{
			Running: true,
			Socket:  s.home.Socket(),
			Build:   &build,
		},
	}
	out.Branch = s.branchState(ctx, req.WorkingPath)
	if binding, err := s.store.GateBinding(ctx, req.WorkingPath); err == nil {
		out.Gate = &machine.Gate{Present: true, ID: binding.GateID}
	} else {
		out.Gate = &machine.Gate{Detail: "this working copy is not bound to a gate; run assistant init"}
	}
	repository, err := s.repositoryAt(ctx, req.WorkingPath)
	if err != nil {
		if !errors.Is(err, ErrNoRepository) {
			return machine.Status{}, err
		}
		if out.KeptWork, err = s.keptWorkInStatus(ctx, ""); err != nil {
			return machine.Status{}, err
		}
		out.Detail = "this working copy has no repository record; run assistant init"
		return out, nil
	}
	out.Repository = &repository
	if out.KeptWork, err = s.keptWorkInStatus(ctx, repository.ID); err != nil {
		return machine.Status{}, err
	}
	if out.Branch == nil || out.Branch.Name == "" {
		return out, nil
	}
	active, found, err := s.activeRun(ctx, repository.ID, out.Branch.Name)
	if err != nil {
		return machine.Status{}, err
	}
	if !found {
		parked, err := s.parkedRun(ctx, repository.ID, out.Branch.Name)
		if err != nil {
			return machine.Status{}, err
		}
		out.ParkedRun = parked
		out.Custody = s.custodyStatus(ctx, repository)
		return out, nil
	}
	if _, err := s.reconcile(ctx, active.ID); err != nil {
		return machine.Status{}, err
	}
	view, err := s.view(ctx, active.ID)
	if err != nil {
		return machine.Status{}, err
	}
	out.ActiveRun = &view
	out.Custody = s.custodyStatus(ctx, repository)
	return out, nil
}

// keptWorkInStatus is the open kept work a status lists: every entry whose
// project is repository, whichever branch it was kept on, because PRD
// section 6 has kept work surface in status at once and holds a project
// rather than a branch, and every entry whose owner names no project, an
// investigation task's with no repository among them, because such an entry
// belongs to no working copy and so has to be shown by every status for a
// status to show it at all. An empty repository is a working copy with no
// repository record, which lists the second kind alone.
func (s *Service) keptWorkInStatus(ctx context.Context, repository string) ([]store.KeptWork, error) {
	open, err := s.store.OpenKeptWork(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.KeptWork
	for _, k := range open {
		project, named := k.RepositoryID.Get()
		if !named || (repository != "" && project == repository) {
			out = append(out, k)
		}
	}
	return out, nil
}

// parkedRun is the answer for a branch's latest run when its record names the
// bound that parked it, and nil when the branch has no run or its latest run
// ended any other way. It reads the record's park rather than the checkpoint's
// status, so status and runs say a run parked on the one record that says so.
func (s *Service) parkedRun(ctx context.Context, repositoryID, branch string) (*machine.Run, error) {
	records, err := s.store.RunsForRepository(ctx, repositoryID)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.Branch != branch {
			continue
		}
		if !record.Park.IsKnown() {
			return nil, nil
		}
		view, err := s.view(ctx, record.ID)
		if err != nil {
			return nil, err
		}
		return &view, nil
	}
	return nil, nil
}

// branchState reads the working copy the caller is standing in. A working copy
// that cannot be read carries the reason rather than failing the report, since
// the rest of a status is still worth having.
func (s *Service) branchState(ctx context.Context, workingPath string) *machine.Branch {
	state := &machine.Branch{WorkingPath: workingPath}
	working, err := vcs.OpenWorktree(ctx, workingPath, s.git()...)
	if err != nil {
		state.Detail = err.Error()
		return state
	}
	if branch, err := working.HeadBranch(ctx); err == nil {
		state.Name = branch
	} else {
		state.Detail = err.Error()
	}
	if head, err := working.ResolveCommit(ctx, "HEAD"); err == nil {
		state.Head = head
	}
	return state
}

// listRuns reports a repository's runs, newest first.
func (s *Service) listRuns(ctx context.Context, req machine.RunsRequest) (machine.Runs, error) {
	repository, err := s.repositoryAt(ctx, req.WorkingPath)
	if err != nil {
		return machine.Runs{}, err
	}
	records, err := s.store.RunsForRepository(ctx, repository.ID)
	if err != nil {
		return machine.Runs{}, err
	}
	if req.Limit > 0 && len(records) > req.Limit {
		records = records[:req.Limit]
	}
	out := machine.Runs{Runs: records}
	for _, record := range records {
		if park, ok := record.Park.Get(); ok {
			out.Parked = append(out.Parked, s.parkedReport(ctx, record.ID, park))
		}
		if listed, ok := s.listedStanding(ctx, record); ok {
			out.Standings = append(out.Standings, listed)
		}
	}
	return out, nil
}

// listedStanding reads one listed run's standing on the terms report reads a
// run's: the record, its merge record, whether a segment is advancing it, and
// where its execution stands at its latest checkpoint, in that order, decided
// through machine.ListedStandingOf. A run with no checkpoint is the
// unrecorded execution it is. A merge record or a checkpoint that is there
// and cannot be read leaves the run with no standing, false, and the service
// log saying why, rather than failing the listing, which is about every run
// and not this one, and rather than reading the run as having no position,
// which would tell a person to end a run whose history is intact; the
// listing then reads that run from its record alone.
func (s *Service) listedStanding(ctx context.Context, record store.Run) (machine.ListedStanding, bool) {
	standing := machine.Standing{
		Record:     record.Status,
		Park:       record.Park,
		NoCodeHost: record.NoCodeHost,
		Execution:  machine.ExecutionUnrecorded(),
	}
	merge, ended, err := s.store.MergeRecord(ctx, record.ID)
	if err != nil {
		s.log.Printf("listing run %s from its record alone: its merge record could not be read: %v", record.ID, err)
		return machine.ListedStanding{}, false
	}
	if ended {
		standing.PullRequestEnd = merge.End
	}
	standing.Advancing = s.isAdvancing(record.ID)
	latest, err := s.checkpoints.Latest(ctx, record.ID)
	switch {
	case err == nil:
		read := latest.Result()
		standing.Execution = machine.ExecutionAt(read.Status, read.State)
	case errors.Is(err, graph.ErrNoSuchRun):
	default:
		s.log.Printf("listing run %s from its record alone: its position could not be read: %v", record.ID, err)
		return machine.ListedStanding{}, false
	}
	return machine.ListedStandingOf(record.ID, standing), true
}

// parkedReport reads the report the stage a park names recorded, off the
// checkpoint the park record names. A checkpoint or a report that cannot be
// read is said in Detail rather than failing the listing, which is about every
// run and not this one, and rather than leaving Report absent with nothing
// saying why, which would read as a stage that never ran.
func (s *Service) parkedReport(ctx context.Context, runID string, park store.RunPark) machine.ParkedReport {
	out := machine.ParkedReport{Run: runID, Stage: park.Stage}
	stage, ok := pipeline.ParseStage(park.Stage)
	if !ok {
		return out
	}
	// A parked run's record has ended, so nothing in this build writes past
	// the checkpoint its park was read off and the latest is that one; the
	// history is read only when it is not.
	history := []graph.Checkpoint{}
	latest, err := s.checkpoints.Latest(ctx, runID)
	if err == nil && latest.Seq == park.Checkpoint {
		history = append(history, latest)
	} else if history, err = s.checkpoints.History(ctx, runID); err != nil {
		out.Detail = fmt.Sprintf("the run's checkpoints could not be read: %v", err)
		return out
	}
	for _, cp := range history {
		if cp.Seq != park.Checkpoint {
			continue
		}
		if !pipeline.StageRan(cp.State, stage) {
			return out
		}
		report, err := pipeline.StageReport(cp.State, stage)
		if err != nil {
			out.Detail = fmt.Sprintf("the %s stage's report could not be read: %v", stage, err)
			return out
		}
		out.Report = &report
		return out
	}
	out.Detail = fmt.Sprintf("the run has no checkpoint %d, which its park record names", park.Checkpoint)
	return out
}

// listTasksIn reports fleet work with its resolved current state.
//
// The state is resolved by internal/taskstate from the records that own the
// facts, not read from a stored copy and not taken from the event log, which
// P8 and P14 make the only two ways to get it wrong, with the terminal of
// each task's latest attempt probed through this service's Terminals, so a
// launched task reads Running from the process tier while its worker runs
// and what precedence gives with Dead once it has exited. Each task is
// composed through taskView, which is also what the supervisor loop
// publishes, so a paused worker's declared wait reaches the answer here on
// the same terms it reaches the stream. A caller placed as a domain's
// subordinate coordinator is answered its domain's tasks alone, which
// listTasksFor decides and scoped carries: set, only the tasks whose row
// names domain are answered.
func (s *Service) listTasksIn(ctx context.Context, domain string, scoped bool) (machine.Tasks, error) {
	observed, err := s.tasks().Observe(ctx)
	if err != nil {
		return machine.Tasks{}, err
	}
	out := machine.Tasks{Tasks: make([]machine.Task, 0, len(observed))}
	for _, o := range observed {
		if scoped && o.Snapshot.Task.Domain != domain {
			continue
		}
		out.Tasks = append(out.Tasks, s.taskView(ctx, o))
	}
	supervision := s.supervision(ctx)
	out.Supervision = &supervision
	return out, nil
}

// listTasksFor is tasks.list for a request: the whole fleet for the
// coordinator, a person and a worker, and the caller's domain alone for a
// subordinate coordinator, PRD section 9's "within its domain". The method
// is open, so a peer the platform cannot identify is answered as before,
// since nothing places it anywhere; a chain that cannot be read while a
// subordinate is live is refused, on placement's terms.
func (s *Service) listTasksFor(ctx context.Context, req ipc.Request) (machine.Tasks, error) {
	p, err := s.placeOpen(req)
	if err != nil {
		return machine.Tasks{}, err
	}
	return s.listTasksIn(ctx, p.domain(), p.isSubordinate)
}

// taskFor is task.get for a request, on listTasksFor's terms: a subordinate
// naming a task outside its domain is refused with ipc.ErrOutsideDomain.
func (s *Service) taskFor(ctx context.Context, req ipc.Request, r machine.TaskRequest) (machine.Task, error) {
	p, err := s.placeOpen(req)
	if err != nil {
		return machine.Task{}, err
	}
	if err := s.scopeTask(ctx, req, p, r.Task); err != nil {
		return machine.Task{}, err
	}
	return s.task(ctx, r)
}

// placeOpen is placeRequest for an open method: a peer the kernel cannot
// identify is placed as nobody rather than refused, because an open method
// is served without credentials and nothing can place such a peer in a
// domain; every other failure to place is the refusal placeRequest gives.
func (s *Service) placeOpen(req ipc.Request) (placedPeer, error) {
	p, err := s.placeRequest(req)
	if errors.Is(err, ipc.ErrUnidentifiedPeer) {
		return placedPeer{}, nil
	}
	return p, err
}

// refuseSubordinateOnly refuses a request from a peer placed as a domain's
// subordinate coordinator with ipc.ErrSubordinate and admits every other
// peer, a worker included, for the methods PRD section 9 refuses a
// subordinate and which held a worker to nothing before: the run methods
// and the service's lifecycle. It changes nothing for a worker.
func (s *Service) refuseSubordinateOnly(req ipc.Request) error {
	p, err := s.placeRequest(req)
	if err != nil {
		return err
	}
	if p.isSubordinate {
		return fmt.Errorf("%w: %s is the subordinate coordinator of domain %s, and %s is one it does not hold",
			ipc.ErrSubordinate, peerOf(req), p.subordinate.Domain, req.Method)
	}
	return nil
}

// supervision is the pull guard over this home, computed for an answer the
// service gives about its fleet. The in-flight reading is this service's own,
// through the resolver that knows which runs it is driving; the beacon, the
// instance file and the lock are read the way a command with the service
// down reads them, through internal/supervisor/feed, so the two surfaces
// differ in that one input and not in the predicate. The lock probe answers
// Held from inside the process that holds it, which is this one, so what the
// guard adds here is whether a loop under this instance is writing a fresh
// beacon, which supervise.go's loop does from its own select; a fleet with
// work in flight reads unsupervised exactly while that loop is not writing.
func (s *Service) supervision(ctx context.Context) machine.Supervision {
	inFlight, _ := feed.InFlightFrom(ctx, s.store, s.tasks())
	verdict := feed.GuardWith(ctx, s.home, inFlight, supervisor.FreshnessFor(s.cfg.SupervisionBeaconInterval), time.Now())
	return machine.SupervisionOf(verdict)
}

// GlobalConfig resolves the operator's own configuration file alone, with
// the schema defaults for everything it leaves unset and no repository
// layer, which is what the service itself resolves at start for its
// service-scoped decisions. It is exported for the one command that acts
// with the service down and needs a global-only value, the status command's
// pull guard, so that reading the operator's file has one owner. A missing
// file is the defaults; a file that does not parse is an error.
func GlobalConfig(path string) (config.Config, error) {
	_, cfg, _, err := resolveConfig(path)
	return cfg, err
}

// task reports one piece of fleet work with its resolved current state,
// composed through taskView on listTasks' terms.
func (s *Service) task(ctx context.Context, req machine.TaskRequest) (machine.Task, error) {
	o, err := s.tasks().ObserveTask(ctx, req.Task)
	if err != nil {
		return machine.Task{}, err
	}
	return s.taskView(ctx, o), nil
}

// tasks is the resolver over the service's store, built through reader with
// the probe of the service's Terminals under the per-task bound alone, so a
// provider that does not answer costs a read reconcileProbeTimeout per task
// and never hangs it. In production Terminals is the service's own session
// provider, which terminals.go states. Whether a run is advancing is this
// service's own fact, read off the slot map the way report reads it, so a
// task's resolution can say whether its validation is being driven and not
// only that a run is recorded as running.
func (s *Service) tasks() *taskstate.Reader { return s.reader(s.boundedProbe(context.Background())) }

// reader is the one constructor of a task reader in this service: every
// reader is built here, over the service's store and its advancing fact, with
// whichever probe the caller bounded. The task surface, the queue,
// reconciliation and the supervisor loop therefore differ in the bound on
// the probe and in nothing else, so two readers cannot disagree about which
// terminal a task has.
func (s *Service) reader(probe func(ctx context.Context, sessionRef string) taskstate.Liveness) *taskstate.Reader {
	return taskstate.NewReader(s.store, probe, s.isAdvancing)
}

// stageReport is how an agent running a stage returns its result. It is the
// one method a contained caller may reach, because returning its own stage is
// what such a caller is there to do.
//
// It refuses, and the refusal is the honest answer rather than a gap: no run
// in this build carries a stage body through an agent launch, so no caller of
// this can be one. Recording a result for a stage nothing is running would put
// a report against a stage that never ran.
func (s *Service) stageReport(_ context.Context, req machine.StageReportRequest) (struct{}, error) {
	return struct{}{}, fmt.Errorf(
		"%w: this build launches no stage agent, so there is no %s stage of run %s to return a result for",
		ipc.ErrUnavailable, req.Stage, req.Run)
}

// lifecycle stops or restarts the service.
//
// PRD section 9 makes both refuse while runs are active, list the affected
// runs, and require an explicit force flag rather than a general
// yes-to-everything one. Force is that flag, and the refusal carries the runs
// so a caller can see what it would be ending.
//
// A restart also reads the home's configuration file first, the one a
// replacement resolves at start, and refuses when it cannot be read or
// parsed, leaving this service serving: stopping it for a replacement that
// would refuse to start leaves the home with no service at all. Force does
// not override that, because it is runs being interrupted that force
// consents to. What this establishes is that the file parsed when the
// restart was asked for, not that the replacement starts: the replacement
// reads the file again, so an edit in between is not covered, and neither is
// a start that fails for a reason other than this file, which the
// replacement writes to the service log instead.
//
// Every invocation is recorded with who called it, which is what the peer's
// credentials say and never what the caller says about itself.
func (s *Service) lifecycle(ctx context.Context, req ipc.Request, params machine.LifecycleRequest, restarting bool) (machine.Lifecycle, error) {
	verb := "stop"
	if restarting {
		verb = "restart"
	}
	s.log.Printf("%s requested by %s (marker %q, force %v)", verb, peerOf(req), req.Peer.Marker(), params.Force)

	if restarting {
		if _, _, _, err := resolveConfig(s.home.ConfigFile()); err != nil {
			s.log.Printf("restart refused: a replacement would not start on the configuration file: %v", err)
			return machine.Lifecycle{ConfigurationRefused: true, Detail: err.Error()}, nil
		}
	}

	active, err := s.activeRuns(ctx)
	if err != nil {
		return machine.Lifecycle{}, err
	}
	if len(active) > 0 && !params.Force {
		s.log.Printf("%s refused: %d run(s) active", verb, len(active))
		return machine.Lifecycle{
			Active: active,
			Detail: fmt.Sprintf("%d run(s) are active. Pass the force flag for this command to %s anyway.", len(active), verb),
		}, nil
	}
	s.log.Printf("%s accepted", verb)
	// The answer has to reach the caller before the connection goes, and this
	// is what orders it: internal/ipc runs the hook on the goroutine that
	// wrote the answer, after it wrote it. Stopping concurrently with the
	// handler's return would race a stop that closes this connection against
	// the write of the answer explaining why.
	req.AfterAnswer(func() { s.Stop(restarting) })
	return machine.Lifecycle{Accepted: true, Restarting: restarting}, nil
}

// peerOf renders who made a request, from what the kernel attributed to the
// connection. An unidentified peer is reported as unidentified rather than as
// whatever the caller claimed, because a record of who called it that a caller
// can write is not a record of who called it.
func peerOf(req ipc.Request) string {
	creds, err := req.Peer.Credentials()
	if err != nil {
		return "an unidentified peer"
	}
	return creds.String()
}

// resolveConfig reads the operator's global configuration document, which is
// YAML and is parsed with config.ParseYAML, and returns it as a layer,
// resolved on its own against the schema defaults, and as the digest that
// identifies the document. A refusal names the file.
//
// A document that is there and cannot be read or parsed refuses, rather than
// falling back to defaults: PRD section 10 makes invalid configuration a
// parse-time failure that names the offending key. A document that is not
// there is a different case and is the defaults, which that section says in as
// many words.
//
// The repository layer is deliberately absent here. PRD section 10 reads its
// two copies per run - the trusted one from the default branch at a freshly
// fetched commit, the pushed one from the branch under validation - so they
// are read in resolveRunConfig when a run begins or resumes, and what this
// resolution answers for is the service-scoped decisions alone: the agent
// this service resolves and its run service's session policy.
func resolveConfig(path string) (config.Layer, config.Config, string, error) {
	document, err := os.ReadFile(path)
	global := config.Absent(config.OriginGlobal)
	switch {
	case errors.Is(err, os.ErrNotExist):
		document = nil
	case err != nil:
		return config.Layer{}, config.Config{}, "", fmt.Errorf("service: reading %s: %w", path, err)
	default:
		if global, err = config.ParseYAML(config.OriginGlobal, document); err != nil {
			return config.Layer{}, config.Config{}, "", fmt.Errorf("service: %s: %w", path, err)
		}
	}
	resolution, err := config.Resolve(global, config.Absent(config.OriginTrusted))
	if err != nil {
		return config.Layer{}, config.Config{}, "", err
	}
	return global, resolution.Config, digestOf(document), nil
}

// digestOf identifies a configuration by the bytes it was resolved from, so a
// run's record traces to the settings that reached it. PRD section 8 requires
// a surprising verdict to be traceable to the configuration as well as to the
// build.
//
// It digests the source rather than the resolved value on purpose: two
// documents that resolve identically today may stop doing so when a default
// changes, and the question a reader asks is which settings the run was given.
func digestOf(document []byte) string {
	sum := sha256.Sum256(document)
	return hex.EncodeToString(sum[:])
}
