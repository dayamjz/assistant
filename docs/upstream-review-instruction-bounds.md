# Outstanding upstream ask: bound one review rule, not the pool

Status: open. Nothing in this repository is waiting on it to build or pass, and
everything in it is waiting on it to grow.

This is an ask against `no-mistakes`, the validation gate this repository is
pushed through. It is recorded here because the constraint is ours and the
triage that works around it is ours; the change is not.

Every figure below states the method that produces it. The two methods are:

- **Accounting.** `config.LoadRepoFromBytes` on this repository's
  `.no-mistakes.yaml`, then `config.ReviewPathInstructionsBytes` on the parsed
  entries. This is the function the gate itself checks against the cap.
- **Rendering.** `reviewPathInstructionsSection(matchPathInstructions(changed,
  entries))` in `internal/pipeline/steps`, which is what a review prompt
  actually carries for a given changed-file set.

Both were run against `no-mistakes`' own source rather than a reimplementation.

## The ask

Bound `review.path_instructions` per rule rather than as one shared pool, so
adding a rule can never cost an existing one.

The shape is not invented for this ask. `docs/prd.html` section 10 already
specifies it for the same setting in this project's own configuration schema:

| Limit | Value | Measured on |
| --- | --- | --- |
| Path-scoped review rules | 64 | Elements of `review.path_rules` |
| Patterns per review rule | 64 | The paths one rule scopes to |
| Review rule guidance length | 4096 | Runes of one rule's guidance text |

Two properties follow from that table and neither holds today upstream. A rule
is bounded on its own, so a new rule is refused only when it is itself too long,
never because of what other rules already say. And one rule carries many
patterns, so a rule shared by several packages is one rule with several
patterns rather than one copy per package, each re-paying the block frame.

Raising `MaxReviewPathInstructionsBytes` is a valid interim and the weaker
form: it moves the wall without changing that adding a rule spends a shared
budget.

## Why, established rather than asserted

From `internal/config/config.go` in `no-mistakes`:

```go
MaxReviewPathInstructions      = 32
MaxReviewPathInstructionsBytes = 16384   // = 32 x 512
```

The byte cap's own doc comment says it "leaves room for the entry cap to be
reached with a rule of ordinary length, so neither cap makes the other
unusable". It is derived from the entry cap. The danger it names - an oversized
prompt fails the agent invocation outright rather than degrading - is real, but
16 KB was not measured against a prompt or a model budget, so this is not a
request to weaken a calibrated limit.

The accounting charges the configured entries, not what a run renders. Reading
`ReviewPathInstructionsBytes`, one entry costs `229 + len(path) + len(trimmed
instructions)`, plus a 2-byte separator before every entry after the first, on
top of a fixed 193 bytes for the section's blank line, heading and newline. The
229 includes the full 192-byte matched-file allowance whether or not the entry's
glob matches the change. That is a correct upper bound, because the check runs
before a run starts and so has to hold for every diff, but it means the number
that gets refused is the worst case across all diffs rather than anything one
review sees.

By rendering, a change touching one file in one package carries 1442 to 1450
bytes of this section for the twelve packages with no block of their own, and
2013 (`internal/graph`) to 4341 (`internal/findings`) bytes for the eleven that
have one, median 2223. So the gap between what is charged and what is delivered
is roughly four to eleven times, and it is the charge that refuses a run.

## What it blocks here

By accounting, `review.path_instructions` has 11 entries totalling 16273 of the
16384 allowed, leaving 111 bytes. Ten of those entries are package blocks; the
eleventh is the shared `path: "*"` block, which is not a package. The ten globs
cover eleven of this repository's twenty-three packages under `internal/`,
because `internal/agents/**` matches both `internal/agents` and
`internal/agents/standin`. Twelve packages have no block.

Those ten package blocks are charged 14639 bytes, an average of 1464 each.
Completing the set for the remaining twelve at that average costs
`12 x 1464 + 12 x 2 = 17592` bytes more, for a section of 33865. Estimated
instead from the `AGENTS.md` bullets those blocks would be drawn from - the
twelve bullets measure 11664 bytes with each line stripped of its indent, and
their globs 222 - it is `11664 + 12 x 229 + 222 + 12 x 2 = 14658` bytes more,
for a section of 30931. Either basis puts the completed section at roughly twice
the 16384 cap, so the channel is about half the size this repository needs, and
the shortfall is structural rather than a matter of a few rules being verbose.

## What was tried locally, and the result

Nothing. This is a null result, not a partial win.

The attempt was to split review rules across two surfaces by one test - would
this rule still need to apply if the contributor were hostile? - keeping only
what has to survive a hostile branch in the trusted, capped channel and moving
the rest to `AGENTS.md`. Two things sank it.

The destination is not reliable. No part of the gate reads `AGENTS.md`;
delivery is entirely the resolved agent CLI's own project-doc discovery. The
global configuration selects the agent automatically rather than pinning one,
`no-mistakes` implements a project-instruction suppression knob for only three
of its adapters, one of those knobs is a byte cap on `AGENTS.md` itself whose
size is set outside this repository, and the gate's `disable_project_settings`
suppresses the file wholesale. Each of those ends a moved rule's reach with no
error, no failed parse and no refused run - the same silent stop-applying the
move was meant to prevent, relocated rather than removed.

And the room it would free is not worth having. A first pass suggested roughly
1.4 KB sat in per-package limits disclaimers, on the reasoning that a rule also
present in `AGENTS.md` is safe to drop from the trusted block. That reasoning is
wrong: `AGENTS.md` is read from the pushed branch, so the duplicate is exactly
the copy a contributor deletes, and duplication says nothing about whether
removal is safe. Under the test that does apply - if a contributor deleted this
from `AGENTS.md` on their branch, would its absence from the review matter? -
text naming a mechanism's residual gaps stays, because a reviewer who does not
know a gap exists cannot see a change that widens it. Hand-measuring the four
spans sampled that way, in `internal/findings` and `internal/agents`, about 405
bytes of 1402 survive as removable, under a third, and buying under 3% of the
cap costs the rules that make a gap visible.

So the split was dropped and `.no-mistakes.yaml` is unchanged by this work: 11
entries, 16273 of 16384, 111 bytes free, the same as before it started.

**111 bytes is not room for a rule.** By the accounting above a new block costs
at least 232 bytes before a word of guidance, so no new block fits at all, and
an addition to an existing block has under 111 bytes. Measure with
`ReviewPathInstructionsBytes` before writing anything. A section over the cap is
refused rather than truncated, and the gate validates the pushed copy too, so a
branch that overfills it fails its own run at start and cannot merge; reaching
later runs takes a commit that lands on the default branch without a gate run.

Compaction and per-package placement were both considered and rejected.
Compaction compresses hard-won prose toward a number that is not a measured
budget. Per-package placement is arithmetically backwards: each copy re-pays the
229-byte frame as well as the text, so the `path: "*"` block is the cheapest
home a shared rule has.

There is no local fix. The bound has to come from upstream.
