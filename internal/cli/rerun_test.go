package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// A rerun validates the branch's last known head, not the working
// copy's HEAD. When a person commits after a run ends, those are two commits,
// and a rerun that went ahead would re-vet the one they have moved on from.
// So it is refused, naming both commits and the two ways to validate the
// local one: a push to the gate, or a start with an intent.
//
// P1 is the consent boundary, and the half established here is that a rerun
// the person did not get stays a read: the refusal begins no run, and the
// working copy's HEAD, branches, index and files are as they were. P13 is the
// refusal's sentence, which is the phrase-table row with the commits and the
// commands beside it rather than in it.
//
// The run is ended before it reaches the push stage, so it pushed nothing and
// the head a rerun validates is the commit it was submitted. The positive
// control is the same branch with no local commit, where the rerun proceeds
// and validates that commit, so the refusal is about the commit and not about
// reruns in general. A checkout behind the submitted commit is refused with
// the same remedy and not sent to sync, because a run that pushed nothing has
// no work of its own for sync to bring in.
func TestARerunIsRefusedWhenTheCheckoutMovedPastTheRecordedHead(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P1)
	principles.Cite(t, principles.P13)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	const branch = "a-change"
	standingOnChange(t, subject, branch)
	first := decodeRun(t, run(t, h, subject, "--json", "--intent", "a change with its tradeoffs stated").stdout)
	if ended := run(t, h, subject, "--json", "--cancel"); ended.code != machine.ExitOK {
		t.Fatalf("ending the first run exited %s:\n%s%s", ended.code, ended.stdout, ended.stderr)
	}
	// The run was ended at its first hold, before the push stage, so it
	// pushed nothing and its last known head is the commit it was submitted.
	recorded := first.Record.SubmittedHead

	// The control: the checkout is where the run left it, so the rerun
	// proceeds, and what it validates is the recorded head.
	if local := git(t, subject, "rev-parse", "HEAD"); local != recorded {
		t.Fatalf("before any local commit the checkout is at %s and the run recorded %s", local, recorded)
	}
	again := run(t, h, subject, "--json", "rerun")
	if again.code != machine.ExitOK {
		t.Fatalf("a rerun from the recorded head exited %s:\n%s%s", again.code, again.stdout, again.stderr)
	}
	rerun := decodeRun(t, again.stdout)
	if rerun.Record.ID == first.Record.ID || rerun.Record.SubmittedHead != recorded {
		t.Fatalf("the rerun is run %s of %s, want a fresh run of %s", rerun.Record.ID, rerun.Record.SubmittedHead, recorded)
	}
	if ended := run(t, h, subject, "--json", "--cancel"); ended.code != machine.ExitOK {
		t.Fatalf("ending the rerun exited %s:\n%s%s", ended.code, ended.stdout, ended.stderr)
	}

	// A follow-up committed locally, and something left uncommitted beside
	// it, which a rerun that touched the working copy would disturb.
	if err := os.WriteFile(filepath.Join(subject, "follow-up.txt"), []byte("corrected locally\n"), 0o600); err != nil {
		t.Fatalf("writing the follow-up: %v", err)
	}
	git(t, subject, "add", "-A")
	git(t, subject, "commit", "--quiet", "-m", "a follow-up made after the run ended")
	if err := os.WriteFile(filepath.Join(subject, "uncommitted.txt"), []byte("not yet\n"), 0o600); err != nil {
		t.Fatalf("writing an uncommitted file: %v", err)
	}
	local := git(t, subject, "rev-parse", "HEAD")
	before := checkoutState(t, subject)
	runsBefore := listRuns(t, h, subject)

	refused := run(t, h, subject, "--json", "rerun")
	if refused.code != machine.ExitFailure {
		t.Fatalf("a rerun from a moved checkout exited %s, want the operational failure:\n%s%s", refused.code, refused.stdout, refused.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(refused.stdout), &failure); err != nil {
		t.Fatalf("the refusal is not one document: %v\n%s", err, refused.stdout)
	}
	if failure.Code != string(ipc.CodeRefused) {
		t.Fatalf("the refusal crossed the wire as %q, want %q: %s", failure.Code, ipc.CodeRefused, failure.Error)
	}
	for _, want := range []string{local, recorded, "git push " + gate.RemoteName + " " + branch, "assistant --intent"} {
		if !strings.Contains(failure.Error, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, failure.Error)
		}
	}
	if strings.Contains(failure.Error, "assistant sync") {
		t.Errorf("a checkout ahead of the recorded head was sent to sync:\n%s", failure.Error)
	}
	t.Logf("assistant --json rerun, from a checkout one commit past the recorded head:\n%s", refused.stdout)

	human := run(t, h, subject, "rerun")
	if human.code != machine.ExitFailure {
		t.Fatalf("the human rendering exited %s:\n%s", human.code, human.stderr)
	}
	for _, want := range []string{machine.RerunMoved{Reason: machine.RerunMovedElsewhere}.Phrase().String(), local, recorded} {
		if !strings.Contains(human.stderr, want) {
			t.Errorf("the person reads:\n%s\nwhich does not say %q", human.stderr, want)
		}
	}

	// It stayed a read: no run began, and the checkout is as it was.
	if after := listRuns(t, h, subject); len(after) != len(runsBefore) {
		t.Fatalf("the refused rerun recorded a run: %d runs before and %d after", len(runsBefore), len(after))
	}
	if after := checkoutState(t, subject); after != before {
		t.Fatalf("the refused rerun changed the checkout:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// Behind the submitted commit, which a reset produces. The run pushed
	// nothing, so there is no work of its own for sync to bring in, and the
	// refusal keeps the general remedy.
	git(t, subject, "reset", "--quiet", "--hard", recorded+"^")
	parent := git(t, subject, "rev-parse", "HEAD")
	reset := refusedRerun(t, h, subject)
	for _, want := range []string{parent, recorded, "assistant --intent"} {
		if !strings.Contains(reset.Error, want) {
			t.Errorf("the refusal from behind the submitted commit does not say %q:\n%s", want, reset.Error)
		}
	}
	if strings.Contains(reset.Error, "assistant sync") {
		t.Errorf("a checkout behind the submitted commit of a run that pushed nothing was sent to sync:\n%s", reset.Error)
	}
}

// checkoutState is what a refused rerun must leave alone in a working copy:
// where HEAD points and what it resolves to, every reference, and the index
// and files as git status sees them.
func checkoutState(t *testing.T, dir string) string {
	t.Helper()
	return strings.Join([]string{
		git(t, dir, "symbolic-ref", "HEAD"),
		git(t, dir, "rev-parse", "HEAD"),
		git(t, dir, "for-each-ref"),
		git(t, dir, "status", "--porcelain=v1", "--untracked-files=all"),
	}, "\n")
}

// A rerun validates what the branch's last run pushed. The run here is a real
// one: the product's own intent, rebase, review and push bodies and its own fix
// path, over a scripted agent, with the test stage replaced by a check that
// fails until a file exists in the run's copy. The fix round writes that file
// and the fix path commits it, so the push stage forwards a commit past the one
// submitted, and that commit reaches the run's checkpoint the way any push
// does. Nothing writes a head into the run's record, and the rerun reads none.
//
// From the submitted commit the checkout is behind what the run pushed, which
// is the case assistant sync reconciles, so the refusal names sync and not a
// push or a start. The controls beside it keep the general remedy: a checkout
// diverged from the pushed commit, and one ahead of it, both validate the
// person's own commit, and sync is named for neither. Every refusal begins no
// run and changes nothing in the checkout, on P1's terms, and says it in the
// phrase-table row P13 asks for.
//
// The remedy is then taken as the refusal names it: assistant sync --confirm
// brings the pushed commit into the checkout, and the rerun that follows
// proceeds and validates that commit rather than the one first submitted.
func TestARerunValidatesWhatTheRunPushedAndBehindItNamesSync(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P1)
	principles.Cite(t, principles.P13)
	h := newHome(t)
	subject := newSubject(t)
	agent := standin.New(t, standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")},
		{Times: standin.Always, Reply: standin.Text("added " + fixedFile + ", which the test stage asked for")},
	}})
	serveFixing(t, h, fixedByARound(h), fixWritingTheFile(h), agentCatalog(agent.Runner()))

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	identifyGate(t, h, subject)
	const branch = "a-change"
	standingOnChange(t, subject, branch)
	submitted := git(t, subject, "rev-parse", "HEAD")
	started := run(t, h, subject, "--json", "--intent", "a change with its tradeoffs stated")
	if started.code != machine.ExitOK {
		t.Fatalf("the start exited %s:\n%s%s\nthe agent was asked:\n%v", started.code, started.stdout, started.stderr, agent.Calls())
	}
	first := decodeRun(t, started.stdout)
	if first.Record.SubmittedHead != submitted {
		t.Fatalf("the run validated %s, want the commit the checkout stood at, %s", first.Record.SubmittedHead, submitted)
	}
	if first.Decision != nil || first.Advancing {
		t.Fatalf("the run did not walk to its end: %+v", first)
	}

	// What the run pushed, read off the upstream rather than off anything
	// the product reports: a commit whose parent is the submitted one.
	pushed := strings.Fields(git(t, subject, "ls-remote", "origin", "refs/heads/"+branch))
	if len(pushed) == 0 {
		t.Fatalf("the run pushed nothing to the upstream's %s", branch)
	}
	work := pushed[0]
	git(t, subject, "fetch", "--quiet", "origin")
	if work == submitted || git(t, subject, "rev-parse", work+"^") != submitted {
		t.Fatalf("the upstream's %s is %s, want the fix round's commit on top of %s", branch, work, submitted)
	}

	// Behind what the run pushed: the refusal names sync.
	before := checkoutState(t, subject)
	runsBefore := listRuns(t, h, subject)
	behind := refusedRerun(t, h, subject)
	for _, want := range []string{submitted, work, "assistant sync", "assistant sync --confirm"} {
		if !strings.Contains(behind.Error, want) {
			t.Errorf("the refusal from behind the run's push does not say %q:\n%s", want, behind.Error)
		}
	}
	if strings.Contains(behind.Error, "assistant --intent") {
		t.Errorf("a checkout behind the run's push was told to validate its own commit:\n%s", behind.Error)
	}
	t.Logf("assistant --json rerun, from a checkout behind the run's push:\n%s", behind.Error)
	human := run(t, h, subject, "rerun")
	if want := (machine.RerunMoved{Reason: machine.RerunMovedBehind}).Phrase().String(); human.code != machine.ExitFailure ||
		!strings.Contains(human.stderr, want) {
		t.Fatalf("the person reads, exit %s:\n%s\nwhich does not say %q", human.code, human.stderr, want)
	}
	if after := listRuns(t, h, subject); len(after) != len(runsBefore) {
		t.Fatalf("the refused rerun recorded a run: %d runs before and %d after", len(runsBefore), len(after))
	}
	if after := checkoutState(t, subject); after != before {
		t.Fatalf("the refused rerun changed the checkout:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// The controls: diverged from the push, and ahead of it. Each validates
	// the person's own commit, and neither is sent to sync.
	commitFile := func(name string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(subject, name), []byte("made locally\n"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		git(t, subject, "add", "-A")
		git(t, subject, "commit", "--quiet", "-m", "a local commit, "+name)
		return git(t, subject, "rev-parse", "HEAD")
	}
	for _, c := range []struct {
		name  string
		from  string
		local func() string
	}{
		{"diverged from the push", submitted, func() string { return commitFile("diverged.txt") }},
		{"ahead of the push", work, func() string { return commitFile("ahead.txt") }},
	} {
		git(t, subject, "reset", "--quiet", "--hard", c.from)
		local := c.local()
		before := checkoutState(t, subject)
		refused := refusedRerun(t, h, subject)
		for _, want := range []string{local, work, "git push " + gate.RemoteName + " " + branch, "assistant --intent"} {
			if !strings.Contains(refused.Error, want) {
				t.Errorf("the refusal from a checkout %s does not say %q:\n%s", c.name, want, refused.Error)
			}
		}
		if strings.Contains(refused.Error, "assistant sync") {
			t.Errorf("a checkout %s was sent to sync:\n%s", c.name, refused.Error)
		}
		if after := checkoutState(t, subject); after != before {
			t.Fatalf("the refused rerun from a checkout %s changed it:\nbefore:\n%s\nafter:\n%s", c.name, before, after)
		}
	}
	if after := listRuns(t, h, subject); len(after) != len(runsBefore) {
		t.Fatalf("the refused reruns recorded a run: %d runs before and %d after", len(runsBefore), len(after))
	}

	// The remedy the refusal named, taken: sync brings the push into the
	// checkout, and the rerun then validates it.
	git(t, subject, "reset", "--quiet", "--hard", submitted)
	var plan machine.Plan
	synced := run(t, h, subject, "--json", "sync", "--confirm")
	if err := json.Unmarshal([]byte(synced.stdout), &plan); err != nil || !plan.Applied {
		t.Fatalf("assistant sync --confirm exited %s and answered %+v (%v):\n%s%s", synced.code, plan, err, synced.stdout, synced.stderr)
	}
	if local := git(t, subject, "rev-parse", "HEAD"); local != work {
		t.Fatalf("after sync the checkout is at %s, want the run's push %s", local, work)
	}
	again := run(t, h, subject, "--json", "rerun")
	if again.code == machine.ExitFailure {
		t.Fatalf("a rerun from the pushed commit exited %s:\n%s%s", again.code, again.stdout, again.stderr)
	}
	rerun := decodeRun(t, again.stdout)
	if rerun.Record.ID == first.Record.ID || rerun.Record.SubmittedHead != work {
		t.Fatalf("the rerun is run %s of %s, want a fresh run of the pushed commit %s (submitted was %s)",
			rerun.Record.ID, rerun.Record.SubmittedHead, work, submitted)
	}
}

// identifyGate gives the gate's repository a committer identity. A run's
// isolated copy is a linked worktree of that repository and shares its
// configuration, and a fix round that changed something commits there, which
// needs an identity. On an operator's machine their global configuration
// supplies it; a CI runner carries none, and without one the fix round fails
// with git's "Author identity unknown" and the run never reaches its push. It
// is internal/service's identifyGateRepository, reached through the command
// surface's status rather than the service's records.
func identifyGate(t *testing.T, h *home.Home, subject string) {
	t.Helper()
	var status machine.Status
	if got := run(t, h, subject, "--json", "status"); json.Unmarshal([]byte(got.stdout), &status) != nil ||
		status.Gate == nil || status.Gate.ID == "" {
		t.Fatalf("status names no gate:\n%s%s", got.stdout, got.stderr)
	}
	repository, err := gate.RepositoryFor(h.Root(), status.Gate.ID)
	if err != nil {
		t.Fatalf("locating the gate repository: %v", err)
	}
	git(t, repository, "config", "user.name", "test")
	git(t, repository, "config", "user.email", "test@example.invalid")
}

// fixedFile is the file the stand-in test stage below asks for and the fix
// round below writes.
const fixedFile = "fixed.txt"

// copyOfRun is where internal/home places the run a stage or a fix round is
// reading the state of.
func copyOfRun(h *home.Home, state pipeline.Reader) (string, error) {
	var ids [2]string
	for i, key := range []pipeline.Key{pipeline.KeyRepository, pipeline.KeyRun} {
		v, err := state.Get(key)
		if err != nil {
			return "", err
		}
		ids[i], _ = v.Text()
	}
	return h.Worktree(ids[0], ids[1]), nil
}

// fixedByARound serves the product's stages with the test stage replaced by a
// check that fails, fix-eligible, until fixedFile is in the run's copy, and
// the stages after lint up to the push with ones that pass, so the run reaches
// the push without a person and without a code host. The push and everything
// before it are the product's bodies.
func fixedByARound(h *home.Home) func(stages.StageDeps) pipeline.Stages {
	return func(deps stages.StageDeps) pipeline.Stages {
		nine := stages.All(deps)
		nine.Test = pipeline.Implementation{
			Reads: []pipeline.Key{pipeline.KeyRepository, pipeline.KeyRun},
			NewBody: func() pipeline.Body {
				return func(_ context.Context, in pipeline.Input) (pipeline.Output, error) {
					dir, err := copyOfRun(h, in.State)
					if err != nil {
						return pipeline.Output{}, err
					}
					if _, err := os.Stat(filepath.Join(dir, fixedFile)); err == nil {
						return pipeline.Output{Report: findings.Report{Summary: "the check passed"}}, nil
					}
					return pipeline.Output{Report: findings.Report{
						Summary: "the check failed",
						Findings: []findings.Finding{{
							ID: "missing-file", Severity: findings.SeverityError, Action: findings.ActionFix,
							Description: fixedFile + " is missing",
						}},
					}}, nil
				}
			},
		}
		nine.Document = pipeline.Constant("nothing to document")
		nine.Lint = pipeline.Constant("nothing to lint")
		nine.PR = pipeline.Constant("no code host in this test")
		nine.CI = pipeline.Constant("no checks in this test")
		return nine
	}
}

// fixWritingTheFile is the product's fix path with one thing added before the
// scripted fixer is asked: fixedFile is written into the run's copy, which is
// the edit a real agent would make and the stand-in cannot. The fix path then
// commits it as it commits any round's work.
func fixWritingTheFile(h *home.Home) func(stages.FixDeps) pipeline.Fixer {
	return func(deps stages.FixDeps) pipeline.Fixer {
		real := stages.Fix(deps)
		wrapped := real
		wrapped.NewBody = func() pipeline.FixBody {
			body := real.NewBody()
			return func(ctx context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
				dir, err := copyOfRun(h, in.State)
				if err != nil {
					return pipeline.FixOutput{}, err
				}
				if err := os.WriteFile(filepath.Join(dir, fixedFile), []byte("present\n"), 0o600); err != nil {
					return pipeline.FixOutput{}, err
				}
				return body(ctx, in)
			}
		}
		return wrapped
	}
}

// refusedRerun runs assistant --json rerun, requires the refusal, and returns
// it as it crossed the wire.
func refusedRerun(t *testing.T, h *home.Home, subject string) machine.Failure {
	t.Helper()
	got := run(t, h, subject, "--json", "rerun")
	if got.code != machine.ExitFailure {
		t.Fatalf("the rerun exited %s, want the operational failure:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
		t.Fatalf("the refusal is not one document: %v\n%s", err, got.stdout)
	}
	if failure.Code != string(ipc.CodeRefused) {
		t.Fatalf("the refusal crossed the wire as %q, want %q: %s", failure.Code, ipc.CodeRefused, failure.Error)
	}
	return failure
}
