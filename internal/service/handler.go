package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// handle serves one request. The method table is internal/ipc's and it is
// closed, so a method that reaches here is one this build serves and one this
// switch has a row for; a method added to that table without a row here
// reaches the default and is refused rather than answered with silence.
//
// Authority is decided before this is called. internal/ipc refuses a
// restricted method to a caller its Ancestry places inside an active
// validation stage, so nothing here asks who is calling.
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
		return call(ctx, req, s.start)
	case ipc.MethodRunRerun:
		return call(ctx, req, s.rerun)
	case ipc.MethodRunRespond:
		return call(ctx, req, s.respond)
	case ipc.MethodRunCancel:
		return call(ctx, req, s.cancel)
	case ipc.MethodGateAdmit:
		return call(ctx, req, s.admit)
	case ipc.MethodGateNotify:
		return call(ctx, req, s.notify)
	case ipc.MethodStageReport:
		return call(ctx, req, s.stageReport)
	case ipc.MethodTasksList:
		return call(ctx, req, func(ctx context.Context, _ struct{}) (machine.Tasks, error) {
			return s.listTasks(ctx)
		})
	case ipc.MethodTaskGet:
		return call(ctx, req, s.task)
	case ipc.MethodServiceStop:
		return call(ctx, req, func(ctx context.Context, r machine.LifecycleRequest) (machine.Lifecycle, error) {
			return s.lifecycle(ctx, req, r, false)
		})
	case ipc.MethodServiceRestart:
		return call(ctx, req, func(ctx context.Context, r machine.LifecycleRequest) (machine.Lifecycle, error) {
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
// and is exactly the state assistant init exists to change.
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
		out.Detail = "this working copy has no repository record; run assistant init"
		return out, nil
	}
	out.Repository = &repository
	if out.Branch == nil || out.Branch.Name == "" {
		return out, nil
	}
	active, found, err := s.activeRun(ctx, repository.ID, out.Branch.Name)
	if err != nil {
		return machine.Status{}, err
	}
	if !found {
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
	return out, nil
}

// branchState reads the working copy the caller is standing in. A working copy
// that cannot be read carries the reason rather than failing the report, since
// the rest of a status is still worth having.
func (s *Service) branchState(ctx context.Context, workingPath string) *machine.Branch {
	state := &machine.Branch{WorkingPath: workingPath}
	working, err := vcs.OpenWorktree(ctx, workingPath, vcs.WithRedactor(redact.New()))
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
	return machine.Runs{Runs: records}, nil
}

// listTasks reports fleet work with its resolved current state.
//
// The state is read per task rather than derived from the task record, because
// P8 makes the resolved state a record with its own owner and reading it any
// other way is the failure that principle names.
func (s *Service) listTasks(ctx context.Context) (machine.Tasks, error) {
	records, err := s.store.Tasks(ctx)
	if err != nil {
		return machine.Tasks{}, err
	}
	out := machine.Tasks{Tasks: make([]machine.Task, 0, len(records))}
	for _, record := range records {
		state, err := s.store.TaskState(ctx, record.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return machine.Tasks{}, err
		}
		out.Tasks = append(out.Tasks, machine.Task{Record: record, State: state})
	}
	return out, nil
}

// task reports one piece of fleet work with its resolved current state.
func (s *Service) task(ctx context.Context, req machine.TaskRequest) (machine.Task, error) {
	record, err := s.store.Task(ctx, req.Task)
	if err != nil {
		return machine.Task{}, err
	}
	state, err := s.store.TaskState(ctx, req.Task)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return machine.Task{}, err
	}
	return machine.Task{Record: record, State: state}, nil
}

// stageReport is how an agent running a stage returns its result. It is the
// one method a contained caller may reach, because returning its own stage is
// what such a caller is there to do.
//
// It refuses, and the refusal is the honest answer rather than a gap: no stage
// in this build launches an agent, so no caller of this can be one. Recording
// a result for a stage nothing is running would put a report against a stage
// that never ran.
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
// Every invocation is recorded with who called it, which is what the peer's
// credentials say and never what the caller says about itself.
func (s *Service) lifecycle(ctx context.Context, req ipc.Request, params machine.LifecycleRequest, restarting bool) (machine.Lifecycle, error) {
	verb := "stop"
	if restarting {
		verb = "restart"
	}
	s.log.Printf("%s requested by %s (marker %q, force %v)", verb, peerOf(req), req.Peer.Marker(), params.Force)

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

// resolveConfig reads the operator's global configuration document and
// resolves it, and returns the digest that identifies what it resolved.
//
// A document that is there and cannot be read or parsed refuses, rather than
// falling back to defaults: PRD section 10 makes invalid configuration a
// parse-time failure that names the offending key. A document that is not
// there is a different case and is the defaults, which that section says in as
// many words.
//
// The repository layer is deliberately absent. PRD section 10 reads it from
// the default branch at a freshly fetched commit, and nothing here fetches, so
// what a run resolves is the global layer and the schema defaults. doc.go says
// what that leaves.
func resolveConfig(path string) (config.Config, string, error) {
	document, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		resolution, err := config.Resolve(config.Absent(config.OriginGlobal), config.Absent(config.OriginTrusted))
		if err != nil {
			return config.Config{}, "", err
		}
		return resolution.Config, digestOf(nil), nil
	case err != nil:
		return config.Config{}, "", fmt.Errorf("service: reading %s: %w", path, err)
	}
	global, err := config.Parse(config.OriginGlobal, document)
	if err != nil {
		return config.Config{}, "", err
	}
	resolution, err := config.Resolve(global, config.Absent(config.OriginTrusted))
	if err != nil {
		return config.Config{}, "", err
	}
	return resolution.Config, digestOf(document), nil
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
