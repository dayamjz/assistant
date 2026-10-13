package forgetest_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
)

func TestMain(m *testing.M) {
	forgetest.Main()
	os.Exit(m.Run())
}

// Every reply this package writes is read by the real GitHub adapter, over
// the wire, and comes back as that adapter's own values: a pull request's
// state, head and base through its parser, a provider that refused as its
// rejection carrying the message, the authentication status as its own
// reason, and bytes it cannot read as malformed. The stand-in records what
// it was asked and answers a vector gh would refuse the way gh does.
func TestRepliesAreReadByTheRealAdapter(t *testing.T) {
	const head = "6f1a3c2b0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a"
	p := forgetest.New(t, forgetest.Script{
		forgetest.OpView: {
			forgetest.PullRequest(forgetest.PullRequestView{Number: 12, State: "MERGED", Mergeable: "UNKNOWN",
				Head: "fm/work", HeadCommit: head, Base: "main", Title: "feat: a change", URL: "https://github.com/owner/name/pull/12"}),
			forgetest.Exit(1, "GraphQL: Could not resolve to a PullRequest"),
			forgetest.Unauthenticated(),
			forgetest.Raw("not json"),
		},
	})
	provider, err := p.Host().Open("owner/name")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	pr, err := provider.Get(ctx, 12)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pr.Number != 12 || pr.State != forge.PullRequestStateMerged || pr.HeadCommit != head || pr.Base != "main" ||
		pr.Head != "fm/work" || pr.Mergeability != forge.MergeabilityUnknown {
		t.Fatalf("the adapter read %+v", pr)
	}

	var refusal *forge.Refusal
	if _, err := provider.Get(ctx, 12); !errors.As(err, &refusal) || refusal.Reason != forge.ReasonRejected ||
		!strings.Contains(refusal.Detail, "Could not resolve") {
		t.Fatalf("an exit = %v, want the adapter's rejection carrying the message", err)
	}
	if _, err := provider.Get(ctx, 12); !errors.As(err, &refusal) || refusal.Reason != forge.ReasonUnauthenticated {
		t.Fatalf("the authentication status = %v, want the adapter's unauthenticated reason", err)
	}
	if _, err := provider.Get(ctx, 12); !errors.As(err, &refusal) || refusal.Reason != forge.ReasonMalformed {
		t.Fatalf("raw bytes = %v, want the adapter's malformed reason", err)
	}
	// Past the script's end the last reply stands.
	if _, err := provider.Get(ctx, 12); !errors.As(err, &refusal) || refusal.Reason != forge.ReasonMalformed {
		t.Fatalf("past the script = %v, want the last reply", err)
	}
	// An operation nobody scripted is a provider that refuses, with a
	// message saying why.
	if _, _, err := provider.Find(ctx, "fm/work"); !errors.As(err, &refusal) || refusal.Reason != forge.ReasonRejected ||
		!strings.Contains(refusal.Detail, "no scripted answer for list") {
		t.Fatalf("an unscripted operation = %v, want a rejection naming it", err)
	}

	calls := p.Calls()
	if len(calls) != 6 || calls[0].Op != forgetest.OpView || calls[5].Op != forgetest.OpList {
		t.Fatalf("the stand-in recorded %+v", calls)
	}
	for _, want := range []string{"pr", "view", "12", "--repo=owner/name"} {
		found := false
		for _, a := range calls[0].Args {
			found = found || a == want
		}
		if !found {
			t.Fatalf("the first call's vector %v lacks %q", calls[0].Args, want)
		}
	}

	// Rescript replaces what follows.
	p.Rescript(t, forgetest.Script{forgetest.OpView: {forgetest.PullRequest(forgetest.PullRequestView{Number: 12, State: "OPEN"})}})
	if pr, err := provider.Get(ctx, 12); err != nil || pr.State != forge.PullRequestStateOpen {
		t.Fatalf("after Rescript Get = %+v, %v", pr, err)
	}
}

// The write confirmation is answered from the repository reply, and a
// mismatch is the adapter's own wrong-repository refusal.
func TestTheRepositoryReplyAnswersTheWriteConfirmation(t *testing.T) {
	p := forgetest.New(t, forgetest.Script{forgetest.OpRepository: {forgetest.Repository("someone/else")}})
	provider, err := p.Host().Open("owner/name")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var refusal *forge.Refusal
	if _, err := provider.UpdateBody(context.Background(), 12, "body"); !errors.As(err, &refusal) || refusal.Reason != forge.ReasonWrongRepository {
		t.Fatalf("a write against a provider resolving elsewhere = %v, want wrong-repository", err)
	}
	if calls := p.Calls(); len(calls) != 1 || calls[0].Op != forgetest.OpRepository {
		t.Fatalf("the stand-in recorded %+v, want the confirmation and nothing written", calls)
	}
}

// Exit refuses a zero status, because a zero status is an answer.
func TestExitTakesANonZeroStatus(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Exit(0) was accepted")
		}
	}()
	forgetest.Exit(0, "fine")
}
