package service

import (
	"context"
	"os"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/notify"
	"github.com/dayamjz/assistant/internal/store"
)

// notificationGateway builds the gateway openParts installs. It is off
// unless cfg says otherwise, and a transport on Options does not turn it
// on. The identity it trusts is this process's user identifier, which is
// the credential the local socket reports for a peer of the same user.
func (s *Service) notificationGateway(o Options, cfg config.Config) *notify.Gateway {
	var transport notify.Transport
	if cfg.Notifications {
		transport = o.NotifyTransport
		if transport == nil {
			transport = notify.Private{Path: s.home.Notifications()}
		}
	}
	g := notify.New(notify.Options{
		Enabled:   cfg.Notifications,
		Transport: transport,
		Ledger:    noteLedger{db: s.store},
	})
	g.Trust(notify.Identity{UID: os.Getuid(), GID: os.Getgid(), PID: os.Getpid()})
	return g
}

// noteLedger is the store's notification event row, as notify.Ledger.
type noteLedger struct {
	db *store.Store
}

func (l noteLedger) Seen(ctx context.Context, eventID string) (bool, error) {
	return l.db.NotificationEventSeen(ctx, eventID)
}

func (l noteLedger) Record(ctx context.Context, eventID string) (bool, error) {
	return l.db.RecordNotificationEvent(ctx, eventID)
}

// notifySend delivers one notice. A worker is refused before the gateway
// is asked. The gateway's refusal, including a transport error, is returned
// as it is: this writes no task, hold, or merge.
func (s *Service) notifySend(ctx context.Context, req ipc.Request, r machine.NotifySendRequest) (machine.NotifySent, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.NotifySent{}, err
	}
	if s.notes == nil {
		return machine.NotifySent{}, notify.ErrDisabled
	}
	res, err := s.notes.Send(ctx, notify.Notice{EventID: r.EventID, Text: r.Text, Public: r.Public})
	if err != nil {
		return machine.NotifySent{}, err
	}
	return machine.NotifySent{EventID: res.EventID, Duplicate: res.Duplicate}, nil
}

// notifyAction accepts one action. The identity is the peer credential.
// r.User is passed through so the gateway can ignore it; it is not put on
// the identity.
func (s *Service) notifyAction(ctx context.Context, req ipc.Request, r machine.NotifyActionRequest) (machine.NotifyActed, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.NotifyActed{}, err
	}
	if s.notes == nil {
		return machine.NotifyActed{}, notify.ErrDisabled
	}
	creds, err := req.Peer.Credentials()
	if err != nil {
		return machine.NotifyActed{}, err
	}
	res, err := s.notes.Accept(ctx, notify.Action{
		EventID: r.EventID,
		User:    r.User,
		Kind:    r.Kind,
	}, notify.Identity{UID: creds.UID, GID: creds.GID, PID: creds.PID})
	if err != nil {
		return machine.NotifyActed{}, err
	}
	return machine.NotifyActed{EventID: res.EventID, ActorUID: res.ActorUID, Duplicate: res.Duplicate}, nil
}
