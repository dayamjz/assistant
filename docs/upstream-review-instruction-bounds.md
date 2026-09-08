# Prepared upstream ask: bound one review rule, not the pool

Status: prepared, not submitted. This ask has not been filed anywhere. Where it
would go is `kunchenguid/no-mistakes`, a third-party repository, and sending it
there is an outward-facing act and a separate decision that needs its own
authorization. This document does not carry that authorization, and no sentence
in it asks anyone to file it.

So nothing here is pending in an upstream queue; there is no queue it is in.
What it is is the case, written out so it can be checked rather than trusted,
for whoever weighs that decision. It stops being current when the bound it
describes changes - not when this repository works around it, and not by being
edited to match whatever it settled for meanwhile.

It is recorded here because the constraint is ours and the triage that works
around it is ours; the change is not.

Every figure below states the method that produces it. The three methods are:

- **Accounting.** `config.LoadRepoFromBytes` on this repository's
  `.no-mistakes.yaml`, then `config.ReviewPathInstructionsBytes` on the parsed
  entries. This is the function the gate itself checks against the cap.
- **Rendering.** `reviewPathInstructionsSection(matchPathInstructions(changed,
  entries))` in `internal/pipeline/steps`, which is what a review prompt
  actually carries. Its result moves one byte per byte of the changed paths, so
  every rendering figure below names the `changed` set it was measured against.
- **Quoting.** A named span of one entry's `review.path_instructions` guidance,
  located by the opening and closing words quoted here inside that entry's
  instruction text with the YAML block indent removed and wrapped lines joined
  by a single space, and measured in UTF-8 bytes. For an interior span that
  count equals the count against the raw block scalar, because each newline the
  join replaces is one byte and no line in these blocks carries trailing
  whitespace; both were checked against the file.

The first two were run against `no-mistakes`' own source rather than a
reimplementation.

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

From `internal/config/config.go` in `no-mistakes`, verbatim:

```go
	// MaxReviewPathInstructions is the largest number of path_instructions
	// entries a repository may configure.
	MaxReviewPathInstructions = 32
	// MaxReviewPathInstructionsBytes is the largest review-prompt section
	// path_instructions may produce, measured by ReviewPathInstructionsBytes.
	// It leaves room for the entry cap to be reached with a rule of ordinary
	// length, so neither cap makes the other unusable.
	MaxReviewPathInstructionsBytes = 16384
```

That 16384 is 32 x 512 is this document's arithmetic, not the source's; the
source states neither the product nor the factorisation. What grounds reading
the byte cap as derived from the entry cap is the source's own doc comment
above it: it "leaves room for the entry cap to be reached with a rule of
ordinary length, so neither cap makes the other unusable". The danger the
surrounding comment names - an oversized prompt fails the agent invocation
outright rather than degrading - is real, but nothing states that 16 KB was
measured against a prompt or a model budget: `grep -rnE --include='*.go'
'16384|16 KB'` over `internal` and `docs` in the `no-mistakes` checkout returns
a single line, the constant itself at `config.go:262`. So this is not a request
to weaken a calibrated limit. What the sweep cannot reach is a budget recorded
somewhere outside that source.

The accounting charges the configured entries, not what a run renders. Reading
`ReviewPathInstructionsBytes`, one entry costs `229 + len(path) + len(trimmed
instructions)`, plus a 2-byte separator before every entry after the first, on
top of a fixed 193 bytes for the section's blank line, heading and newline. The
229 includes the full 192-byte matched-file allowance whether or not the entry's
glob matches the change. That is a correct upper bound, because the check runs
before a run starts and so has to hold for every diff, but it means the number
that gets refused is the worst case across all diffs rather than anything one
review sees.

By rendering, with `changed` set to the single path `<package>/file.go`, one
package at a time: for the twelve packages no glob covers, the section is the
heading and the `path: "*"` block alone and comes to `1422 + len(changed)`, so
1442 for `internal/cli/file.go` up to 1450 for `internal/checkpoints/file.go`.
For the eleven a glob does cover it runs from 2013 (`internal/graph/file.go`)
to 4341 (`internal/findings/file.go`), median 2223. So the gap between what is
charged and what is delivered is roughly four to eleven times, and it is the
charge that refuses a run.

## What it blocks here

By accounting, `review.path_instructions` has 11 entries totalling 16273 of the
16384 allowed, leaving 111 bytes. Ten of those entries are package blocks; the
eleventh is the shared `path: "*"` block, which is not a package. The ten globs
cover eleven of this repository's twenty-three packages under `internal/`,
because `internal/agents/**` matches both `internal/agents` and
`internal/agents/standin`. Twelve packages have no block.

By that accounting the ten package blocks are charged 14659 bytes, an average
of 1466 each. Every one of the ten includes its 2-byte separator, since all ten
follow the `path: "*"` entry, which is charged 1421 and pays none; with the
fixed 193 that closes on the 16273 above. Completing the set for the remaining
twelve at that average costs `12 x 1466 = 17592` bytes more, for a section of
33865.

Estimated instead from the `AGENTS.md` bullets those blocks would be drawn from,
it is `11664 + 12 x 229 + 222 + 12 x 2 = 14658` bytes more, for a section of
30931. The 11664 is those twelve bullets measured this way: take each bullet's
first line and its indented continuation lines, remove leading and trailing
whitespace from each line, rejoin with one newline between lines and none after
the last, and sum the twelve. The 222 is their twelve globs. Either basis puts
the completed section at roughly twice the 16384 cap, so the channel is about
half the size this repository needs, and the shortfall is structural rather than
a matter of a few rules being verbose.

## What was tried locally, and the result

Nothing that touches the shortfall. One small recovery was measured and then
declined; the arithmetic for both is below, and neither is a partial win.

The attempt was to split review rules across two surfaces by one test - would
this rule still need to apply if the contributor were hostile? - keeping only
what has to survive a hostile branch in the trusted, capped channel and moving
the rest to `AGENTS.md`. Two things sank it.

The destination is not reliable. No part of the gate reads `AGENTS.md`:
`grep -rnE --include='*.go' 'AGENTS\.md|CLAUDE\.md'` over the `no-mistakes`
checkout, excluding `_test.go`, returns 23 lines, and every one is a comment, a
prompt body or an error string - none opens the file. That sweep is over the
literal filenames, so a path assembled at runtime would not appear in it.
Delivery is therefore the resolved agent CLI's own project-doc discovery, and
nothing in this repository selects or pins that adapter: this repository's
`.no-mistakes.yaml` has four top-level keys - `commands`, `ignore_patterns`,
`document` and `review` - and no `agent` key at any level. Which adapter
resolves is settled outside the branch under review.
Two mechanisms decide whether a moved rule reaches a reviewer, and neither
guarantees that it does. `no-mistakes` implements a project-instruction
suppression knob for only three of its adapters, so for any other resolved
adapter whether the file is read at all is that CLI's own business and not
something the gate settles - unguaranteed, and silent whichever way it falls.
And the gate's `disable_project_settings` is what actually ends the reach, but
only when it is set: with it set the file is suppressed silently only when
every adapter in the resolved set neutralizes with its effective knob intact,
and otherwise `agent.EnsureGateNeutralized` refuses the run rather than
launching it, naming codex, claude and pi. With it unset - the default, and what
holds here - `no-mistakes` appends no suppression flag and the reach is simply
not settled by the gate either way. That refusal is
the only place the gate fails closed on any of this, and neither branch is
reached in this repository: `git show origin/main:.no-mistakes.yaml | grep -n
'disable_project_settings'` returns nothing against the default branch as it
stands, which is the copy the gate honours for this key, this branch's only
change to that file is the `document.instructions` lines, and the field is a
plain bool whose missing key is falsy (`internal/config/config.go:180`). A
destination that is never guaranteed and can end without a word is the same
stop-applying the move was meant to prevent, relocated rather than removed.

An earlier draft counted a third mechanism here, codex's
`project_doc_max_bytes`, as a byte cap on `AGENTS.md` whose size is set outside
this repository. That is withdrawn. `buildArgs` appends
`-c project_doc_max_bytes=0` only inside `if a.disableProjectSettings`
(`internal/agent/codex.go`), and the doc comment on `NeutralizesGateInstructions`
in that file says the knob is meaningful only under that opt-out - the same
guard the claude and pi knobs sit behind. So under the opt-out the cap is the
wholesale suppression already described, set to zero, and outside it
`no-mistakes` passes nothing. A third hazard would need codex's own behaviour
absent the opt-out, which is not something `no-mistakes` sets and was not
verified here.

And the room it would free is not worth having. A first pass suggested roughly
1.4 KB of per-package limits disclaimers could go, on the reasoning that a rule
also present in `AGENTS.md` is safe to drop from the trusted block. That
reasoning is wrong: `AGENTS.md` is read from the pushed branch, so the duplicate
is exactly the copy a contributor deletes, and duplication says nothing about
whether removal is safe. Under the test that does apply - if a contributor
deleted this from `AGENTS.md` on their branch, would its absence from the review
matter? - text naming a mechanism's residual gaps stays, because a reviewer who
does not know a gap exists cannot see a change that widens it.

By quoting, four spans were sampled, and they total the 1402 bytes that "roughly
1.4 KB" names:

| Entry | Span | Bytes |
| --- | --- | --- |
| `internal/findings/**` | "What the binding does not do" ... "reasoning rests on." | 397 |
| `internal/agents/**` | "Two gaps that leaves" ... "for a Runner it returned." | 495 |
| `internal/store/**` | "Reading it as an authorization check" ... "standing authority permits." | 135 |
| `internal/store/**` | "What keeps the person value out" ... "and this repository has none." | 375 |

Three sub-spans of those survive the deletion test as removable, totalling 358:

| Entry | Span | Bytes |
| --- | --- | --- |
| `internal/agents/**` | "and nothing in this package refuses a suppression request" ... "when it builds the topology." | 181 |
| `internal/store/**` | "Reading it as an authorization check" ... "standing authority permits." | 135 |
| `internal/store/**` | "The absent check is not the gap to report;" | 42 |

An earlier draft of this document put that second figure at 405. That was an
estimate reported as though it had been measured; measured, it is 358, so the
triage frees less than the estimate claimed. 358 of 1402 is just over a quarter,
and 2.2% of the 16384 cap. Those 358 bytes come out at no cost to
gap-visibility - surviving the deletion test is what that means - and the other
1044 stay, because they are what lets a reviewer see a change that widens a gap.
So the triage is not blocked. It is small, and past the 358 this sample starts
costing gap-visible text.

That 358 is what one sample yielded, not a ceiling on what triage recovers. By
quoting, the four spans total 1402 bytes against 13369 bytes of guidance across
all 11 entries - the ten package blocks carry 12178 and the shared block 1191 -
so the sample is 10.5% of the text and touches three of the eleven entries,
`internal/findings/**`, `internal/agents/**` and `internal/store/**`. The other
eight were not triaged, and that triage is deliberately deferred. The claim that
does cover the whole section is the triage ceiling, measured by deleting every
byte of guidance in the whole section, and it does not rest on this sample.

So the split was dropped and `review.path_instructions` is unchanged by this
work, byte for byte: 11 entries, 16273 of 16384, 111 bytes free, the same as
before it started. The change does touch `.no-mistakes.yaml` elsewhere, adding
this document's owner line to `document.instructions`, which is outside the
capped section.

**111 bytes is not room for a rule.** By the accounting above a new block costs
at least 232 bytes before a word of guidance, so no new block fits at all, and
an addition to an existing block has at most 111 bytes. Measure with
`ReviewPathInstructionsBytes` before writing anything. A section over the cap is
refused rather than truncated, and the gate validates the pushed copy too, so a
branch that overfills it fails its own run at start and cannot merge; reaching
later runs takes a commit that lands on the default branch without a gate run.

The triage above would raise that figure, and the raise is worth stating
exactly. 111 free plus the 358 it frees is 469 bytes, which by the same
accounting fits one new block carrying about 220 bytes of guidance once a
package glob is paid for, or a 469-byte addition to an existing block - 39% of
the 1191 bytes of guidance the shared `path: "*"` block carries today. That is
roughly the one-more-rule case, so the recovery is real and it was declined
rather than unavailable: 469 bytes is 2.7% to 3.2% of the 14658 to 17592 a
completed set needs, so it buys one rule and changes nothing about the bound.

Compaction and per-package placement were both considered and rejected.
Compaction compresses hard-won prose toward a number that is not a measured
budget. Per-package placement is arithmetically backwards: each copy re-pays the
229-byte frame as well as the text, so the `path: "*"` block is the cheapest
home a shared rule has.

There is no local fix for the shortfall, and this does not depend on how much
triage would find. By the accounting above the eleven entries carry 13369
bytes of guidance between them, 12178 in the ten package blocks and 1191 in the
shared one, so deleting every byte of it - the whole section's guidance, far past
anything the deletion test would allow - leaves the section at 2904, which is its
frames and heading alone. Adding the 14658 this document's own lower estimate
says a completed set needs gives 17562, so the shortfall survives the deletion of
every rule in the section by 1178 bytes against the 16384 cap, and by 4112 on the
higher estimate's 20496. Triage buys rules; it
cannot buy the bound. That can only change in `no-mistakes`.
