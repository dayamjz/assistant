//go:build unix

package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// These tests drive PRD section 11's "reap orphaned agent processes" with
// real processes on both sides: every agent is the production adapter over
// internal/agents/standin, every group is one the adapter started, and the
// service that recovers is a real one opened over the home. What is arranged
// rather than produced is the run's status, written through internal/store
// with a move internal/runs' table allows, because the decision under test
// reads it and nothing here is about how a run reaches it.

// requiresIdentity skips a test on a platform where internal/agents has no
// process start-time read, saying what that leaves unestablished: every
// record there carries no identity, recovery leaves every one alone as
// unverified, and nothing about ending a group is shown.
func requiresIdentity(t *testing.T) {
	t.Helper()
	if _, _, err := agents.Identify(os.Getpid()); errors.Is(err, agents.ErrIdentityUnreadable) {
		t.Skipf("%s has no process start-time read, so no record can be verified and no reaping is "+
			"established here: %v", runtime.GOOS, err)
	}
}

// holdingAgent is a stand-in that answers every invocation by holding, so a
// process it starts lives until something ends it.
func holdingAgent(t *testing.T) *standin.Agent {
	t.Helper()
	return standin.New(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Hang()}}})
}

// reapHome returns a home with one repository and the named runs recorded,
// pending, which is where a run stands before anything executes it.
func reapHome(t *testing.T, runIDs ...string) *home.Home {
	t.Helper()
	root, err := os.MkdirTemp("", "h")
	if err != nil {
		t.Fatalf("making a home root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	records := openHomeStore(t, h)
	defer func() { _ = records.Close() }()
	if _, err := records.UpsertRepository(t.Context(), store.Repository{
		ID: "subject", WorkingPath: root, UpstreamURL: "https://example.invalid/subject.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading the build: %v", err)
	}
	for _, id := range runIDs {
		if _, err := records.CreateRun(t.Context(), store.Run{
			ID: id, RepositoryID: "subject", Branch: "topic", SubmittedHead: "aaaa", Base: "bbbb",
			Intent: "validate", IntentSource: "push", Build: build, ConfigDigest: "cfg",
		}); err != nil {
			t.Fatalf("recording run %s: %v", id, err)
		}
	}
	return h
}

// openHomeStore opens the home's database directly, as a test that arranges
// or inspects what a service left behind has to.
func openHomeStore(t *testing.T, h *home.Home) *store.Store {
	t.Helper()
	seedDatabase(t, h.Database())
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	return records
}

// terminate moves a run to terminated, which is what a cancel writes before
// its segment's invocations have ended.
func terminate(t *testing.T, records *store.Store, id string) {
	t.Helper()
	if _, err := records.TransitionRun(t.Context(), id, []store.RunStatus{store.RunPending}, store.RunTerminated); err != nil {
		t.Fatalf("terminating run %s: %v", id, err)
	}
}

// openReaping opens a real service over the home, skipping where this
// platform has no local socket transport for it to serve on.
func openReaping(t *testing.T, h *home.Home) *Service {
	t.Helper()
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading the build: %v", err)
	}
	runner := standin.New(t, standin.Script{}).Runner()
	running, err := Open(t.Context(), Options{
		Home:      h,
		NewStages: stages.All,
		NewFixer:  stages.Fix,
		Build:     build,
		Catalog:   agents.NewCatalog(fixedRunner{runner: runner}, cursorFactory{runner: runner}),
		LockWait:  -1,
	})
	if err != nil {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skipf("%s has no local socket transport to serve this protocol over, so no service opens "+
				"and no recovery is established here: %v", runtime.GOOS, err)
		}
		t.Fatalf("opening the service: %v", err)
	}
	t.Cleanup(func() {
		if err := running.Close(); err != nil {
			t.Errorf("closing the service: %v", err)
		}
	})
	return running
}

// held is one invocation this test process holds open through a runner, and
// the identity of the group the adapter reported for it.
type held struct {
	id     agents.Identity
	cancel context.CancelFunc
	done   chan struct{}
}

// end cancels the invocation and waits for the adapter to finish ending it.
func (h held) end() { h.cancel(); <-h.done }

// hold launches one invocation of runner and waits for the adapter to report
// its group, which is the moment Started is called and so the moment any
// registration it makes has been made. contain, when set, wraps the runner
// outside the announcement, the way driverFor wraps the resolved one, so the
// Started value the announcement wraps is the one contain set. The
// invocation is ended on cleanup.
func hold(ctx context.Context, t *testing.T, runner agents.Runner, contain func(agents.Runner) agents.Runner) held {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	groups := make(chan int, 1)
	done := make(chan struct{})
	var launched agents.Runner = announcingTo{inner: runner, groups: groups}
	if contain != nil {
		launched = contain(launched)
	}
	go func() {
		defer close(done)
		_, _ = launched.Run(ctx, agents.PurposeReview, holdInvocation())
	}()
	var pgid int
	select {
	case pgid = <-groups:
	case <-done:
		cancel()
		t.Fatal("the held invocation returned before its process was reported started")
	case <-time.After(30 * time.Second):
		cancel()
		<-done
		t.Fatal("the adapter never reported the held invocation's group")
	}
	h := held{cancel: cancel, done: done}
	t.Cleanup(h.end)
	id, found, err := agents.Identify(pgid)
	if err != nil || !found {
		t.Fatalf("identifying the held group %d: %+v, %t, %v", pgid, id, found, err)
	}
	h.id = id
	return h
}

// announcingTo is announcing for this process: it hands the group the adapter
// reported to a channel once the Started value it wraps has returned, and
// otherwise passes everything through.
type announcingTo struct {
	inner  agents.Runner
	groups chan<- int
}

func (a announcingTo) Name() string                      { return a.inner.Name() }
func (a announcingTo) Capabilities() agents.Capabilities { return a.inner.Capabilities() }
func (a announcingTo) Run(ctx context.Context, purpose agents.Purpose, inv agents.Invocation) (agents.Result, error) {
	registered := inv.Started
	inv.Started = func(pgid int) func() {
		ended := func() {}
		if registered != nil {
			ended = registered(pgid)
		}
		a.groups <- pgid
		return ended
	}
	return a.inner.Run(ctx, purpose, inv)
}

// presence reads whether the identity's process is running, failing the
// test on a read that could not be made.
func presence(t *testing.T, id agents.Identity) agents.Presence {
	t.Helper()
	p, err := id.Presence()
	if err != nil {
		t.Fatalf("reading the presence of %s: %v", id, err)
	}
	return p
}

// accountFor is the one account recovery gave for a group, failing when it
// gave none or several.
func accountFor(t *testing.T, accounts []reaped, run string, group int) reaped {
	t.Helper()
	var found []reaped
	for _, a := range accounts {
		if a.Record.RunID == run && a.Record.Group == group {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("recovery gave %d accounts of group %d of run %s, want one: %v", len(found), group, run, accounts)
	}
	return found[0]
}

// TestARecordStandsWhileAnInvocationRunsAndIsClearedWhenItsTreeEnds pins the
// record's life on the product's own path: a service opened over a home
// wires the durable record into its containment registry, an invocation
// through its wrapper is recorded under the run and purpose with the
// identity the adapter's group reads now, and the record is gone once the
// adapter has ended the tree.
func TestARecordStandsWhileAnInvocationRunsAndIsClearedWhenItsTreeEnds(t *testing.T) {
	t.Parallel()
	requiresIdentity(t)
	h := reapHome(t, "run-live")
	s := openReaping(t, h)
	running := hold(withAdvancing(t.Context(), "run-live"), t, holdingAgent(t).Runner(), s.containRunner)

	records, err := s.store.ProcessRecords(t.Context())
	if err != nil {
		t.Fatalf("listing the process records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("a running invocation left %d records, want one: %+v", len(records), records)
	}
	got := records[0]
	start, known := got.Identity.Get()
	if got.RunID != "run-live" || got.Stage != string(agents.PurposeReview) || got.Group != running.id.PID ||
		!known || start != running.id.Start {
		t.Fatalf("the record reads %+v, want run-live's review group %s", got, running.id)
	}

	running.end()
	records, err = s.store.ProcessRecords(t.Context())
	if err != nil || len(records) != 0 {
		t.Fatalf("after the invocation's tree ended the records are %+v, %v, want none", records, err)
	}
}

// TestAKilledServiceLeavesItsRecordAndTheNextOneEndsThatGroupAlone is the
// kill path end to end. An earlier service, a separate process, launches a
// held invocation through its containment wrapper and is killed with SIGKILL,
// so nothing of its own ends the group; the run had been ended, which is the
// window a cancel opens by moving the record before its segment returns. A
// sibling started by the same adapter with the same command line runs in a
// group of its own under the same user. The next service over the home ends
// the orphaned group, clears its record, and leaves the sibling running.
//
// The positive control is that a pattern would have reached the sibling: the
// argument that names the stand-in's control directory is on both command
// lines, so a kill matched on it, which is what a broad kill is, takes both.
// The assertion that the sibling survived is therefore about scope rather
// than about a sibling nothing could have matched.
func TestAKilledServiceLeavesItsRecordAndTheNextOneEndsThatGroupAlone(t *testing.T) {
	t.Parallel()
	requiresIdentity(t)
	h := reapHome(t, "run-orphaned")
	agent := holdingAgent(t)

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("finding this test binary: %v", err)
	}
	earlier := exec.Command(self)
	earlier.Env = append(os.Environ(),
		earlierHomeVar+"="+h.Root(),
		earlierRunVar+"=run-orphaned",
		earlierAgentVar+"="+strings.Join(agent.Arguments(), "\n"))
	var stderr bytes.Buffer
	earlier.Stderr = &stderr
	stdout, err := earlier.StdoutPipe()
	if err != nil {
		t.Fatalf("piping the earlier service's output: %v", err)
	}
	if err := earlier.Start(); err != nil {
		t.Fatalf("starting the earlier service: %v", err)
	}
	var reapEarlier sync.Once
	exited := make(chan struct{})
	t.Cleanup(func() {
		reapEarlier.Do(func() { _ = earlier.Process.Kill(); _ = earlier.Wait(); close(exited) })
	})

	announced := make(chan int, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if rest, ok := strings.CutPrefix(scanner.Text(), "started "); ok {
				if pgid, err := strconv.Atoi(rest); err == nil {
					announced <- pgid
				}
			}
		}
	}()
	var orphan int
	select {
	case orphan = <-announced:
	case <-time.After(60 * time.Second):
		t.Fatalf("the earlier service never reported its invocation's group; it said: %s", stderr.String())
	}
	t.Cleanup(func() { _ = syscall.Kill(-orphan, syscall.SIGKILL) })
	orphanID, found, err := agents.Identify(orphan)
	if err != nil || !found {
		t.Fatalf("identifying the orphan group %d: %t, %v", orphan, found, err)
	}

	records := openHomeStore(t, h)
	terminate(t, records, "run-orphaned")
	reapEarlier.Do(func() {
		if err := earlier.Process.Signal(syscall.SIGKILL); err != nil {
			t.Fatalf("killing the earlier service: %v", err)
		}
		_ = earlier.Wait()
		close(exited)
	})
	<-exited

	left, err := records.ProcessRecords(t.Context())
	_ = records.Close()
	if err != nil || len(left) != 1 || left[0].Group != orphan || left[0].RunID != "run-orphaned" {
		t.Fatalf("the killed service left %+v, %v, want the one record of group %d", left, err, orphan)
	}
	if presence(t, orphanID) != agents.PresencePresent {
		t.Fatal("the orphan did not outlive the service that started it, so there is nothing for recovery to reap")
	}

	sibling := hold(t.Context(), t, agent.Runner(), nil)
	if sibling.id.PID == orphan {
		t.Fatal("the sibling is the orphan's group, so its surviving says nothing about scope")
	}
	pattern := agent.Arguments()[0]
	matched := matchingCommandLines(t, pattern)
	if !matched[orphan] || !matched[sibling.id.PID] {
		t.Fatalf("a kill matched on %q reaches %v, want both the orphan %d and the sibling %d; without that the "+
			"sibling surviving is no control", pattern, matched, orphan, sibling.id.PID)
	}

	s := openReaping(t, h)
	account := accountFor(t, s.reaped, "run-orphaned", orphan)
	if account.Outcome != reapEnded || account.Err != nil {
		t.Fatalf("recovery's account of the orphan is %s, want %s", account, reapEnded)
	}
	if got := presence(t, orphanID); got != agents.PresenceAbsent {
		t.Fatalf("the orphan reads %s after recovery reported it ended, want absent", got)
	}
	if got := presence(t, sibling.id); got != agents.PresencePresent {
		t.Fatalf("the sibling outside the orphan's group reads %s after recovery, want present", got)
	}
	if left, err := s.store.ProcessRecords(t.Context()); err != nil || len(left) != 0 {
		t.Fatalf("after recovery the records are %+v, %v, want none", left, err)
	}
}

// TestRecoveryLeavesAloneWhatItCannotVerifyAndWhatIsActive establishes P6's
// refuse-and-report on the kill path at recovery. Three live groups are
// recorded the way a service that died would have left them, and each is
// left running for its own reason, reported in a typed account and in the
// service log rather than refused at start:
//
//   - a record whose identity is the group's real reading with the start time
//     changed, which is the one shape an identifier reused since launch has,
//     is left alone as replaced and its record cleared;
//   - a record carrying no identity, which the journal writes when the read
//     failed at launch, is left alone as unverified and its record stands;
//   - a record whose run is still pending is active and not looked at, and its
//     record stands.
//
// The counterfeit start time is a mutation of a real reading, since nothing
// can make the kernel reuse a live identifier on demand.
func TestRecoveryLeavesAloneWhatItCannotVerifyAndWhatIsActive(t *testing.T) {
	t.Parallel()
	requiresIdentity(t)
	principles.Cite(t, principles.P6)
	h := reapHome(t, "run-ended", "run-active")
	agent := holdingAgent(t)
	reused := hold(t.Context(), t, agent.Runner(), nil)
	unknown := hold(t.Context(), t, agent.Runner(), nil)
	active := hold(t.Context(), t, agent.Runner(), nil)

	records := openHomeStore(t, h)
	terminate(t, records, "run-ended")
	for _, r := range []store.ProcessRecord{
		{RunID: "run-ended", Stage: "review", Group: reused.id.PID, Identity: store.Known(reused.id.Start + 1)},
		{RunID: "run-ended", Stage: "review", Group: unknown.id.PID},
		{RunID: "run-active", Stage: "review", Group: active.id.PID, Identity: store.Known(active.id.Start)},
	} {
		if _, err := records.RecordProcess(t.Context(), r); err != nil {
			t.Fatalf("recording %+v: %v", r, err)
		}
	}
	_ = records.Close()

	s := openReaping(t, h)
	if got := accountFor(t, s.reaped, "run-ended", reused.id.PID); got.Outcome != reapReplaced {
		t.Errorf("the reused identifier's account is %s, want %s", got, reapReplaced)
	}
	got := accountFor(t, s.reaped, "run-ended", unknown.id.PID)
	if got.Outcome != reapUnverified || !errors.Is(got.Err, agents.ErrIdentityUnreadable) {
		t.Errorf("the identity-less record's account is %s, want %s naming ErrIdentityUnreadable", got, reapUnverified)
	}
	if got := accountFor(t, s.reaped, "run-active", active.id.PID); got.Outcome != reapActive {
		t.Errorf("the active run's account is %s, want %s", got, reapActive)
	}
	for name, process := range map[string]held{"reused": reused, "unverified": unknown, "active": active} {
		if got := presence(t, process.id); got != agents.PresencePresent {
			t.Errorf("the %s group reads %s after recovery, want it left running", name, got)
		}
	}

	left, err := s.store.ProcessRecords(t.Context())
	if err != nil {
		t.Fatalf("listing the records after recovery: %v", err)
	}
	standing := map[int]bool{}
	for _, r := range left {
		standing[r.Group] = true
	}
	if standing[reused.id.PID] || !standing[unknown.id.PID] || !standing[active.id.PID] || len(left) != 2 {
		t.Fatalf("after recovery the records are %+v, want the unverified and the active ones standing and the replaced one cleared", left)
	}

	logged, err := os.ReadFile(h.ServiceLog())
	if err != nil {
		t.Fatalf("reading the service log: %v", err)
	}
	for _, want := range []string{
		"process group " + strconv.Itoa(unknown.id.PID) + " of run run-ended",
		"3 run process record(s)",
		"1 process(es) left alone and reported above",
	} {
		if !strings.Contains(string(logged), want) {
			t.Errorf("the service log does not report %q:\n%s", want, logged)
		}
	}
}
