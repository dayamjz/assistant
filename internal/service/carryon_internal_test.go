package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// What becomes of a run whose segment ended, for every ending that can end
// one.
//
// carryOn acts on this and nothing else, and it is driven here rather than
// through a service because the endings it tells apart are signalled from
// other goroutines within microseconds of each other: a caller giving up, an
// ending through the protocol, the service stopping. Producing a chosen one of
// them by timing is a race a test cannot be relied on to win, which is the
// same reason TestEveryPathThatCreatesARunAsksForTheBranchGateFirst drives the
// branch gate directly.
//
// The row that matters most is the ending through the protocol. A run ended
// that way has had its segment cancelled, so it reaches carryOn looking
// exactly like a run whose caller walked away, and continuing it would execute
// stage bodies on a run a caller was already told was over.
func TestWhatBecomesOfARunWhoseSegmentEnded(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name     string
		how      ending
		stopping bool
		want     disposition
	}{
		{
			name: "a segment that settled the record leaves nothing standing",
			how:  ending{},
			want: dispositionSettled,
		},
		{
			name: "a step that failed is reported to the caller and stands where it is",
			how:  ending{stranded: true},
			want: dispositionReported,
		},
		{
			name: "a caller that gave up leaves a run this service picks up",
			how:  ending{stranded: true, byContext: true},
			want: dispositionContinued,
		},
		{
			name: "a run ended through the protocol is not picked up",
			how:  ending{stranded: true, byContext: true, byProtocol: true},
			want: dispositionEnded,
		},
		{
			name:     "the service stopping leaves the run to recovery",
			how:      ending{stranded: true, byContext: true},
			stopping: true,
			want:     dispositionRecovered,
		},
		{
			name:     "an ending through the protocol outranks the service stopping",
			how:      ending{stranded: true, byContext: true, byProtocol: true},
			stopping: true,
			want:     dispositionEnded,
		},
		{
			name:     "a step that failed as the service stopped is still not continued",
			how:      ending{stranded: true},
			stopping: true,
			want:     dispositionReported,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.how.disposition(c.stopping); got != c.want {
				t.Fatalf("%+v with stopping=%v becomes %s, want %s", c.how, c.stopping, got, c.want)
			}
		})
	}
}

// An ending through the protocol is ordered against the segment it ends,
// whichever side of the segment's release it arrives on.
//
// cancel signals the segment and only then moves the record, so a segment
// ended that way returns the same context error a lost caller's segment does.
// Reading the record to tell the two apart would be a read racing that move,
// and the window is one store transaction wide. The slot is what orders them
// instead, because it is the one thing both sides take the same mutex for, and
// this drives both interleavings of it directly.
func TestAnEndingThroughTheProtocolIsOrderedAgainstTheSegmentItEnds(t *testing.T) {
	t.Parallel()

	t.Run("while a segment holds the slot", func(t *testing.T) {
		t.Parallel()
		s := &Service{advancing: make(map[string]*slot)}
		segment, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := s.claim("run", cancel); err != nil {
			t.Fatalf("claiming the slot of a run nothing is advancing: %v", err)
		}
		if !s.isAdvancing("run") {
			t.Fatal("a run whose slot a segment holds is not reported as advancing")
		}

		forget := s.endRun("run")
		defer forget()
		if segment.Err() == nil {
			t.Fatal("ending the run left the segment advancing it running")
		}
		if !s.release("run") {
			t.Fatal("the segment gave its slot back without being told the run had been ended")
		}
	})

	t.Run("after the segment gave its slot back", func(t *testing.T) {
		t.Parallel()
		s := &Service{advancing: make(map[string]*slot)}

		forget := s.endRun("run")
		if s.isAdvancing("run") {
			t.Fatal("a slot standing for an ending reports a segment executing under it")
		}
		if err := s.claim("run", func() {}); !errors.Is(err, ErrRunAdvancing) {
			t.Fatalf("a segment took the slot of a run whose ending was being written: %v", err)
		}

		// A second caller ending the same run finds no segment under that
		// slot, so there is nothing there to end.
		second := s.endRun("run")
		defer second()
		if s.isAdvancing("run") {
			t.Fatal("a second ending of the same run reports a segment executing under its slot")
		}

		// The ending is written, so the slot is free again and a caller may
		// take it. Whether that caller then advances the run is a question for
		// the record it read, which the slot no longer answers.
		forget()
		if err := s.claim("run", func() {}); err != nil {
			t.Fatalf("the slot was not given back once the ending was written: %v", err)
		}
		if s.release("run") {
			t.Fatal("a segment that ran after the ending was written was told the run had been ended")
		}
	})
}

// Background work a continuation registers is ordered against the wait Close
// makes for it, rather than decided beside that wait.
//
// carryOn reaches continueRun from whichever goroutine a segment ended on, so
// nothing orders its reading of the stop against Close. What is ordered is the
// registration: startWork and stopWork take one mutex, so either the work is
// registered before Close stops taking any and Close waits for it, or Close
// ran first and the work is refused. The interleaving neither allows is work
// registered after Close has waited, which is a WaitGroup misuse at best and a
// continuation reading a closed database at worst.
//
// Both halves are driven, because a startWork that refused everything would
// satisfy the second on its own and register nothing at all.
func TestBackgroundWorkIsEitherWaitedForOrRefused(t *testing.T) {
	t.Parallel()

	t.Run("work registered before the refusal is waited for", func(t *testing.T) {
		t.Parallel()
		s := &Service{}
		if !s.startWork() {
			t.Fatal("a service that has not begun giving up refused background work")
		}

		waited := make(chan struct{})
		go func() {
			s.stopWork()
			s.work.Wait()
			close(waited)
		}()
		select {
		case <-waited:
			t.Fatal("the wait finished while work it had taken was still registered")
		case <-time.After(50 * time.Millisecond):
		}

		s.work.Done()
		select {
		case <-waited:
		case <-time.After(30 * time.Second):
			t.Fatal("the wait never finished after the work it took was done")
		}
	})

	t.Run("work is refused once the refusal is in place", func(t *testing.T) {
		t.Parallel()
		s := &Service{}
		s.stopWork()
		if s.startWork() {
			t.Fatal("a service that has stopped taking background work took some")
		}
		// Nothing was registered, so this returns rather than waiting on a
		// counter the refusal left behind.
		s.work.Wait()
	})
}
