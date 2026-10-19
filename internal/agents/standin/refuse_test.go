package standin

import (
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// A Refuse exchange is refused when the Agent is built beside anything its
// refusal would replace, for each of those, and a Refuse exchange alone
// builds. A stream worker, which has no request to answer with an error,
// exits on one rather than printing a reply nobody scripted.
func TestARefuseExchangeIsBuiltAloneAndAnsweredOnlyOverJSONRPC(t *testing.T) {
	for _, bad := range []Exchange{
		{Contains: "x", Refuse: "no", Reply: "done"},
		{Contains: "x", Refuse: "no", Recorded: []string{"{}"}},
		{Contains: "x", Refuse: "no", Ends: true},
		{Contains: "x", Refuse: "no", Run: true},
		{Contains: "x", Refuse: "no", Write: true},
	} {
		fake := &fatalTB{TB: t}
		run(func() { NewCodex(fake, Script{Worker: &WorkerScript{Exchanges: []Exchange{bad}}}) })
		if !fake.failed || !strings.Contains(fake.message, "Refuse exchange") {
			t.Errorf("a script with the exchange %+v was built, or refused as %q", bad, fake.message)
		}
	}

	agent := New(t, Script{Worker: &WorkerScript{Exchanges: []Exchange{{Contains: "refuse", Refuse: "no"}}}})
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "begin"})
	if err != nil {
		t.Fatalf("a Refuse exchange alone was not built: %v", err)
	}
	defer worker.Close()
	select {
	case <-worker.Done():
		t.Fatal("the stream worker ended before any line asked it to refuse")
	case <-time.After(50 * time.Millisecond):
	}
	if err := worker.Send(t.Context(), "refuse this"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case <-worker.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("a stream worker given a Refuse exchange did not exit")
	}
}
