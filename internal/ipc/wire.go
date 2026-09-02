package ipc

import (
	"bufio"
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

// read returns the next frame. It reports io.EOF at a clean end of input, and
// ErrFrameTooLarge for a frame past the limit. The latter ends the connection
// for the caller: the rest of an over-long frame cannot be told apart from the
// frames that follow it, so there is no position to resume reading from.
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

// write encodes f and writes it as one line.
//
// A frame past the limit is refused here rather than written for the peer to
// choke on, because the peer's only recovery is to drop the connection. PRD
// section 8 makes the full log the authority and what travels a bounded
// projection of it, so an oversized frame is a producer that did not bound its
// projection.
func (fw *frameWriter) write(f frame) error {
	body, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("ipc: encoding a frame: %w", err)
	}
	if len(body)+1 > fw.limit {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(body)+1)
	}
	body = append(body, '\n')
	fw.mu.Lock()
	defer fw.mu.Unlock()
	_, err = fw.w.Write(body)
	return err
}
