package safety_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/safety"
)

// The shape of a run: observe the target before doing the work, do the work,
// then decide. The observation is the anchor, and it is the only thing the
// decision will accept as one.
func Example() {
	git := &fakeGit{
		parents: map[string][]string{
			"base": nil, "submitted": {"base"}, "rebased": {"base"},
		},
		advertised: map[string][][]advert{
			"gate": {{branch("refs/heads/feature", "submitted")}},
		},
	}
	guard := safety.New(git)
	target := safety.Target{Remote: "gate", Ref: "refs/heads/feature"}

	observed, err := guard.Observe(context.Background(), target)
	if err != nil {
		fmt.Println(err)
		return
	}

	// The run rebases and verifies, producing "rebased".

	decision, err := guard.Decide(context.Background(), safety.Update{
		Target:   target,
		Proposed: "rebased",
		Anchor:   observed,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(decision.Kind(), "leased on", decision.Anchor().State())
	fmt.Println("rewrites", decision.Rewritten())

	// Output:
	// anchored-force leased on submitted
	// rewrites [submitted]
}

// A refusal is a typed result. There is no decision inside it, so a caller
// that reads only the reason still cannot push.
func ExampleRefusal() {
	git := &fakeGit{
		parents: map[string][]string{
			"base": nil, "submitted": {"base"}, "theirs": {"submitted"}, "rebased": {"base"},
		},
		advertised: map[string][][]advert{"gate": {
			{branch("refs/heads/feature", "submitted")},
			{branch("refs/heads/feature", "theirs")},
		}},
	}
	guard := safety.New(git)
	target := safety.Target{Remote: "gate", Ref: "refs/heads/feature"}
	observed, _ := guard.Observe(context.Background(), target)

	_, err := guard.Decide(context.Background(), safety.Update{
		Target: target, Proposed: "rebased", Anchor: observed,
	})
	var refusal *safety.Refusal
	if errors.As(err, &refusal) {
		fmt.Println(refusal.Reason, refusal.Discarded)
	}
	fmt.Println(errors.Is(err, safety.ErrRefused))

	// Output:
	// would-discard [theirs submitted]
	// true
}
