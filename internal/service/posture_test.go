package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
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

// homeDelivery writes the operator's own configuration file naming posture,
// which is the only layer that may loosen the delivery key (#304).
func homeDelivery(t *testing.T, h *home.Home, posture config.Delivery) {
	t.Helper()
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"delivery": "`+string(posture)+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Intake records the posture the operator's file and the project's trusted
// copy resolve to, the copy read from the upstream's default branch at a
// fresh fetch through task.create; the coordinator may ask for a stricter
// one and never a lighter one, and a trusted copy may tighten the operator's
// local to gated; and a trusted copy that cannot be read or parsed refuses
// the intake as unavailable and records nothing, with no posture assumed in
// its place (P7). Each refusal's control is the request before it that was
// recorded.
func TestTaskCreateRecordsTheTrustedPostureAndOnlyTightensIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	homeDelivery(t, h, config.DeliveryLocal)
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
			policyOf(t, running.client, created.Record.ID, onLocalPath[want])
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

		publishTrusted(t, subject, `{"delivery": "gated"}`)
		created, err = create("task/tightened", "")
		if err != nil {
			t.Fatalf("a delivery task of a project tightening the operator's local: %v", err)
		}
		recorded(created, config.DeliveryGated)
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

// A trusted copy cannot loosen the operator's posture (#304): with the
// home's file saying gated, or saying nothing so the default gated stands,
// an intake over a repository whose trusted copy says local is refused as
// unavailable, naming the key and the trusted layer, and records nothing:
// it records neither local nor gated in local's place. The control is the
// same home and repository once the trusted copy says gated, which records
// gated, so the refusal is the loosening and not the intake.
func TestTaskCreateRefusesATrustedCopyLooserThanTheHome(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	for _, explicit := range []bool{true, false} {
		h := newHome(t)
		if explicit {
			homeDelivery(t, h, config.DeliveryGated)
		}
		subject := newSubject(t)
		repository := recordRepository(t, h, subject)
		publishTrusted(t, subject, `{"delivery": "local"}`)
		withService(t, h, func(running serviceUnderTest) {
			ctx := t.Context()
			create := func(branch string) (machine.TaskCreated, error) {
				var created machine.TaskCreated
				err := running.client.Call(ctx, ipc.MethodTaskCreate, machine.TaskCreateRequest{
					Shape: "delivery", Project: "one", Instructions: "Do the work.\n", Mode: "interactive",
					Repository: repository.ID, Branch: branch,
				}, &created)
				return created, err
			}
			before := taskCount(t, h)
			_, err := create("task/loosened")
			if !errors.Is(err, ipc.ErrUnavailable) {
				t.Fatalf("home gated (explicit %v), trusted local: intake = %v, want unavailable", explicit, err)
			}
			for _, says := range []string{"delivery", `"local"`, "trusted layer"} {
				if !strings.Contains(err.Error(), says) {
					t.Errorf("the refusal %q does not say %s", err, says)
				}
			}
			if got := taskCount(t, h); got != before {
				t.Fatalf("the refused intake recorded %d tasks", got-before)
			}

			publishTrusted(t, subject, `{"delivery": "gated"}`)
			created, err := create("task/held")
			if err != nil {
				t.Fatalf("home gated (explicit %v), trusted gated: %v", explicit, err)
			}
			if stored, err := recordsOf(t, h).Task(ctx, created.Record.ID); err != nil ||
				stored.Delivery != store.Known(config.DeliveryGated) {
				t.Fatalf("the task was recorded under %v (%v), want gated", stored.Delivery, err)
			}
		})
	}
}

// The rows a task's answer is expected to carry, written out rather than
// derived through config so a test of the answer does not agree with the
// code it checks by calling it.
var (
	localRow         = config.Policy{Delivery: config.DeliveryLocal, Validation: config.ValidationNone, Publishes: config.PublishNothing, Lands: config.AuthorityApproval}
	gatedRemoteRow   = config.Policy{Delivery: config.DeliveryGated, Validation: config.ValidationFullRun, Publishes: config.PublishBranchAndPullRequest, Lands: config.AuthorityRemote}
	gatedCodeHostRow = config.Policy{Delivery: config.DeliveryGated, Validation: config.ValidationFullRun, Publishes: config.PublishBranchAndPullRequest, Lands: config.AuthorityCodeHost}
	// onLocalPath is what each posture states on a repository whose
	// upstream is a local path, which is what newSubject builds.
	onLocalPath = map[config.Delivery]*config.Policy{config.DeliveryLocal: &localRow, config.DeliveryGated: &gatedRemoteRow}
)

// policyOf reads the task through task.get and fails unless its answer
// states exactly want, or states none when want is nil.
func policyOf(t *testing.T, client *ipc.Client, id string, want *config.Policy) machine.Task {
	t.Helper()
	var task machine.Task
	if err := client.Call(t.Context(), ipc.MethodTaskGet, machine.TaskRequest{Task: id}, &task); err != nil {
		t.Fatalf("reading task %s: %v", id, err)
	}
	switch {
	case want == nil && task.Policy != nil:
		t.Errorf("task %s states the policy %+v, want none", id, *task.Policy)
	case want != nil && task.Policy == nil:
		t.Errorf("task %s states no policy, want %+v", id, *want)
	case want != nil && *task.Policy != *want:
		t.Errorf("task %s states the policy %+v, want %+v", id, *task.Policy, *want)
	}
	return task
}

// A task's answer states a policy exactly when the posture is on its record
// and the repository it names is one a run can start against, and its
// landing authority is where that run would land: the code host for a
// repository on github.com or gitlab.com, the remote for one whose upstream
// is a local path. A repository on a network host this build does not
// address, or naming a fork that does not resolve, starts no run, so its
// task states none; so does a task naming no repository or one with no row,
// a delivery task whose row holds no posture (the record holds none, though
// its push is followed as the gate), and an investigation. A local task
// states its own row whatever the upstream. The records are written through
// internal/store so the posture-less row, which intake no longer writes, can
// be stated at all.
func TestATasksAnswerStatesThePolicyOfItsRecordedPostureWhereItsRunWouldLand(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	onPath := recordRepository(t, h, subject)
	records := openRecords(t, h)
	ctx := t.Context()
	for _, repository := range []store.Repository{
		{ID: "github", UpstreamURL: "https://github.com/example/widgets.git"},
		{ID: "gitlab", UpstreamURL: "https://gitlab.com/example/widgets.git"},
		{ID: "unsupported", UpstreamURL: "https://git.example.com/example/widgets.git"},
		{ID: "badfork", UpstreamURL: "https://github.com/example/widgets.git", ForkURL: store.Known("https://git.example.com/me/widgets.git")},
	} {
		repository.WorkingPath = filepath.Join(t.TempDir(), repository.ID)
		repository.DefaultBranch = "main"
		if _, err := records.UpsertRepository(ctx, repository); err != nil {
			t.Fatalf("recording repository %s: %v", repository.ID, err)
		}
	}
	gated, local := store.Known(config.DeliveryGated), store.Known(config.DeliveryLocal)
	cases := []struct {
		task store.Task
		want *config.Policy
	}{
		{store.Task{ID: "localonpatha", Delivery: local, RepositoryID: store.Known(onPath.ID)}, &localRow},
		{store.Task{ID: "gatedonpatha", Delivery: gated, RepositoryID: store.Known(onPath.ID)}, &gatedRemoteRow},
		{store.Task{ID: "gatedgithubx", Delivery: gated, RepositoryID: store.Known("github")}, &gatedCodeHostRow},
		{store.Task{ID: "gatedgitlabx", Delivery: gated, RepositoryID: store.Known("gitlab")}, &gatedCodeHostRow},
		{store.Task{ID: "localgithubx", Delivery: local, RepositoryID: store.Known("github")}, &localRow},
		{store.Task{ID: "gatedunsuppx", Delivery: gated, RepositoryID: store.Known("unsupported")}, nil},
		{store.Task{ID: "gatedbadfork", Delivery: gated, RepositoryID: store.Known("badfork")}, nil},
		{store.Task{ID: "gatedmissing", Delivery: gated, RepositoryID: store.Known("no-such-repository")}, nil},
		{store.Task{ID: "gatednorepox", Delivery: gated}, nil},
		{store.Task{ID: "unrecordedaa", RepositoryID: store.Known(onPath.ID)}, nil},
		{store.Task{ID: "investigates", Shape: store.TaskInvestigation}, nil},
	}
	for _, c := range cases {
		task := c.task
		if task.Shape == "" {
			task.Shape = store.TaskDelivery
		}
		if task.RepositoryID.IsKnown() {
			task.Branch = store.Known("task/" + task.ID)
		}
		task.Project, task.Mode, task.WorktreePath = "one", "interactive", "/w/"+task.ID
		if _, err := records.CreateTask(ctx, task, store.Disposition{}); err != nil {
			t.Fatalf("recording %s: %v", task.ID, err)
		}
	}
	_ = records.Close()
	withService(t, h, func(running serviceUnderTest) {
		for _, c := range cases {
			answer := policyOf(t, running.client, c.task.ID, c.want)
			if c.task.ID == "unrecordedaa" && answer.Record.Delivery.IsKnown() {
				t.Errorf("the posture-less task's record reads %v, want no posture", answer.Record.Delivery)
			}
		}
	})
}
