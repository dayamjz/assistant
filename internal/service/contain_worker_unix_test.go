//go:build unix

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
)

// A worker is not a validation stage, and its push is the one thing the gate
// exists to admit: PRD section 6 has a delivery task's worker push to the
// assistant remote the way a person does. So a worker's process group must
// not be in the containment registry, or every push it makes is refused
// ErrContained before admission.
//
// Two halves hold that. containRunner grows no worker arm: wrapping a
// WorkerRunner yields a Runner that is no WorkerRunner and declares none, so
// a worker started through the wrapped agent is refused by name and the one
// obtained from the unwrapped agent registers nothing here. And the restricted check, asked
// about the worker's own process, answers not contained while a stage is
// running; the control is the same question answered contained once the
// worker's group is registered the way a stage's is, which is what shows the
// check was looking.
func TestAWorkerIsNotContainedAndTheWrapperOffersNoWorker(t *testing.T) {
	t.Parallel()
	agent := standin.NewWorker(t, standin.Script{}, agents.WithTerminationGrace(300*time.Millisecond))
	resolved := agent.Runner()
	if _, ok := resolved.(agents.WorkerRunner); !ok {
		t.Fatal("the resolved runner carries no worker mechanism, so wrapping it removes nothing")
	}

	s := &Service{registry: newRegistry()}
	wrapped := s.containRunner(resolved)
	if _, ok := wrapped.(agents.WorkerRunner); ok {
		t.Fatal("containRunner gave the wrapped agent a worker arm, so a worker started through it " +
			"would be registered as a stage and its push refused")
	}
	if _, ok := wrapped.(agents.SessionRunner); !ok {
		t.Fatal("wrapping lost the session mechanism agents.Resolve vouched for")
	}
	if wrapped.Capabilities().Has(agents.CapabilityWorkerSessions) {
		t.Fatal("the wrapper declares worker sessions and carries no worker, which is the disagreement " +
			"agents.Resolve refuses an adapter for")
	}
	if !wrapped.Capabilities().Has(agents.CapabilityResumableSessions) {
		t.Fatal("the wrapper dropped the session declaration along with the worker one")
	}
	_, err := agents.OpenWorker(t.Context(), wrapped, agents.WorkerSpec{Dir: t.TempDir(), Instructions: "work"})
	var refusal *agents.CapabilityError
	if !errors.As(err, &refusal) || refusal.Capability != agents.CapabilityWorkerSessions {
		t.Fatalf("OpenWorker on the wrapped agent answered %v, want a *CapabilityError naming %s",
			err, agents.CapabilityWorkerSessions)
	}

	worker, err := agents.OpenWorker(t.Context(), resolved, agents.WorkerSpec{Dir: t.TempDir(), Instructions: "work"})
	if err != nil {
		t.Fatalf("starting a worker on the unwrapped agent: %v", err)
	}
	defer worker.Close()

	// A stage is running, so the registry is not empty and the check reads
	// the peer's group rather than answering no without looking.
	forget := s.StageStarted("run-under-validation", "review", 424242)
	defer forget()
	peer := ipc.Credentials{PID: worker.Group()}
	got, err := s.registry.Contained(context.Background(), peer)
	if err != nil {
		t.Fatalf("asking whether the worker is contained: %v", err)
	}
	if got.Contained {
		t.Fatalf("the worker's process is contained by %+v, so its push would be refused", got)
	}

	// The control: registered as a stage would be, the same process is
	// contained, so the answer above came from the registry and not from a
	// check that was not looking.
	release := s.StageStarted("run-under-validation", "fix", worker.Group())
	got, err = s.registry.Contained(context.Background(), peer)
	release()
	if err != nil {
		t.Fatalf("asking again with the group registered: %v", err)
	}
	if !got.Contained || got.Stage != "fix" {
		t.Fatalf("the worker's process registered as a stage reads %+v, want contained by the fix stage", got)
	}
}
