package forgetest_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
)

// argOf returns the value of the first --name= flag in args, and whether
// there was one.
func argOf(args []string, name string) (string, bool) {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

// callsOf is the recorded calls of one operation, in order.
func callsOf(p *forgetest.Provider, op forgetest.Op) []forgetest.Call {
	var out []forgetest.Call
	for _, c := range p.Calls() {
		if c.Op == op {
			out = append(out, c)
		}
	}
	return out
}

// A pull request from a fork is proposed to the upstream with the fork as its
// head, and the write is held to both repositories it names: the upstream is
// confirmed, then the fork, and only then is the pull request created, with
// owner:branch as its head and the upstream as its repository. The read that
// finds it again keeps only the pull request whose head lives in the fork, so
// another fork's open pull request under the same branch name is not taken
// for this run's. The control is the same submission without a fork, which
// confirms one repository and names the branch alone.
func TestAForkSubmissionIsProposedToTheUpstreamAndConfirmsBoth(t *testing.T) {
	const branch = "feature/work"
	p := forgetest.New(t, forgetest.Script{
		forgetest.OpList: {
			// Before the create: another fork's pull request under the same
			// branch name, which is not this run's.
			forgetest.List(forgetest.ListEntry{Number: 3, HeadOwner: "someone", HeadName: "assistant"}),
			// After it: that one, and this fork's.
			forgetest.List(forgetest.ListEntry{Number: 3, HeadOwner: "someone", HeadName: "assistant"},
				forgetest.ListEntry{Number: 8, HeadOwner: "Me", HeadName: "Assistant"}),
		},
		forgetest.OpRepository: {forgetest.Repository("upstream/assistant"), forgetest.Repository("me/assistant")},
		forgetest.OpCreate:     {forgetest.Raw("https://github.com/upstream/assistant/pull/8\n")},
		forgetest.OpView: {forgetest.PullRequest(forgetest.PullRequestView{
			Number: 8, State: "OPEN", Mergeable: "MERGEABLE", Head: branch, Base: "main",
		})},
	})
	provider, err := p.Host().OpenFork("upstream/assistant", "me/assistant")
	if err != nil {
		t.Fatalf("OpenFork: %v", err)
	}
	pr, err := forge.Submit(context.Background(), provider, forge.OpenSpec{Head: branch, Base: "main", Title: "a change"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if pr.Number != 8 {
		t.Fatalf("the submission answered pull request %d, want this fork's 8 and never the other fork's 3", pr.Number)
	}

	confirms := callsOf(p, forgetest.OpRepository)
	if len(confirms) != 2 || !slices.Contains(confirms[0].Args, "upstream/assistant") || !slices.Contains(confirms[1].Args, "me/assistant") {
		t.Fatalf("the write was confirmed by %+v, want the upstream and then the fork", confirms)
	}
	creates := callsOf(p, forgetest.OpCreate)
	if len(creates) != 1 {
		t.Fatalf("the stand-in recorded %d creates, want one", len(creates))
	}
	if head, _ := argOf(creates[0].Args, "--head"); head != "me:"+branch {
		t.Fatalf("the pull request was created with head %q, want the fork's owner and the branch", head)
	}
	if repo, _ := argOf(creates[0].Args, "--repo"); repo != "upstream/assistant" {
		t.Fatalf("the pull request was created on %q, want the upstream", repo)
	}
	for _, list := range callsOf(p, forgetest.OpList) {
		if head, _ := argOf(list.Args, "--head"); head != branch {
			t.Fatalf("a find listed under %q, want the bare branch name: %v", head, list.Args)
		}
	}

	// The control: without a fork, one repository is confirmed and the head
	// is the branch alone.
	plain := forgetest.New(t, forgetest.Script{
		forgetest.OpList:       {forgetest.Raw("[]"), forgetest.Raw(`[{"number":5}]`)},
		forgetest.OpRepository: {forgetest.Repository("upstream/assistant")},
		forgetest.OpCreate:     {forgetest.Raw("https://github.com/upstream/assistant/pull/5\n")},
		forgetest.OpView: {forgetest.PullRequest(forgetest.PullRequestView{
			Number: 5, State: "OPEN", Mergeable: "MERGEABLE", Head: branch, Base: "main",
		})},
	})
	same, err := plain.Host().Open("upstream/assistant")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := forge.Submit(context.Background(), same, forge.OpenSpec{Head: branch, Base: "main", Title: "a change"}); err != nil {
		t.Fatalf("Submit without a fork: %v", err)
	}
	if n := len(callsOf(plain, forgetest.OpRepository)); n != 1 {
		t.Fatalf("a submission without a fork confirmed %d repositories, want one", n)
	}
	if head, _ := argOf(callsOf(plain, forgetest.OpCreate)[0].Args, "--head"); head != branch {
		t.Fatalf("a submission without a fork created head %q, want the branch alone", head)
	}
}

// A fork the provider resolves to another repository writes nothing, though
// the upstream confirmed: the write names the fork as well, and it is held
// to every repository it names.
func TestAForkThatResolvesElsewhereWritesNothing(t *testing.T) {
	p := forgetest.New(t, forgetest.Script{
		forgetest.OpList:       {forgetest.List()},
		forgetest.OpRepository: {forgetest.Repository("upstream/assistant"), forgetest.Repository("someone/else")},
	})
	provider, err := p.Host().OpenFork("upstream/assistant", "me/assistant")
	if err != nil {
		t.Fatalf("OpenFork: %v", err)
	}
	_, err = forge.Submit(context.Background(), provider, forge.OpenSpec{Head: "work", Base: "main", Title: "a change"})
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != forge.ReasonWrongRepository || !strings.Contains(refusal.Detail, "me/assistant") {
		t.Fatalf("a fork resolving elsewhere = %v, want wrong-repository naming the fork", err)
	}
	if creates := callsOf(p, forgetest.OpCreate); len(creates) != 0 {
		t.Fatalf("a pull request was created after the fork's confirmation failed: %+v", creates)
	}
}

// Find from a fork is ambiguous where the fork carries two open pull requests
// for the branch, and where the read came back as full as it may, because
// the one this run owns may be past what it returned. A pull request whose
// head repository is reported absent, a deleted fork's, is never this run's.
func TestFindFromAForkRefusesWhatItCannotSettle(t *testing.T) {
	full := make([]forgetest.ListEntry, 100)
	for i := range full {
		full[i] = forgetest.ListEntry{Number: i + 1, HeadOwner: "someone", HeadName: "assistant"}
	}
	p := forgetest.New(t, forgetest.Script{
		forgetest.OpList: {
			forgetest.List(forgetest.ListEntry{Number: 1, HeadOwner: "me", HeadName: "assistant"},
				forgetest.ListEntry{Number: 2, HeadOwner: "me", HeadName: "assistant"}),
			forgetest.List(full...),
			forgetest.List(forgetest.ListEntry{Number: 4, HeadOwner: "me"}),
		},
	})
	provider, err := p.Host().OpenFork("upstream/assistant", "me/assistant")
	if err != nil {
		t.Fatalf("OpenFork: %v", err)
	}
	ctx := context.Background()
	var refusal *forge.Refusal
	for _, what := range []string{"two from the fork", "a full read"} {
		if _, _, err := provider.Find(ctx, "work"); !errors.As(err, &refusal) || refusal.Reason != forge.ReasonAmbiguous {
			t.Fatalf("%s = %v, want ambiguous", what, err)
		}
	}
	if _, found, err := provider.Find(ctx, "work"); err != nil || found {
		t.Fatalf("a deleted fork's pull request = found %t, %v, want none found", found, err)
	}
}

// OpenFork refuses a fork it was not given and a fork naming the upstream
// itself, which is not a fork of it, before any request is made.
func TestOpenForkRefusesAForkThatIsNotOne(t *testing.T) {
	p := forgetest.New(t, forgetest.Script{})
	for _, c := range []struct{ upstream, fork string }{
		{"upstream/assistant", ""},
		{"", "me/assistant"},
		{"upstream/assistant", "Upstream/Assistant"},
		{"upstream/assistant", "https://github.com/me/assistant"},
	} {
		if _, err := p.Host().OpenFork(c.upstream, c.fork); !errors.Is(err, forge.ErrInvalidArgument) {
			t.Fatalf("OpenFork(%q, %q) = %v, want an invalid argument", c.upstream, c.fork, err)
		}
	}
	if calls := p.Calls(); len(calls) != 0 {
		t.Fatalf("a refused OpenFork reached the provider: %+v", calls)
	}
}
