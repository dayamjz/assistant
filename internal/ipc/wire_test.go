package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := newFrameWriter(&buf, 0)
	sent := []frame{
		{ID: 1, Method: MethodRunGet, Marker: "review", Params: json.RawMessage(`{"run":"r1"}`)},
		{ID: 1, Result: json.RawMessage(`{"status":"running"}`)},
		{ID: 2, Error: &Error{Code: CodeContained, Message: "no", Method: MethodRunStart}},
		{ID: 3, Event: &Event{Type: TypeRunState, Revision: 12, Payload: json.RawMessage(`{"a":1}`)}},
		{ID: 3, Done: true},
	}
	for _, f := range sent {
		if err := w.write(context.Background(), f); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if lines := strings.Count(buf.String(), "\n"); lines != len(sent) {
		t.Fatalf("wrote %d lines for %d frames", lines, len(sent))
	}
	r := newFrameReader(bytes.NewReader(buf.Bytes()), 0)
	for i, want := range sent {
		got, err := r.read()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if got.ID != want.ID || got.Method != want.Method || got.Marker != want.Marker || got.Done != want.Done {
			t.Errorf("frame %d = %+v, want %+v", i, got, want)
		}
		if want.Event != nil {
			if got.Event == nil || got.Event.Type != want.Event.Type ||
				got.Event.Revision != want.Event.Revision ||
				!bytes.Equal(got.Event.Payload, want.Event.Payload) {
				t.Errorf("frame %d event = %+v, want %+v", i, got.Event, want.Event)
			}
		}
		if want.Error != nil {
			if got.Error == nil || *got.Error != *want.Error {
				t.Errorf("frame %d error = %+v, want %+v", i, got.Error, want.Error)
			}
		}
	}
	if _, err := r.read(); !errors.Is(err, io.EOF) {
		t.Errorf("read past the end = %v, want io.EOF", err)
	}
}

// TestWireCarriesAnEventTypeThisBuildDoesNotKnow models what a newer peer puts
// on the wire, and shows the decoder produces exactly the shape the consumer's
// guard is written for: an unrecognized type, classified as state, with no
// revision to order it by.
func TestWireCarriesAnEventTypeThisBuildDoesNotKnow(t *testing.T) {
	line := []byte(`{"id":4,"event":{"type":"custody.state","payload":{"branch":"main"}}}` + "\n")
	f, err := newFrameReader(bytes.NewReader(line), 0).read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Event == nil {
		t.Fatal("the frame carried no event")
	}
	if got := f.Event.Class(); got != ClassState {
		t.Fatalf("class of a type this build does not know = %s, want state", got)
	}
	if f.Event.Revision != 0 {
		t.Fatalf("revision = %d, want 0: the frame carried none", f.Event.Revision)
	}
	c := NewCursor()
	c.Reconciled(3)
	if got := c.Observe(*f.Event); got != DispositionReconcile {
		t.Errorf("Observe = %s, want reconcile", got)
	}
}

func TestReadRefusesAFrameOverTheLimit(t *testing.T) {
	long := `{"id":1,"method":"health","params":"` + strings.Repeat("x", 4096) + `"}` + "\n"
	r := newFrameReader(strings.NewReader(long), 512)
	if _, err := r.read(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("read = %v, want ErrFrameTooLarge", err)
	}
}

func TestWriteRefusesAFrameOverTheLimit(t *testing.T) {
	var buf bytes.Buffer
	w := newFrameWriter(&buf, 256)
	payload, err := json.Marshal(strings.Repeat("y", 1024))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	err = w.write(context.Background(), frame{ID: 1, Event: &Event{Type: TypeLogLine, Payload: payload}})
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("write = %v, want ErrFrameTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %d bytes of a refused frame", buf.Len())
	}
}

func TestReadIgnoresBlankLinesAndAcceptsAFinalFrameWithNoNewline(t *testing.T) {
	r := newFrameReader(strings.NewReader("\n\n"+`{"id":9,"method":"health"}`), 0)
	f, err := r.read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.ID != 9 || f.Method != MethodHealth {
		t.Errorf("frame = %+v, want id 9 health", f)
	}
}

func TestReadRefusesAMalformedFrame(t *testing.T) {
	r := newFrameReader(strings.NewReader("not json\n"), 0)
	if _, err := r.read(); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("read = %v, want ErrInvalidRequest", err)
	}
}

// wireSentinels states what every code means, independently of the table the
// package keeps. The package supplies which codes exist and this supplies the
// value each one has to resolve to, so a code the package added without a
// stated meaning fails here, a meaning stated for a code the package does not
// have fails here, and a code wired to the wrong sentinel fails here.
//
// The set is complete by construction rather than by anyone maintaining it: an
// exported sentinel is taken out of codeRows by its code, so a sentinel with no
// row cannot be declared and no sentinel can escape this table.
var wireSentinels = map[Code]error{
	CodeUnknownMethod:      ErrUnknownMethod,
	CodeInvalidRequest:     ErrInvalidRequest,
	CodeUnidentifiedPeer:   ErrUnidentifiedPeer,
	CodeContained:          ErrContained,
	CodeUnavailable:        ErrUnavailable,
	CodeSubscriberStalled:  ErrSubscriberStalled,
	CodeEventUndeliverable: ErrEventUndeliverable,
	CodePayloadTooLarge:    ErrPayloadTooLarge,
	CodeInvalidPayload:     ErrInvalidPayload,
	CodeStreamClosed:       ErrStreamClosed,
	CodeConnectionBusy:     ErrConnectionBusy,
	CodeFrameTooLarge:      ErrFrameTooLarge,
	CodeClientClosed:       ErrClientClosed,
	CodeInternal:           ErrInternal,
}

// TestEveryCodeIsStatedHere is the coverage the tables below rest on. Without
// it they test whichever codes somebody remembered to retype, which is how a
// code added later stops being covered while every test still passes.
func TestEveryCodeIsStatedHere(t *testing.T) {
	// statedBySentinel reads the table the other way, so the resolved value can
	// be named by the code that states it. What follows compares those codes,
	// which are strings and not errors, on purpose: this table is about a code
	// resolving to exactly the sentinel stated for it, and errors.Is would also
	// accept a value that merely wraps that sentinel. A lookup by value keeps
	// the identity the table is asserting, so do not simplify this back into a
	// single errors.Is call.
	statedBySentinel := make(map[error]Code, len(wireSentinels))
	for code, sentinel := range wireSentinels {
		statedBySentinel[sentinel] = code
	}
	seen := map[Code]bool{}
	for _, row := range codeRows {
		want, stated := wireSentinels[row.Code]
		if !stated {
			t.Errorf("the package maps %q, which this test states no sentinel for", row.Code)
			continue
		}
		if seen[row.Code] {
			t.Errorf("%q appears twice in the table", row.Code)
		}
		seen[row.Code] = true
		got := sentinelFor(row.Code)
		if statedBySentinel[got] != row.Code {
			t.Errorf("%q resolves to %v, want %v", row.Code, got, want)
		}
	}
	for code := range wireSentinels {
		if !seen[code] {
			t.Errorf("this test states a sentinel for %q, which the package does not map, so that failure would cross the wire as %q", code, CodeInternal)
		}
	}
}

// TestErrorCodesSurviveTheWire is what lets a client match a refusal against
// the sentinel the service returned rather than against its prose.
func TestErrorCodesSurviveTheWire(t *testing.T) {
	for code, sentinel := range wireSentinels {
		var buf bytes.Buffer
		if err := newFrameWriter(&buf, 0).write(context.Background(), frame{ID: 1, Error: &Error{Code: code, Message: "m"}}); err != nil {
			t.Fatalf("write: %v", err)
		}
		f, err := newFrameReader(&buf, 0).read()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !errors.Is(f.Error, sentinel) {
			t.Errorf("a %q error does not match its sentinel after the wire", code)
		}
	}
	unknown := &Error{Code: "invented-later", Message: "m"}
	if !errors.Is(unknown, ErrInternal) {
		t.Error("a code this build does not know matches nothing, so a caller could read it as success")
	}
}

func TestCodeForClassifiesRefusals(t *testing.T) {
	for want, sentinel := range wireSentinels {
		if got := codeFor(sentinel); got != want {
			t.Errorf("codeFor(%v) = %q, want %q", sentinel, got, want)
		}
	}
	if got := codeFor(errors.New("something else entirely")); got != CodeInternal {
		t.Errorf("codeFor(an error naming no sentinel) = %q, want %q", got, CodeInternal)
	}
}

// TestEverySentinelSurvivesTheWire is the claim the errors block makes about
// all of them rather than most of them. A sentinel that crossed as "internal"
// would leave a consumer telling "attach again and reconcile" from "the service
// broke" by reading prose, which is what a code exists to avoid.
func TestEverySentinelSurvivesTheWire(t *testing.T) {
	for code, sentinel := range wireSentinels {
		// A handler's failure reaches the wire wrapped in whatever it said
		// about it, which is the shape codeFor really classifies.
		wrapped := fmt.Errorf("while doing the work: %w", sentinel)
		var buf bytes.Buffer
		if err := newFrameWriter(&buf, 0).write(context.Background(), frame{ID: 1, Error: newError("status", wrapped)}); err != nil {
			t.Fatalf("write: %v", err)
		}
		f, err := newFrameReader(&buf, 0).read()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if f.Error == nil {
			t.Fatalf("%v arrived with no error at all", sentinel)
		}
		if !errors.Is(f.Error, sentinel) {
			t.Errorf("%v crossed the wire as %q, want %q", sentinel, f.Error.Code, code)
		}
	}
}

// TestTheWriterSaysWhichSideAFailureCameFrom is the distinction pump and answer
// both act on: a frame that could not be built leaves a healthy connection to
// report on, and a connection that failed leaves nowhere to report anything.
// Every failure the writer decides before it touches the connection has to land
// on the first side, whichever one it is.
func TestTheWriterSaysWhichSideAFailureCameFrom(t *testing.T) {
	oversized := frame{ID: 1, Event: &Event{Type: TypeLogLine, Payload: json.RawMessage(`"` + strings.Repeat("x", 4096) + `"`)}}
	unencodable := frame{ID: 2, Event: &Event{Type: TypeServiceStopping, Payload: json.RawMessage("not json")}}

	var kept bytes.Buffer
	built := map[string]frame{"a frame past the limit": oversized, "a frame that does not encode": unencodable}
	for name, f := range built {
		err := newFrameWriter(&kept, 512).write(context.Background(), f)
		if err == nil {
			t.Fatalf("%s was written", name)
		}
		if !unbuildable(err) {
			t.Errorf("%s reads as a connection failure (%v), so a caller would drop a healthy connection", name, err)
		}
		if kept.Len() != 0 {
			t.Errorf("%s reached the connection anyway", name)
		}
	}
	if !errors.Is(newFrameWriter(&kept, 512).write(context.Background(), oversized), ErrFrameTooLarge) {
		t.Error("a frame past the limit no longer names ErrFrameTooLarge")
	}
	if !errors.Is(newFrameWriter(&kept, 512).write(context.Background(), unencodable), ErrInternal) {
		t.Error("a frame that does not encode no longer names ErrInternal")
	}

	// A connection that failed is the other side, and stays there.
	err := newFrameWriter(failingWriter{}, 0).write(context.Background(), frame{ID: 3, Result: json.RawMessage("null")})
	if err == nil {
		t.Fatal("a write to a failed connection reported success")
	}
	if unbuildable(err) {
		t.Errorf("a connection failure reads as a frame that could not be built (%v), so a caller would answer on a connection that is gone", err)
	}
}

// failingWriter is a connection that is already gone.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
