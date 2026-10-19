package service_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/notify"
	"github.com/dayamjz/assistant/internal/store"
)

// Sending a notification and accepting an acknowledgement leave the task
// list empty and an open hold open, and a name in the payload is not who
// the action was accepted for. A transport error and a duplicate, including
// one after the service is opened again, do not deliver a second time and
// do not close the hold.
func TestANotificationCannotAuthorizeWork(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	if err := os.WriteFile(h.ConfigFile(), []byte("notifications: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	records := openRecords(t, h)
	hold, err := records.RegisterHold(t.Context(), store.Hold{
		Key:     "notify-hold",
		Subject: "a hold a notification must not close",
		Detail:  "still open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}

	scripted := &notify.Scripted{}
	o := options(t, h)
	o.NotifyTransport = scripted
	running := withServiceOptions(t, h, o)

	var sent machine.NotifySent
	if err := running.client.Call(t.Context(), ipc.MethodNotifySend, machine.NotifySendRequest{
		EventID: "n1", Text: "hello",
	}, &sent); err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent.Duplicate || sent.EventID != "n1" {
		t.Fatalf("send = %+v", sent)
	}
	if got := scripted.Notices(); len(got) != 1 || got[0].Text != "hello" {
		t.Fatalf("delivered %v", got)
	}
	if err := running.client.Call(t.Context(), ipc.MethodNotifySend, machine.NotifySendRequest{
		EventID: "n1", Text: "again",
	}, &sent); err != nil {
		t.Fatalf("duplicate send: %v", err)
	}
	if !sent.Duplicate || len(scripted.Notices()) != 1 {
		t.Fatalf("duplicate delivered again: %+v, notices %d", sent, len(scripted.Notices()))
	}
	err = running.client.Call(t.Context(), ipc.MethodNotifySend, machine.NotifySendRequest{
		EventID: "n-public", Public: true,
	}, &sent)
	if err == nil || !strings.Contains(err.Error(), "public") {
		t.Fatalf("public send = %v, want a refusal", err)
	}

	scripted.Fail = errors.New("transport down")
	err = running.client.Call(t.Context(), ipc.MethodNotifySend, machine.NotifySendRequest{
		EventID: "n-down", Text: "lost",
	}, &sent)
	if err == nil || !strings.Contains(err.Error(), "transport down") {
		t.Fatalf("failed transport = %v", err)
	}
	scripted.Fail = nil
	if len(scripted.Notices()) != 1 {
		t.Fatalf("a failed delivery was recorded as sent: %v", scripted.Notices())
	}

	var acted machine.NotifyActed
	if err := running.client.Call(t.Context(), ipc.MethodNotifyAction, machine.NotifyActionRequest{
		EventID: "a1", User: "root", Kind: "ack",
	}, &acted); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if acted.ActorUID != os.Getuid() || acted.Duplicate {
		t.Fatalf("ack = %+v, want uid %d", acted, os.Getuid())
	}
	if err := running.client.Call(t.Context(), ipc.MethodNotifyAction, machine.NotifyActionRequest{
		EventID: "a1", User: "someone-else", Kind: "ack",
	}, &acted); err != nil {
		t.Fatalf("duplicate ack: %v", err)
	}
	if !acted.Duplicate || acted.ActorUID != os.Getuid() {
		t.Fatalf("duplicate ack = %+v", acted)
	}
	for _, kind := range []string{"create_task", "answer_hold", "merge"} {
		err := running.client.Call(t.Context(), ipc.MethodNotifyAction, machine.NotifyActionRequest{
			EventID: "deny-" + kind, User: "root", Kind: kind,
		}, &acted)
		if err == nil || !strings.Contains(err.Error(), "cannot") {
			t.Fatalf("action %s = %v, want a refusal", kind, err)
		}
	}

	ro, err := store.OpenReadOnly(t.Context(), h.Database())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	tasks, err := ro.Tasks(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("tasks = %d, want none", len(tasks))
	}
	holds, err := ro.OpenHolds(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(holds) != 1 || holds[0].Key != hold.Key {
		t.Fatalf("open holds = %+v, want %s still open", holds, hold.Key)
	}
	seen, err := ro.NotificationEventSeen(t.Context(), "n1")
	if err != nil || !seen {
		t.Fatalf("n1 seen = %v, %v", seen, err)
	}
	seen, err = ro.NotificationEventSeen(t.Context(), "n-down")
	if err != nil || seen {
		t.Fatalf("a failed delivery was recorded: %v, %v", seen, err)
	}
	repos, err := ro.Repositories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 0 {
		t.Fatalf("repositories = %d, want none", len(repos))
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}

	if err := running.service.Close(); err != nil {
		t.Fatal(err)
	}
	next := &notify.Scripted{}
	again := options(t, h)
	again.NotifyTransport = next
	restarted := withServiceOptions(t, h, again)
	if err := restarted.client.Call(t.Context(), ipc.MethodNotifySend, machine.NotifySendRequest{
		EventID: "n1", Text: "after restart",
	}, &sent); err != nil {
		t.Fatalf("send after restart: %v", err)
	}
	if !sent.Duplicate || len(next.Notices()) != 0 {
		t.Fatalf("restart redelivered: %+v, notices %d", sent, len(next.Notices()))
	}
}

func TestTheGatewayStaysOffUnlessTheOperatorTurnsItOn(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	scripted := &notify.Scripted{}
	o := options(t, h)
	o.NotifyTransport = scripted
	running := withServiceOptions(t, h, o)
	var sent machine.NotifySent
	err := running.client.Call(t.Context(), ipc.MethodNotifySend, machine.NotifySendRequest{
		EventID: "off", Text: "no",
	}, &sent)
	if err == nil || !strings.Contains(err.Error(), "gateway is off") {
		t.Fatalf("send with the gateway off = %v", err)
	}
	if len(scripted.Notices()) != 0 {
		t.Fatalf("an injected transport delivered while the gateway was off: %v", scripted.Notices())
	}
}

func TestANoticeLandsInThePrivateChannelFile(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	if err := os.WriteFile(h.ConfigFile(), []byte("notifications: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	running := withServiceOptions(t, h, options(t, h))
	var sent machine.NotifySent
	if err := running.client.Call(t.Context(), ipc.MethodNotifySend, machine.NotifySendRequest{
		EventID: "file-1", Text: "hello",
	}, &sent); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(h.Notifications())
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "file-1 hello\n" {
		t.Fatalf("channel = %q", body)
	}
	info, err := os.Stat(h.Notifications())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}
