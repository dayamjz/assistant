package ipc

import (
	"bytes"
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
		if err := w.write(f); err != nil {
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
	err = w.write(frame{ID: 1, Event: &Event{Type: TypeLogLine, Payload: payload}})
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

// TestErrorCodesSurviveTheWire is what lets a client match a refusal against
// the sentinel the service returned rather than against its prose.
func TestErrorCodesSurviveTheWire(t *testing.T) {
	cases := map[Code]error{
		CodeUnknownMethod:      ErrUnknownMethod,
		CodeInvalidRequest:     ErrInvalidRequest,
		CodeUnidentifiedPeer:   ErrUnidentifiedPeer,
		CodeContained:          ErrContained,
		CodeUnavailable:        ErrUnavailable,
		CodeSubscriberStalled:  ErrSubscriberStalled,
		CodeEventUndeliverable: ErrEventUndeliverable,
		CodePayloadTooLarge:    ErrPayloadTooLarge,
		CodeStreamClosed:       ErrStreamClosed,
		CodeConnectionBusy:     ErrConnectionBusy,
		CodeFrameTooLarge:      ErrFrameTooLarge,
		CodeClientClosed:       ErrClientClosed,
		CodeInternal:           ErrInternal,
	}
	for code, sentinel := range cases {
		var buf bytes.Buffer
		if err := newFrameWriter(&buf, 0).write(frame{ID: 1, Error: &Error{Code: code, Message: "m"}}); err != nil {
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
	cases := map[Code]error{
		CodeUnknownMethod:      ErrUnknownMethod,
		CodeInvalidRequest:     ErrInvalidRequest,
		CodeUnidentifiedPeer:   ErrUnidentifiedPeer,
		CodeContained:          ErrContained,
		CodeUnavailable:        ErrUnavailable,
		CodeSubscriberStalled:  ErrSubscriberStalled,
		CodeEventUndeliverable: ErrEventUndeliverable,
		CodePayloadTooLarge:    ErrPayloadTooLarge,
		CodeStreamClosed:       ErrStreamClosed,
		CodeConnectionBusy:     ErrConnectionBusy,
		CodeFrameTooLarge:      ErrFrameTooLarge,
		CodeClientClosed:       ErrClientClosed,
		CodeInternal:           errors.New("something else entirely"),
	}
	for want, err := range cases {
		if got := codeFor(err); got != want {
			t.Errorf("codeFor(%v) = %q, want %q", err, got, want)
		}
	}
}

// TestEverySentinelSurvivesTheWire is the claim the errors block makes about
// all of them rather than most of them. A sentinel with no row crosses as
// "internal", and a consumer is left telling "attach again and reconcile" from
// "the service broke" by reading prose, which is what a code exists to avoid.
func TestEverySentinelSurvivesTheWire(t *testing.T) {
	all := []error{
		ErrStreamClosed,
		ErrSubscriberStalled,
		ErrUnknownMethod,
		ErrInvalidRequest,
		ErrUnidentifiedPeer,
		ErrContained,
		ErrUnavailable,
		ErrInternal,
		ErrFrameTooLarge,
		ErrClientClosed,
		ErrConnectionBusy,
		ErrPayloadTooLarge,
		ErrEventUndeliverable,
	}
	for _, sentinel := range all {
		// A handler's failure reaches the wire wrapped in whatever it said
		// about it, which is the shape codeFor really classifies.
		wrapped := fmt.Errorf("while doing the work: %w", sentinel)
		var buf bytes.Buffer
		if err := newFrameWriter(&buf, 0).write(frame{ID: 1, Error: newError("status", wrapped)}); err != nil {
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
			t.Errorf("%v crossed the wire as %q, which resolves to %v", sentinel, f.Error.Code, sentinels[f.Error.Code])
		}
	}
}
