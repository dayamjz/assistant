# Gate shadow pilot pre-registration, 2026-11-17

**Status: Draft, not frozen.** This text is the complete protocol, apart from
the items section 7.11 lists as still open. It freezes only when Joel and the
voters (#juno, #A1, #claude) each sign this exact text at a named commit, and
that commit is the freeze point. Merging it is not a freeze point. Nothing is
enrolled and no shadow run happens before then. No pilot head has run.

This is row 12 of the agreed order (ORDER r2), the a#806 shadow pilot.
Row 12 (`ft:docs/critique/03-juno-response.md:52`) reads: "At least 10
representative heads as a pilot floor, not proof, covering clean, real-finding,
hold, CI-failure, race and recovery cases and live PRs. Per head: gate
activation, matched, missed and extra findings, stage times and cost. Every
disagreement gets a ruling (gate right, review right, both, neither) under rules
frozen before the run. Promotion criteria are written before the run." Step 1,
a#827, landed as a#952 (`865db281`). Step 2, the draft, landed as a#955
(`e10e7059`). This text folds in the section 7 rulings. Step 3, the run, has its
own brief and starts only after this protocol is signed.

Notation: `a#N` is `dayamjz/assistant`, `ft#N` is `dayamjz/fleet-tools`. A path
with no repository named is in this repository at this draft's head. A path
prefixed `ft:` is in the private `dayamjz/fleet-tools` repository at `5b1b73d2`.
A bare 10-digit number is an issue comment id on ft#545
(`gh api repos/dayamjz/fleet-tools/issues/comments/<id>`). "Q1" to "Q10" are the
questions of section 7.

## Terms

- **Head:** one commit, named by its 40-hex sha. **Gate run:** the run the gate
  records for a head, set up as [the shadow runbook](../dogfood.md) describes.
  **The review:** section 1's full verdict at the same head.
- **Item:** one finding or one verdict-level difference. **Disagreement:** an item
  the two sides do not share (section 2.2).
- **Voters:** #juno, #A1 and #claude. **Collator:** whoever prepares a head's
  blinded packet. **Operator:** whoever drives the gate run. **Manifest:** the
  enrollment list of section 3.
- **Compared head:** a head with a review, that is not `base_mismatch` and not
  adjudication pending. Section 5's criteria count compared heads.
- A **hold** waits on a person (`docs/prd.md:120`). A **park** is a bound
  stopping a run.

## 1. What is compared

The gate in shadow against the review, on the same head.

- **The gate side.** All nine stages run for real against the mirror, including
  push, pull request and checks (`docs/dogfood.md:18-23`). Nothing reaches this
  repository.
- **The review side.** For a live or replayed head, the review is the **full
  verdict at that head** under the landing rules: the line of
  `ft:reviews/<N>.verdicts` whose kind is `full` and whose short sha is the
  head's, with `ft:reviews/<N>.md` the review it records, whose line 1 names the
  full 40-hex head. That is the record `ft:landing-check.sh --assistant` reads
  (`ft:landing-check.sh:13-16`), and its verdict is what
  `ft:review-findings.py --verdict` prints. A delta verdict is not the review.
  When a head has several full verdicts, the last line in `.verdicts` is the
  review.
- **A planted head's review (Q6).** A planted head gets a blinded full review of
  the exact commit, outside a PR, by a reviewer who is not a voter. The reviewer
  is not shown the planted truth. The review is kept in the pilot records
  (Q10), apart from `.verdicts`, and creates no landing eligibility.
- **A head with no full verdict.** It is not compared and does not count toward
  the ten. Its gate run is still recorded, with `review.verdict` set to `none`,
  so its activation, stage times and usage stay in the pilot's totals.
- **Bases (Q4).** The review reads the change against its base, the merge base
  of the head with `origin/main`. Before the push, the operator resets the
  mirror's `main` to that base, so the gate reads the change against the same
  commit. Section 3 says what is
  recorded and checked. A head that fails the check is `base_mismatch`.
- **Checks.** The review runs no CI. For a CI-failure head the gate's checks
  stage is compared with the head's own CI conclusion on this repository, not
  with the review (section 3).

## 2. Adjudication rules

Sections 2.1 to 2.4 are unchanged from the draft. No section 7 question ruled
on them, and signing this text adopts them as written. Section 2.5 carries Q1
and Q2.

### 2.1 The four-way ruling

Each disagreement gets exactly one ruling. "Real" means the described defect
exists at the head, shown by a citation to code at the head, a reproduction, or
a later fix that names the change (`Regresses: #N`).

| Ruling | Means | Worked example |
| --- | --- | --- |
| **Gate right** | The gate's position holds and the review's does not. | The gate raises that a refusal in `internal/cli` is reached by a path the review did not read; the voters reproduce it at the head. The review approved. |
| **Review right** | The review's position holds and the gate's does not. | The review's blocker cites a missing fail-before test, and the test is indeed absent at the head. The gate passed its review stage with no finding on it. |
| **Both** | Each side's position is defensible: the item is real, and leaving it out is not a fault at its adjudicated severity. | The gate raises a real nit in a doc comment that the review left out. The nit is real; omitting a nit is not a fault. |
| **Neither** | Neither side's position holds. | The gate holds on a finding that is not real, the review approves, and adjudication finds a different real blocker at the same lines that neither raised. |

A matched item is not a disagreement and gets no ruling. A new defect found
during adjudication is recorded as `found_in_adjudication` with its severity and
is counted against both sides, never for either.

### 2.2 What counts as a disagreement

- **Missed finding:** a review finding with no matching gate finding.
- **Extra finding:** a gate finding with no matching review finding.
- **Hold the review did not make:** the gate's review stage held on a finding
  (its decision names the review stage, `internal/machine/wire.go:178`, `:289`)
  while the review's verdict is `APPROVE`.
- **Pass the review refused:** the gate's review stage passed while the review's
  verdict is `CHANGES REQUESTED`.

Two findings match when they name the same defect at overlapping locations. The
collator proposes matches. Any voter may split a proposed match, which turns it
into one missed and one extra item, each ruled.

Holds that come from the runbook's setup are not disagreements: the test, lint
and document stages hold when no command is configured
(`docs/dogfood.md:202-206`), and the push stage holds when the review was
answered `skipped` (`docs/dogfood.md:356-361`). They are recorded with their
stage and excluded from the comparison.

### 2.3 Blinding, storage and freezing

ORDER r2 item 5 (`ft:docs/critique/03-juno-response.md:39`): "Both outputs are
stored and frozen before anyone compares them."

1. **Capture.** The operator saves each output to a file as it is produced:
   `assistant --json runs <run>` at every hold before answering it and once at
   the run's end, the usage view, and copies of the review's files once the
   full verdict exists.
2. **Freeze.** The files for a head are committed together to the pilot
   records (Q10), with the sha256 of each in the head's record. That commit is
   the head's `frozen_commit`. Nobody proposes a match or reads both sides
   together before it exists.
3. **Packet.** The collator renders every finding from both sides into one
   neutral form: location and description only. Severity labels, actions,
   reviewer names and the side are removed. Each disagreement shows its two
   positions as A and B, in an order recorded in the record.
4. **Ruling.** Each voter rules from the packet, the diff at the head and the
   repository at the head, and records the ruling before reading another
   voter's ruling on that head.
5. **Unblinding.** The side behind A and B is revealed only after all three
   rulings for a head are recorded. Planted truth and the side mapping stay
   sealed from the voters until then (Q10).

Residual gaps: style can reveal a side, since the gate's agent and the reviewers
write differently; step 4's order is a rule the voters keep, not something a
mechanism enforces; and a commit does not stop a privileged operator from
reading a sealed record (Q10).

### 2.4 Severity

The adjudicator sets severity, not the reviewer's label (ORDER r2 item 5,
`ft:docs/critique/03-juno-response.md:39`; item 7, `:44`).
Each voter assigns one of `blocker`, `should-fix`, `nit` or `not-real` to every
item, using the review's scale (`ft:review-findings.py:10-11`). The gate's
`error`, `warning` and `info` and the review's labels are kept in the raw files
and are not shown in the packet.

### 2.5 Who rules, and how a ruling is decided

The voters #juno, #A1 and #claude rule every disagreement, each ruling recorded
with its voter and time (Joel, ft#545 6954624923, answer 4). This replaces the
"owner-recorded" ruling of `ft:docs/critique/03-juno-response.md:167` for row 12.
Joel signs the promotion criteria and decides row 13.

**Split vote (Q1).** Two of three voters decide the four-way ruling. When all
three rulings differ, the item goes to Joel. Every dissent is recorded with its
evidence. Severity is ruled separately from the four-way ruling. When the
voters assign different severities, the item is held at the higher severity
until the voters reconcile or Joel rules.

**Recusal (Q2).** A voter recuses from a head's gate-versus-review
disagreements when either holds:

- the voter wrote the compared review; or
- the voter's model family ran the gate's review or fix stage on that head.

Recusals are pre-registered per head, from the stage model the gate run
actually used, before any result is read. They are recorded in the head's
`recusals[]`. Joel takes a recused voter's seat. If independence still cannot
be met, the head is adjudication pending and is not a compared head. No panel
of two breaks a tie.

With the gate's review and fix stages pinned to Claude (section 7, the pin),
#claude recuses from gate-versus-review disagreements on every head whose
review or fix stage ran on Claude, and Joel takes that seat.

## 3. Case mix, at least 10 heads

Each head counts toward one case, assigned at enrollment. Enrollment is recorded
in the manifest, with its time, before the gate run starts and before the
review's verdict exists, so no head is picked by its result.

| Case | Minimum | A head qualifies when, before either result is known |
| --- | --- | --- |
| Clean | 1 | A planted commit that is behavior-preserving by construction, with its manifest entry saying `no defect`. |
| Real finding | 1 | A planted commit carrying one known defect, with its manifest entry naming the defect, its location and its family, or a replayed head that a later revert or `Regresses: #N` PR names. |
| Hold | 1 | A planted commit whose finding touches intent or judgment, which PRD section 5 makes an `ask` (`docs/prd.md:422-426`), with that written in its manifest entry. |
| CI failure | 1 | A planted commit that fails a named check of `ci.yml`, written in its manifest entry. Required. It is measured only once the mirror's Actions are on (Q3); until then it is required and unmeasured, and the report says so. |
| Race | 1 | A live or planted head the operator pushes a second time while its run is in flight, or whose mirror `main` is moved while it waits on checks (Q5). The injection is declared in the manifest before the push. |
| Recovery | 1 | A head during whose run the operator restarts the service with `assistant service restart`, at a predeclared stage (Q5). The injection is declared in the manifest before the push. |
| Live PRs | 4 | A head named in a `REVIEW-REQUEST full <sha>` relay during the pilot window, enrolled in the order of those relays at the time of the relay. |

**Marking planted and replayed heads apart from live PRs.** The manifest records
each head's `provenance`: `live`, `replayed` or `planted`. Every head is pushed to
the gate as `shadow/h<NN>`, where `NN` is its enrollment number, rather than
`shadow/pr-<number>` (`docs/dogfood.md:275`). Nothing in the commit, its message
or the branch name says how a head was made, so neither the gate's agent nor a
reviewer can read it there.

**What the manifest records before each push.** These entries are written
before the head is pushed, so every check below rests on facts recorded
beforehand:

- **Base (Q4).** The base SHA, the SHA the mirror's `main` is reset to, and
  `git rev-parse main` on the mirror taken right after the reset. The operator
  then checks that `git merge-base` of the mirror's `main` and the head returns
  the base, and that the head's patch-id against the base matches the reviewed
  diff. If either check fails, the head is `base_mismatch`: it is left out of
  C1, C3, C5 and C7, and its activation and usage are still reported.
- **Injection (Q5).** For a race or recovery head: the injection point, its
  time, and the expected outcome and invariant.
- **Intent (Q8).** Every head's run starts with `--intent` set to one
  predeclared goal sentence, identical across cases (`docs/dogfood.md:301-316`).
  The manifest holds the sentence's sha256.
- **Configuration (Q7).** The `fix_rounds.review` the run uses and which run
  set the head belongs to (below).

**Run sets (Q7).** The primary comparison runs at `fix_rounds.review: 0` in the
home's `config.yaml`, so every review finding goes to a hold and the first
report survives for adjudication. That is not the configuration that ships
(`docs/prd.md:3432` defaults it to 1), and the report says so. One extra run at
the default of 1 is approved. It is reported apart from the primary comparison
and never pooled with it.

**The mirror's Actions (Q3).** Actions with billed minutes go on only on the
isolated private mirror, and only after a#952's first-run path is proven there,
as part of step 3. Until then the mirror's Actions stay off.

## 4. Per-head record schema

One JSON record per head. "Required" is `yes`, `no`, or the cases it applies to.
`<run>` is the gate run's id. Sources are at this draft's head.

| Field | Type | Source | Required |
| --- | --- | --- | --- |
| `head_id` | string `hNN` | manifest | yes |
| `head` | 40-hex | manifest; `git rev-parse` of the PR head or planted commit | yes |
| `case`, `provenance` | enum | manifest (section 3) | yes |
| `enrolled_at` | RFC 3339 time | manifest, written at enrollment | yes |
| `pr` | integer or null | manifest; `gh pr view` | live, replayed |
| `planted_truth` | text | manifest: the defect, location and expected action, or `no defect`; sealed from the voters until unblinding (section 2.3) | planted |
| `review_base` | 40-hex | `git merge-base origin/main <head>` | yes |
| `mirror_reset`, `mirror_main_after_reset` | 40-hex, 40-hex | manifest, before the push: the SHA the mirror's `main` is reset to, and the mirror's `git rev-parse main` right after (section 3) | yes |
| `base_check` | {merge_base, patch_id_head, patch_id_reviewed, base_mismatch} | the operator's checks before the push (section 3) | yes |
| `injection` | {kind, point, time, expected} | manifest, before the push (section 3) | race, recovery |
| `intent_sha256` | 64-hex | manifest: the sha256 of the goal sentence (section 3) | yes |
| `run_set`, `fix_rounds_review` | `primary` or `default-1`, integer | manifest (section 3) | yes |
| `gate.run` | string | `assistant --json runs`: `record.id` (`internal/store/run.go:54`) whose `record.submitted_head` (`:61`) is the head | yes |
| `gate.activated` | bool | true exactly when `gate.run` exists | yes |
| `gate.base` | 40-hex | `record.base` (`internal/store/run.go:63`) | yes |
| `gate.reviewed_revision` | 40-hex | `stages[review].report.revision` (`internal/findings/report.go:41`) | when review ran |
| `gate.outcome`, `gate.reason` | string | `assistant --json runs <run>`: `outcome`, `reason` (`internal/machine/wire.go:215`, `:282`) | yes |
| `gate.park` | object or null | `record.park` (`internal/store/run.go:143`) | yes |
| `gate.build`, `gate.config_digest` | object, string | `record.build`, `record.config_digest` (`internal/store/run.go:111`, `:115`) | yes |
| `gate.stages[]` | list of 9 | `stages[]`: `stage`, `outcome`, `ran`, `skip_reason` (`internal/machine/wire.go:184-207`) | yes |
| `gate.stages[].times` | list of {started, ended} | `stages[].executions` (`internal/machine/wire.go:202`, `internal/store/stageexecution.go:27-30`); empty for a stage whose body never ran | yes |
| `gate.stage_models[]` | list of {stage, agent, model} | for the review and fix stages: the usage view's `by_agent` rows, which group by agent, model and role (`internal/machine/usage.go:52-57`, `:164-184`), read with a window that holds only this run's calls; `by_agent` does not name a run, so no per-run read of the model is established here | review or fix ran |
| `gate.holds[]` | list of {stage, finding ids, captured_at} | `decision` in the capture taken at each hold (`internal/machine/wire.go:289`) | yes |
| `gate.answers[]` | list of {stage, answer, time} | the operator's own log; no product read is established for it here | yes |
| `gate.findings[]` | list of {id, severity, action, path, line, description} | `stages[].report.findings` (`internal/findings/report.go:57`, `internal/findings/finding.go:215-291`) | yes |
| `gate.usage[]` | per stage {calls, failures, duration_ns, input, output, cache_read, cache_creation} | `assistant --json runs --usage=<window>`, with a window reaching back past the run's start: the `by_run` rows whose `run` is `<run>` (`internal/machine/usage.go:63`, `:84-125`, `:186-195`); each token count is {total, reported, unreported} | yes |
| `gate.usage_scope` | {covers, excludes} | the same view's `covers`, `excludes` (`internal/machine/usage.go:76-77`) | yes |
| `gate.dollars` | null | **gap**: nothing in this build records a price (`price` is in `UsageExcludes`) | no |
| `gate.checks_basis` | `live-checks` or `no-ci-declaration` | the home's `config.yaml` `no_ci` line and `gh api repos/<mirror>/actions/permissions` (`docs/dogfood.md:152-166`) | yes |
| `origin_ci` | list of {name, conclusion} | `gh api repos/dayamjz/assistant/commits/<head>/check-runs` | CI failure, live |
| `review.head`, `review.kind`, `review.reviewer` | 40-hex, `full`, string | live and replayed: line 1 of `ft:reviews/<N>.md`, `# <Reviewer> review of PR #N at <40-hex> (full...)` (`ft:landing-check.sh:13-16`); planted: the pilot review record (section 1) | when a review exists |
| `review.verdict` | `APPROVE`, `CHANGES REQUESTED` or `none` | `ft:review-findings.py --verdict` on the review file, checked against the `.verdicts` line for a live or replayed head | yes |
| `review.findings[]` | list of {severity, location, title} | `ft:review-findings.py` on the review file (`ft:review-findings.py:10-18`) | when a review exists |
| `review.block_present` | bool | false when that command prints its `NO_BLOCK` line (`ft:review-findings.py:261`) | when a review exists |
| `review.record_commit` | 40-hex | the fleet-tools commit the review files were copied from | when a review exists |
| `recusals[]` | list of {voter, reason, stage_model, seat_taken_by, registered_at} | `reason` is `wrote-review` or `model-family`; registered before any result is read (section 2.5) | when a voter recuses |
| `adjudication` | `compared`, `pending`, `base_mismatch` or `not-compared` | `pending` when independence cannot be met (section 2.5); `not-compared` when there is no full verdict (section 1) | yes |
| `frozen_commit`, `raw[]` | 40-hex, list of {file, sha256} | the freeze commit (section 2.3) | yes |
| `items[]` | list of {item, kind, gate_id, review_index, order} | collator; `kind` is `matched`, `missed`, `extra`, `hold-not-made` or `pass-refused` | yes |
| `planted_match` | gate finding id or null | collator, split by any voter as a match is (section 2.2) | planted |
| `outcome_rulings[]` | list of {voter, ruled_at, correct, reason} | each voter: whether `gate.outcome` and `gate.reason` are right under PRD sections 5 and 11, and whether the manifest's `injection.expected` held | race, recovery |
| `rulings[]` | list of {item, voter, ruled_at, ruling, severity, reason, evidence, posted} | each voter, one row per item; `posted` is the comment URL or file the ruling was recorded in | per disagreement |
| `decided[]` | list of {item, ruling, severity, how} | from `rulings[]` by section 2.5; `how` is `unanimous`, `two-of-three` or `joel`, and a severity still held at the higher value says so | per disagreement |

**Example record.** Illustrative scenario: every value is invented to show the
shape, and no token count or time here was measured. Token counts are shown
unreported, which is not the same as zero. It omits `recusals[]` and
`gate.stage_models[]`, since how a head with two recusals is ruled is still open
(section 7.11).

```json
{"head_id": "h07", "head": "<40-hex>", "case": "live", "provenance": "live",
 "enrolled_at": "2026-11-20T17:02:11Z", "pr": 960, "planted_truth": null, "review_base": "<40-hex>",
 "mirror_reset": "<40-hex>", "mirror_main_after_reset": "<40-hex>",
 "base_check": {"merge_base": "<40-hex>", "patch_id_head": "<40-hex>",
  "patch_id_reviewed": "<40-hex>", "base_mismatch": false},
 "intent_sha256": "<64-hex>", "run_set": "primary", "fix_rounds_review": 0,
 "adjudication": "compared",
 "gate": {"run": "<run>", "activated": true, "base": "<40-hex>", "reviewed_revision": "<40-hex>",
  "outcome": "checks-passed", "reason": "", "park": null, "checks_basis": "no-ci-declaration", "dollars": null,
  "stages": [{"stage": "review", "outcome": "approved", "ran": true, "skip_reason": "",
   "times": [{"started": "2026-11-20T17:04:00Z", "ended": "2026-11-20T17:11:30Z"}]}],
  "holds": [{"stage": "review", "findings": ["F1"], "captured_at": "2026-11-20T17:11:41Z"}],
  "answers": [{"stage": "review", "answer": "approved", "time": "2026-11-20T17:14:02Z"}],
  "findings": [{"id": "F1", "severity": "warning", "action": "ask",
   "path": "internal/cli/runs.go", "line": 512, "description": "..."}],
  "usage": [{"stage": "review", "calls": 1, "failures": {}, "duration_ns": 450000000000,
   "input": {"total": null, "reported": 0, "unreported": 1},
   "output": {"total": null, "reported": 0, "unreported": 1}}],
  "usage_scope": {"covers": ["stage-agent-calls", "jev-compaction-stats"],
   "excludes": ["worker-turns", "price"]}},
 "review": {"head": "<40-hex>", "kind": "full", "reviewer": "#A1", "verdict": "APPROVE",
  "findings": [], "block_present": true, "record_commit": "<40-hex>"},
 "frozen_commit": "<40-hex>", "raw": [{"file": "gate-run.json", "sha256": "<64-hex>"}],
 "items": [{"item": "I1", "kind": "extra", "gate_id": "F1", "review_index": null, "order": "A=gate"},
  {"item": "I2", "kind": "hold-not-made", "gate_id": "F1", "review_index": null, "order": "A=review"}],
 "rulings": [{"item": "I1", "voter": "#juno", "ruled_at": "2026-11-21T16:40:00Z",
  "ruling": "gate right", "severity": "should-fix", "reason": "...",
  "evidence": "internal/cli/runs.go:512 at the head", "posted": "<comment URL>"}],
 "decided": [{"item": "I1", "ruling": "gate right", "severity": "should-fix",
  "how": "two-of-three"}]}
```

## 5. Promotion criteria

**C3, C5 and C8 are Joel's ruling (Q9).** C1, C2, C4, C6 and C7 are the
draft's proposals: Joel has not ruled on them, and they stay proposed until he
signs or changes them. Each criterion is computed from section 4's fields over
the compared heads of the primary run set (section 3). The default-1 run is
reported apart and decides nothing here.

| # | Status | Criterion | Pass when |
| --- | --- | --- | --- |
| C1 | proposed | Sample | At least 10 compared heads, with every case in section 3 at its minimum. A `base_mismatch` or adjudication-pending head does not count. |
| C2 | proposed | Activation | `gate.activated` is true for every enrolled head. |
| C3 | ruled (Q9) | No missed real blocker | Zero: no `missed` or `pass-refused` item is decided `review right` with severity `blocker`. |
| C4 | proposed | Planted defects found | Every planted real-finding and hold head has a non-null `planted_match`. |
| C5 | ruled (Q9) | Few false holds | Zero false holds on clean heads, and at most one across the rest. A false hold is a head with a `hold-not-made` item decided `review right` with severity `not-real`. |
| C6 | proposed | CI failure seen | Every CI-failure head's `gate.checks_basis` is `live-checks` and its checks stage did not pass. |
| C7 | proposed | Race and recovery | Every race and recovery head's `outcome_rulings[]` decide `correct`, by section 2.5's split-vote rule. |
| C8 | ruled (Q9) | Usage reported | Report only, with no pass mark. Token counts are reported with their unreported counts. |

The pilot passes only if every criterion with a pass mark passes. C8 has none.

**If the criteria are not met,** the gate is not promoted, and the shadow failures
become the prioritized defect backlog (ORDER r2 item 13,
`ft:docs/critique/03-juno-response.md:56`): every item behind a
failed criterion is filed as its own issue on this repository with its record,
ordered by decided severity and then by how many heads it appeared on.

**Prerequisites, not measured by this pilot.** Item 13 promotes only "with a
server-protected required check and an auditable break-glass in place". Both
must exist before a promotion decision, whatever the pilot shows.

## 6. Known limits

- **A pilot floor, not proof.** Row 12's own words. At 10 heads, with one head
  per case at the minimum, no rate here generalizes.
- **The primary run is not the shipped configuration.** It runs at
  `fix_rounds.review: 0` (Q7). The default-1 run is the only evidence about the
  shipped setting, and it is one run.
- **a#807.** `eject --confirm` is refused for good once a task record exists
  (`docs/dogfood.md:405-419`). The pilot creates no task: runs start with
  `--intent` (Q8), or by push for a race head's second push.
- **a#808.** A held run is answered only from the registered working copy on
  the run's branch (`docs/dogfood.md:363-368`). The operator answers from there.
- **a#809.** No hold names `--cancel=<run>`. The operator ends a run that way and
  records it in `gate.answers`.
- **The runbook predates a#952.** `docs/dogfood.md:115-150` still says to keep the
  mirror's Actions off "until #827 is fixed", and a#952 closed a#827. With Actions
  off the checks stage settles on a declaration, and the CI-failure case cannot
  be measured until Q3's proof is done.
- **No dollars.** The gate records token counts and no price: `price` and
  `worker-turns` are in `UsageExcludes` (`internal/machine/usage.go:76-77`).
  Usage is reported as recorded token usage only.
- **No review-side cost.** No source for the review's own tokens or time was
  verified for this document, so the schema has none.
- **First-round findings in the default-1 run.** At `fix_rounds.review` of 1
  (`docs/prd.md:3432`), a `fix` finding gets an automatic round, and a later read
  may return the re-review's report. Nothing found for this document keeps the
  first report but the best-effort stage log.
- **One goal sentence for every head.** The scope lens runs (Q8), but it reads
  each change against the same sentence, not against the change's own goal.
- **The stage model is read by window.** `gate.stage_models[]` relies on a usage
  window holding only one run's calls (section 4), so heads whose runs overlap
  in time cannot be told apart there.
- **Prose reviews.** A review with no findings block is read from its prose
  (`review.block_present` false), which is less reliable than the block.
- **Activation is by hand.** The operator pushes each head, so C2 measures the
  operator as much as the gate.

## 7. Rulings on the section 7 questions

The questions are #coordinator 6968279847. Voter positions: #A1 6968578460 and
6969457247, #claude 6968805490 and 6969180917, #juno 6968999892. Owner rulings:
Joel by voice, 20:37 to 20:41 PT Nov 17, relayed verbatim by #claude
6969273611. All three voters agree on Q1 to Q8 and Q10, including the Q2 and Q4
additions, which #A1 6969457247 calls unanimous. Sections 2 to 5 state each
ruling as a rule. This section owns the quotes.

1. **Split vote. Ruled: two of three decide.** Section 2.5.
   - #A1 6968578460: "three distinct rulings escalate to Joel"; "A split in
     severity is resolved separately, conservatively at the higher severity
     until the voters reconcile or Joel rules".
   - #claude 6968805490: "Two of three decide, and three different rulings go to
     Joel. Severity is ruled separately from the four-way label and is held at
     the higher severity until it's reconciled."
   - #juno 6968999892: "Two of three decide, three different rulings go to Joel,
     and severity is ruled separately and held at the higher severity until
     reconciled."
2. **A voter who wrote the review. Ruled: recuse, with the model-family
   addition, and Joel takes the seat.** Section 2.5.
   - #A1 6969457247: "A voter whose model family actually ran the gate's review
     or fix stage on that head must recuse from gate-versus-review
     adjudication, just as the author of the compared review recuses."; "Joel's
     recorded acceptance to replace a recused voter fills the seat; if
     independence still cannot be achieved, mark pending and exclude from the
     compared denominator."
   - #juno 6968999892: "Any voter whose model family runs the gate's review or
     fix stage on a head recuses from gate-vs-review disagreements on that head.
     The same holds for a voter who wrote the review."
   - #claude 6968805490: "The same rule should apply to any voter whose model
     family runs the gate stage."; "A two-person tie-break should never happen
     silently."
   - Joel 6969273611, asked "whether he steps in as the replacement voter when a
     voter recuses from a head": "Yes." The relay adds: "#claude recuses from
     gate-vs-review disagreements on heads whose review or fix stage ran on
     Claude, and Joel takes that seat."
3. **The mirror's Actions. Ruled: on, on the isolated private mirror only,
   after a#952's first-run path is proven there.** Section 3.
   - Joel 6969273611, asked "whether to turn Actions on, with billed minutes, on
     the isolated private mirror only, after a#952's first-run path is proven
     there": "Yes." The relay adds: "They go on only on the isolated private
     mirror, after the first-run check is proven there, as part of step 3."
   - #juno 6968999892: "Until Joel rules, the CI-failure case stays required and
     unmeasured, and the report must say so."
   - #A1 6969457247: "Keep Q3 Actions disabled until isolated-mirror first-run
     validation and authorized setup."
4. **Bases. Ruled: reset the mirror's `main` to the base, record it before the
   push, and check it.** Sections 1 and 3.
   - #claude 6968805490: "Before the run, the mirror's main is reset to exactly
     that SHA, and the run checks that `git merge-base` gives it back. The run
     also checks that the head's patch-id against that base matches the
     reviewed diff. If either check fails, the head is `base_mismatch` and is
     left out of C1/C3/C5/C7."
   - #juno 6968999892: "record the mirror reset SHA and the post-reset `git
     rev-parse main` in the manifest before the push."
   - #A1 6969457247: "A mismatch is `base_mismatch`, excluded from C1/C3/C5/C7
     while usage and activation remain reported. This is also unanimous."
5. **Race and recovery. Ruled: (a)/(a).** Section 3.
   - #A1 6968578460: "operational second-push or moving-base race, and service
     restart at a predeclared stage".
   - #juno 6968999892: "The injection point, time and expected invariant go in
     the manifest before the push."
   - #claude 6968805490: "with the injection point and the expected invariant
     written into the manifest before the push".
6. **The review of a planted head. Ruled: (b), outside a PR, by a non-voter.**
   Section 1.
   - #juno 6968999892: "A blinded full review outside a PR at the exact commit,
     by a non-voter, kept apart from `.verdicts`."
   - #claude 6968805490: "The reviewer must not be one of the voters."
   - #A1 6968578460: "with the reviewer blinded to planted truth"; "do not create
     artificial landing eligibility or expose the planted defect to reviewers".
7. **Fix rounds. Ruled: 0 for the primary comparison, plus one extra default-1
   run.** Section 3.
   - #juno 6968999892: "The report has to state that this config differs from
     what ships. Any default-1 run is reported separately, never pooled, and
     needs Joel's spend approval."
   - Joel 6969273611, asked "whether to add a separate run at the default one fix
     round, reported apart from the primary zero-round comparison and never
     pooled with it": "Yes." The relay adds: "Q7 approves one extra default-1
     run for the pilot. It does not approve the k=5 spend for row 11."
8. **Intent. Ruled: (b), one predeclared goal sentence.** Section 3.
   - #A1 6968578460: "held identical across cases and captured in the manifest
     before the run".
   - #juno 6968999892: "One predeclared goal sentence, hashed in the manifest."
   - #claude 6968805490: "One predeclared goal sentence, used for every case and
     recorded in the manifest."
9. **Thresholds. Ruled by Joel: #A1's recommendation as proposed.** Section 5.
   - #A1 6968578460: "recommend C3 zero missed real blockers, C5 zero false holds
     on clean heads and at most one across the rest, and C8 report-only tokens
     for this small pilot".
   - Joel 6969273611, asked about "C3 zero missed real blockers; C5 zero false
     holds on clean heads and at most one across the rest; C8 report-only tokens
     with no pass mark": "Take those as they are."
10. **Where the records live. Ruled: (a), a private pilot directory in
    fleet-tools; (b) only if access cannot be separated there.** Section 2.3.
    - #A1 6968578460: "under a dedicated immutable-by-convention pilot directory
      with raw capture hashes, frozen commit and access boundaries"; "No claim
      that a Git commit alone prevents a privileged operator from peeking."
    - #juno 6968999892: "Planted truth and side mapping stay sealed from voters
      until rulings."
    - Joel 6969273611, asked "whether a new private repo may be created if
      access to the pilot records can't be kept separate inside fleet-tools":
      "Yes." The relay adds: "Q10(b) allows a new repo only if separation inside
      fleet-tools fails. The default is still (a)."

**The gate-stage model pin. Ruled by Joel: "Yes, for now."** The gate's review
and fix stages run on Claude. It is temporary.

- Joel 6969273611, asked "whether to pin the gate's review and fix stages to
  Claude in the eval subject config, so the fix stage can't fall back to
  Cursor": "Yes, for now." The relay adds: "It is temporary and he can revisit
  it." and "The pin goes in only through a reviewed PR to the eval subject
  config. No roles.py change, and no Codex workaround."
- #coordinator 6969298359: "It goes in through the row 11 link that pins gate
  roles in `build-subject.sh`, as a reviewed PR."
- #claude 6969180917: "My Q2 recusal can only be pre-registered head by head
  once that pin is fixed."

### 7.11 Still open

These are open questions with no pick. Each must be answered before the text is
signed.

1. **Two recusals on one head.** A live head reviewed by a voter, whose gate
   review or fix stage ran on Claude, loses two voters, and section 2.5 gives
   Joel one seat. Is that head adjudication pending, or is it ruled another way?
2. **When a recusal is registered.** #A1 6969457247: "register recusals before
   enrollment, based on observed stage/model identity rather than post-result
   preference". #juno 6968999892: "so recusals can be pre-registered per head,
   not decided after results are seen". The stage model a run used is observed
   only once it runs, after enrollment. Section 2.5 registers it before any
   result is read. Is that what the voters ruled?
3. **Where the pin reaches the pilot.** Joel's ruling (6969273611) names "the
   eval subject config", and #coordinator 6969298359 places it in
   `build-subject.sh`. The pilot's gate runs from the shadow home of
   `docs/dogfood.md`, and its `roles` key is of trust class `TrustCommands`
   (`internal/config/key.go:22-35`, `:546`). No comment says whether, or how,
   the pilot's home carries the pin.
4. **Which heads the default-1 run covers.** The relay 6969273611 says Q7
   approves "one extra default-1 run for the pilot". Whether it re-runs every
   enrolled head, or a named subset, is not ruled.
5. **The planted heads' reviewer and record.** No comment names the non-voter
   reviewer, the path of the pilot records directory, or the form of the
   planted review record.
6. **A three-way split on a head where Joel holds a seat.** Q1 sends it to Joel,
   who is then also one of the three.
7. **The criteria Joel has not ruled on.** C1, C2, C4, C6 and C7 (section 5),
   and the promotion prerequisites, which #A1 6968578460 asked Joel to sign:
   "Joel must sign thresholds and promotion prerequisites."
8. **C8's completeness condition.** The draft's C8 also required: "Every
   compared head has `gate.usage` and `gate.stages[].times` for each stage that
   ran." Joel's ruling (6969273611) is "report-only tokens with no pass mark".
   Whether that completeness requirement still gates the pilot is not ruled.
9. **Requirements one voter added.** The other voters did not address these:
   - Q3, #A1 6968578460: "with budget and permissions recorded".
   - Q4, #A1 6968578460: "with a fresh branch/mirror state per case and explicit
     provenance".
   - Q6, #A1 6968578460: "using the same review template, severity rubric and
     full-head identity"; "This needs a documented comparable review adapter,
     not a fabricated .verdicts line."
   - Q8, #A1 6968578460: "Verify that the intent-start path still exercises the
     same required stages."
10. **The waiver band's wording.** Section 8 quotes "the 301-400 band" (#claude
    6969273611), "301–700 delegation" (#A1 6969457247) and "301–700 waiver
    delegation" (#A1 6968578460). The voters say which wording the protocol
    carries.
11. **The four owner dispositions are not named.** #A1 6969457247 refers to
    "four owner dispositions", and #A1 6968578460 to "The four prior owner
    dispositions", without listing them, and this text does not list them
    either.

## 8. What does not change

This protocol changes none of the following. The wording is the comments'.

- #A1 6969457247: "four owner dispositions stand; no activation of proposed
  301–700 delegation, no relaxation of owner-granted waiver requirements, rule
  #68, admin bypass or gate promotion."
- #A1 6968578460: "The four prior owner dispositions remain binding, 301–700
  waiver delegation remains inactive, and no rule #68/admin-bypass/gate-promotion
  change is authorized."
- #claude 6969273611: "Nothing changes on rule #68, waivers, the 301-400 band,
  promotion or bypass."
