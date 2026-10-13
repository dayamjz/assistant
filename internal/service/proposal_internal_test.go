package service

import (
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/judge/judgetest"
	"github.com/dayamjz/assistant/internal/store"
)

// history is a repository's runs as the store records them: each created in
// order, moved to its status, and carrying one fired answer of the screen
// about a.go.
type history struct {
	t *testing.T
	s *Service
	n int
}

func newHistory(t *testing.T) *history {
	t.Helper()
	s := openDecisionService(t, nil, judgetest.New(t, judgetest.Always(judgetest.Answer(0.1))))
	if _, err := s.store.UpsertRepository(t.Context(), store.Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one", UpstreamURL: "https://example.test/one.git",
		DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	return &history{t: t, s: s}
}

// add records a run in status that fired once, and returns its identifier.
func (h *history) add(status store.RunStatus) string {
	h.t.Helper()
	ctx := h.t.Context()
	h.n++
	id := "run-" + strconv.Itoa(h.n)
	build, err := store.CurrentBuild()
	if err != nil {
		h.t.Fatalf("reading the build: %v", err)
	}
	if _, err := h.s.store.CreateRun(ctx, store.Run{
		ID: id, RepositoryID: "repo-1", Branch: "topic", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", Build: build, ConfigDigest: "cfg",
	}); err != nil {
		h.t.Fatalf("creating %s: %v", id, err)
	}
	if status != store.RunPending {
		if _, err := h.s.store.TransitionRun(ctx, id, []store.RunStatus{store.RunPending}, status); err != nil {
			h.t.Fatalf("moving %s to %s: %v", id, status, err)
		}
	}
	answerAbout(h.t, h.s, id, "a.go")
	return id
}

// proposalsOf is what the run's answer carries, built the way every surface
// builds it.
func (h *history) proposalsOf(run string) []string {
	h.t.Helper()
	answer, err := h.s.report(h.t.Context(), run, nil)
	if err != nil {
		h.t.Fatalf("reporting %s: %v", run, err)
	}
	var out []string
	for _, p := range answer.Proposals {
		out = append(out, p.Description)
	}
	return out
}

// The proposal to turn a check off is read from the real record: it is raised
// on a run's answer when the check fired in each of the ten most recent
// passed runs of the repository in which it fired, with nothing following,
// and it names exactly those runs. A run still running and a run that was
// cancelled each fall outside the count, so either one in place of a passed
// run leaves the window one short and no proposal is raised; one firing that
// something followed keeps it back too. The first case is the positive
// control for the others.
func TestTheProposalCountsOnlyPassedRunsInWhichTheCheckFired(t *testing.T) {
	t.Run("ten passed runs", func(t *testing.T) {
		h := newHistory(t)
		var window []string
		for range 11 {
			window = append([]string{h.add(store.RunPassed)}, window...)
		}
		current := h.add(store.RunRunning)
		got := h.proposalsOf(current)
		if len(got) != 1 {
			t.Fatalf("the answer carries %d proposals, want 1: %v", len(got), got)
		}
		_, named, _ := strings.Cut(got[0], "are the window: ")
		named, _, _ = strings.Cut(named, ". Check them")
		if named != strings.Join(window[:10], ", ") {
			t.Fatalf("the proposal names %q, want the ten newest passed runs %v", named, window[:10])
		}
		if !strings.Contains(got[0], "10 most recent passed runs of this repository in which it fired") {
			t.Fatalf("the proposal does not say what its window is: %q", got[0])
		}
	})
	for _, other := range []store.RunStatus{store.RunRunning, store.RunTerminated} {
		t.Run("nine passed and one "+string(other), func(t *testing.T) {
			h := newHistory(t)
			for range 9 {
				h.add(store.RunPassed)
			}
			h.add(other)
			if got := h.proposalsOf(h.add(store.RunRunning)); len(got) != 0 {
				t.Fatalf("a %s run was counted toward the window: %v", other, got)
			}
		})
	}
	t.Run("one firing followed", func(t *testing.T) {
		h := newHistory(t)
		var followedRun string
		for i := range 10 {
			id := h.add(store.RunPassed)
			if i == 4 {
				followedRun = id
			}
		}
		mark, _ := h.s.store.LastDecisionAnswer(t.Context(), followedRun)
		h.s.followUp(followedRun, "a.go", store.FollowUpAnswer, "review", "approved", mark)
		if got := h.proposalsOf(h.add(store.RunRunning)); len(got) != 0 {
			t.Fatalf("a window with a followed firing raised a proposal: %v", got)
		}
	})
}
