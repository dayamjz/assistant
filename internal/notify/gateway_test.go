package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// idLedger records by the identifier the gateway passes. It is the stand-in
// for the store row, so a package test can show the decision without a
// database.
type idLedger struct {
	mu  sync.Mutex
	ids map[string]struct{}
	// failRecord makes the write after a successful delivery fail.
	failRecord error
}

func (l *idLedger) Seen(_ context.Context, id string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.ids[id]
	return ok, nil
}

func (l *idLedger) Record(_ context.Context, id string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failRecord != nil {
		return false, l.failRecord
	}
	if l.ids == nil {
		l.ids = map[string]struct{}{}
	}
	if _, ok := l.ids[id]; ok {
		return false, nil
	}
	l.ids[id] = struct{}{}
	return true, nil
}

func gateway(t *testing.T, tr Transport, ledger Ledger) *Gateway {
	t.Helper()
	g := New(Options{Enabled: true, Transport: tr, Ledger: ledger})
	g.Trust(Identity{UID: 7, GID: 7, PID: 1})
	return g
}

func TestADisabledGatewayDeliversNothing(t *testing.T) {
	script := &Scripted{}
	g := New(Options{Enabled: false, Transport: script, Ledger: &idLedger{}})
	g.Trust(Identity{UID: 7})
	if _, err := g.Send(context.Background(), Notice{EventID: "n1", Text: "hello"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Send on an off gateway: %v, want ErrDisabled", err)
	}
	if _, err := g.Accept(context.Background(), Action{EventID: "a1", User: "7", Kind: KindAck}, Identity{UID: 7}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Accept on an off gateway: %v, want ErrDisabled", err)
	}
	if len(script.Notices()) != 0 {
		t.Fatalf("an off gateway delivered %v", script.Notices())
	}
	var zero Gateway
	if _, err := zero.Send(context.Background(), Notice{EventID: "n1"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("the zero gateway: %v, want ErrDisabled", err)
	}
}

func TestSendRefusesAPublicAudienceAndDoesNotRecordIt(t *testing.T) {
	script := &Scripted{}
	ledger := &idLedger{}
	g := gateway(t, script, ledger)
	_, err := g.Send(context.Background(), Notice{EventID: "n1", Text: "hello", Public: true})
	if !errors.Is(err, ErrPublic) {
		t.Fatalf("Send of a public notice: %v, want ErrPublic", err)
	}
	if len(script.Notices()) != 0 {
		t.Fatalf("a public notice was delivered: %v", script.Notices())
	}
	seen, err := ledger.Seen(context.Background(), "n1")
	if err != nil || seen {
		t.Fatalf("a refused notice was recorded: seen %v, %v", seen, err)
	}
}

func TestADuplicateEventIsDeliveredOnce(t *testing.T) {
	script := &Scripted{}
	g := gateway(t, script, &idLedger{})
	ctx := context.Background()
	first, err := g.Send(ctx, Notice{EventID: "n1", Text: "hello"})
	if err != nil || first.Duplicate {
		t.Fatalf("first Send = %+v, %v", first, err)
	}
	second, err := g.Send(ctx, Notice{EventID: "n1", Text: "hello again"})
	if err != nil || !second.Duplicate {
		t.Fatalf("second Send = %+v, %v, want a duplicate", second, err)
	}
	got := script.Notices()
	if len(got) != 1 || got[0].Text != "hello" || got[0].EventID != "n1" {
		t.Fatalf("delivered %v, want the first notice once", got)
	}
}

func TestOverlappingSendsDeliverOnce(t *testing.T) {
	script := &Scripted{}
	held := make(chan struct{})
	release := make(chan struct{})
	script.Before = func() {
		close(held)
		<-release
	}
	g := gateway(t, script, &idLedger{})
	ctx := context.Background()
	errc := make(chan error, 2)
	go func() {
		_, err := g.Send(ctx, Notice{EventID: "n1", Text: "one"})
		errc <- err
	}()
	<-held
	go func() {
		_, err := g.Send(ctx, Notice{EventID: "n1", Text: "two"})
		errc <- err
	}()
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errc; err != nil {
			t.Fatalf("overlapping Send: %v", err)
		}
	}
	if got := script.Notices(); len(got) != 1 || got[0].Text != "one" {
		t.Fatalf("overlapping sends delivered %v, want the first notice once", got)
	}
}

func TestATransportErrorRecordsNothing(t *testing.T) {
	boom := errors.New("channel unavailable")
	script := &Scripted{Fail: boom}
	ledger := &idLedger{}
	g := gateway(t, script, ledger)
	_, err := g.Send(context.Background(), Notice{EventID: "n1", Text: "hello"})
	if !errors.Is(err, boom) {
		t.Fatalf("Send = %v, want the transport error", err)
	}
	seen, err := ledger.Seen(context.Background(), "n1")
	if err != nil || seen {
		t.Fatalf("a failed delivery was recorded: seen %v, %v", seen, err)
	}
	script.Fail = nil
	if _, err := g.Send(context.Background(), Notice{EventID: "n1", Text: "hello"}); err != nil {
		t.Fatalf("a retry after the transport recovered: %v", err)
	}
	if got := script.Notices(); len(got) != 1 {
		t.Fatalf("the retry delivered %d notices, want one", len(got))
	}
}

func TestARecordFailureAfterDeliveryCanDeliverAgain(t *testing.T) {
	script := &Scripted{}
	ledger := &idLedger{failRecord: errors.New("ledger down")}
	g := gateway(t, script, ledger)
	_, err := g.Send(context.Background(), Notice{EventID: "n1", Text: "hello"})
	if err == nil || !errors.Is(err, ledger.failRecord) {
		t.Fatalf("Send = %v, want the record error", err)
	}
	if got := script.Notices(); len(got) != 1 {
		t.Fatalf("the delivery that could not be recorded delivered %d notices, want one", len(got))
	}
	seen, err := ledger.Seen(context.Background(), "n1")
	if err != nil || seen {
		t.Fatalf("a failed record left the id seen: %v, %v", seen, err)
	}
	ledger.failRecord = nil
	if _, err := g.Send(context.Background(), Notice{EventID: "n1", Text: "hello"}); err != nil {
		t.Fatalf("retry after the record recovered: %v", err)
	}
	if got := script.Notices(); len(got) != 2 {
		t.Fatalf("the retry delivered %d notices, want a second delivery of an unrecorded id", len(got))
	}
}

func TestAForgedNameDoesNotAdmitAnAction(t *testing.T) {
	g := gateway(t, &Scripted{}, &idLedger{})
	ctx := context.Background()
	// The payload names the trusted user. The credential is someone else.
	_, err := g.Accept(ctx, Action{EventID: "a1", User: "7", Kind: KindAck}, Identity{UID: 99, GID: 99, PID: 4})
	if !errors.Is(err, ErrUntrusted) {
		t.Fatalf("Accept with a forged name and an untrusted credential: %v, want ErrUntrusted", err)
	}
	trusted := Identity{UID: 7, GID: 7, PID: 9}
	res, err := g.Accept(ctx, Action{EventID: "a1", User: "root", Kind: KindAck}, trusted)
	if err != nil {
		t.Fatalf("Accept with a trusted credential and a forged name: %v", err)
	}
	if res.ActorUID != trusted.UID || res.Duplicate {
		t.Fatalf("Accept = %+v, want actor %d", res, trusted.UID)
	}
	again, err := g.Accept(ctx, Action{EventID: "a1", User: "someone-else", Kind: KindAck}, trusted)
	if err != nil || !again.Duplicate || again.ActorUID != trusted.UID {
		t.Fatalf("duplicate Accept = %+v, %v", again, err)
	}
}

func TestAnAuthorizingActionIsRefusedForATrustedIdentity(t *testing.T) {
	ledger := &idLedger{}
	g := gateway(t, &Scripted{}, ledger)
	who := Identity{UID: 7, GID: 7, PID: 1}
	for _, kind := range []string{KindCreateTask, KindAnswerHold, KindMerge, "delete", ""} {
		_, err := g.Accept(context.Background(), Action{EventID: "a-" + kind, User: "7", Kind: kind}, who)
		if !errors.Is(err, ErrScope) {
			t.Fatalf("Accept %q = %v, want ErrScope", kind, err)
		}
		seen, err := ledger.Seen(context.Background(), "a-"+kind)
		if err != nil || seen {
			t.Fatalf("a refused %q action was recorded: seen %v, %v", kind, seen, err)
		}
	}
}

func TestPrivateRefusesAPublicNoticeAndAppendsAPrivateOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.log")
	p := Private{Path: path}
	err := p.Deliver(context.Background(), Notice{EventID: "n1", Text: "secret", Public: true})
	if !errors.Is(err, ErrPublic) {
		t.Fatalf("public Deliver = %v, want ErrPublic", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a public notice created %s: %v", path, err)
	}
	if err := p.Deliver(context.Background(), Notice{EventID: "n1", Text: "secret"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "n1 secret\n" {
		t.Fatalf("channel = %q", body)
	}
	if err := p.Deliver(context.Background(), Notice{EventID: "n2", Text: "line\nbreak"}); !errors.Is(err, ErrNotice) {
		t.Fatalf("a broken line: %v, want ErrNotice", err)
	}
}

func TestAnEnabledGatewayWithoutAChannelRefuses(t *testing.T) {
	g := New(Options{Enabled: true})
	if _, err := g.Send(context.Background(), Notice{EventID: "n1", Text: "x"}); err == nil {
		t.Fatal("Send with no channel was accepted")
	}
}
