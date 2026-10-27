//go:build unix

package cli_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// PRD section 9's housekeeping mark through the coordinator's client: create
// --housekeeping records an investigation of the repository under the
// housekeeping role with the program's part of its instructions before the
// coordinator's notes; a second while it is open is refused, naming it,
// with nothing recorded; and a marked create naming a role is incorrect
// usage.
func TestTheCoordinatorCreatesAHousekeepingPassAndASecondIsRefused(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	requireSweepClears(t)
	h := newHome(t)
	subject := newSubject(t)
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serveCoordinated(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	repository := repositoryOf(t, h, subject)

	var created machine.TaskCreated
	coordinate(t, h, subject, &created, "create", "--housekeeping", "--project", "agent context",
		"--repository", repository, "--instructions", "The person renamed the fixer role on Friday.")
	record := created.Record
	if !record.Housekeeping || record.Shape != store.TaskInvestigation || record.Role != config.RoleHousekeeping {
		t.Fatalf("create --housekeeping answered %+v, want a marked investigation under the housekeeping role", record)
	}
	raw, err := os.ReadFile(created.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	program, notes, ok := strings.Cut(string(raw), "## The coordinator's notes")
	if !ok || !strings.Contains(program, "# Housekeeping pass of repository "+repository) || !strings.Contains(notes, "renamed the fixer role") {
		t.Fatalf("the pass's instructions are not the program's part then the notes:\n%s", raw)
	}

	before := taskCountOf(t, h, subject)
	second := run(t, h, subject, "--json", "coordinator", "create", "--housekeeping", "--project", "again", "--repository", repository)
	if second.code != machine.ExitFailure {
		t.Fatalf("a second pass exited %s, want an operational failure:\n%s%s", second.code, second.stdout, second.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(second.stdout), &failure); err != nil {
		t.Fatalf("the refusal wrote no failure document: %v\n%s", err, second.stdout)
	}
	if failure.Code != string(ipc.CodeRefused) || !strings.Contains(failure.Error, record.ID) {
		t.Fatalf("the second pass was refused %+v, want it refused naming open pass %s", failure, record.ID)
	}
	named := run(t, h, subject, "--json", "coordinator", "create", "--housekeeping", "--project", "again", "--repository", repository,
		"--role", "worker")
	if named.code != machine.ExitUsage {
		t.Fatalf("a marked create naming a role exited %s, want incorrect usage:\n%s%s", named.code, named.stdout, named.stderr)
	}
	if after := taskCountOf(t, h, subject); after != before {
		t.Fatalf("the refused creates recorded tasks: %d, want %d", after, before)
	}
}
