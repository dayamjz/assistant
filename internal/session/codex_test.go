package session_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/session"
)

// A Codex app-server that dies in the middle of a turn probes Dead through
// the handle, for the reason a process that exited has, and the session's
// next Send is the adapter's typed mid-turn failure rather than a write into
// a pipe nobody reads. The stand-in accepts the turn, plays back the first
// frame Codex CLI 0.163.0 emitted for one, an agent message delta recorded
// in internal/agents/testdata, and exits before the turn settles.
func TestACodexWorkerThatDiesMidTurnProbesDead(t *testing.T) {
	sub := newSubject(t)
	raw, err := os.ReadFile("../agents/testdata/codex-app-server-0.163.0.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	delta := strings.Split(string(raw), "\n")[3]
	if !strings.Contains(delta, "item/agentMessage/delta") {
		t.Fatalf("line 4 of the recording is not the delta: %s", delta)
	}
	agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{Exit: 3, Exchanges: []standin.Exchange{
		{Contains: "die", Recorded: []string{delta}, Ends: true},
	}}}, agents.WithCodexTerminationGrace(2*time.Second))
	p := session.NewPipe(sub.home, agent.Runner())
	s := create(t, p, sub.spec("begin"))
	ref := s.Reference().String()
	if err := p.Send(context.Background(), s, "die now"); err != nil && !errors.Is(err, agents.ErrWorkerEnded) {
		t.Fatalf("the Send that ended the worker answered %v", err)
	}
	expect(t, waitProbe(t, p, ref, session.ReasonExited), session.ReasonExited)
	err = p.Send(context.Background(), s, "anything")
	var protocol *agents.WorkerError
	if !errors.As(err, &protocol) || protocol.Failure != agents.FailureProcess || !errors.Is(err, agents.ErrWorkerEnded) {
		t.Fatalf("a Send after the mid-turn exit answered %v, want the adapter's typed process failure", err)
	}
}
