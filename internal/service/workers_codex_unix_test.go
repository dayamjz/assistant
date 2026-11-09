//go:build unix

package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
)

// codexRan has the Codex worker run command through a Run exchange, which
// the stand-in asks the adapter's permission for the way Codex CLI 0.166.0
// asked it, as a shell line, and returns what the worker reported: the
// command's Ran, or the refusal text when the adapter declined it. Like
// asWorker it fails the test if the command ran in the worker's own process
// group, which is not the shape a CLI's command has.
func codexRan(t *testing.T, provider session.Provider, launched *session.Session, command string) (standin.Ran, bool) {
	t.Helper()
	answered := len(codexTexts(t, provider, launched))
	if err := provider.Send(t.Context(), launched, "run "+command); err != nil {
		t.Fatalf("sending the Codex worker a command: %v", err)
	}
	deadline := time.Now().Add(launchWait)
	for {
		texts := codexTexts(t, provider, launched)
		if len(texts) > answered {
			reply := texts[answered]
			if reply == "standin: command permission refused" {
				return standin.Ran{}, false
			}
			var ran standin.Ran
			if err := json.Unmarshal([]byte(reply), &ran); err != nil || ran.Command != command {
				t.Fatalf("the Codex worker answered %q with %q", command, reply)
			}
			if ran.Group <= 0 || ran.Group == launched.Reference().PID {
				t.Fatalf("the Codex worker ran %q in group %d, and its own group is %d", command, ran.Group, launched.Reference().PID)
			}
			return ran, true
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the Codex worker never answered %q", command)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// codexTexts is every text the Codex worker's capture holds, in order.
func codexTexts(t *testing.T, provider session.Provider, launched *session.Session) []string {
	t.Helper()
	got, err := provider.Capture(launched)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	var texts []string
	for _, event := range agents.ReadTranscript(got.Text, agents.OutputStreamJSON, false).Events {
		if event.Kind == agents.TranscriptText {
			texts = append(texts, event.Text)
		}
	}
	return texts
}

// A Codex worker's push reaches the gate as the worker's and is held to the
// worker rule. The worker is the production Codex adapter over the
// stand-in's app-server, which asks each command's permission as Codex CLI
// 0.166.0 did, inside the shell line the CLI runs (recorded in
// internal/agents/testdata/codex-app-server-approvals-0.166.0.jsonl), and
// runs it only once the adapter accepts. Its push of its own branch is
// accepted under the worker grant, runs in a process group of its own, and
// is admitted, moving the gate's branch to the commit; its push of the same
// commit to another branch is accepted by the grant and refused by the
// gate's worker rule, which a person's push would not be, so the push was
// placed as the worker's; and the gate's references are read with git after
// each push rather than taken from the answer.
func TestACodexWorkersPushReachesTheGateUnderTheWorkerRule(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	gateRepo := gateRepositoryOf(t, h, subject)
	quietMaintenance(t, gateRepo)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")
	agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{
			{Contains: "git rev-parse --show-toplevel", Reply: "worker ready"},
			{Contains: "run ", Run: true},
		},
	}}, agents.WithCodexTerminationGrace(2*time.Second))
	o := options(t, h)
	o.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()})
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := launchedSession(t, running.service, id)
		waitCapture(t, provider, launched, "worker ready")
		copyPath := resolvedCopy(t, h, id)
		quietMaintenance(t, copyPath)
		if err := os.WriteFile(filepath.Join(copyPath, "work.txt"), []byte("the worker's change\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(t, copyPath, "add", "-A")
		git(t, copyPath, "commit", "--quiet", "-m", "the worker's change")
		head := strings.TrimSpace(git(t, copyPath, "rev-parse", "HEAD"))

		ran, accepted := codexRan(t, provider, launched, "git push "+gate.RemoteName+" task/one")
		if !accepted || ran.Exit != 0 {
			t.Fatalf("the Codex worker's push of its own branch was not admitted (accepted %t): %+v", accepted, ran)
		}
		if !strings.Contains(gateRefs(t, gateRepo), "refs/heads/task/one "+head) {
			t.Fatalf("the admitted push did not move the gate's branch to %s:\n%s", head, gateRefs(t, gateRepo))
		}
		before := gateRefs(t, gateRepo)

		ran, accepted = codexRan(t, provider, launched, "git push "+gate.RemoteName+" task/one:refs/heads/other")
		if !accepted {
			t.Fatal("the grant declined a git push, so the gate's worker rule was never asked")
		}
		if ran.Exit == 0 || !strings.Contains(ran.Stdout+ran.Stderr, "is not that branch") {
			t.Fatalf("the Codex worker's push outside its branch was not refused by the worker rule: %+v", ran)
		}
		if got := gateRefs(t, gateRepo); got != before {
			t.Fatalf("the refused push changed the gate's references:\nbefore: %s\nafter:  %s", before, got)
		}
	})
}
