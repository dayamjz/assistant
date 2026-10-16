package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// publishTrusted commits document as the repository document on the
// subject's main and pushes it to the upstream, so the default branch's
// trusted copy carries it; an empty document removes the file. The subject
// is put back on the branch it stood on.
func publishTrusted(t *testing.T, subject, document string) {
	t.Helper()
	git(t, subject, "checkout", "--quiet", "main")
	defer git(t, subject, "checkout", "--quiet", "work")
	path := filepath.Join(subject, config.RepositoryDocument)
	if document == "" {
		git(t, subject, "rm", "--quiet", config.RepositoryDocument)
	} else {
		if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		git(t, subject, "add", config.RepositoryDocument)
	}
	git(t, subject, "commit", "--quiet", "-m", "the trusted configuration")
	git(t, subject, "push", "--quiet", "origin", "main")
}

// Intake records the posture the project's trusted copy declares, read from
// the upstream's default branch at a fresh fetch through task.create; the
// coordinator may ask for a stricter one and never a lighter one; and a
// trusted copy that cannot be read or parsed refuses the intake as
// unavailable and records nothing, with no posture assumed in its place
// (P7). Each refusal's control is the request before it that was recorded.
func TestTaskCreateRecordsTheTrustedPostureAndOnlyTightensIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	publishTrusted(t, subject, `{"delivery": "local"}`)
	withService(t, h, func(running serviceUnderTest) {
		ctx := t.Context()
		create := func(branch, posture string) (machine.TaskCreated, error) {
			var created machine.TaskCreated
			err := running.client.Call(ctx, ipc.MethodTaskCreate, machine.TaskCreateRequest{
				Shape: "delivery", Project: "one", Instructions: "Do the work.\n", Mode: "interactive",
				Repository: repository.ID, Branch: branch, Delivery: posture,
			}, &created)
			return created, err
		}
		recorded := func(created machine.TaskCreated, want config.Delivery) {
			t.Helper()
			stored, err := recordsOf(t, h).Task(ctx, created.Record.ID)
			if err != nil || stored.Delivery != store.Known(want) || stored.Mode != "interactive" {
				t.Fatalf("the task was recorded under %v with launch notation %q (%v), want %s", stored.Delivery, stored.Mode, err, want)
			}
		}

		created, err := create("task/project", "")
		if err != nil {
			t.Fatalf("a delivery task of a local project: %v", err)
		}
		recorded(created, config.DeliveryLocal)
		created, err = create("task/stricter", "gated")
		if err != nil {
			t.Fatalf("a stricter posture than the project's: %v", err)
		}
		recorded(created, config.DeliveryGated)

		publishTrusted(t, subject, "")
		before := taskCount(t, h)
		if _, err := create("task/lighter", "local"); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("local asked of a gated project = %v, want refused", err)
		}
		if _, err := create("task/direct", "direct"); !errors.Is(err, ipc.ErrInvalidRequest) {
			t.Fatalf("direct = %v, want invalid", err)
		}

		publishTrusted(t, subject, `{"delivery": "direct"}`)
		if _, err := create("task/unparsed", ""); !errors.Is(err, ipc.ErrUnavailable) {
			t.Fatalf("an intake over an unparseable trusted copy = %v, want unavailable", err)
		}
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		unreachable := repository
		unreachable.UpstreamURL = filepath.Join(t.TempDir(), "nowhere.git")
		if _, err := records.UpsertRepository(ctx, unreachable); err != nil {
			t.Fatal(err)
		}
		if _, err := create("task/unread", "gated"); !errors.Is(err, ipc.ErrUnavailable) {
			t.Fatalf("an intake over an unreachable trusted copy = %v, want unavailable", err)
		}
		if got := taskCount(t, h); got != before {
			t.Fatalf("refused intakes recorded %d tasks", got-before)
		}
	})
}
