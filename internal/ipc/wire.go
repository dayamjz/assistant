package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// DefaultMaxFrameBytes bounds one frame. Input from a local socket is still
// input: a peer that never sends a newline would otherwise make the reader
// grow until the service dies, which is a way to take the service down without
// any authority at all.
const DefaultMaxFrameBytes = 1 << 20

// reservedID is the identifier a report that belongs to no request carries. A
// frame the service could not read has none of its own to answer, so it is
// answered with this one instead, and both sides read that the same way: the
// service refuses a request frame carrying it, because it could not be answered
// without saying something else, and a client takes a frame carrying it as a
// report about the connection rather than as an answer to anything it asked.
const reservedID uint64 = 0

// frame is one line on the wire, in either direction. One shape for both
// directions keeps the encoding in one place; which fields are set says what
// the frame is.
type frame struct {
	// ID ties a response or a stream delivery to the request that asked for
	// it. A client chooses it and the service echoes it.
	ID uint64 `json:"id"`
	// Method is set on a request.
	Method Method `json:"method,omitempty"`
	// Marker is what the caller said about its own environment. It is
	// diagnostic evidence and never authorization; see MarkerVar.
	Marker Marker `json:"marker,omitempty"`
	// Params is a request body.
	Params json.RawMessage `json:"params,omitempty"`
	// Cancel is set on a request that ends a stream the caller opened.
	Cancel bool `json:"cancel,omitempty"`
	// Result is a successful answer.
	Result json.RawMessage `json:"result,omitempty"`
	// Error is a failed answer.
	Error *Error `json:"error,omitempty"`
	// Event is one delivery on a stream.
	Event *Event `json:"event,omitempty"`
	// Done says a stream ended. Error says why, when it ended for a reason.
	Done bool `json:"done,omitempty"`
}

// frameReader reads newline-delimited frames with a size limit.
type frameReader struct {
	r     *bufio.Reader
	limit int
}

// newFrameReader reads from r, refusing any frame longer than limit bytes.
func newFrameReader(r io.Reader, limit int) *frameReader {
	if limit <= 0 {
		limit = DefaultMaxFrameBytes
	}
	return &frameReader{r: bufio.NewReader(r), limit: limit}
}

// read returns the next frame. It reports io.EOF at a clean end of input,
// ErrFrameTooLarge for a frame past the limit, and ErrInvalidRequest for a line
// that does not decode.
//
// The two failures leave a caller in different places, and this is where that
// difference comes from. A whole line is taken before it is decoded, so after
// one that did not decode the reader sits at the start of the next frame and a
// caller may carry on. The rest of an over-long frame cannot be told apart from
// the frames that follow it, so there is no position to resume reading from and
// the connection is all a caller can end.
func (fr *frameReader) read() (frame, error) {
	for {
		line, err := fr.readLine()
		if err != nil {
			return frame{}, err
		}
		if len(line) == 0 {
			continue
		}
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			return frame{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		return f, nil
	}
}

// readLine returns one line without its terminator, bounded by the limit.
func (fr *frameReader) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := fr.r.ReadSlice('\n')
		if len(buf)+len(chunk) > fr.limit {
			return nil, ErrFrameTooLarge
		}
		buf = append(buf, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if len(buf) > 0 && errors.Is(err, io.EOF) {
				// A last line with no terminator is still a frame.
				return trimEOL(buf), nil
			}
			return nil, err
		}
		return trimEOL(buf), nil
	}
}

// trimEOL removes a trailing newline and carriage return.
func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// frameWriter writes newline-delimited frames. Writes are serialized, so a
// frame from one goroutine never interleaves with a frame from another.
type frameWriter struct {
	mu    sync.Mutex
	w     io.Writer
	limit int
}

// newFrameWriter writes frames to w, refusing any frame longer than limit
// bytes.
func newFrameWriter(w io.Writer, limit int) *frameWriter {
	if limit <= 0 {
		limit = DefaultMaxFrameBytes
	}
	return &frameWriter{w: w, limit: limit}
}

// frameError says a frame could not be built. Everything the writer decides
// before it touches the connection is returned inside one of these, so the two
// failures a caller has to tell apart are told apart by where they came from
// rather than by a list of the ones a caller happens to know about, and a
// reason added here later lands on the same side without that caller changing.
//
// It wraps its cause rather than replacing it, so a caller that wants the
// specific reason still matches ErrFrameTooLarge or ErrInternal with errors.Is.
type frameError struct{ cause error }

// Error renders the cause.
func (e frameError) Error() string { return e.cause.Error() }

// Unwrap resolves to the cause, so the sentinel it names still matches.
func (e frameError) Unwrap() error { return e.cause }

// unbuildable reports whether err says a frame could not be built, as opposed
// to a connection that failed under one that could. The distinction decides
// what a caller does next: a frame that could not be built leaves a healthy
// connection to report the failure on, and a connection that is gone leaves
// nowhere to report anything.
func unbuildable(err error) bool {
	var fe frameError
	return errors.As(err, &fe)
}

// write encodes f and writes it as one line.
//
// A frame past the limit is refused here rather than written for the peer to
// choke on, because the peer's only recovery is to drop the connection. PRD
// section 8 makes the full log the authority and what travels a bounded
// projection of it, so an oversized frame is a producer that did not bound its
// projection.
//
// A frame this refuses, and a frame that does not encode, are both failures
// about the frame rather than about the connection, and both come back as a
// frameError, so a caller can tell them from a connection that went away and
// answer with something smaller instead of leaving its peer waiting.
func (fw *frameWriter) write(f frame) error {
	body, err := fw.build(f)
	if err != nil {
		return frameError{cause: err}
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	_, err = fw.w.Write(body)
	return err
}

// build renders f as the line that would be written, or reports why it cannot
// be. Nothing it decides has touched the connection yet, which is what makes
// every failure it returns one about the frame.
func (fw *frameWriter) build(f frame) ([]byte, error) {
	var line bytes.Buffer
	enc := json.NewEncoder(&line)
	// A producer bounds a payload by its length, so a payload has to cost what
	// it measures once it is in a frame. Escaping is what would break that, by
	// rewriting ordinary characters into longer sequences, so a payload goes
	// out as it stands and what a producer counted is what its event costs
	// here. TestAnEscapeHeavyPayloadAtTheBoundStillTravels is what holds this
	// to the content that would otherwise expand.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("%w: encoding a frame: %w", ErrInternal, err)
	}
	if line.Len() > fw.limit {
		return nil, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, line.Len())
	}
	return line.Bytes(), nil
}
