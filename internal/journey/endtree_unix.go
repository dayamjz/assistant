//go:build linux || darwin

package journey

import (
	"errors"
	"fmt"
	"syscall"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// endTree ends the serving process and every process it started that is
// still its descendant, and returns once each is gone, or with an error
// naming what is not, so Close removes the home after nothing it started
// can write there.
//
// The service is stopped first, so it starts nothing while its descendants
// are read: the processes whose chain of parents, read from the kernel
// (agents.ReadLineage) for each process of one settled listing
// (standin.ProcessesMatching), reaches it. Then it is killed and reaped,
// and each descendant that leads a process group, which a program the
// service ran through internal/procgroup does, is ended with its group
// (agents.ForceGroup), so what that program started in its group goes too;
// any other descendant is killed alone. Each kill follows a check that the
// identity still names the process.
//
// What it does not reach: a process the service started whose parent had
// exited before the listing, which is no longer a descendant; one that left
// its group; and one another process started on the service's behalf, such
// as launchd or a terminal server.
func (s *serving) endTree() error {
	descendants, listed := s.stopAndList()
	errs := []error{s.end()}
	if listed != nil {
		errs = append(errs, fmt.Errorf("journey: listing what the service started: %w", listed))
	}
	for _, d := range descendants {
		errs = append(errs, endDescendant(d))
	}
	return errors.Join(errs...)
}

// stopAndList stops the service and lists its descendants. A service that
// has already exited cannot be stopped, and its children have been given to
// another parent, so none is listed.
func (s *serving) stopAndList() ([]agents.Lineage, error) {
	pid := s.cmd.Process.Pid
	if exited, _ := s.exited(); exited {
		return nil, nil
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		return nil, fmt.Errorf("stopping the service %d: %w", pid, err)
	}
	listed, err := standin.ProcessesMatching(func([]string) bool { return true })
	if err != nil {
		return nil, err
	}
	children := map[int][]agents.Lineage{}
	for _, p := range listed {
		lineage, found, err := agents.ReadLineage(p.PID)
		if err != nil {
			return nil, fmt.Errorf("reading the parent of %s: %w", p, err)
		}
		if !found || lineage.Identity != p.Identity {
			continue
		}
		children[lineage.Parent] = append(children[lineage.Parent], lineage)
	}
	var descendants []agents.Lineage
	for frontier := []int{pid}; len(frontier) > 0; {
		parent := frontier[0]
		frontier = frontier[1:]
		for _, child := range children[parent] {
			descendants = append(descendants, child)
			frontier = append(frontier, child.PID)
		}
	}
	return descendants, nil
}

// endDescendant ends one descendant the service left: with its group when
// it leads one, and alone otherwise.
func endDescendant(d agents.Lineage) error {
	if d.Group == d.PID {
		if _, err := agents.ForceGroup(d.Identity, reapGrace); err != nil {
			return fmt.Errorf("journey: ending the group of process %d the service started: %w", d.PID, err)
		}
		return nil
	}
	process := standin.Process{Identity: d.Identity}
	if presence, err := d.Presence(); err != nil || presence != agents.PresencePresent {
		return err
	}
	_ = syscall.Kill(d.PID, syscall.SIGKILL)
	if !process.GoneWithin(reapGrace) {
		return fmt.Errorf("journey: process %d the service started was still running %v after it was killed", d.PID, reapGrace)
	}
	return nil
}
