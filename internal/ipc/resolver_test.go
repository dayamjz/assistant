package ipc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// holdRecorder is the seam a service wires between these two packages: it takes
// who the protocol says resolved a hold and hands that to the store, which is
// the only accessor that writes the record.
//
// It is a real store rather than a double on purpose. A double would let this
// test assert about a value it carried itself; what the tests below read back is
// the row a run's history would actually carry.
type holdRecorder struct {
	store *store.Store

	mu sync.Mutex
	n  int
}

// recorded is what the handler answers with: the hold it wrote and what the
// record says about who resolved it.
type recorded struct {
	Key   string `json:"key"`
	By    string `json:"by"`
	Known bool   `json:"known"`
}

func (h *holdRecorder) Serve(ctx context.Context, req ipc.Request) (json.RawMessage, error) {
	h.mu.Lock()
	h.n++
	key := fmt.Sprintf("hold-%d", h.n)
	h.mu.Unlock()

	if _, err := h.store.RegisterHold(ctx, store.Hold{Key: key, Subject: string(req.Method)}); err != nil {
		return nil, err
	}
	held, err := h.store.ResolveHold(ctx, key, "approve", req.HoldResolver())
	if err != nil {
		return nil, err
	}
	by, known := held.ResolvedBy.Get()
	return json.Marshal(recorded{Key: key, By: by.String(), Known: known})
}

// recordingStore opens the database the handler writes to. The redactor is the
// shape internal/store requires of whoever opens it; nothing here stores a URL.
func recordingStore(t *testing.T) *store.Store {
	t.Helper()
	userinfo := regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s]+)@`)
	redactor := vcs.RedactorFunc(func(s string) string {
		return userinfo.ReplaceAllString(s, "${1}REDACTED@")
	})
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), store.WithRedactor(redactor))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// serveRecorder starts a server whose handler records a hold resolution for
// every call that reaches it.
func serveRecorder(t *testing.T) (*harness, *holdRecorder) {
	t.Helper()
	rec := &holdRecorder{store: recordingStore(t)}
	h := serveOnSocket(t, func(c *ipc.ServerConfig) { c.Handler = rec })
	return h, rec
}

// personClaims are the ways a caller can say it is a person with the protocol
// as it stands: in a parameter body, in the marker it attaches to its requests,
// and in fields of the frame itself that this build does not define.
var personClaims = []struct {
	name   string
	marker ipc.Marker
	params any
}{
	{"nothing claimed", "", map[string]string{"hold": "k", "action": "approve"}},
	{"a resolver in the body", "", map[string]string{"resolver": "person", "resolved_by": "person", "by": "person"}},
	{"a person in the marker", "person", map[string]string{"hold": "k"}},
	{"both", "person", map[string]any{"resolver": "person", "human": true, "peer": map[string]string{"uid": "0"}}},
}

// PRD section 8: the value meaning a person decided is unreachable from any
// path an agent reaches. This protocol is such a path, so the test drives every
// method it serves, with every claim a caller can make, and reads back the row
// the store actually holds.
//
// It needs a peer the kernel will identify, because the methods that resolve a
// hold are restricted. The skip leaves nothing unchecked: a platform that
// reports no peer credentials refuses every restricted method outright, so no
// resolution reaches a handler there at all, and
// TestHoldResolverAnswersTheSameForEveryRequest holds the seam itself on every
// platform.
func TestNoCallOnThisProtocolCanRecordThatAPersonDecided(t *testing.T) {
	requiresIdentifiedPeer(t)
	h, rec := serveRecorder(t)
	ctx, cancel := callCtx(t)
	defer cancel()

	person := store.ResolvedByPerson().String()
	for _, spec := range ipc.Methods() {
		if spec.Kind != ipc.KindRequest {
			continue
		}
		for _, claim := range personClaims {
			c := h.dial(t, ipc.ClientConfig{Marker: claim.marker})
			var got recorded
			if err := c.Call(ctx, spec.Method, claim.params, &got); err != nil {
				t.Fatalf("%s with %s: %v", spec.Method, claim.name, err)
			}
			if !got.Known {
				t.Fatalf("%s with %s recorded nobody", spec.Method, claim.name)
			}
			if got.By == person {
				t.Fatalf("%s with %s recorded that a person decided", spec.Method, claim.name)
			}
			if want := store.ResolvedByMachineInterface().String(); got.By != want {
				t.Fatalf("%s with %s recorded %q, want %q", spec.Method, claim.name, got.By, want)
			}
			c.Close()
		}
	}
	// The replies above are what the handler said; this is what the database
	// holds after every one of those calls.
	assertNoPersonInTheStore(t, rec)
}

// The frame is where a caller would put a resolver if the protocol had a field
// for one, so the claim is written straight onto the wire rather than through
// the client, which would not send a field it does not define.
func TestAFrameThatNamesAResolverDoesNotRecordOne(t *testing.T) {
	requiresIdentifiedPeer(t)
	h, rec := serveRecorder(t)

	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	frames := []string{
		`{"id":1,"method":"run.respond","resolver":"person","resolved_by":"person","params":{"resolver":"person"}}`,
		`{"id":2,"method":"run.respond","marker":"person","peer":{"uid":0,"pid":1},"credentials":{"uid":0}}`,
	}
	reader := bufio.NewReader(conn)
	for _, line := range frames {
		if _, err := conn.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		var reply struct {
			Error  *ipc.Error `json:"error"`
			Result recorded   `json:"result"`
		}
		raw, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
		if reply.Error != nil {
			t.Fatalf("%s was refused: %v", line, reply.Error)
		}
		if reply.Result.By == store.ResolvedByPerson().String() {
			t.Fatalf("%s recorded that a person decided", line)
		}
	}
	// Every hold the frames above resolved is in the store, and none of them
	// says a person decided.
	assertNoPersonInTheStore(t, rec)
}

// The absence above has to be a property of this path rather than of a build
// where nothing can record a person at all. A check that can pass without
// checking anything is worse than no check, so this is the same store, the same
// accessor, and the value the protocol could not produce.
func TestTheStoreRecordsThatAPersonDecidedWhenAPersonIsNamed(t *testing.T) {
	ctx := context.Background()
	s := recordingStore(t)
	if _, err := s.RegisterHold(ctx, store.Hold{Key: "k", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	held, err := s.ResolveHold(ctx, "k", "approve", store.ResolvedByPerson())
	if err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	by, known := held.ResolvedBy.Get()
	if !known || by != store.ResolvedByPerson() {
		t.Fatalf("the store recorded %s (known=%v) for a person's own decision", by, known)
	}
}

// The derivation reads nothing out of the request, which is what makes the
// answer independent of anything a caller composes. This is the same property
// the tests above establish over a socket, checked at the seam itself so a
// future field on Request cannot quietly reach it.
func TestHoldResolverAnswersTheSameForEveryRequest(t *testing.T) {
	want := store.ResolvedByMachineInterface()
	for _, spec := range ipc.Methods() {
		for _, claim := range personClaims {
			body, err := json.Marshal(claim.params)
			if err != nil {
				t.Fatalf("marshalling %s: %v", claim.name, err)
			}
			req := ipc.Request{Method: spec.Method, Params: body}
			if got := req.HoldResolver(); got != want {
				t.Fatalf("%s with %s answered %s, want %s", spec.Method, claim.name, got, want)
			}
		}
	}
}

// assertNoPersonInTheStore reads every hold the handler wrote and fails on one
// that says a person decided. The sweep is bounded by the recorder's own count
// rather than by the first read that fails, so a read that fails for any other
// reason - a row outside the closed set, a closed store - fails this test
// instead of ending it early as if the list had run out.
func assertNoPersonInTheStore(t *testing.T, rec *holdRecorder) {
	t.Helper()
	ctx := context.Background()
	person := store.ResolvedByPerson()

	rec.mu.Lock()
	n := rec.n
	rec.mu.Unlock()
	if n == 0 {
		t.Fatal("no hold was recorded at all, so this test checked nothing")
	}

	for i := 1; i <= n; i++ {
		key := fmt.Sprintf("hold-%d", i)
		held, err := rec.store.Hold(ctx, key)
		if err != nil {
			t.Fatalf("reading %s: %v", key, err)
		}
		by, known := held.ResolvedBy.Get()
		if !known {
			t.Fatalf("hold %s records nobody", held.Key)
		}
		if by == person {
			t.Fatalf("hold %s records that a person decided", held.Key)
		}
	}
	// The sweep covered every hold the recorder wrote, which the store agrees
	// with by not finding the one past the last.
	if _, err := rec.store.Hold(ctx, fmt.Sprintf("hold-%d", n+1)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reading past the last hold answered %v, want ErrNotFound", err)
	}
}
