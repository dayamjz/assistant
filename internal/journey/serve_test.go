package journey_test

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// TestServingAHomeTheServiceRefusesReportsTheExitRatherThanWaitingItOut drives
// the readiness wait against a service that dies at startup.
//
// It cites no principle. What it is about is this harness rather than the
// product: the wait exists to tell a service that is still starting from one
// that is already gone, and it can only do that if something reaps the child.
// An exec.Cmd fills in its ProcessState inside Wait alone, so a harness that
// waited nowhere would ask a process that had already exited for its readiness
// until the timeout ran out, and then report that nothing answered rather than
// the exit that is the actual answer. TestARunSurvivesTheServiceBeingKilledAt
// EveryStageBoundary serves once for every boundary a run reaches, so that
// mistake costs the whole timeout that many times over and loses the
// diagnostic each time.
//
// The service that dies is one the product already refuses to start: PRD
// section 2 has a standing skip stop it before it serves, which
// TestAPassMeansTheSameThingEverywhere drives as the refusal it is.
//
// Why it exited is established rather than assumed, because a startup failure
// of any other kind - a socket that will not bind, a home whose lock is held,
// a database that will not open - would satisfy an assertion that only said
// the child was gone, and the diagnostic this test exists for would then be
// read off a child that died of something else. Two things establish it: the
// refusal names the key this home's document asked for, and the same home with
// that key taken back out comes up. The second is what rules out this home,
// this harness and this machine, none of which the key changed.
func TestServingAHomeTheServiceRefusesReportsTheExitRatherThanWaitingItOut(t *testing.T) {
	scenario, dir := cloned(t)
	j := open(t, scenario, func(o *journey.Options) { o.Dir = dir })
	if err := j.WriteConfiguration(map[string]any{
		standingSkipKey: []string{pipeline.StageReview.String()},
	}); err != nil {
		t.Fatalf("writing a configuration the service refuses: %v", err)
	}

	started := time.Now()
	err := serving(t, j)
	took := time.Since(started)

	if err == nil {
		t.Fatal("this home carries a standing skip and the service reported itself ready over it")
	}
	if !strings.Contains(err.Error(), "exited before it was ready") {
		t.Fatalf("serving reported %v, and a child that has already exited is reported as one rather "+
			"than as a readiness check nothing answered", err)
	}
	if !strings.Contains(err.Error(), standingSkipKey) {
		t.Fatalf("serving reported %v, and nothing in it names %q, which is the only thing this "+
			"home's document asked for that a service refuses; what the child exited for is "+
			"unestablished, and any other startup failure would read the same",
			err, standingSkipKey)
	}
	if took >= journey.ReadyTimeout {
		t.Fatalf("serving spent %s noticing a child that had already exited, and the readiness timeout "+
			"is %s, so the exit was found by the timeout running out rather than by the child being "+
			"reaped", took, journey.ReadyTimeout)
	}

	if err := j.Kill(); err != nil {
		t.Fatalf("reaping the refused child before serving the same home again: %v", err)
	}
	if err := j.WriteConfiguration(nil); err != nil {
		t.Fatalf("rewriting the home's configuration without the standing skip: %v", err)
	}
	if err := serving(t, j); err != nil {
		t.Fatalf("the same home, differing only in that its document no longer asks for a standing "+
			"skip, did not come up either: %v\n\nso the exit above is not attributable to the "+
			"document, and what this test read as a refusal was something about this home, this "+
			"harness or this machine", err)
	}
}

// TestKillingAServiceThatEndedOnItsOwnLeavesNothingHoldingTheHome drives the
// end of a service this harness started but did not stop.
//
// It cites no principle, and like its sibling above it is about this harness
// rather than the product. Kill promises that nothing is serving afterwards
// and that what the harness opened for the service is let go, and a service
// that died on its own already satisfies the first half while leaving the
// second entirely to Kill. Reading the operating system's answer to a signal
// aimed at a process already reaped is what got that wrong: the answer is not
// the same everywhere, so a Kill that refused on one of them returned before
// closing the log it had opened, and the home could then not be removed on the
// platform that holds an open file against a delete. Closing is asserted
// through the home actually going away rather than through Close's answer
// alone, because the file left open and the directory left standing are the
// same failure and only the second is observable everywhere.
//
// It takes no platform guard, and that is the point of it: the service here
// exits over its own configuration document before any transport is involved,
// so there is nothing about it a platform without one would answer differently.
func TestKillingAServiceThatEndedOnItsOwnLeavesNothingHoldingTheHome(t *testing.T) {
	scenario, dir := cloned(t)
	j := open(t, scenario, func(o *journey.Options) { o.Dir = dir })
	if err := j.WriteConfiguration(map[string]any{
		standingSkipKey: []string{pipeline.StageReview.String()},
	}); err != nil {
		t.Fatalf("writing a configuration the service refuses: %v", err)
	}

	if err := j.Serve(); err == nil {
		t.Fatal("this home carries a standing skip and the service reported itself ready over it")
	}

	if err := j.Kill(); err != nil {
		t.Fatalf("ending a service that had already exited: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("closing the journey after it: %v", err)
	}
	if _, err := os.Stat(j.Root()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the home %s answers %v after closing rather than being gone, so something this "+
			"harness opened for the service is still held against it", j.Root(), err)
	}
}
