# Outstanding upstream ask: bound one review rule, not the pool

Status: open. Nothing in this repository is waiting on it to build or pass, and
everything in it is waiting on it to grow.

This is an ask against `no-mistakes`, the validation gate this repository is
pushed through. It is recorded here because the constraint is ours and the
triage that works around it is ours; the change is not.

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

`ReviewPathInstructionsBytes` charges the configured entries, not what a run
renders. Every entry pays a 229-byte frame, including the full 192-byte
matched-file allowance, whether or not its glob matches the change, plus a
190-byte heading once. That is a correct upper bound - the check runs before a
run starts, so it has to hold for every diff - but it means the number that gets
refused is the worst case across all diffs, while a change touching one package
renders under 2 KB of that section.

## What it blocks here

Eleven of this repository's twenty-three packages have a rule block. The
thirteen without one include `pipeline`, `runs`, `service`, `checkpoints`,
`stages`, `cli`, `scope`, and `principles`, each of which `AGENTS.md` gives a
multi-sentence contract. Completing the set at the current average needs roughly
22-28 KB against a 16 KB pool, so the channel is undersized for this repository
by something between a third and a half, and the shortfall is structural rather
than a matter of a few rules being verbose.

## What was done instead, and what it did not solve

Review rules were split across two surfaces by one test - would this rule still
need to apply if the contributor were hostile? - with the trusted, capped
channel keeping only what has to survive a branch that edits `AGENTS.md`. See
the `.no-mistakes.yaml` bullet in `AGENTS.md`.

That split is worth having on its own terms, and it did not free space. Triaging
the shared `path: "*"` block released about 320 bytes and the guard protecting
the split cost about 350, so the section is tighter after the pass than before
it. Roughly 1.4 KB more sits in per-package limits disclaimers that fail the
same test and are already duplicated in `AGENTS.md`; removing those is a
separate decision about rules this pass did not write, not a consequence of this
one.

Compaction and per-package placement were both considered and rejected.
Compaction compresses hard-won prose toward a number that is not a measured
budget. Per-package placement is arithmetically backwards: each copy re-pays the
block frame as well as the text, so the `path: "*"` block is the cheapest home a
shared rule has.
