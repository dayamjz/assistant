//go:build unix

package service_test

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
)

// A Codex subordinate coordinator starts under GrantSubordinate once the
// app-server thread uses approvalPolicy untrusted: domain.delegate holds a
// session, the stand-in asked each Run exchange as Codex CLI 0.166.0 asked
// a command approval, and the adapter accepts only the coordinator route
// (git is declined). Before that recording, OpenSubordinate refused with
// ErrGrantUnenforced.
func TestACodexSubordinateStartsUnderItsGrant(t *testing.T) {
	t.Parallel()
	const unestablished = "a Codex subordinate starting under its grant"
	service.RequireSweepClearsHere(t, unestablished)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")
	route := "echo routed"
	agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{
			{Contains: "git rev-parse --show-toplevel", Reply: "worker ready"},
			{Contains: "subordinate coordinator", Reply: "worker ready"},
			{Contains: "run ", Run: true},
		},
	}}, agents.WithCodexTerminationGrace(2*time.Second))
	o := options(t, h)
	o.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()})
	o.CoordinatorRoute = route
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, session := launchedSession(t, running.service, id)
		waitCapture(t, provider, session, "worker ready")
		w := launched{
			running: running, home: h, repository: repository,
			gateRepo: gateRepositoryOf(t, h, subject), task: id,
			group: session.Reference().PID, provider: provider, session: session,
		}
		d := delegateDomain(t, w, "alpha", "gated")
		if _, accepted := codexRan(t, d.as.provider, d.as.session, "git status"); accepted {
			t.Fatal("the Codex subordinate accepted git status, want it declined under GrantSubordinate")
		}
		ran, accepted := codexRan(t, d.as.provider, d.as.session, route)
		if !accepted || ran.Exit != 0 {
			t.Fatalf("the Codex subordinate's route was not accepted (accepted %t): %+v", accepted, ran)
		}
	})
}
