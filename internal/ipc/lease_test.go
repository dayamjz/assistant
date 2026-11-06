package ipc_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
)

// testLease is short enough for a test to wait out and long enough beside
// its beat that a client beating on time under the race detector keeps it.
var testLease = ipc.Lease{Beat: 20 * time.Millisecond, Expiry: 300 * time.Millisecond}

// leasedEvents serves the open event stream under testLease.
func leasedEvents(t *testing.T) *harness {
	t.Helper()
	return serveOnSocket(t, func(sc *ipc.ServerConfig) {
		sc.Leases = map[ipc.Method]ipc.Lease{ipc.MethodEventsSubscribe: testLease}
	})
}

// leaseFrame is a frame as the wire carries it, read by the protocol's own
// text contract rather than through this package's client.
type leaseFrame struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *ipc.Error      `json:"error"`
	Event  *ipc.Event      `json:"event"`
	Done   bool            `json:"done"`
}

// readWire reads one frame within five seconds.
func readWire(t *testing.T, conn net.Conn, r *bufio.Reader) leaseFrame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f leaseFrame
	if err := json.Unmarshal(line, &f); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return f
}

// A leased stream is attached from its acknowledgement, which tells the
// client the beat. A holder that sends nothing after opening, as a stopped
// process sends nothing, loses the lease once its expiry passes, and is
// sent its stream's end as lease-expired after its gap; the publisher
// reports no attachment from then on. The control is a client of this
// package holding a second stream for several expiries: it beats, and keeps
// the same lease throughout.
func TestALeasedStreamThatStopsBeatingLosesItsLease(t *testing.T) {
	h := leasedEvents(t)
	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"id":1,"method":"events.subscribe"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := bufio.NewReader(conn)
	ack := readWire(t, conn, r)
	var opened struct {
		Lease struct {
			Beat time.Duration `json:"beat"`
		} `json:"lease"`
	}
	if err := json.Unmarshal(ack.Result, &opened); err != nil || opened.Lease.Beat != testLease.Beat {
		t.Fatalf("the acknowledgement is %s, want the lease's beat of %s", ack.Result, testLease.Beat)
	}
	n, open := h.events.Attachment()
	if !open || n == 0 {
		t.Fatalf("an acknowledged leased stream reports attachment %d open=%v", n, open)
	}
	if gap := readWire(t, conn, r); gap.Event == nil || gap.Event.Type != ipc.TypeGap {
		t.Fatalf("the stream opened with %+v, want its gap", gap)
	}
	end := readWire(t, conn, r)
	if !end.Done || end.Error == nil || end.Error.Code != ipc.CodeLeaseExpired {
		t.Fatalf("a holder that never beat was sent %+v, want its stream ended as %s", end, ipc.CodeLeaseExpired)
	}
	if got, open := h.events.Attachment(); got != n || open {
		t.Fatalf("after the lease expired the publisher reports attachment %d open=%v, want %d not held", got, open, n)
	}

	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()
	stream, err := c.Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stream.Close()
	beating, open := h.events.Attachment()
	if !open || beating == n {
		t.Fatalf("a new stream reports attachment %d open=%v, want a new lease held", beating, open)
	}
	time.Sleep(4 * testLease.Expiry)
	if got, open := h.events.Attachment(); got != beating || !open {
		t.Fatalf("a client that beats reports attachment %d open=%v after four expiries, want %d still held", got, open, beating)
	}
}

// One stream holds a publisher's lease: a second leased stream takes it
// under a new number and ends the first as stream-replaced, so two are never
// counted as one attachment. Closing the second leaves nothing attached,
// though the first was never closed by its own client.
func TestASecondLeasedStreamReplacesTheFirst(t *testing.T) {
	h := leasedEvents(t)
	ctx, cancel := callCtx(t)
	defer cancel()
	first, err := h.dial(t, ipc.ClientConfig{}).Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer first.Close()
	n1, _ := h.events.Attachment()
	second, err := h.dial(t, ipc.ClientConfig{}).Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	n2, open := h.events.Attachment()
	if !open || n2 == n1 {
		t.Fatalf("the second stream reports attachment %d open=%v, want a lease other than %d held", n2, open, n1)
	}
	for {
		_, err := first.Recv(ctx)
		if err == nil {
			continue
		}
		if !errors.Is(err, ipc.ErrStreamReplaced) {
			t.Fatalf("the first stream ended with %v, want ErrStreamReplaced", err)
		}
		break
	}
	second.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, open := h.events.Attachment(); !open {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the publisher still reports a lease after its one holder closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A restricted leased stream is granted the lease only once its handler
// admitted it: while the decision is open the publisher holds the
// subscription and reports no attachment, and a refused one never holds the
// lease. The positive control is the same method admitted, which is held.
func TestARestrictedStreamIsLeasedOnlyOnceAdmitted(t *testing.T) {
	requiresIdentifiedPeer(t)
	wakes := publisher(t, ipc.PublisherConfig{})
	handler := &gatedHandler{asked: make(chan ipc.Method, 1), release: make(chan struct{}), err: errors.New("this caller is a task's worker")}
	h := serveOnSocket(t, func(sc *ipc.ServerConfig) {
		sc.Handler = handler
		sc.Streams = map[ipc.Method]*ipc.Publisher{ipc.MethodWakesSubscribe: wakes}
		sc.Leases = map[ipc.Method]ipc.Lease{ipc.MethodWakesSubscribe: testLease}
	})
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()
	refused := make(chan error, 1)
	go func() {
		_, err := c.SubscribeTo(ctx, ipc.MethodWakesSubscribe, nil)
		refused <- err
	}()
	<-handler.asked
	if wakes.Subscribers() != 1 {
		t.Fatalf("while the decision is open the publisher holds %d subscriptions, want 1", wakes.Subscribers())
	}
	if n, open := wakes.Attachment(); n != 0 || open {
		t.Fatalf("a stream still being admitted reports attachment %d open=%v", n, open)
	}
	close(handler.release)
	if err := <-refused; err == nil {
		t.Fatal("the refused stream was opened")
	}
	if n, open := wakes.Attachment(); n != 0 || open {
		t.Fatalf("a refused stream left attachment %d open=%v", n, open)
	}

	admitting := &gatedHandler{asked: make(chan ipc.Method, 1), release: make(chan struct{})}
	close(admitting.release)
	admitted := serveOnSocket(t, func(sc *ipc.ServerConfig) {
		sc.Handler = admitting
		sc.Streams = map[ipc.Method]*ipc.Publisher{ipc.MethodWakesSubscribe: wakes}
		sc.Leases = map[ipc.Method]ipc.Lease{ipc.MethodWakesSubscribe: testLease}
	})
	stream, err := admitted.dial(t, ipc.ClientConfig{}).SubscribeTo(ctx, ipc.MethodWakesSubscribe, nil)
	if err != nil {
		t.Fatalf("SubscribeTo once admitted: %v", err)
	}
	defer stream.Close()
	if n, open := wakes.Attachment(); n != 1 || !open {
		t.Fatalf("an admitted stream reports attachment %d open=%v, want the first lease held", n, open)
	}
}

// beatOwner is the goroutine label a test sets on itself so the beats it
// counts are its own: a goroutine inherits its creator's labels, and
// SubscribeTo starts a stream's beat from its caller's goroutine.
const beatOwner = "beat-owner"

// ownBeats labels the calling test's goroutine and returns a wait for the
// count of live goroutines beating a leased stream under that label, read
// from the runtime's goroutine profile, to be want. A beat another test left
// exiting carries no such label and is never counted. The profile lists a
// goroutine only once it has run, so a beat is not counted the moment
// SubscribeTo returns; a test that waits for a stream's beat to be counted
// before ending the stream knows its count of none is a beat gone, not one
// not yet run.
func ownBeats(t *testing.T) func(want int, what string) {
	t.Helper()
	pprof.SetGoroutineLabels(pprof.WithLabels(context.Background(), pprof.Labels(beatOwner, t.Name())))
	label := fmt.Sprintf("%q:%q", beatOwner, t.Name())
	count := func() int {
		var profile bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&profile, 1); err != nil {
			t.Fatalf("reading the goroutine profile: %v", err)
		}
		// The profile opens with a header line ahead of its first record.
		_, records, _ := strings.Cut(profile.String(), "\n")
		total := 0
		for _, record := range strings.Split(records, "\n\n") {
			if !strings.Contains(record, "internal/ipc.(*Client).beat+") || !strings.Contains(record, label) {
				continue
			}
			var n int
			if _, err := fmt.Sscanf(record, "%d @", &n); err != nil {
				t.Fatalf("reading a goroutine count from %q: %v", record, err)
			}
			total += n
		}
		return total
	}
	return func(want int, what string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			got := count()
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s has %d beating goroutines, want %d", what, got, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// A leased stream stops beating as it ends, not at its next beat: under a
// long beat, each of 21 streams one client opens and closes in turn holds
// no beating goroutine once it has closed, and neither does a stream whose
// client queue stalled, which the client ends without its consumer closing
// it. The control is each stream while it is held, which has exactly one,
// counted before it ends so that none after is a beat that ended.
func TestAnEndedLeasedStreamStopsBeatingAtOnce(t *testing.T) {
	beats := ownBeats(t)
	long := ipc.Lease{Beat: 10 * time.Minute, Expiry: 30 * time.Minute}
	h := serveOnSocket(t, func(sc *ipc.ServerConfig) {
		sc.Leases = map[ipc.Method]ipc.Lease{ipc.MethodEventsSubscribe: long}
	})
	ctx, cancel := callCtx(t)
	defer cancel()

	c := h.dial(t, ipc.ClientConfig{})
	for i := range 21 {
		stream, err := c.Subscribe(ctx, nil)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		beats(1, fmt.Sprintf("held leased stream %d", i+1))
		stream.Close()
		beats(0, fmt.Sprintf("closed leased stream %d", i+1))
	}

	// A queue one event deep that nothing reads: the opening gap marker fills
	// it, and the control event behind that ends it as stalled.
	stalling, err := h.dial(t, ipc.ClientConfig{Backlog: 1}).Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stalling.Close()
	beats(1, "a held leased stream with a one-event queue")
	publish(t, h.events, control("stopping"))
	beats(0, "a leased stream whose client queue stalled")
}
