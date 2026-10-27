package service

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// housekeepingConfig is the operator's file the loop tests open under: a
// short beacon and escalation window, and housekeeping.interval at its
// floor, which the injected clock passes in one step.
const housekeepingConfig = `{"supervision_beacon_interval": "1s", "escalation_window": "1m", "recheck_window": "1m",
	"housekeeping": {"interval": "1h"}}` + "\n"

// housekeepingOff is housekeepingConfig with the interval at its default.
const housekeepingOff = `{"supervision_beacon_interval": "1s", "escalation_window": "1m", "recheck_window": "1m"}` + "\n"

// hkRepository is the repository every housekeeping loop test records.
const hkRepository = "subject"

// hkCheck is one due check's report as its hook delivered it.
type hkCheck struct {
	repository string
	report     taskReport
}

// hkSubject is a loop over a home that records one repository with a gate
// and an upstream, under housekeepingConfig or what the test states.
type hkSubject struct {
	*loopSubject
	h         *home.Home
	terminals *scriptedTerminals
	primary   string
	upstream  string
	checks    chan hkCheck
	first     passReport
	// offerMu guards onDueRead, which the loop's afterDueRead hook calls
	// when a test set it.
	offerMu   sync.Mutex
	onDueRead func()
}

// afterDueRead sets what the loop does between a due check's read of the
// default branch and its write of the wake or the marker.
func (s *hkSubject) afterDueRead(f func()) {
	s.offerMu.Lock()
	defer s.offerMu.Unlock()
	s.onDueRead = f
}

// hkPlant is what a test records before the service opens.
type hkPlant func(t *testing.T, records *store.Store, sub *hkSubject)

// openHousekeeping records the repository, runs plant, and opens the loop
// under document and an injected clock starting now, past its first pass.
func openHousekeeping(t *testing.T, document string, plant hkPlant) *hkSubject {
	t.Helper()
	h, records := loopHomeConfigured(t, document)
	sub := &hkSubject{h: h, terminals: newScriptedTerminals(), checks: make(chan hkCheck, 64)}
	sub.primary, sub.upstream = plantHousekeepingRepository(t, h, records)
	if plant != nil {
		plant(t, records, sub)
	}
	clock := &fakeClock{now: time.Now()}
	sub.loopSubject = openLoopHooked(t, h, records, sub.terminals, clock, nil, func(hooks *supervisionHooks) {
		hooks.afterHousekeepingCheck = func(repository string, r taskReport) { sub.checks <- hkCheck{repository, r} }
		hooks.afterDueRead = func(string) {
			sub.offerMu.Lock()
			f := sub.onDueRead
			sub.offerMu.Unlock()
			if f != nil {
				f()
			}
		}
	})
	t.Cleanup(func() { _ = sub.service.Close() })
	sub.first = sub.nextPass(t)
	return sub
}

// plantHousekeepingRepository records a primary checkout with an upstream,
// its repository record and its gate, and returns the primary and the
// upstream.
func plantHousekeepingRepository(t *testing.T, h *home.Home, records *store.Store) (string, string) {
	t.Helper()
	ctx := t.Context()
	primary, upstream, _ := heldSubject(t)
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID: hkRepository, WorkingPath: primary, UpstreamURL: upstream, DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Initialize(ctx, gate.Spec{Home: h.Root(), WorkingPath: primary, Command: command},
		gate.WithIndex(records)); err != nil {
		t.Fatalf("initializing the gate: %v", err)
	}
	return primary, upstream
}

// tip is the upstream's default branch.
func (s *hkSubject) tip(t *testing.T) string {
	t.Helper()
	return heldGit(t, s.upstream, "rev-parse", "main")
}

// merge moves the upstream's default branch on by one commit, as a merge
// does, and returns the new tip.
func (s *hkSubject) merge(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(s.primary, path)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.primary, path), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	heldGit(t, s.primary, "add", "-A")
	heldGit(t, s.primary, "commit", "--quiet", "-m", "merge "+path)
	heldGit(t, s.primary, "push", "--quiet", "origin", "main")
	return s.tip(t)
}

// check waits for the next due check's report.
func (s *hkSubject) check(t *testing.T) taskReport {
	t.Helper()
	select {
	case c := <-s.checks:
		if c.repository != hkRepository {
			t.Fatalf("a due check reported repository %s, want %s", c.repository, hkRepository)
		}
		if c.report.Err != nil {
			t.Fatalf("the due check could not record what it found: %v", c.report.Err)
		}
		return c.report
	case <-time.After(loopWait):
		t.Fatal("no due check reported")
		return taskReport{}
	}
}

// checkStarted reports whether the pass started the repository's due check,
// and waits for it when it did.
func (s *hkSubject) checkStarted(t *testing.T, r passReport, open bool) bool {
	t.Helper()
	started := r.Housekeeping[hkRepository].Watched && !open
	if started {
		s.check(t)
	}
	return started
}

// wakes is every housekeeping wake, oldest first.
func (s *hkSubject) wakes(t *testing.T) []store.HousekeepingWake {
	t.Helper()
	all, err := s.records(t).HousekeepingWakes(t.Context())
	if err != nil {
		t.Fatalf("listing the housekeeping wakes: %v", err)
	}
	return all
}

// passTask records a housekeeping pass of the repository as intake does, a
// marked investigation under the housekeeping role.
func passTask(t *testing.T, records *store.Store, id string) store.Task {
	t.Helper()
	task, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: "/w/" + id,
		RepositoryID: store.Known(hkRepository), Role: config.RoleHousekeeping, Housekeeping: true,
	}, store.Disposition{Delivered: taskstate.Delivered})
	if err != nil {
		t.Fatalf("creating pass %s: %v", id, err)
	}
	return task
}

// launched begins the pass's first attempt, as a dispatch does, recording
// base as the knowledge revision its launch read when base is not empty,
// and ref as its session when ref is not empty.
func launched(t *testing.T, records *store.Store, id, base, ref string) {
	t.Helper()
	ctx := t.Context()
	if _, err := records.BeginFirstTaskAttempt(ctx, id, store.OnThisMachine()); err != nil {
		t.Fatalf("beginning pass %s: %v", id, err)
	}
	if base != "" {
		if err := records.SetAttemptKnowledge(ctx, id, 1, store.AttemptKnowledge{Commit: base}); err != nil {
			t.Fatalf("recording pass %s's base: %v", id, err)
		}
	}
	if ref != "" {
		if err := records.SetAttemptSession(ctx, id, 1, ref, store.SessionHost{}); err != nil {
			t.Fatalf("recording pass %s's session: %v", id, err)
		}
	}
}

// cutAt records that the pass's copy was cut at head, as a dispatch that
// proved the copy records its start snapshot.
func cutAt(t *testing.T, records *store.Store, id, head string) {
	t.Helper()
	if err := records.SetTaskStartSnapshot(t.Context(), id, store.CopySnapshot{Head: head}); err != nil {
		t.Fatalf("recording where pass %s's copy was cut: %v", id, err)
	}
}

// relayed closes a pass that found nothing: its report, then its findings
// relayed record, the terminal tier's Complete.
func relayed(t *testing.T, records *store.Store, id string) {
	t.Helper()
	ctx := t.Context()
	if _, _, err := records.StoreTaskReport(ctx, store.TaskReport{TaskID: id, Body: "nothing drifted",
		Report: []byte(`{"summary":"nothing drifted"}`)}, nil); err != nil {
		t.Fatalf("filing pass %s's report: %v", id, err)
	}
	events, err := records.TaskEvents(ctx, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.RecordTaskRelay(ctx, id, events[len(events)-1].Sequence); err != nil {
		t.Fatalf("relaying pass %s: %v", id, err)
	}
}

// closedPass records a pass whose copy was cut at base, launched on it as
// its knowledge revision too, that found nothing and was relayed: the last
// pass of the repository, closed.
func closedPass(t *testing.T, records *store.Store, id, base string) {
	t.Helper()
	passTask(t, records, id)
	launched(t, records, id, base, "")
	cutAt(t, records, id, base)
	relayed(t, records, id)
}

// PRD section 6's housekeeping: the base of a pass is the default-branch
// commit its copy was cut at, which is what its worker read, and not the
// knowledge revision its launch recorded at a fresh fetch of the upstream.
// A primary checkout behind the upstream cuts the copy at the older commit
// while the launch records the newer one, so a pass that found nothing
// drifted at the older tree has not checked what the newer commit changed:
// with the upstream still at that commit, the due check raises the wake
// naming it past the copy's commit. The control is a pass whose copy was
// cut at the upstream's tip, which raises nothing.
func TestTheBaseOfAPassIsTheCommitItsCopyWasCutAtNotTheKnowledgeItsLaunchRead(t *testing.T) {
	t.Parallel()
	var older, newer string
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		older = sub.tip(t)
		newer = sub.merge(t, "CLAUDE.md", "a claim the older tree does not hold\n")
		passTask(t, records, "taskhkbaseaa")
		launched(t, records, "taskhkbaseaa", newer, "")
		cutAt(t, records, "taskhkbaseaa", older)
		relayed(t, records, "taskhkbaseaa")
	})
	sub.clock.Advance(time.Hour)
	if r := sub.pass(t); !sub.checkStarted(t, r, false) {
		t.Fatalf("once the interval passed, the pass did not check: %+v", r.Housekeeping)
	}
	if wakes := sub.wakes(t); len(wakes) != 1 || !strings.Contains(wakes[0].Detail, newer) || !strings.Contains(wakes[0].Detail, older) {
		t.Fatalf("the due wake is %+v, want one naming the tip %s past %s, the commit the pass's copy was cut at", wakes, newer, older)
	}

	control := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		passTask(t, records, "taskhkbaseab")
		launched(t, records, "taskhkbaseab", "", "")
		cutAt(t, records, "taskhkbaseab", sub.tip(t))
		relayed(t, records, "taskhkbaseab")
	})
	control.clock.Advance(time.Hour)
	if r := control.pass(t); !control.checkStarted(t, r, false) {
		t.Fatalf("the control did not check: %+v", r.Housekeeping)
	}
	if wakes := control.wakes(t); len(wakes) != 0 {
		t.Fatalf("a pass whose copy was cut at the tip raised %+v", wakes)
	}
}

// PRD section 13's housekeeping row: with housekeeping.interval zero, no
// wake is raised however far the default branch moves and however long ago
// the last pass was created, and no due check fetches anything. The
// positive control is the same home under the interval, below.
func TestHousekeepingIsNeverDueWithTheIntervalZero(t *testing.T) {
	t.Parallel()
	var base string
	sub := openHousekeeping(t, housekeepingOff, func(t *testing.T, records *store.Store, sub *hkSubject) {
		base = heldGit(t, sub.upstream, "rev-parse", "main")
		closedPass(t, records, "taskhkzeroaa", base)
	})
	sub.merge(t, "AGENTS.md", "a renamed command\n")
	sub.clock.Advance(200 * time.Hour)
	r := sub.pass(t)
	if _, ok := r.Housekeeping[hkRepository]; ok || r.Housekeeping != nil {
		t.Fatalf("with the interval zero the pass reported housekeeping %+v, want nothing", r.Housekeeping)
	}
	if got := sub.wakes(t); len(got) != 0 {
		t.Fatalf("with the interval zero the loop raised %+v", got)
	}
}

// The row's combination, one half: the default branch moved past the last
// pass's base while the interval has not passed since that pass was created
// raises nothing and fetches nothing; once the interval has passed, the
// check fetches and raises one wake naming the pass, its base and the tip.
func TestAPassIsNotDueOnAMoveAloneButOnceTheIntervalAlsoPassed(t *testing.T) {
	t.Parallel()
	var base string
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		base = heldGit(t, sub.upstream, "rev-parse", "main")
		closedPass(t, records, "taskhkmoveaa", base)
	})
	if sub.checkStarted(t, sub.first, false) {
		t.Fatal("the first pass checked a repository whose last pass was created within the interval")
	}
	moved := sub.merge(t, "AGENTS.md", "a renamed command\n")
	if r := sub.pass(t); sub.checkStarted(t, r, false) || len(sub.wakes(t)) != 0 {
		t.Fatalf("a move within the interval checked or raised: %+v, %+v", r.Housekeeping, sub.wakes(t))
	}
	sub.clock.Advance(time.Hour)
	if r := sub.pass(t); !sub.checkStarted(t, r, false) {
		t.Fatalf("once the interval passed, the pass did not check: %+v", r.Housekeeping)
	}
	wakes := sub.wakes(t)
	if len(wakes) != 1 || wakes[0].Kind != string(supervisor.KindHousekeepingDue) || wakes[0].Pass != store.Known("taskhkmoveaa") ||
		!strings.Contains(wakes[0].Detail, base) || !strings.Contains(wakes[0].Detail, moved) {
		t.Fatalf("the due wake is %+v, want one about pass taskhkmoveaa naming its base %s and the tip %s", wakes, base, moved)
	}
}

// The row's combination, other half: the interval passed with the default
// branch still at the last pass's base raises nothing, and the check is not
// made again within another interval; once the branch has moved and another
// interval has passed, it raises the wake.
func TestAPassIsNotDueOnTheIntervalAloneButOnceTheBranchAlsoMoved(t *testing.T) {
	t.Parallel()
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		closedPass(t, records, "taskhkstilla", heldGit(t, sub.upstream, "rev-parse", "main"))
	})
	sub.clock.Advance(time.Hour)
	if r := sub.pass(t); !sub.checkStarted(t, r, false) {
		t.Fatalf("once the interval passed, the pass did not check: %+v", r.Housekeeping)
	}
	if got := sub.wakes(t); len(got) != 0 {
		t.Fatalf("an unmoved branch raised %+v", got)
	}
	marker, found, err := sub.records(t).HousekeepingMarkerOf(t.Context(), hkRepository)
	if err != nil || !found || marker.Pass != "taskhkstilla" {
		t.Fatalf("the due check's marker is %+v, %v, %v, want it advanced for the pass", marker, found, err)
	}
	moved := sub.merge(t, "CLAUDE.md", "a moved path\n")
	if r := sub.pass(t); sub.checkStarted(t, r, false) {
		t.Fatal("the check ran again within an interval of the last")
	}
	sub.clock.Advance(time.Hour)
	if r := sub.pass(t); !sub.checkStarted(t, r, false) {
		t.Fatalf("another interval on, the pass did not check: %+v", r.Housekeeping)
	}
	if wakes := sub.wakes(t); len(wakes) != 1 || !strings.Contains(wakes[0].Detail, moved) {
		t.Fatalf("the due wake is %+v, want one naming the tip %s", wakes, moved)
	}
}

// A fetch that fails raises the wake naming what could not be read, rather
// than reading the failure as nothing having changed. The positive control
// is the same repository with a working fetch and no movement, which raises
// nothing.
func TestADueCheckThatCannotFetchRaisesTheWakeNamingIt(t *testing.T) {
	t.Parallel()
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		closedPass(t, records, "taskhkfetcha", heldGit(t, sub.upstream, "rev-parse", "main"))
	})
	sub.clock.Advance(time.Hour)
	if r := sub.pass(t); !sub.checkStarted(t, r, false) || len(sub.wakes(t)) != 0 {
		t.Fatalf("the control check raised %+v", sub.wakes(t))
	}
	if err := os.Rename(sub.upstream, sub.upstream+".gone"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sub.upstream + ".gone") })
	sub.clock.Advance(time.Hour)
	if r := sub.pass(t); !sub.checkStarted(t, r, false) {
		t.Fatalf("the pass did not check: %+v", r.Housekeeping)
	}
	wakes := sub.wakes(t)
	if len(wakes) != 1 || !strings.Contains(wakes[0].Detail, "could not read") {
		t.Fatalf("a failed fetch raised %+v, want one wake naming what could not be read", wakes)
	}
}

// PRD principle P7 for the due check: a trusted housekeeping.paths that is
// empty raises nothing, because housekeeping is off for the repository, and
// the same empty list in a copy that is not the default branch has no
// effect, so a repository that never had a pass is due.
func TestAnEmptyTrustedScopeRaisesNothingAndAnUntrustedOneHasNoEffect(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	off := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		writeDocument(t, sub.primary, config.RepositoryDocument, `{"housekeeping": {"paths": []}}`)
		heldGit(t, sub.primary, "add", "-A")
		heldGit(t, sub.primary, "commit", "--quiet", "-m", "turn housekeeping off")
		heldGit(t, sub.primary, "push", "--quiet", "origin", "main")
	})
	if !off.checkStarted(t, off.first, false) {
		t.Fatal("the first pass did not check a repository that never had a pass")
	}
	if got := off.wakes(t); len(got) != 0 {
		t.Fatalf("an empty trusted scope raised %+v", got)
	}

	untrusted := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		writeDocument(t, sub.primary, config.RepositoryDocument, `{"housekeeping": {"paths": []}}`)
		heldGit(t, sub.primary, "add", "-A")
		heldGit(t, sub.primary, "commit", "--quiet", "-m", "turn housekeeping off, unmerged")
	})
	if !untrusted.checkStarted(t, untrusted.first, false) {
		t.Fatal("the first pass did not check a repository that never had a pass")
	}
	wakes := untrusted.wakes(t)
	if len(wakes) != 1 || wakes[0].Pass.IsKnown() || !strings.Contains(wakes[0].Detail, "was ever created") {
		t.Fatalf("an empty list outside the default branch left %+v, want the due wake before the first pass", wakes)
	}
}

// A due check's facts are the passes as the loop read them before the
// fetch, and a pass created while the fetch is in flight makes them stale:
// a wake raised from them would name a base the new pass replaced, or say
// no pass was ever created, and the coordinator acting on it once that pass
// closed would create a second pass at the same base. The store reads the
// passes again in the write (store.ErrDueCheckStale), so the check writes
// neither wake nor marker, whether the pass created meanwhile is already
// closed or still open, and the next check follows the pass that was
// created. The positive controls are that next check, which reads the new
// pass and advances the marker, and the open pass's own wake, which is
// written while it is open.
func TestADueCheckWritesNothingOnceAPassWasCreatedWhileItRead(t *testing.T) {
	t.Parallel()
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		closedPass(t, records, "taskhkraceaa", heldGit(t, sub.upstream, "rev-parse", "main"))
	})
	if sub.checkStarted(t, sub.first, false) {
		t.Fatal("the first pass checked a repository whose last pass was created within the interval")
	}
	moved := sub.merge(t, "AGENTS.md", "a renamed command\n")
	sub.clock.Advance(time.Hour)
	sub.afterDueRead(func() {
		sub.afterDueRead(nil)
		closedPass(t, sub.records(t), "taskhkracebb", moved)
	})
	if r := sub.pass(t); !sub.checkStarted(t, r, false) {
		t.Fatalf("once the interval passed, the pass did not check: %+v", r.Housekeeping)
	}
	if wakes := sub.wakes(t); len(wakes) != 0 {
		t.Fatalf("a check whose repository got a pass while it read raised %+v, want nothing", wakes)
	}
	if _, found, err := sub.records(t).HousekeepingMarkerOf(t.Context(), hkRepository); err != nil || found {
		t.Fatalf("the stale check advanced the marker (found %v, %v), want it left alone", found, err)
	}
	sub.clock.Advance(time.Hour)
	if r := sub.pass(t); !sub.checkStarted(t, r, false) {
		t.Fatalf("the next interval's pass did not check: %+v", r.Housekeeping)
	}
	marker, found, err := sub.records(t).HousekeepingMarkerOf(t.Context(), hkRepository)
	if err != nil || !found || marker.Pass != "taskhkracebb" {
		t.Fatalf("the next check's marker is %+v, %v, %v, want it advanced for the pass created meanwhile", marker, found, err)
	}
	if wakes := sub.wakes(t); len(wakes) != 0 {
		t.Fatalf("a branch unmoved since the pass created meanwhile raised %+v", wakes)
	}

	sub.merge(t, "CLAUDE.md", "a moved path\n")
	sub.clock.Advance(time.Hour)
	sub.afterDueRead(func() {
		sub.afterDueRead(nil)
		passTask(t, sub.records(t), "taskhkracecc")
	})
	if r := sub.pass(t); !sub.checkStarted(t, r, false) {
		t.Fatalf("after a move and an interval the pass did not check: %+v", r.Housekeeping)
	}
	if wakes := sub.wakes(t); len(wakes) != 0 {
		t.Fatalf("a check whose repository got an open pass while it read raised %+v, want nothing", wakes)
	}
}

// writeDocument writes a file into a checkout.
func writeDocument(t *testing.T, dir, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
