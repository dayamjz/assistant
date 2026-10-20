//go:build unix

package service_test

import (
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
)

// A tmux-hosted subordinate that outlived the service that started it is
// placed by its terminal server too: the delegation attempt records the
// server's identity beside the reference, as the launch records it from
// session.Session.Host, and the restarted service adopts the survivor with
// it, so a shell in a new window of the survivor's server, the server's
// child and not the pane's, is placed as the subordinate of the domain and
// refused the coordinator's commands with the subordinate code, while the
// commands the subordinate holds are admitted (P14). With the server's
// identity left off the record, the same window is placed as nobody and
// admitted to domain.end as the first coordinator, which is the defect this
// guards; the coordinator's own domain.end then ends the survivor.
func TestASurvivingTmuxSubordinatesServerWindowIsPlacedAsTheSubordinate(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	requiresIdentifiedPeer(t)
	tmux := standin.Tmux(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	_, agent := workerAgent(t)
	provider := session.NewTmux(h, agent.Runner(), tmux)
	dir, err := h.Domain("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	survivor, err := provider.Delegate(ctx, session.SubordinateSpec{Domain: "alpha", Dir: dir, Instructions: "Coordinate alpha.\n"})
	if err != nil {
		t.Fatalf("starting the survivor under tmux: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close(survivor) })
	waitCapture(t, provider, survivor, "worker ready")
	ref, err := survivor.Reference().Encode()
	if err != nil {
		t.Fatal(err)
	}
	server := survivor.Host()
	if server.PID <= 0 || server.PID == survivor.Reference().PID {
		t.Fatalf("the survivor's host is %+v, want its tmux server, a process other than the pane's %d", server, survivor.Reference().PID)
	}
	records := openRecords(t, h)
	delegation, err := records.Delegate(ctx, store.DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.BeginDelegationAttempt(ctx, delegation.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := records.SetDelegationAttemptSession(ctx, delegation.ID, 1, ref,
		store.SessionHost{PID: server.PID, Start: server.Start}); err != nil {
		t.Fatal(err)
	}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		w := launched{running: running, home: h, repository: repository, group: survivor.Reference().PID, provider: provider, session: survivor}
		if _, _, held := running.service.SubordinateSessionOf("alpha"); held {
			t.Fatal("the service holds a session for a subordinate it did not start")
		}
		for _, r := range []struct {
			method ipc.Method
			params any
		}{
			{ipc.MethodDomainEnd, machine.DomainEndRequest{Domain: "alpha"}},
			{ipc.MethodTaskRespond, machine.TaskRespondRequest{Domain: "alpha", Answer: machine.AnswerRetry}},
			{ipc.MethodServiceStop, machine.LifecycleRequest{}},
		} {
			if _, code := callFromWindow(t, w, r.method, r.params); code != ipc.CodeSubordinate {
				t.Fatalf("%s from a window of the survivor's server answered %q, want %q: the window is the subordinate's, not the coordinator's",
					r.method, code, ipc.CodeSubordinate)
			}
		}
		if latest, _, err := recordsOf(t, h).Delegation(ctx, "alpha"); err != nil || latest.ID != delegation.ID || !latest.Standing() {
			t.Fatalf("after the refused domain.end the delegation reads %+v (%v), want it standing", latest, err)
		}
		if _, code := callFromWindow(t, w, ipc.MethodSupervisionAssert, struct{}{}); code != "" {
			t.Fatalf("supervision.assert from a window of the survivor's server was refused with %q", code)
		}
		var ended machine.DelegationEnded
		if err := running.client.Call(ctx, ipc.MethodDomainEnd, machine.DomainEndRequest{Domain: "alpha"}, &ended); err != nil {
			t.Fatalf("domain.end from the coordinator: %v", err)
		}
		if ended.Record.Standing() || !ended.SessionEnded {
			t.Fatalf("domain.end answered %+v, want the delegation ended and the survivor ended", ended)
		}
	})
}

// The launch records a tmux-hosted subordinate's server beside its
// reference: domain.delegate under the tmux provider starts the
// subordinate, and its delegation attempt reads back the identity the held
// session's Host reports, a process other than the pane's, which is what a
// restart's survivor placement is given. A launch that recorded no host
// leaves the attempt's Host zero and fails here.
func TestATmuxSubordinatesLaunchRecordsItsServer(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	launchedTmuxWorker(t, "task/tmux-domain", func(w launched) {
		d := delegateDomain(t, w, "alpha", "gated")
		server := d.as.session.Host()
		if server.PID <= 0 || server.PID == d.as.group {
			t.Fatalf("the held subordinate's host is %+v, want its tmux server", server)
		}
		latest, found, err := recordsOf(t, w.home).Delegation(t.Context(), "alpha")
		if err != nil || !found {
			t.Fatalf("the delegation: %v, %v", found, err)
		}
		attempts, err := recordsOf(t, w.home).DelegationAttempts(t.Context(), latest.ID)
		if err != nil || len(attempts) != 1 {
			t.Fatalf("the attempts are %+v (%v), want one", attempts, err)
		}
		if got := attempts[0].Host; got != (store.SessionHost{PID: server.PID, Start: server.Start}) {
			t.Fatalf("the attempt records host %+v, want the server %+v", got, server)
		}
	})
}
