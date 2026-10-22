//go:build linux || darwin

package cli_test

import (
	"os"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// TestALaunchedServiceLeadsItsOwnGroupAndItsStopEndsIt holds the terms
// under which the service's launch of itself is a long-lived exception to
// P15's invocation deadline rather than a program left running: the one
// process a start launches for the home leads a process group of its own,
// which is what a stop or an outright kill ends it by, and a stop asked of
// the surface ends it, read gone by identity once the stop has answered.
//
// It runs serially, as the other tests that start a service do: a start
// waits on a bound for the service to answer, which the load of the
// package's parallel tests could exhaust.
func TestALaunchedServiceLeadsItsOwnGroupAndItsStopEndsIt(t *testing.T) {
	principles.Cite(t, principles.P15)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	if got := run(t, h, "", "service", "start"); got.code != machine.ExitOK {
		t.Fatalf("starting the service exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	all, err := standin.ProcessesOf(executable)
	if err != nil {
		t.Fatalf("listing what this binary runs: %v", err)
	}
	var launched []standin.Process
	for _, p := range all {
		if serves(p, h) {
			launched = append(launched, p)
		}
	}
	if len(launched) != 1 {
		t.Fatalf("the start left %v running for the home, want the one service it launched", launched)
	}
	service := launched[0]
	lineage, found, err := agents.ReadLineage(service.PID)
	if err != nil || !found || lineage.Identity != service.Identity {
		t.Fatalf("reading the lineage of %s: found %t, %v", service, found, err)
	}
	if lineage.Group != service.PID {
		t.Fatalf("%s is in process group %d, want it to lead a group of its own", service, lineage.Group)
	}

	if got := run(t, h, "", "service", "stop"); got.code != machine.ExitOK {
		t.Fatalf("stopping the service exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	gone := time.Now().Add(10 * time.Second)
	for {
		presence, err := service.Presence()
		if err == nil && presence != agents.PresencePresent {
			return
		}
		if !time.Now().Before(gone) {
			t.Fatalf("%s reads present (%v) after its stop answered", service, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
