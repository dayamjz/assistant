//go:build unix

package cli_test

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// PRD section 8's kept-work wake through the command line a person and the
// coordinator use. A person's run, started and cancelled here, keeps its
// copy because its commit landed nowhere, and no task names the run. The
// person's listing, assistant coordinator wakes, shows the notice under the
// entry's project, phrased, and takes nothing; the coordinator's --json
// drain carries the row under kept_work_wakes; assistant coordinator
// acknowledge kNUMBER closes it, after which neither lists it, while the
// entry stays open in status; and naming it again is refused as an
// invalid request.
func TestAKeptWorkWakeIsListedAndAcknowledgedThroughTheCommandLine(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	h := newHome(t)
	subject := newSubject(t)
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serve(t, h)

	var built machine.Init
	if got := run(t, h, subject, "--json", "init"); got.code != machine.ExitOK || json.Unmarshal([]byte(got.stdout), &built) != nil {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
	if got := run(t, h, subject, "--json", "--intent", "a change held at its first stage"); got.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s", got.code, got.stdout)
	}
	if got := run(t, h, subject, "--json", "--cancel"); got.code != machine.ExitOK {
		t.Fatalf("ending the run exited %s:\n%s", got.code, got.stdout)
	}
	var status machine.Status
	if got := run(t, h, subject, "--json", "status"); json.Unmarshal([]byte(got.stdout), &status) != nil || len(status.KeptWork) != 1 {
		t.Fatalf("status lists kept work %+v, want the cancelled run's one entry:\n%s", status.KeptWork, got.stdout)
	}
	entry := status.KeptWork[0]
	branch, ok := entry.Branch.Get()
	if !ok {
		t.Fatalf("the entry %+v names no branch, and a person's run is kept on one", entry)
	}

	var wake store.KeptWorkWake
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := run(t, h, subject, "coordinator", "wakes")
		if strings.Contains(got.stdout, branch+": ") {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the person's listing never showed a notice under branch %s:\n%s%s", branch, got.stdout, got.stderr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var drained machine.Wakes
	coordinate(t, h, subject, &drained, "wakes")
	if len(drained.KeptWorkWakes) != 1 || drained.KeptWorkWakes[0].KeptWork != entry.ID || drained.KeptWorkWakes[0].Project != entry.RepositoryID ||
		drained.KeptWorkWakes[0].Branch != entry.Branch {
		t.Fatalf("the coordinator's drain carries kept-work wakes %+v, want entry %d's one under its project and branch", drained.KeptWorkWakes, entry.ID)
	}
	wake = drained.KeptWorkWakes[0]
	listing := run(t, h, subject, "coordinator", "wakes")
	if line := branch + ": " + machine.QueuedKeptWorkWakePhrase(wake).String(); !strings.Contains(listing.stdout, line) {
		t.Fatalf("the person's listing does not read %q:\n%s", line, listing.stdout)
	}

	var acknowledged machine.Acknowledged
	coordinate(t, h, subject, &acknowledged, "acknowledge", "k"+strconv.FormatInt(wake.ID, 10))
	if len(acknowledged.KeptWorkWakes) != 1 || acknowledged.KeptWorkWakes[0] != wake.ID || len(acknowledged.Wakes)+len(acknowledged.DomainWakes) != 0 ||
		acknowledged.By != store.AcknowledgedByCoordinator().String() {
		t.Fatalf("acknowledge k%d answered %+v, want that kept-work wake alone closed by the coordinator", wake.ID, acknowledged)
	}
	var after machine.Wakes
	coordinate(t, h, subject, &after, "wakes")
	if len(after.KeptWorkWakes) != 0 {
		t.Fatalf("the drain after the acknowledgement carries %+v, want nothing", after.KeptWorkWakes)
	}
	if got := run(t, h, subject, "coordinator", "wakes"); strings.Contains(got.stdout, branch+": ") {
		t.Fatalf("the person's listing still shows the notice after it was acknowledged:\n%s", got.stdout)
	}
	if got := run(t, h, subject, "--json", "status"); json.Unmarshal([]byte(got.stdout), &status) != nil || len(status.KeptWork) != 1 || status.KeptWork[0].ID != entry.ID {
		t.Fatalf("after the wake was acknowledged status lists %+v, want entry %d still open:\n%s", status.KeptWork, entry.ID, got.stdout)
	}
	refusedAs(t, run(t, h, subject, "--json", "coordinator", "acknowledge", "k"+strconv.FormatInt(wake.ID, 10)), ipc.CodeInvalidRequest, machine.ExitUsage)
}
