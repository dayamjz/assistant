package stages_test

import (
	"encoding/json"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// diffEmpty reports whether out takes PRD section 5's empty-diff short
// circuit, read the way every stage node reads it: the KeyDiffEmpty write.
func diffEmpty(out pipeline.Output) bool {
	v, ok := out.Writes[pipeline.KeyDiffEmpty]
	if !ok {
		return false
	}
	set, _ := v.Bool()
	return set
}

// positionAfter is the run's position once the rebase stage's output is
// written: the run inputs the stage read, with every write it returned on
// top. It goes through the state's own JSON encoding, which is how a
// checkpoint carries a position back to a reader, so the reader is handed
// what a checkpoint could hold and nothing assembled beside it.
func positionAfter(t *testing.T, run *pushRun, out pipeline.Output) graph.State {
	t.Helper()
	values := map[string]graph.Value{
		string(pipeline.KeySubmitted): graph.TextValue(run.submitted),
		string(pipeline.KeyPushed):    graph.TextValue(run.pushed),
	}
	for key, value := range out.Writes {
		values[string(key)] = value
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("encoding the position: %v", err)
	}
	var s graph.State
	if err := json.Unmarshal(encoded, &s); err != nil {
		t.Fatalf("decoding the position: %v", err)
	}
	return s
}

// nothingAfter reads what the position after out says about nothing left to
// deliver, through the one reader internal/service uses.
func nothingAfter(t *testing.T, run *pushRun, out pipeline.Output) stages.NothingToDeliver {
	t.Helper()
	got, err := stages.NothingToDeliverOf(positionAfter(t, run, out))
	if err != nil {
		t.Fatalf("reading the position: %v", err)
	}
	return got
}

// PRD section 5's rebase row: if nothing remains to change after the rebase,
// the run ends successfully and the rest is skipped. A branch whose one
// commit makes the edit the default branch already carries under another
// commit is replayed by a real rebase, which drops the commit, and the stage
// takes the short circuit. The positive control is the same branch rebased
// onto a default branch that moved on a path of its own: something remains,
// and the stage does not.
func TestARebaseThatDropsTheBranchsOnlyCommitTakesTheShortCircuit(t *testing.T) {
	t.Run("the same edit landed under another commit", func(t *testing.T) {
		run := newPushRun(t)
		submitted := run.head
		run.commitOnTopOfUpstream(t, "main", "change.go", "package subject\n\n// the change under validation\n")

		out := run.rebase(t)
		if run.head == submitted {
			t.Fatalf("the rebase left the head at %s, so it replayed nothing", submitted)
		}
		if !diffEmpty(out) {
			t.Fatalf("a rebase that left nothing to change wrote %v, without the short circuit (report: %+v)",
				out.Writes, out.Report)
		}
	})

	t.Run("control: the default branch moved elsewhere", func(t *testing.T) {
		run := newPushRun(t)
		run.commitOnTopOfUpstream(t, "main", "elsewhere.go", "package subject\n\n// landed first\n")

		if out := run.rebase(t); diffEmpty(out) {
			t.Fatalf("a rebase that left the change in place took the short circuit: %+v", out.Report)
		}
	})
}

// PRD section 6's "nothing left to deliver", as the rebase stage records it
// (P6, P8): beside the short circuit, in the same write, the default-branch
// commit it read and P12's content check of the run's own commits against
// it, read back from the position by stages.NothingToDeliverOf and never
// from the stage's report. Lands holds for a branch the upstream does not
// hold whose one commit the default branch already carries by content under
// another commit. Each other case takes the same short circuit and must not
// land: a default branch that applied the same edit and then reverted it,
// which the replay drops cleanly by patch identity while the default branch
// lacks the edit, so the content check is shown to fail; a branch already
// published where the run would push; a branch with no commit of its own; a
// commit together with its revert; and a conflict resolved by a fix round
// that drops the change. The positive control is a branch whose change the
// default branch does not carry, which takes no short circuit and records no
// check at all.
func TestTheRebaseStageRecordsWhetherNothingIsLeftToDeliver(t *testing.T) {
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P8)

	const change = "package subject\n\n// the change under validation\n"

	t.Run("the same edit landed under another commit lands", func(t *testing.T) {
		run := newPushRun(t)
		landed := run.commitOnTopOfUpstream(t, "main", "change.go", change)

		out := run.rebase(t)
		got := nothingAfter(t, run, out)
		if lands, why := got.Lands(); !lands {
			t.Fatalf("the position reads %+v, which does not land: %s", got, why)
		}
		if got.Against != landed || got.Submitted != run.submitted || len(got.Examined) != 1 || got.Examined[0] != run.submitted {
			t.Fatalf("the position reads %+v, want the check of %s against %s", got, run.submitted, landed)
		}
	})

	t.Run("an edit the default branch applied and reverted does not land", func(t *testing.T) {
		run := newPushRun(t)
		run.commitOnTopOfUpstream(t, "main", "change.go", change)
		other := t.TempDir()
		git(t, other, "clone", "--quiet", run.upstream, other)
		git(t, other, "revert", "--no-edit", "HEAD")
		git(t, other, "push", "--quiet", "origin", "main")
		reverted := git(t, other, "rev-parse", "HEAD")

		out := run.rebase(t)
		if !diffEmpty(out) {
			t.Fatalf("the replay of an edit the default branch reverted did not take the short circuit, "+
				"so the case the content check exists for is not exercised: %+v", out.Report)
		}
		got := nothingAfter(t, run, out)
		if lands, _ := got.Lands(); lands || got.Present || got.Against != reverted {
			t.Fatalf("the position reads %+v: the default branch %s lacks the edit and the run must not land", got, reverted)
		}
	})

	t.Run("a branch already published does not land", func(t *testing.T) {
		run := newPushRun(t)
		git(t, run.source, "push", "--quiet", "origin", run.baseCommit+":refs/heads/topic")
		run.commitOnTopOfUpstream(t, "main", "change.go", change)

		out := run.rebase(t)
		if !diffEmpty(out) {
			t.Fatalf("the published branch's replay took no short circuit: %+v", out.Report)
		}
		got := nothingAfter(t, run, out)
		if lands, _ := got.Lands(); lands || got.Unpublished || !got.Present {
			t.Fatalf("the position reads %+v: the branch was published and the run must not land", got)
		}
	})

	t.Run("a branch with no commit of its own does not land", func(t *testing.T) {
		run := newPushRun(t)
		git(t, run.copy, "checkout", "--quiet", "--detach", run.baseCommit)
		run.head, run.submitted = run.baseCommit, run.baseCommit

		out := run.rebase(t)
		if !diffEmpty(out) {
			t.Fatalf("a head on the default branch took no short circuit: %+v", out.Report)
		}
		got := nothingAfter(t, run, out)
		if lands, _ := got.Lands(); lands || got.Present || len(got.Examined) != 0 || got.Against == "" {
			t.Fatalf("the position reads %+v: a branch with nothing of its own must record no answer", got)
		}
	})

	t.Run("a commit together with its revert does not land", func(t *testing.T) {
		run := newPushRun(t)
		git(t, run.copy, "config", "user.name", "test")
		git(t, run.copy, "config", "user.email", "test@example.invalid")
		git(t, run.copy, "revert", "--no-edit", "HEAD")
		run.head = git(t, run.copy, "rev-parse", "HEAD")
		run.submitted = run.head

		out := run.rebase(t)
		if !diffEmpty(out) {
			t.Fatalf("a commit and its revert took no short circuit: %+v", out.Report)
		}
		got := nothingAfter(t, run, out)
		if lands, _ := got.Lands(); lands || got.Present || len(got.Examined) != 2 {
			t.Fatalf("the position reads %+v: the commit on its own is not present and the run must not land", got)
		}
	})

	t.Run("a conflict a fix round resolved by dropping the change does not land", func(t *testing.T) {
		run := newPushRun(t)
		run.commitOnTopOfUpstream(t, "main", "change.go", "package subject\n\n// somebody else's change\n")
		out := run.rebase(t)
		if len(out.Report.Findings) != 1 || out.Report.Findings[0].ID != "rebase-conflict" {
			t.Fatalf("the replay did not stop on its conflict: %+v", out.Report)
		}
		// What the rebase fix round does with a resolution that matches the
		// commit the rebase stopped on: it skips the step (fix.go).
		git(t, run.copy, "-c", "core.editor=true", "rebase", "--skip")
		run.head = git(t, run.copy, "rev-parse", "HEAD")

		out = run.rebase(t)
		if !diffEmpty(out) {
			t.Fatalf("the round that dropped the change took no short circuit: %+v", out.Report)
		}
		got := nothingAfter(t, run, out)
		if lands, _ := got.Lands(); lands || got.Present {
			t.Fatalf("the position reads %+v: the fix round dropped the change and the run must not land", got)
		}
	})

	t.Run("control: a change the default branch lacks records no check", func(t *testing.T) {
		run := newPushRun(t)
		run.commitOnTopOfUpstream(t, "main", "elsewhere.go", "package subject\n\n// landed first\n")

		out := run.rebase(t)
		if _, ok := out.Writes[pipeline.KeyDiffEmptyCheck]; ok {
			t.Fatalf("a run with something left to change recorded a check: %v", out.Writes)
		}
		if lands, _ := nothingAfter(t, run, out).Lands(); lands {
			t.Fatal("a run with something left to change lands")
		}
	})
}

// A run that pushed before its rebase left nothing to push never lands this
// way, whatever its position holds: the stage's pass after a push holds
// rather than taking the short circuit, and the reader refuses a pushed run
// even were a check recorded beside one.
func TestAPushedRunNeverLandsOnNothingLeft(t *testing.T) {
	run := newPushRun(t)
	run.commitOnTopOfUpstream(t, "main", "change.go", "package subject\n\n// the change under validation\n")
	out := run.rebase(t)
	if lands, why := nothingAfter(t, run, out).Lands(); !lands {
		t.Fatalf("the unpushed control does not land: %s", why)
	}
	run.pushed = run.submitted
	if lands, _ := nothingAfter(t, run, out).Lands(); lands {
		t.Fatal("a run that pushed lands on nothing left")
	}
}
