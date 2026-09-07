package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Every path that creates a run for a branch asks for that branch's gate before
// it reads or writes anything, which is what makes the decision that a branch
// has no run a single decision rather than one per verb.
//
// It is asserted by holding the gate and giving each path a context that runs
// out: a path that waits reports that its context ended, and a path that never
// asked would carry on into the store and the create behind it. A behavioural
// test cannot tell those apart, because the window between the read and the
// write is two store operations wide and a race that narrow is not one a test
// can be relied on to lose.
func TestEveryPathThatCreatesARunAsksForTheBranchGateFirst(t *testing.T) {
	t.Parallel()
	key := branchKey{repository: "subject", branch: "work"}

	for _, path := range []struct {
		name string
		call func(context.Context, *Service) error
	}{
		{"start", func(ctx context.Context, s *Service) error {
			_, _, err := s.claimBranch(ctx, key, run{repository: key.repository, branch: key.branch})
			return err
		}},
		{"rerun", func(ctx context.Context, s *Service) error {
			_, err := s.claimRerun(ctx, key)
			return err
		}},
		{"push", func(ctx context.Context, s *Service) error {
			// The driver a push's claim is handed is nil here, and that is the
			// point: a path that did not wait for the gate would reach it.
			_, _, err := s.claimPush(ctx, nil, key.repository, key.branch, "head", "base")
			return err
		}},
	} {
		s := &Service{starting: make(map[branchKey]*branchGate)}
		release, err := s.holdBranch(t.Context(), key)
		if err != nil {
			t.Fatalf("holding the branch: %v", err)
		}

		bounded, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		// A path that did not wait reads a store this service does not have,
		// which is recovered here so the failure reads as what it is rather
		// than taking the rest of the package's tests down with it.
		err = func() (err error) {
			defer func() {
				if value := recover(); value != nil {
					err = fmt.Errorf("it read on past the gate and panicked: %v", value)
				}
			}()
			return path.call(bounded, s)
		}()
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the %s path answered %v while the branch was held, so it did not wait for the gate", path.name, err)
		}
		release()

		// And the gate is given back, so nothing is left holding a branch
		// nobody is starting.
		if len(s.starting) != 0 {
			t.Errorf("after the %s path, %d branch gate(s) are still held", path.name, len(s.starting))
		}
	}
}
