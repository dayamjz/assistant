# Caliper Q4 replay pilot, 2026-10-27

This is a pilot for Q4 in `docs/caliper-evaluation-plan.md` (section 3.5):
which agent setup clears review findings most efficiently. It does not
answer that question. One case was judged, at k = 1 per arm, so nothing here
ranks the arms, and nothing here is a basis for changing a default or the
ladder order (plan section 5, step 8). It checks that a replayed fix round
can be run and judged end to end, and it records what that cost.

It did not run under caliper. It is the fleet's own replay benchmark, run
before the harness was ported (plan section 5, step 7).

**Provenance: Recorded run.**

- **Command:** `python3 harness.py queue` for the workers, and
  `python3 harness.py queue --judge-case 475-A` for the deferred judges.
  `python3 collect.py` then built the per-run table.
- **Capture context:** the owner's MacBook, from 2026-10-26 21:11 to
  2026-10-27 07:10 PDT. Up to 2 arms ran at a time, beside one live fleet
  worker that shared the machine's suite lock.
- **Where the raw evidence is:** the private `dayamjz/fleet-tools`
  repository at `3fd42ad`, under `experiments/replay/`
  (`REPORT.md`, `results/replay-report.md`, `results/runs.csv`,
  `results/judgments.json`, plus each run's diff, report, review and Codex
  session).
  - The harness that produced them (`harness.py`, `collect.py`) is not in
    that commit. It lives on the MacBook.
  - A reader of this repository therefore cannot re-derive the figures
    below. Every one of them is copied from those files.
  - Step 7 of the plan brings the harness here.

## What ran

| | |
| --- | --- |
| Worker agent | Claude Code 2.1.311, `claude -p --output-format json --permission-mode bypassPermissions` |
| Worker models | `claude-opus-5-5` and `claude-fable-5-1` (as each run's `modelUsage` names them) |
| Worker effort | the CLI's `--effort`: `high`, `xhigh` or `max` |
| Context | fresh for every arm: a new session, with any earlier attempt's transcript and memory for that checkout deleted first |
| Cap | 75 minutes per worker. A worker that has not committed by then counts as a failure. |
| Judge | codex-cli 0.163.0, `gpt-6.1-sol`, `model_reasoning_effort="xhigh"`, `sandbox_mode="workspace-write"`, `project_doc_max_bytes=0` |
| Judge scope | a delta review: the case commit as base, and the case's findings as prior findings |
| Judge prompt | the fleet's `review-prompt.md`, as rendered for each run (`results/<run>.review-prompt.txt`) |
| Caliper | not used |

The five arms are agent x model x effort, all on Claude Code with fresh
context: `opus-high`, `opus-xhigh`, `opus-max`, `fable-high`, `fable-xhigh`.

Four cases were run: 475-A, 475-B, 659-A and 659-B. Each is a historical
failing fix round from PR #475 or PR #659. The worker's prompt was the same
for every arm of a case: the task text, the worker rules, and that round's
review findings verbatim (`results/task-<case>.txt`). The cases were picked
by hand, not harvested. Section 6 "Learning what converges" of
`docs/prd.md` proposes the harvest, and it is not built.

**Case 475-A**, the only judged case:

- **Commit:** `facc85f`, on PR #475.
- **Findings:** one Codex delta review (effort high) with one blocker. A new
  comment in `internal/tasks/promote.go` promised a refusal that the
  isolation assertion did not enforce: a kept copy whose `.git` links to
  another clone passed both file-identity checks.
- **The live round from that commit:** a Fable 5.1 worker at effort high,
  in a fresh session with a handoff. Its next delta review (Codex xhigh, at
  `38ce420`) asked for changes, with one blocker.
- **The accepted fix:** PR #475 landed on 2026-10-27 at head `b277ca3`. That
  is 76 non-merge commits after `facc85f`.

## Where the run differs from the suite the plan describes

The fleet's `REPORT.md` lists most of these deviations. The two about graders
come from holding the run against plan section 3.5.

- **Worker rules.** Each worker got the fleet's standing rules with four
  lines changed:
  - "push and open a PR" became "commit locally only";
  - the end-of-round whole-suite run was dropped, because the task itself
    said not to run it;
  - "before pushing" became "before committing";
  - the report asks for a head sha instead of a PR URL.

  The edits were the same for every arm.
- **Guarded clones, not worktrees.**
  - **What a worker got:** a `git clone --shared` of the local repository,
    detached at the case commit. Its `origin/*` refs mirrored the local
    tracking refs, and its push URL pointed at a path that does not exist.
    A real `git push origin` was tried and refused.
  - **Two other guards failed first.** The first guard set `GIT_CONFIG_*`
    variables and a refusing pre-push hook. It broke this repository's own
    tests, which push between temporary repositories. It also changed what
    was measured, because the product treats `GIT_CONFIG_*` as redirecting.
    Two runs under it were discarded. The second guard removed only the
    GitHub CLI token, and failed its test: git still found a token in the
    keychain.
  - **Residual gap, accepted:** a worker that pushed to an explicit
    `https://github.com/...` URL would still find that keychain token. The
    post-run audit found no push or PR command in any transcript, and none
    of the 18 worker commits on GitHub.
- **Pinned judge base.** Where the review prompt names `origin/main`, the
  judge got the case's merge-base with `origin/main` as of 2026-10-26
  20:30 PDT (`99c6b37` for 475-A). A fetch during the benchmark could
  therefore not change what was reviewed. The workers were not pinned the
  same way. Each saw `origin/main` as it stood when its clone was made, and
  it had moved from `da9c42e` (20:30) to `93df9a7` by 21:14 PDT.
- **Judging stopped at one case.** The shared Codex account was on pace to
  run out of its weekly quota, which the live fleet's reviews need. The
  owner chose to judge 475-A only. The other 15 runs are kept as
  `refs/replay-results/<case>-<arm>` in the MacBook's checkout, so they can
  be judged later on the exact same tree.
- **No objective grader ran, though one was available.** Nothing ran any
  landed test against an arm's result.
  - Section 3.5 of the plan rules out running all of 475-A's landed tests,
    because they also cover later findings. It does not rule out the tests
    for the case's own finding.
  - The live round's fix, `38ce420`, added two tests for exactly that
    finding:
    - `TestPromoteRefusesACopyWhoseRepositoryDirectoryIsALink`;
    - a new sub-test of
      `TestAssertIsolatedRefusesEverythingButAnIsolatedCopy`.
  - Its commit message reports that both passed the link before the fix
    and refuse it after. Both are still in the landed head `b277ca3`.
  - They are 475-A's candidate objective graders. Nobody checked them
    against the case's commit or ran them against the arms, so the pilot's
    outcome rests on the judge alone.
- **No positive control ran before the workers.** Section 3.5 asks that a
  case's blocking finding reproduce at its commit before any arm runs.
  Nothing checked that here. The nearest evidence came afterwards and from
  the judges: four of the five reported reproducing the blocker at the
  case's code. That is narrative inside a review, not a control, so it does
  not rule out the finding having softened in this environment.
- **Judge attempts stopped and rerun.** Four of the five 475-A judges were
  stopped by a quota gate within seconds of starting. None recorded a token
  count, and their files are kept with a `.failed-<time>` suffix. Each was
  rerun later from the start on the same stored commit.

## Results: case 475-A (judged)

**How to read the money columns:**

- Dollar figures are **API-equivalent at list price (illustrative; actual
  billing is subscription-based)**. The Claude and Codex accounts are
  subscriptions, so none of these figures is what was billed.
- Rates come from the Anthropic and OpenAI pricing pages, retrieved on
  2026-10-26, per million tokens:
  - Opus 5.5: input 4, output 20, cache read 0.20, 1-hour cache write 8.
  - Fable 5.1: input 10, output 50, cache read 0.25, 1-hour cache write 20.
  - `gpt-6.1-sol`, short context: input 2, cached input 0.10, output 10.
- Every worker cache write was a 1-hour write.
- Worker dollars are the token counts at those rates. They equal the Claude
  CLI's own `total_cost_usd` (cost basis `list`) to the cent on every
  completed run.
- Review dollars are priced per request from the Codex session log. Every
  request fell in the short-context tier.

**How to read the token columns:**

- These are not `assistant runs --usage` figures. The product recorded
  nothing here.
- Worker tokens are the Claude CLI's `modelUsage`, so input excludes cache
  reads and writes.
- Review tokens are Codex's `total_token_usage`, which counts differently.
  The two are kept in separate tables.

**Outcome and worker usage:**

| Arm | Delta verdict | Blockers | Should-fix | New findings | Worker input | Worker output | Cache read | Cache write | Worker wall | Worker API-eq $ |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| opus-high | APPROVE | 0 | 0 | 0 | 156 | 31,002 | 7,403,958 | 135,459 | 61.3 min | $3.19 |
| opus-xhigh | APPROVE | 0 | 0 | 0 | 184 | 66,088 | 12,409,825 | 192,387 | 53.2 min | $5.34 |
| opus-max | APPROVE | 0 | 0 | 0 | 218 | 140,635 | 25,960,272 | 327,797 | 68.2 min | $10.63 |
| fable-high | APPROVE | 0 | 0 | 0 | 1,098 | 33,131 | 3,626,149 | 116,598 | 39.7 min | $4.91 |
| fable-xhigh | APPROVE | 0 | 0 | 0 | 1,062 | 110,937 | 6,196,246 | 258,662 | 44.7 min | $12.28 |

**Judge usage.** Here input includes cached input, and output includes
reasoning. Codex reported a cache-write count of zero for every review, so
that column is a measured zero, not a missing value.

| Arm judged | Input (incl. cached) | Cached input | Cache write | Output (incl. reasoning) | Reasoning | Judge wall | Review API-eq $ | Worker + review API-eq $ |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| opus-high | 2,037,617 | 1,927,552 | 0 | 8,758 | 3,460 | 4.5 min | $0.50 | $3.69 |
| opus-xhigh | 1,615,797 | 1,500,032 | 0 | 7,676 | 2,863 | 3.0 min | $0.46 | $5.80 |
| opus-max | 1,552,735 | 1,460,992 | 0 | 7,568 | 3,068 | 3.8 min | $0.41 | $11.03 |
| fable-high | 1,553,312 | 1,451,904 | 0 | 7,237 | 2,947 | 3.5 min | $0.42 | $5.33 |
| fable-xhigh | 2,413,868 | 2,312,576 | 0 | 11,517 | 5,451 | 6.1 min | $0.55 | $12.83 |

Every judge confirmed that the arm's head refuses the linked `.git` before
writing. Four judges reported reproducing the original blocker at the case's
code. The `opus-max` judge instead reproduced the unsafe behavior with the
arm's guard disabled. The fixes differed
in reach. For example, `opus-high` added only the `.git` check, while
`opus-xhigh` also refused a linked copy path (`results/judgments.json`).

## Results: the 15 unjudged runs

These runs show whether a worker committed within the cap, and what it
spent. They show nothing about whether the fix was right. A committed,
unjudged run is neither a success nor a failure.

| Arm | Committed within the 75-minute cap |
| --- | --- |
| opus-high | 3 of 3 |
| opus-xhigh | 3 of 3 |
| opus-max | 1 of 3 (timed out on 475-B and 659-B) |
| fable-high | 3 of 3 |
| fable-xhigh | 3 of 3 |

The two timed-out runs left uncommitted changes, which are kept as
`<run>.uncommitted.diff` and were never judged. Their usage comes from
their saved transcripts. On two completed runs, the transcript totals
match the CLI's figures exactly.

<details>
<summary>Every run (20): worker usage and outcome, from <code>results/runs.csv</code></summary>

Worker tokens are Claude CLI categories. "Source" says where the counts were
read from. API-equivalent $ is at list price (illustrative; actual billing is
subscription-based).

| Run | Commits | Timed out | Wall | Input | Output | Cache read | Cache write | Source | Worker API-eq $ | Judged |
| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | --- | ---: | --- |
| 475-A-opus-high | 1 | no | 61.3 min | 156 | 31,002 | 7,403,958 | 135,459 | CLI JSON | $3.19 | APPROVE |
| 475-A-opus-xhigh | 1 | no | 53.2 min | 184 | 66,088 | 12,409,825 | 192,387 | CLI JSON | $5.34 | APPROVE |
| 475-A-opus-max | 1 | no | 68.2 min | 218 | 140,635 | 25,960,272 | 327,797 | CLI JSON | $10.63 | APPROVE |
| 475-A-fable-high | 1 | no | 39.7 min | 1,098 | 33,131 | 3,626,149 | 116,598 | CLI JSON | $4.91 | APPROVE |
| 475-A-fable-xhigh | 1 | no | 44.7 min | 1,062 | 110,937 | 6,196,246 | 258,662 | CLI JSON | $12.28 | APPROVE |
| 475-B-opus-high | 1 | no | 45.6 min | 180 | 59,519 | 13,492,150 | 203,266 | CLI JSON | $5.52 | no |
| 475-B-opus-xhigh | 1 | no | 55.1 min | 282 | 134,211 | 29,783,443 | 308,096 | CLI JSON | $11.11 | no |
| 475-B-opus-max | 0 | yes | 75.2 min | 336 | 239,795 | 51,641,366 | 494,773 | transcripts | $19.08 | failure (timeout) |
| 475-B-fable-high | 1 | no | 26.8 min | 1,318 | 39,310 | 4,823,418 | 155,021 | CLI JSON | $6.28 | no |
| 475-B-fable-xhigh | 1 | no | 38.9 min | 1,136 | 71,974 | 6,413,116 | 215,043 | CLI JSON | $9.51 | no |
| 659-A-opus-high | 3 | no | 65.7 min | 216 | 68,003 | 17,583,914 | 232,998 | CLI JSON | $6.74 | no |
| 659-A-opus-xhigh | 1 | no | 57.8 min | 254 | 102,826 | 27,988,658 | 331,299 | CLI JSON | $10.31 | no |
| 659-A-opus-max | 1 | no | 70.9 min | 314 | 204,819 | 48,996,407 | 469,127 | CLI JSON | $17.65 | no |
| 659-A-fable-high | 1 | no | 72.0 min | 2,198 | 79,577 | 12,753,630 | 240,705 | CLI JSON | $12.00 | no |
| 659-A-fable-xhigh | 1 | no | 62.7 min | 2,068 | 118,081 | 16,679,099 | 337,047 | CLI JSON | $16.84 | no |
| 659-B-opus-high | 1 | no | 74.9 min | 260 | 62,747 | 20,422,579 | 221,330 | CLI JSON | $7.11 | no |
| 659-B-opus-xhigh | 1 | no | 66.0 min | 258 | 99,282 | 23,438,340 | 279,623 | CLI JSON | $8.91 | no |
| 659-B-opus-max | 0 | yes | 75.2 min | 372 | 196,928 | 50,944,626 | 424,481 | transcripts | $17.52 | failure (timeout) |
| 659-B-fable-high | 1 | no | 62.3 min | 2,398 | 110,919 | 16,076,693 | 274,268 | CLI JSON | $15.07 | no |
| 659-B-fable-xhigh | 1 | no | 51.0 min | 1,996 | 129,373 | 14,610,573 | 322,546 | CLI JSON | $16.59 | no |

**Totals for the 20 counted runs:**

- Worker usage: $216.60 API-equivalent.
- The five 475-A reviews: 9,216,085 Codex tokens, $2.33 API-equivalent.

**Not counted:** the two runs discarded under the first guard, the
coordinator's own turns, and the live rounds these cases were taken from.

</details>

## What the pilot suggests

None of these is a ranking. Each is one judged run per arm, on a case every
arm passed.

- **The cheapest arm cleared the case.** All five arms got a blocker-free
  delta review on 475-A. The lowest API-equivalent cost was `opus-high`, at
  $3.69 for worker and review together.
- **Dollars and tokens do not rank the arms the same way.** `fable-high`
  used half of `opus-high`'s worker tokens (3.8M against 7.6M) but cost more
  ($4.91 against $3.19). Fable's input, output and cache-write rates are 2.5
  times Opus's, while its cache-read rate is 1.25 times.
  This is why the plan reports tokens and API-equivalent dollars side by
  side.
- **On Opus, each step up in effort spent more on this case.** Output went
  from 31K to 66K to 141K tokens, and cost from $3.19 to $5.34 to $10.63. All
  three arms got the same verdict.
- **The replay judge disagreed with the live round's reviewer.** The live
  Fable-high round from the same commit and findings drew a blocker. The
  replayed `fable-high` arm drew an APPROVE. The live worker's handoff
  context, the different output, or reviewer variance could each explain
  that. One sample cannot say which. It is the reason the plan's metrics
  need k >= 3.

## What it cannot show

- **Any ranking of the arms.** There is one judged case and one run per
  arm, and every arm passed it. The case does not separate the arms, and a
  success rate is not defined at k = 1.
- **Whether escalating model or effort pays off on harder rounds.** That
  needs the other three cases judged. They carry one to three findings
  each, and `opus-max` timed out on two of them.
- **That a timeout means an arm cannot fix the case.** The 75-minute cap is
  the harness's. A timeout says that arm was slower than the cap on that
  case, and nothing about what it would have produced given longer.
- **Whether a fix would land.** The judge ran a delta review. On PR #475,
  the fleet's own escalation log records each delta approval being
  followed by a full review that found a new blocker. A delta APPROVE is
  therefore a weak signal that a fix will land.
- **Whether the outcome is the arm's or the judge's.** There was one judge,
  with no baseline rescored and no second reviewer.
- **Wall time as the arm's own speed.** Wall time includes waiting for the
  shared suite lock and running beside another arm. On 475-A, `opus-high`
  also overlapped, for about ten minutes, a stray session from one of the
  discarded runs. That session only read the clone, but it may have held
  the lock.
- **Anything about resumed context.** Every arm started fresh.

## Next

- Judge the 15 stored runs once the Codex quota allows. The fleet's report
  estimates about 15% of a Codex week.
- Then port the harness to `evals/caliper/q4/` with its graders verified
  first, as plan section 5 step 7 describes.
