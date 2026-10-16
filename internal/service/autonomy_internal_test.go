package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

const (
	autonomyRepository = "owner/name"
	autonomyHead       = "6f1a3c2b0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a"
	autonomyNumber     = 12
)

// readyView is the pull request the live read accepts: open, not a draft,
// mergeable, at the observed head.
func readyView() forgetest.PullRequestView {
	return forgetest.PullRequestView{Number: autonomyNumber, State: "OPEN", Mergeable: "MERGEABLE", Head: "fm/work",
		HeadCommit: autonomyHead, Base: "main", Title: "feat: a change", URL: "https://github.com/owner/name/pull/12"}
}

// green is a check rollup on head with one succeeded run.
func green(head string) forgetest.Response {
	return forgetest.Checks(head, forgetest.CheckRunView{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"})
}

// readyScript is the code host the positive control runs against, with the
// merge accepted and the read after it reporting merged.
func readyScript() forgetest.Script {
	merged := readyView()
	merged.State, merged.Mergeable = "MERGED", "UNKNOWN"
	return forgetest.Script{
		forgetest.OpRepository:   {forgetest.Repository(autonomyRepository)},
		forgetest.OpView:         {forgetest.PullRequest(readyView()), forgetest.PullRequest(merged)},
		forgetest.OpChecks:       {green(autonomyHead)},
		forgetest.OpMergeMethods: {forgetest.MergeMethods(true, true, false)},
		forgetest.OpMerge:        {forgetest.Merged(autonomyNumber)},
	}
}

// mergeOver runs the live read and the bound merge against a scripted code
// host and returns what it answered and the calls the host recorded.
func mergeOver(t *testing.T, script forgetest.Script, observed string, noCI bool) ([]forgetest.Call, forge.PullRequest, error) {
	t.Helper()
	host := forgetest.New(t, script)
	provider, err := host.Host().Open(autonomyRepository)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.NoCI = noCI
	pr, err := mergeAfterLiveRead(context.Background(), provider, autonomyNumber, observed, forge.DeclaredNoCI(cfg))
	return host.Calls(), pr, err
}

// mergeCalls counts the merges the code host was asked for.
func mergeCalls(calls []forgetest.Call) []forgetest.Call {
	var out []forgetest.Call
	for _, c := range calls {
		if c.Op == forgetest.OpMerge {
			out = append(out, c)
		}
	}
	return out
}

// The live read and the merge, over the real GitHub adapter reading a
// scripted gh. The positive control is merged, bound to the observed head
// through gh's own flag. Every answer the read does not accept is refused
// with no merge asked: closed, merged already, a draft, conflicted, a
// mergeability the forge has not computed, a head that moved from the one
// the run observed, checks read against another head, checks failing,
// checks pending, and an empty check list without no_ci, whose control is
// the same list under no_ci, which merges. A run that recorded no head is
// refused before the code host is asked anything. A refusal from the forge
// itself is reported as its own rejection and not retried.
//
// It stays among the package's slowest tests because each case runs the real
// GitHub adapter against a scripted gh, and every call that adapter makes
// starts this test binary under the race detector. The refusals share
// nothing, so each runs as a parallel subtest.
func TestTheProgramMergesOnlyAfterTheLiveReadOnTheObservedHead(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	calls, pr, err := mergeOver(t, readyScript(), autonomyHead, false)
	if err != nil || pr.State != forge.PullRequestStateMerged {
		t.Fatalf("the ready pull request: %+v, %v", pr, err)
	}
	merges := mergeCalls(calls)
	bound := false
	for _, a := range merges[0].Args {
		bound = bound || a == "--match-head-commit="+autonomyHead
	}
	if len(merges) != 1 || !bound {
		t.Fatalf("the merge was asked as %+v, want once bound to %s", merges, autonomyHead)
	}

	refusals := map[string]func(forgetest.Script){
		"closed": func(s forgetest.Script) {
			v := readyView()
			v.State = "CLOSED"
			s[forgetest.OpView] = []forgetest.Response{forgetest.PullRequest(v)}
		},
		"merged already": func(s forgetest.Script) {
			v := readyView()
			v.State = "MERGED"
			s[forgetest.OpView] = []forgetest.Response{forgetest.PullRequest(v)}
		},
		"a draft": func(s forgetest.Script) {
			v := readyView()
			v.Draft = true
			s[forgetest.OpView] = []forgetest.Response{forgetest.PullRequest(v)}
		},
		"conflicted": func(s forgetest.Script) {
			v := readyView()
			v.Mergeable = "CONFLICTING"
			s[forgetest.OpView] = []forgetest.Response{forgetest.PullRequest(v)}
		},
		"mergeability unknown": func(s forgetest.Script) {
			v := readyView()
			v.Mergeable = "UNKNOWN"
			s[forgetest.OpView] = []forgetest.Response{forgetest.PullRequest(v)}
		},
		"head moved": func(s forgetest.Script) {
			v := readyView()
			v.HeadCommit = strings.Repeat("b", 40)
			s[forgetest.OpView] = []forgetest.Response{forgetest.PullRequest(v)}
		},
		"checks on another head": func(s forgetest.Script) {
			s[forgetest.OpChecks] = []forgetest.Response{green(strings.Repeat("b", 40))}
		},
		"checks failing": func(s forgetest.Script) {
			s[forgetest.OpChecks] = []forgetest.Response{forgetest.Checks(autonomyHead,
				forgetest.CheckRunView{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
				forgetest.CheckRunView{Name: "lint", Status: "COMPLETED", Conclusion: "FAILURE"})}
		},
		"checks pending": func(s forgetest.Script) {
			s[forgetest.OpChecks] = []forgetest.Response{forgetest.Checks(autonomyHead,
				forgetest.CheckRunView{Name: "build", Status: "IN_PROGRESS"})}
		},
		"no checks without no_ci": func(s forgetest.Script) {
			s[forgetest.OpChecks] = []forgetest.Response{forgetest.Checks(autonomyHead)}
		},
	}
	for name, mutate := range refusals {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			script := readyScript()
			mutate(script)
			calls, _, err := mergeOver(t, script, autonomyHead, false)
			if !errors.Is(err, errMergeNotReady) {
				t.Errorf("%s: %v, want the live read's refusal", name, err)
			}
			if n := len(mergeCalls(calls)); n != 0 {
				t.Errorf("%s: the code host was asked to merge %d times", name, n)
			}
		})
	}

	script := readyScript()
	script[forgetest.OpChecks] = []forgetest.Response{forgetest.Checks(autonomyHead)}
	if calls, _, err := mergeOver(t, script, autonomyHead, true); err != nil || len(mergeCalls(calls)) != 1 {
		t.Fatalf("an empty check list under no_ci: %v after %d merges, want merged", err, len(mergeCalls(calls)))
	}

	if calls, _, err := mergeOver(t, readyScript(), "", false); !errors.Is(err, errMergeNotReady) || len(calls) != 0 {
		t.Fatalf("a run that observed no head: %v after %d calls", err, len(calls))
	}

	script = readyScript()
	script[forgetest.OpMerge] = []forgetest.Response{forgetest.Exit(1, "GraphQL: Required status check \"review\" is expected.")}
	calls, _, err = mergeOver(t, script, autonomyHead, false)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != forge.ReasonRejected || !strings.Contains(refusal.Detail, "Required status check") {
		t.Fatalf("the forge's refusal = %v, want it reported as its own rejection", err)
	}
	if n := len(mergeCalls(calls)); n != 1 {
		t.Fatalf("the refused merge was asked %d times, want once and never worked around", n)
	}
}

// A repository autonomy.merge does not list is never merged and the code host
// is never asked anything about it, which includes the default, an empty
// list. The positive control is the same run in a listed repository, which
// gets past the listing to the next step, the run's configuration, which this
// record cannot give, so it is refused there instead of answered nil.
func TestAnUnlistedRepositoryIsNeverMerged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "state.db")
	seedDatabase(t, database)
	st, err := store.Open(ctx, database, store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	host := forgetest.New(t, readyScript())
	facts := passedPullRequest{specifier: autonomyRepository, number: autonomyNumber, observed: autonomyHead}
	record := store.Run{ID: "run-1", RepositoryID: "nowhere", Status: store.RunPassed}

	for name, listed := range map[string][]string{"the default": nil, "another repository": {"owner/other"}} {
		cfg := config.Defaults()
		cfg.AutonomyMerge = listed
		s := &Service{store: st, cfg: cfg, forge: host.Host()}
		if err := s.mergeUnderAutonomy(ctx, nil, record, facts); err != nil {
			t.Fatalf("%s: %v, want nothing done", name, err)
		}
	}
	if calls := host.Calls(); len(calls) != 0 {
		t.Fatalf("an unlisted repository's code host was asked %d things", len(calls))
	}

	cfg := config.Defaults()
	cfg.AutonomyMerge = []string{"Owner/Name"}
	s := &Service{store: st, cfg: cfg, forge: host.Host()}
	if err := s.mergeUnderAutonomy(ctx, nil, record, facts); err == nil || !strings.Contains(err.Error(), "configuration") {
		t.Fatalf("a listed repository = %v, want it past the listing", err)
	}
}
