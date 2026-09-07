package journey_test

import (
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
// EveryStageBoundary serves nine times, so that mistake costs the whole
// timeout nine times over and loses the diagnostic each time.
//
// The service that dies is one the product already refuses to start: PRD
// section 2 has a standing skip stop it before it serves, which
// TestAPassMeansTheSameThingEverywhere drives as the refusal it is. Using it
// here means the child really does exit, rather than exiting because this test
// broke it.
func TestServingAHomeTheServiceRefusesReportsTheExitRatherThanWaitingItOut(t *testing.T) {
	scenario, dir := cloned(t)
	j := open(t, scenario, func(o *journey.Options) { o.Dir = dir })
	if err := j.WriteConfiguration(map[string]any{
		"skip": []string{pipeline.StageReview.String()},
	}); err != nil {
		t.Fatalf("writing a configuration the service refuses: %v", err)
	}

	started := time.Now()
	err := j.Serve()
	took := time.Since(started)

	if err == nil {
		t.Fatal("this home carries a standing skip and the service reported itself ready over it")
	}
	if !strings.Contains(err.Error(), "exited before it was ready") {
		t.Fatalf("serving reported %v, and a child that has already exited is reported as one rather "+
			"than as a readiness check nothing answered", err)
	}
	if took >= journey.ReadyTimeout {
		t.Fatalf("serving spent %s noticing a child that had already exited, and the readiness timeout "+
			"is %s, so the exit was found by the timeout running out rather than by the child being "+
			"reaped", took, journey.ReadyTimeout)
	}
}
