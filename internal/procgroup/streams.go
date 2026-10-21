package procgroup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// streams is the copying Run owns in place of os/exec's.
//
// os/exec copies a standard stream that is not an *os.File in a goroutine
// cmd.Wait waits for, and after WaitDelay it closes its own pipe ends and then
// waits for those goroutines with no bound. A goroutine blocked reading a
// caller's io.Reader, or blocked in a caller's io.Writer, is not released by
// closing a pipe, so cmd.Wait could not be bounded with such a stream. Run
// therefore hands the child an os.Pipe end for every stream that is not
// already a file or nil, which os/exec passes through without a goroutine,
// and does the copying itself, so cmd.Wait returns once the leader is reaped
// and every wait on a copier is one Run bounds.
type streams struct {
	// childEnds are the pipe ends the child holds, closed in this process
	// once the child has started.
	childEnds []*os.File

	// in is the write end of the child's standard input when Run copies it
	// from inFrom, the caller's reader; closeIn closes it once.
	in       *os.File
	inFrom   io.Reader
	inOnce   sync.Once
	inClosed atomic.Bool
	inErr    chan error

	// outs are the read ends of the child's standard output and error that
	// Run copies to the caller's writers; pending counts the copiers that
	// have not reported on outDone.
	outs    []output
	outDone chan error
	pending int
}

type output struct {
	r *os.File
	w io.Writer
}

// plumb replaces each of cmd's streams that is not nil and not an *os.File
// with a pipe end, and returns what Run copies through those pipes. A
// standard error that is the same writer as standard output shares its pipe,
// as os/exec arranges for the same case.
func plumb(cmd *exec.Cmd) (*streams, error) {
	s := &streams{inErr: make(chan error, 1)}
	if cmd.Stdin != nil {
		if _, isFile := cmd.Stdin.(*os.File); !isFile {
			r, w, err := os.Pipe()
			if err != nil {
				return nil, err
			}
			s.inFrom, s.in = cmd.Stdin, w
			s.childEnds = append(s.childEnds, r)
			cmd.Stdin = r
		}
	}
	stdout, err := s.outputFor(cmd.Stdout)
	if err != nil {
		s.abandon()
		return nil, err
	}
	if cmd.Stderr != nil && sameWriter(cmd.Stderr, cmd.Stdout) {
		cmd.Stderr = stdout
	} else if cmd.Stderr, err = s.outputFor(cmd.Stderr); err != nil {
		s.abandon()
		return nil, err
	}
	cmd.Stdout = stdout
	return s, nil
}

// outputFor returns the writer the child is given in place of w.
func (s *streams) outputFor(w io.Writer) (io.Writer, error) {
	if w == nil {
		return nil, nil
	}
	if _, isFile := w.(*os.File); isFile {
		return w, nil
	}
	r, cw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	s.outs = append(s.outs, output{r: r, w: w})
	s.childEnds = append(s.childEnds, cw)
	return cw, nil
}

// sameWriter reports whether a and b are one writer, without panicking on a
// dynamic type that cannot be compared.
func sameWriter(a, b io.Writer) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

// abandon closes every pipe end, for a child that never started.
func (s *streams) abandon() {
	for _, f := range s.childEnds {
		_ = f.Close()
	}
	for _, o := range s.outs {
		_ = o.r.Close()
	}
	s.closeIn()
}

// started closes the child's ends in this process and starts the copiers.
func (s *streams) started() {
	for _, f := range s.childEnds {
		_ = f.Close()
	}
	if s.in != nil {
		go func() {
			pipe := &pipeEnd{f: s.in}
			err := s.copyIn(pipe)
			s.closeIn()
			s.inErr <- s.inputError(err, pipe)
		}()
	}
	s.outDone = make(chan error, len(s.outs))
	s.pending = len(s.outs)
	for _, o := range s.outs {
		// Every error an output copy stops on is reported, whichever side it
		// came from, as os/exec reports its own: before cut, nothing in Run
		// has closed the read end, so an error here is the caller's writer's
		// or the pipe's own, and after cut no copier's report is read. Both
		// sides are wrapped to hide io.ReaderFrom and io.WriterTo, so the
		// copy is io.Copy's own loop of a pipe read and then a caller write,
		// and a caller writer cannot keep reading the pipe on its own.
		go func() {
			_, err := io.Copy(struct{ io.Writer }{o.w}, struct{ io.Reader }{o.r})
			_ = o.r.Close()
			s.outDone <- err
		}()
	}
}

// copyIn copies the caller's reader into the child's standard input until
// the reader ends, a write fails, or Run closes the input. It is a loop of
// its own rather than io.Copy because it checks, before every read, whether
// Run has closed the input, and reads nothing more from the caller's reader
// once it has, whatever the read in flight returns: a (0, nil) answer, which
// io.Copy would follow with another read, ends the copy here. What it cannot
// do is end a read already in flight, so at most that one read is still
// pending when Run returns.
func (s *streams) copyIn(pipe *pipeEnd) error {
	buf := make([]byte, 32<<10)
	for !s.inClosed.Load() {
		n, rerr := s.inFrom.Read(buf)
		if n > 0 {
			if s.inClosed.Load() {
				return nil
			}
			if _, werr := pipe.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if errors.Is(rerr, io.EOF) {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
	return nil
}

// pipeEnd is Run's own write end of the child's standard input, wrapped so
// the input copier can tell an error on that end from one the caller's reader
// returned: it records the last error its own write met, and copyIn returns
// that same error when it is what stopped the copy.
type pipeEnd struct {
	f   *os.File
	err error
}

func (p *pipeEnd) Write(b []byte) (int, error) {
	n, err := p.f.Write(b)
	if err != nil {
		p.err = err
	}
	return n, err
}

// onPipe reports whether err, what stopped a copy, came from pipe rather
// than from the caller's side of that copy.
func onPipe(err error, pipe *pipeEnd) bool {
	return pipe.err != nil && errors.Is(err, pipe.err)
}

// inputError is what the input copy reports. The errors suppressed are a
// write on Run's own pipe end failing with EPIPE, a child that exited
// without reading all of its input, which os/exec suppresses too, or failing
// because Run closed that end. An error from the caller's reader is
// reported.
func (s *streams) inputError(err error, pipe *pipeEnd) error {
	if err == nil {
		return nil
	}
	if onPipe(err, pipe) && (errors.Is(err, syscall.EPIPE) || (s.inClosed.Load() && errors.Is(err, os.ErrClosed))) {
		return nil
	}
	return err
}

// closeIn closes the write end of the child's standard input, once, and
// records that Run closed it before it does.
func (s *streams) closeIn() {
	s.inOnce.Do(func() {
		if s.in != nil {
			s.inClosed.Store(true)
			_ = s.in.Close()
		}
	})
}

// finish ends the input copy and waits for the output copiers, called once
// the leader has been reaped. It waits grace for the output pipes to reach
// their end, which a descendant outside the group can delay by holding a
// write end, and past that cuts them. An expiry is exec.ErrWaitDelay, as
// os/exec reports its own.
//
// The input copier is never waited on: the input is closed here, so once
// the caller's read in flight returns, the copier ends without reading
// again, but a read that never returns keeps that one goroutine, and only
// that, for as long as it does not. So its error, the caller's reader's, is
// reported only when the copy had already ended when this is called.
func (s *streams) finish(grace time.Duration) error {
	s.closeIn()
	var errs []error
	select {
	case err := <-s.inErr:
		if err != nil {
			errs = append(errs, fmt.Errorf("procgroup: copying standard input: %w", err))
		}
	default:
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	for s.pending > 0 {
		select {
		case err := <-s.outDone:
			s.pending--
			if err != nil {
				errs = append(errs, fmt.Errorf("procgroup: copying output: %w", err))
			}
		case <-timer.C:
			return errors.Join(append(errs, s.cut(grace))...)
		}
	}
	return errors.Join(errs...)
}

// cut closes the input and the output read ends, which releases a copier
// blocked reading, and waits grace for the output copiers to return, which a
// copier blocked in a caller's writer does only once that write returns. It
// answers exec.ErrWaitDelay, saying when a copier was left behind; a copier
// left behind makes no write past the one it is blocked in, since its next
// read fails on the closed end. What the copiers report after the cut is not
// read: the cut's expiry is the answer, and the closing it did would
// otherwise read as their failure.
func (s *streams) cut(grace time.Duration) error {
	s.closeIn()
	for _, o := range s.outs {
		_ = o.r.Close()
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	for s.pending > 0 {
		select {
		case <-s.outDone:
			s.pending--
		case <-timer.C:
			return fmt.Errorf("%w: a copier is still blocked writing to the caller's writer", exec.ErrWaitDelay)
		}
	}
	return exec.ErrWaitDelay
}
