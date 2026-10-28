package tasks

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// pass is a well-formed marked request for repo's housekeeping pass, with
// notes as the coordinator's part.
func pass(notes string) Request {
	return Request{Project: "one", Instructions: notes, Repository: repo, Housekeeping: true}
}

// A marked create records an investigation of the repository under the
// housekeeping role, marked, dispatched as independent, with the program's
// part of its instructions before the coordinator's notes: the trusted scope,
// that this is the first pass, the rules, and the notes framed as claims to
// check.
func TestAMarkedCreateRecordsAHousekeepingInvestigation(t *testing.T) {
	f := newFixture(t)
	created := f.create(t, pass("The person decided on 2026-10-24 that the fixer is Cursor."))
	task := created.Task
	if !task.Housekeeping || task.Shape != store.TaskInvestigation || task.Role != config.RoleHousekeeping ||
		task.RepositoryID.Or("") != repo || task.Branch.IsKnown() {
		t.Fatalf("the pass was recorded as %+v", task)
	}
	if created.Cause != CauseIndependent {
		t.Fatalf("the pass was recorded %s, want independent", created.Cause)
	}
	if got := f.resolve(t, task.ID); got.State != taskstate.Dispatched {
		t.Fatalf("the pass resolves %s, want dispatched", got.State)
	}
	text := f.instructionsOf(t, task.ID)
	program, notes, ok := strings.Cut(text, "## The coordinator's notes")
	if !ok {
		t.Fatalf("the instructions carry no notes heading:\n%s", text)
	}
	for _, want := range []string{"`AGENTS.md`", "`.claude/rules/**`", "`assistant-knowledge.md`", "first pass",
		"factual drift", "Never add, remove or reword a rule"} {
		if !strings.Contains(program, want) {
			t.Errorf("the program's part does not say %q:\n%s", want, program)
		}
	}
	if !strings.Contains(notes, "claims to check, not facts") || !strings.Contains(notes, "fixer is Cursor") {
		t.Errorf("the notes are not after the program's part as claims:\n%s", notes)
	}
	if strings.Contains(program, "fixer is Cursor") {
		t.Errorf("the coordinator's notes reached the program's part:\n%s", program)
	}
}

// The program's part of a later pass names the base of the newest earlier
// pass, the default-branch commit its copy was cut at (PassBase) and not
// the knowledge revision its launch recorded, and when it was created, and
// the holds earlier passes raised that are still open, and an empty set of
// notes is said to be none rather than refused.
func TestALaterPassIsToldTheLastBaseAndTheOpenQuestions(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	first := f.create(t, pass("")).Task
	f.beginAttempt(t, first.ID)
	if err := f.store.SetAttemptKnowledge(ctx, first.ID, 1, store.AttemptKnowledge{Commit: "deadbeef"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetTaskStartSnapshot(ctx, first.ID, store.CopySnapshot{Head: "c0ffee"}); err != nil {
		t.Fatal(err)
	}
	hold := store.Hold{Key: HoldKey(first.ID, "f1"), TaskID: store.Known(first.ID), Subject: "whether the rule on P14 should read otherwise",
		Detail: "the proposed wording"}
	if _, _, err := f.store.StoreTaskReport(ctx, store.TaskReport{TaskID: first.ID, Body: "b", Report: []byte(`{"summary":"s"}`)},
		[]store.Hold{hold}); err != nil {
		t.Fatal(err)
	}
	// A pass whose report decision is open cannot be relayed, so it ends
	// by retirement, which leaves the decision open for the next pass.
	retire := store.Hold{Key: "task/" + first.ID + "/housekeeping-retire/1/1/-", TaskID: store.Known(first.ID), Subject: "retire?", Detail: "d"}
	// The hold is offered as the loop offers it, so it is the pass's marked
	// retirement hold, the only hold the store answers retire on.
	inc, err := f.store.BeginIncarnation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.OfferRetirementHold(ctx, inc, store.RetirementOffer{Hold: retire, Attempt: 1,
		Ended: func(store.RunStatus) bool { return true }, Delivered: taskstate.Delivered}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RetireHousekeepingPass(ctx, store.Retirement{Task: first.ID, Attempt: 1, Hold: retire.Key,
		By: store.ResolvedByMachineInterface(), Relaunches: func(string) bool { return false },
		Ended: func(store.RunStatus) bool { return true }, Delivered: taskstate.Delivered}); err != nil {
		t.Fatalf("retiring the first pass: %v", err)
	}
	second := f.create(t, pass(""))
	text := f.instructionsOf(t, second.Task.ID)
	for _, want := range []string{"The last pass was task " + first.ID, "c0ffee", "git log c0ffee..HEAD", hold.Key,
		"whether the rule on P14", "The coordinator added none."} {
		if !strings.Contains(text, want) {
			t.Errorf("the second pass's instructions do not say %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "deadbeef") {
		t.Errorf("the second pass's instructions name the knowledge revision as the base:\n%s", text)
	}
}

// Every refusal of a marked create is typed and records nothing: a role, a
// delivery shape, a branch, a posture, an overlap or no repository is
// incorrect usage; a repository whose trusted housekeeping.paths is empty is
// a *PassRefusal; one whose scope cannot be read is ErrPassScopeUnreadable;
// and any create that names the housekeeping role is refused, marked or not.
func TestAMarkedCreateIsRefusedWithATypedRefusal(t *testing.T) {
	f := newFixture(t)
	for name, mutate := range map[string]func(*Request){
		"names a role":        func(r *Request) { r.Role = "worker" },
		"names the role":      func(r *Request) { r.Role = config.RoleHousekeeping },
		"is a delivery":       func(r *Request) { r.Shape = string(store.TaskDelivery) },
		"names a branch":      func(r *Request) { r.Branch = "b" },
		"names a posture":     func(r *Request) { r.Delivery = "gated" },
		"claims an overlap":   func(r *Request) { r.Overlaps = "taskaaaaaaaa" },
		"names no repository": func(r *Request) { r.Repository = "" },
	} {
		req := pass("")
		mutate(&req)
		if _, err := f.queue.Create(context.Background(), req); !errors.Is(err, ErrRequest) || !IsRefusal(err) {
			t.Errorf("a marked create that %s = %v, want ErrRequest", name, err)
		}
	}
	unmarked := delivery("")
	unmarked.Role = config.RoleHousekeeping
	if _, err := f.queue.Create(context.Background(), unmarked); !errors.Is(err, ErrRequest) || !errors.Is(err, config.ErrTaskRole) {
		t.Errorf("an ordinary create naming the housekeeping role = %v, want it refused", err)
	}

	f.scope = config.PatternSet{}
	_, err := f.queue.Create(context.Background(), pass(""))
	var refused *PassRefusal
	if !errors.As(err, &refused) || refused.Repository != repo || !IsRefusal(err) {
		t.Errorf("a pass of a repository whose trusted scope is empty = %v, want a *PassRefusal", err)
	}
	f.scope, f.scopeErr = nil, errors.New("the default branch could not be fetched")
	if _, err := f.queue.Create(context.Background(), pass("")); !errors.Is(err, ErrPassScopeUnreadable) {
		t.Errorf("a pass whose scope could not be read = %v, want ErrPassScopeUnreadable", err)
	}
	tasks, err := f.store.Tasks(context.Background())
	if err != nil || len(tasks) != 0 {
		t.Fatalf("the refusals recorded %d tasks (%v), want none", len(tasks), err)
	}
	// The positive control: the same request with the scope readable is
	// recorded.
	f.scope, f.scopeErr = config.Defaults().HousekeepingPaths, nil
	f.create(t, pass(""))
}

// PRD section 6's one-at-a-time rule at intake: a marked create while a pass
// of the repository is open is refused naming it, and two marked creates at
// once, both held between their reads and their writes, leave exactly one
// task, because the store decides under its serialization (P6's anchoring
// of a write to what was read).
func TestTwoMarkedCreatesAtOnceLeaveOnePass(t *testing.T) {
	principles.Cite(t, principles.P6)
	f := newFixture(t)
	var arrived sync.WaitGroup
	arrived.Add(2)
	release := make(chan struct{})
	f.queue.betweenReadAndWrite = func() {
		arrived.Done()
		<-release
	}
	results := make([]error, 2)
	var done sync.WaitGroup
	for i := range results {
		done.Add(1)
		go func() {
			defer done.Done()
			_, results[i] = f.queue.Create(context.Background(), pass(""))
		}()
	}
	arrived.Wait()
	close(release)
	done.Wait()
	f.queue.betweenReadAndWrite = nil
	var created, refused int
	for _, err := range results {
		var open *store.PassOpenError
		switch {
		case err == nil:
			created++
		case errors.As(err, &open) && IsRefusal(err):
			refused++
		default:
			t.Fatalf("a concurrent create failed for another reason: %v", err)
		}
	}
	all, err := f.store.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 || refused != 1 || len(all) != 1 {
		t.Fatalf("two concurrent marked creates created %d, refused %d and recorded %d tasks, want one of each and one task",
			created, refused, len(all))
	}
	_, err = f.queue.Create(context.Background(), pass(""))
	var open *store.PassOpenError
	if !errors.As(err, &open) || open.Pass != all[0].ID {
		t.Fatalf("a later create while %s is open = %v, want it refused naming that pass", all[0].ID, err)
	}
}
