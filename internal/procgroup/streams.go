package procgroup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
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
	in     *os.File
	inFrom io.Reader
	inOnce sync.Once
	inErr  chan error

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
			_, err := io.Copy(s.in, s.inFrom)
			s.closeIn()
			s.inErr <- err
		}()
	}
	s.outDone = make(chan error, len(s.outs))
	s.pending = len(s.outs)
	for _, o := range s.outs {
		go func() {
			_, err := io.Copy(o.w, o.r)
			_ = o.r.Close()
			s.outDone <- err
		}()
	}
}

// closeIn closes the write end of the child's standard input, once.
func (s *streams) closeIn() {
	s.inOnce.Do(func() {
		if s.in != nil {
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
// The input copier is never waited on: its write end is closed here, so
// once the caller's reader returns, the copy fails and the goroutine ends,
// but a reader that never returns keeps that one goroutine, and only that,
// for as long as it does not.
func (s *streams) finish(grace time.Duration) error {
	s.closeIn()
	var errs []error
	select {
	case err := <-s.inErr:
		if err != nil && !errors.Is(err, os.ErrClosed) && !errors.Is(err, syscall.EPIPE) {
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
			if err != nil && !errors.Is(err, os.ErrClosed) {
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
// read fails on the closed end.
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
