package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// TestAPushToTheGateStartsARunForThePushedCommit is PRD principle P1's
// positive half as a test: pushing to the gate by name is what authorizes a
// run, and it is the only thing in this product that does so without somebody
// typing a command. internal/gate holds the negative half - a push to origin
// starts nothing - and neither half means much without the other.
//
// Nothing here calls a verb that starts a run. A gate is initialized, a branch
// is pushed to it with ordinary git, and the hooks internal/gate installed are
// what reach the command surface. That is the whole path the product's entry
// point takes, so a break anywhere along it - the hook script, the
// subcommands, the identifier, the home, the reference update lines, the
// service call - fails this rather than being caught by a unit test of a piece
// nobody wired up.
//
// The branch pushed is not the branch the working copy is standing on. That is
// what makes this a test of the pushed reference rather than of the working
// copy's head: an implementation that read the head would start a run for
// main, which is a run about a commit nobody pushed.
func TestAPushToTheGateStartsARunForThePushedCommit(t *testing.T) {
	principles.Cite(t, principles.P1)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	initialize(t, h, subject)

	// A branch with a commit of its own, left unchecked-out so the working
	// copy's head stays on main.
	git(t, subject, "branch", "work")
	head := commitOn(t, subject, "work", "work.txt", "a change to validate\n")
	if branch := git(t, subject, "rev-parse", "--abbrev-ref", "HEAD"); branch != "main" {
		t.Fatalf("the working copy is standing on %q, and this test needs it on main so that the "+
			"branch it pushes is not the one a head would name", branch)
	}

	pushed := git(t, subject, "push", gate.RemoteName, "work")
	t.Logf("git push %s work:\n%s", gate.RemoteName, pushed)

	started := onlyRun(t, h, subject)
	if started.Branch != "work" {
		t.Fatalf("the push started a run for branch %q, want work; the run is about the reference that "+
			"was pushed and not about the working copy's head", started.Branch)
	}
	if started.SubmittedHead != head {
		t.Fatalf("the run validates %q, want the commit the push moved work to, %q",
			started.SubmittedHead, head)
	}
	// A push carries no intent text, and the record has to say which of the
	// three ways that happened. The column is NOT NULL with no CHECK, so a
	// creator that named none writes the empty string and nothing refuses it.
	if started.IntentSource == "" {
		t.Fatalf("the run the push started records an empty intent source; the field is a closed "+
			"vocabulary and the empty string is outside it, so the record says its intent came from "+
			"a source that does not exist:\n%+v", started)
	}

	// And it is a run the service is advancing, not a record nobody picked up.
	// It walks the stages that have a body and stops at the first that does
	// not, which is read from internal/stages rather than named here.
	held := awaitHold(t, h, subject, started.ID)
	if held.Outcome != machine.OutcomeDecision {
		t.Fatalf("the run the push started reached %s, want a decision:\n%+v", held.Outcome, held)
	}
	if want := firstStageWithoutABody(t); held.Decision == nil || held.Decision.Stage != want.String() {
		t.Fatalf("the run holds at %+v, want the first stage with no body, %s", held.Decision, want)
	}
}

// TestASecondPushSupersedesTheRunTheFirstStarted is PRD section 8's "a new
// push supersedes the run in progress".
//
// The first push's run is still in flight when the second arrives, so what is
// checked is a replacement rather than a second run started after the first
// finished on its own. Without it the second push would attach the person who
// pushed to nothing while the record still showed a run of an older commit.
//
// What is checked is the record, which is the whole of what supersession
// guarantees today: the first run's status and the second run's head. The
// displaced run's execution is signalled to cancel and not awaited, so nothing
// here observes it stopping and nothing here could.
func TestASecondPushSupersedesTheRunTheFirstStarted(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	initialize(t, h, subject)

	git(t, subject, "checkout", "--quiet", "-b", "work")
	commitOn(t, subject, "work", "work.txt", "the first change\n")
	git(t, subject, "push", gate.RemoteName, "work")
	first := onlyRun(t, h, subject)
	awaitHold(t, h, subject, first.ID)

	second := commitOn(t, subject, "work", "work.txt", "the second change\n")
	git(t, subject, "push", gate.RemoteName, "work")

	records := listRuns(t, h, subject)
	if len(records) != 2 {
		t.Fatalf("two pushes of one branch left %d run(s), want two:\n%+v", len(records), records)
	}
	replacement, replaced := records[0], records[1]
	if replaced.ID != first.ID {
		t.Fatalf("the older run is %s, want the one the first push started, %s", replaced.ID, first.ID)
	}
	if replaced.Status != store.RunTerminated {
		t.Fatalf("the run the first push started is %s, want it superseded by the second push",
			replaced.Status)
	}
	if replacement.SubmittedHead != second {
		t.Fatalf("the run the second push started validates %q, want %q", replacement.SubmittedHead, second)
	}
	awaitHold(t, h, subject, replacement.ID)
}

// TestAPushThatIsNotAChangeToValidateStartsNoRunAndSaysSo covers the two
// references a push can carry that the gate has nothing to validate: one that
// is not a branch, and a branch the push deletes.
//
// Both are accepted rather than refused - the gate is a repository git can
// push to normally, and refusing a tag would make it something else - and both
// are reported. A push that quietly started nothing would be a push somebody
// waits on a run for.
func TestAPushThatIsNotAChangeToValidateStartsNoRunAndSaysSo(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	initialize(t, h, subject)

	git(t, subject, "checkout", "--quiet", "-b", "work")
	commitOn(t, subject, "work", "work.txt", "a change\n")
	git(t, subject, "tag", "v1")

	tagged := git(t, subject, "push", gate.RemoteName, "v1")
	t.Logf("git push %s v1:\n%s", gate.RemoteName, tagged)
	if !strings.Contains(tagged, "refs/tags/v1") {
		t.Fatalf("the push of a tag says nothing about the tag it carried:\n%s", tagged)
	}
	if got := listRuns(t, h, subject); len(got) != 0 {
		t.Fatalf("pushing a tag started %d run(s):\n%+v", len(got), got)
	}

	git(t, subject, "push", gate.RemoteName, "work")
	if got := listRuns(t, h, subject); len(got) != 1 {
		t.Fatalf("pushing a branch started %d run(s), want one; without it the check above would "+
			"pass for a gate that starts nothing at all", len(got))
	}
	awaitHold(t, h, subject, listRuns(t, h, subject)[0].ID)

	git(t, subject, "checkout", "--quiet", "main")
	deleted := git(t, subject, "push", gate.RemoteName, ":work")
	t.Logf("git push %s :work:\n%s", gate.RemoteName, deleted)
	if got := listRuns(t, h, subject); len(got) != 1 {
		t.Fatalf("deleting a branch started a run: %d run(s) now:\n%+v", len(got), got)
	}
}

// TestAPushReturnsWhileTheRunItStartedIsStillInsideAStage is PRD section 8's
// "a push returns immediately; the notification hands off and exits, and the
// service owns everything long-running".
//
// It is checked against a run that genuinely cannot finish while the push is
// waiting: the first stage's body blocks until this test releases it, and the
// push has to have returned before that. A test that pushed and then observed
// a finished run could not tell a notification that handed off from one that
// waited, because both end with the run having advanced.
func TestAPushReturnsWhileTheRunItStartedIsStillInsideAStage(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)

	inside := make(chan struct{})
	release := make(chan struct{})
	var entered, released sync.Once
	let := func() { released.Do(func() { close(release) }) }
	serveHeldAtIntent(t, h, inside, release, &entered)
	// Registered after the service so it runs before the service is closed: a
	// failure while the body is held would otherwise leave the teardown
	// waiting on a stage nothing is going to release.
	t.Cleanup(let)

	initialize(t, h, subject)
	git(t, subject, "checkout", "--quiet", "-b", "work")
	commitOn(t, subject, "work", "work.txt", "a change\n")
	// Bounded, because the failure this test exists to catch is a notification
	// that waits for the run: the stage body is held open for the whole of
	// this call, so a push that waited would never return at all and would
	// take the whole test binary's deadline rather than reporting anything.
	if out, err := gitWithin(t.Context(), 30*time.Second, subject, "push", gate.RemoteName, "work"); err != nil {
		t.Fatalf("the push did not return while the run it started was inside a stage body: %v\n%s", err, out)
	}

	// The push is over. The run it started is inside the stage body, which is
	// where it has been since before the push returned and where it stays
	// until this test lets go.
	select {
	case <-inside:
	case <-time.After(30 * time.Second):
		t.Fatal("the push returned and no run ever reached the stage body")
	}
	started := onlyRun(t, h, subject)
	if started.Status != store.RunRunning {
		t.Fatalf("the run the push started is %s while its first stage is still executing, want running",
			started.Status)
	}
	let()
	held := awaitHold(t, h, subject, started.ID)
	if held.Outcome != machine.OutcomeDecision {
		t.Fatalf("once the stage body was released the run reached %s, want a decision", held.Outcome)
	}
}

// TestAPushIsRefusedAndChangesNothingWhenNothingCanValidateIt is PRD
// section 5's ordering: admission runs before any reference in the gate
// changes, so a refusal happens instead of a change rather than after one.
//
// The state it refuses from is a gate whose service is not running. A gate that
// accepted that push would take the branch, start nothing, and tell nobody,
// which is the failure a sealed gate exists to prevent arriving through the
// front door. What is checked is both halves: the push fails, and the gate
// holds no reference afterwards.
func TestAPushIsRefusedAndChangesNothingWhenNothingCanValidateIt(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	// No serve: the home has a gate and no service, which is what a person
	// meets after a restart, and admission is what has to notice.
	repository := initialize(t, h, subject)

	git(t, subject, "checkout", "--quiet", "-b", "work")
	commitOn(t, subject, "work", "work.txt", "a change\n")

	out, err := tryGit(subject, "push", gate.RemoteName, "work")
	if err == nil {
		t.Fatalf("the push was accepted although nothing could validate it:\n%s", out)
	}
	t.Logf("git push %s work, with no service running:\n%s", gate.RemoteName, out)
	if !strings.Contains(out, "assistant service start") {
		t.Errorf("the refusal does not name the command that changes it:\n%s", out)
	}
	if refs := gateRefs(t, repository); len(refs) != 0 {
		t.Fatalf("the refused push left %v in the gate; admission runs before any reference changes", refs)
	}
}

// TestARefusedPushLeavesEveryReferenceInTheGateAsItWas is the ordering PRD
// section 5 puts admission before mutation for: a refusal happens instead of a
// change, not after one, and not after half of one.
//
// The gate here already holds a reference, and the push that gets refused
// would both move that reference and create a second. Those are the two
// mutations a partial application could leave behind, and neither is there
// afterwards: the gate's references are read with git directly, before and
// after, and compared whole.
//
// What that establishes is about references, which is what a gate's state is
// for every question this product asks of it. It says nothing about what git
// does with the objects a push transferred before the hook ran; that is git's
// business, this does not look, and no claim here rests on it.
func TestARefusedPushLeavesEveryReferenceInTheGateAsItWas(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	stop := serveUntilStopped(t, h)
	repository := initialize(t, h, subject)

	// A first push that is accepted, so the gate has something to lose. Its
	// run is let reach its hold before the service goes away, so nothing is
	// mid-flight when it does.
	git(t, subject, "checkout", "--quiet", "-b", "work")
	commitOn(t, subject, "work", "work.txt", "the change that lands\n")
	git(t, subject, "push", gate.RemoteName, "work")
	awaitHold(t, h, subject, onlyRun(t, h, subject).ID)

	before := gateRefs(t, repository)
	if len(before) == 0 {
		t.Fatal("the accepted push left the gate holding no reference, so a refusal could not lose one")
	}

	// Now nothing can validate a push, and the next one carries two changes:
	// one that would move the reference already there, and one that would
	// create another.
	stop()
	moved := commitOn(t, subject, "work", "work.txt", "the change that is refused\n")
	git(t, subject, "branch", "second")

	out, err := tryGit(subject, "push", gate.RemoteName, "work", "second")
	if err == nil {
		t.Fatalf("the push was accepted although nothing could validate it:\n%s", out)
	}
	t.Logf("git push %s work second, with no service running:\n%s", gate.RemoteName, out)

	after := gateRefs(t, repository)
	if !slices.Equal(before, after) {
		t.Fatalf("the refused push changed the gate's references.\nbefore: %v\nafter:  %v", before, after)
	}
	for _, ref := range after {
		if strings.Contains(ref, moved) {
			t.Fatalf("the refused push moved a reference to the commit it carried: %s", ref)
		}
		if strings.HasPrefix(ref, "refs/heads/second ") {
			t.Fatalf("the refused push created %s", ref)
		}
	}
}

// initialize creates the gate for a subject and returns its bare repository's
// path, which is what a test reads references out of.
func initialize(t *testing.T, h *home.Home, subject string) string {
	t.Helper()
	got := run(t, h, subject, "--json", "init")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var answer machine.Init
	if err := json.Unmarshal([]byte(got.stdout), &answer); err != nil {
		t.Fatalf("the init answer does not decode: %v\n%s", err, got.stdout)
	}
	if answer.Gate.Repository == "" {
		t.Fatalf("init reports no gate repository:\n%s", got.stdout)
	}
	return answer.Gate.Repository
}

// commitOn puts a commit on a branch and leaves the working copy standing on
// whatever branch it was standing on before, returning the commit.
//
// Putting the working copy back is what lets a test push a branch it is not
// on, which is the arrangement that tells a run about a pushed reference from
// a run about a head.
func commitOn(t *testing.T, subject, branch, name, content string) string {
	t.Helper()
	standing := git(t, subject, "rev-parse", "--abbrev-ref", "HEAD")
	if standing != branch {
		git(t, subject, "checkout", "--quiet", branch)
	}
	if err := os.WriteFile(filepath.Join(subject, name), []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	git(t, subject, "add", "-A")
	git(t, subject, "commit", "--quiet", "-m", "a change on "+branch)
	head := git(t, subject, "rev-parse", "HEAD")
	if standing != branch {
		git(t, subject, "checkout", "--quiet", standing)
	}
	return head
}

// gateRefs is the references a gate repository holds, read with git directly
// rather than through the product, so a test about what a push left behind
// does not ask the code under test what it left behind.
func gateRefs(t *testing.T, repository string) []string {
	t.Helper()
	out := git(t, repository, "for-each-ref", "--format=%(refname) %(objectname)")
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// tryGit runs git and returns its output and error, for the cases where the
// failure is the point.
func tryGit(dir string, args ...string) (string, error) {
	return gitCommand(context.Background(), dir, args...)
}

// gitWithin is tryGit under a deadline, for a test whose regression would be a
// git invocation that never returns rather than one that fails.
func gitWithin(ctx context.Context, within time.Duration, dir string, args ...string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	out, err := gitCommand(bounded, dir, args...)
	if bounded.Err() != nil {
		return out, bounded.Err()
	}
	return out, err
}

// gitCommand runs git in the environment these tests give it, and owns that
// environment: the git helper that fails the test calls this rather than
// assembling its own, so a bounded call and an unbounded one cannot differ in
// what git reads.
func gitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Without a wait delay, killing git on the deadline still leaves this call
	// blocked on the output pipes a surviving hook process holds open, which
	// turns the failure the bound exists to catch into a hung test run.
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// listRuns is the repository's runs, newest first, read through the command
// surface.
func listRuns(t *testing.T, h *home.Home, subject string) []store.Run {
	t.Helper()
	got := run(t, h, subject, "--json", "runs")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant runs exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var answer machine.Runs
	if err := json.Unmarshal([]byte(got.stdout), &answer); err != nil {
		t.Fatalf("the runs answer does not decode: %v\n%s", err, got.stdout)
	}
	return answer.Runs
}

// onlyRun is the one run the repository has, and a failure naming what it
// found when it has any other number.
func onlyRun(t *testing.T, h *home.Home, subject string) store.Run {
	t.Helper()
	records := listRuns(t, h, subject)
	if len(records) != 1 {
		t.Fatalf("the repository has %d run(s), want the one a push started:\n%+v", len(records), records)
	}
	return records[0]
}

// awaitHold waits for a run to reach a decision, which is where a run in this
// build stops: it walks the stages that have a body and holds at the first
// that does not.
//
// It polls a read rather than attaching, because attaching advances a run and
// this is asking where the one the push started got to on its own. A run that
// never gets there fails with where it actually stood.
func awaitHold(t *testing.T, h *home.Home, subject, id string) machine.Run {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last machine.Run
	for time.Now().Before(deadline) {
		got := run(t, h, subject, "--json", "runs", id)
		if got.code != machine.ExitOK {
			t.Fatalf("reading run %s exited %s:\n%s%s", id, got.code, got.stdout, got.stderr)
		}
		if err := json.Unmarshal([]byte(got.stdout), &last); err != nil {
			t.Fatalf("the run answer does not decode: %v\n%s", err, got.stdout)
		}
		if last.Outcome == machine.OutcomeDecision {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s never reached a decision; it last stood at %+v", id, last)
	return machine.Run{}
}

// firstStageWithoutABody is where a run in this build stops. It is read from
// internal/stages rather than named, because a body landing moves it and a
// test that named the stage would fail the day one does, for a reason that has
// nothing to do with pushes.
func firstStageWithoutABody(t *testing.T) pipeline.Stage {
	t.Helper()
	implemented := make(map[pipeline.Stage]bool)
	for _, stage := range stages.Implemented() {
		implemented[stage] = true
	}
	for _, stage := range pipeline.Order() {
		if !implemented[stage] {
			return stage
		}
	}
	t.Fatal("every stage has a body, so no run holds; this test needs rewriting against whatever now stops one")
	return pipeline.StageInvalid
}
