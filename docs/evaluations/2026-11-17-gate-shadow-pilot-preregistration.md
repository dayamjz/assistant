# Gate shadow pilot pre-registration, 2026-11-17

**Status: Draft, not frozen.** Merging this document is not a freeze point. The
only freeze point is a later complete protocol that Joel and the voters (#juno,
#A1, #claude) sign off on before any head is enrolled and before any pilot head
runs. No earlier version, this one included, counts as one. No pilot head has
run.

This is step 2 of row 12 of the agreed order (ORDER r2), the a#806 shadow pilot.
Row 12 (`ft:docs/critique/03-juno-response.md:52`) reads: "At least 10
representative heads as a pilot floor, not proof, covering clean, real-finding,
hold, CI-failure, race and recovery cases and live PRs. Per head: gate
activation, matched, missed and extra findings, stage times and cost. Every
disagreement gets a ruling (gate right, review right, both, neither) under rules
frozen before the run. Promotion criteria are written before the run." Step 1,
a#827, landed as a#952 (`865db281`). Step 3, the run, has its own brief and
starts only after that complete protocol is signed off.

Notation: `a#N` is `dayamjz/assistant`, `ft#N` is `dayamjz/fleet-tools`. A path
with no repository named is in this repository at this draft's head. A path
prefixed `ft:` is in the private `dayamjz/fleet-tools` repository at `5b1b73d2`.

## Terms

- **Head:** one commit, named by its 40-hex sha. **Gate run:** the run the gate
  records for a head, set up as [the shadow runbook](../dogfood.md) describes.
  **The review:** section 1's full verdict at the same head.
- **Item:** one finding or one verdict-level difference. **Disagreement:** an item
  the two sides do not share (section 2.2).
- **Voters:** #juno, #A1 and #claude. **Collator:** whoever prepares a head's
  blinded packet. **Operator:** whoever drives the gate run. **Manifest:** the
  enrollment list of section 3.
- A **hold** waits on a person (`docs/prd.md:120`). A **park** is a bound
  stopping a run.

## 1. What is compared

The gate in shadow against the review, on the same head.

- **The gate side.** All nine stages run for real against the mirror, including
  push, pull request and checks (`docs/dogfood.md:18-23`). Nothing reaches this
  repository.
- **The review side.** For a head, the review is the **full verdict at that
  head** under the landing rules: the line of `ft:reviews/<N>.verdicts` whose kind
  is `full` and whose short sha is the head's, with `ft:reviews/<N>.md` the
  review it records, whose line 1 names the full 40-hex head. That is the record
  `ft:landing-check.sh --assistant` reads (`ft:landing-check.sh:13-16`), and its
  verdict is what
  `ft:review-findings.py --verdict` prints. A delta verdict is not the review.
  When a head has several full verdicts, the last line in `.verdicts` is the
  review.
- **A head with no full verdict.** It is not compared and does not count toward
  the ten. Its gate run is still recorded, with `review.verdict` set to `none`,
  so its activation, stage times and usage stay in the pilot's totals. A planted
  head has no review until one is commissioned (open question 6).
- **Bases.** The review reads the PR's change against its merge base with
  `origin/main`. The gate reads the change against the mirror's `main` after its
  rebase stage (`docs/dogfood.md:90-98`). Both bases are recorded for every
  head, and a head whose bases differ is flagged `base_mismatch` (open
  question 4).
- **Checks.** The review runs no CI. For a CI-failure head the gate's checks
  stage is compared with the head's own CI conclusion on this repository, not
  with the review (section 3).

## 2. Adjudication rules (proposed)

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
   the run's end, the usage view, and copies of `ft:reviews/<N>.md` and
   `ft:reviews/<N>.verdicts` once the full verdict exists.
2. **Freeze.** The files for a head are committed together, with the sha256 of
   each in the head's record. That commit is the head's `frozen_commit`. Nobody
   proposes a match or reads both sides together before it exists.
3. **Packet.** The collator renders every finding from both sides into one
   neutral form: location and description only. Severity labels, actions,
   reviewer names and the side are removed. Each disagreement shows its two
   positions as A and B, in an order recorded in the record.
4. **Ruling.** Each voter rules from the packet, the diff at the head and the
   repository at the head, and records the ruling before reading another
   voter's ruling on that head.
5. **Unblinding.** The side behind A and B is revealed only after all three
   rulings for a head are recorded.

Residual gaps: style can reveal a side, since the gate's agent and the reviewers
write differently; and step 4's order is a rule the voters keep, not something a
mechanism enforces.

### 2.4 Severity

The adjudicator sets severity, not the reviewer's label (ORDER r2 item 5,
`ft:docs/critique/03-juno-response.md:39`; item 7, `:44`).
Each voter assigns one of `blocker`, `should-fix`, `nit` or `not-real` to every
item, using the review's scale (`ft:review-findings.py:10-11`). The gate's
`error`, `warning` and `info` and the review's labels are kept in the raw files
and are not shown in the packet.

### 2.5 Who rules

The voters #juno, #A1 and #claude rule every disagreement, each ruling recorded
with its voter and time (Joel, ft#545 6954624923, answer 4). This replaces the
"owner-recorded" ruling of `ft:docs/critique/03-juno-response.md:167` for row 12.
Joel signs the promotion criteria and decides row 13.

The sources do not say how a split vote is decided, or what happens when a
voter wrote the review under comparison. Both are open questions 1 and 2.

## 3. Case mix, at least 10 heads

Each head counts toward one case, assigned at enrollment. Enrollment is recorded
in the manifest, with its time, before the gate run starts and before the
review's verdict exists, so no head is picked by its result.

| Case | Minimum | A head qualifies when, before either result is known |
| --- | --- | --- |
| Clean | 1 | A planted commit that is behavior-preserving by construction, with its manifest entry saying `no defect`. |
| Real finding | 1 | A planted commit carrying one known defect, with its manifest entry naming the defect, its location and its family, or a replayed head that a later revert or `Regresses: #N` PR names. |
| Hold | 1 | A planted commit whose finding touches intent or judgment, which PRD section 5 makes an `ask` (`docs/prd.md:422-426`), with that written in its manifest entry. |
| CI failure | 1 | A planted commit that fails a named check of `ci.yml`, written in its manifest entry. Needs the mirror's Actions on (open question 3). |
| Race | 1 | The sources do not settle it. Under reading (a) of open question 5, a live or planted head the operator pushes a second time while its run is in flight, or whose mirror `main` is moved while it waits on checks, declared in the manifest before the push. Under (b), a head whose diff changes goroutines, locks or channels, read from the diff at enrollment. |
| Recovery | 1 | The sources do not settle it. Under reading (a), a head whose run the manifest marks for `assistant service restart` at a named stage, before the push. Under (b), a live head that is the first fix-round head after a `CHANGES REQUESTED` verdict. |
| Live PRs | 4 | A head named in a `REVIEW-REQUEST full <sha>` relay during the pilot window, enrolled in the order of those relays at the time of the relay. |

**Marking planted and replayed heads apart from live PRs.** The manifest records
each head's `provenance`: `live`, `replayed` or `planted`. Every head is pushed to
the gate as `shadow/h<NN>`, where `NN` is its enrollment number, rather than
`shadow/pr-<number>` (`docs/dogfood.md:275`). Nothing in the commit, its message
or the branch name says how a head was made, so neither the gate's agent nor a
reviewer can read it there.

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
| `planted_truth` | text | manifest: the defect, location and expected action, or `no defect` | planted |
| `review_base` | 40-hex | `git merge-base origin/main <head>` | yes |
| `gate.run` | string | `assistant --json runs`: `record.id` (`internal/store/run.go:54`) whose `record.submitted_head` (`:61`) is the head | yes |
| `gate.activated` | bool | true exactly when `gate.run` exists | yes |
| `gate.base` | 40-hex | `record.base` (`internal/store/run.go:63`) | yes |
| `gate.reviewed_revision` | 40-hex | `stages[review].report.revision` (`internal/findings/report.go:41`) | when review ran |
| `gate.outcome`, `gate.reason` | string | `assistant --json runs <run>`: `outcome`, `reason` (`internal/machine/wire.go:215`, `:282`) | yes |
| `gate.park` | object or null | `record.park` (`internal/store/run.go:143`) | yes |
| `gate.build`, `gate.config_digest` | object, string | `record.build`, `record.config_digest` (`internal/store/run.go:111`, `:115`) | yes |
| `gate.stages[]` | list of 9 | `stages[]`: `stage`, `outcome`, `ran`, `skip_reason` (`internal/machine/wire.go:184-207`) | yes |
| `gate.stages[].times` | list of {started, ended} | `stages[].executions` (`internal/machine/wire.go:202`, `internal/store/stageexecution.go:27-30`); empty for a stage whose body never ran | yes |
| `gate.holds[]` | list of {stage, finding ids, captured_at} | `decision` in the capture taken at each hold (`internal/machine/wire.go:289`) | yes |
| `gate.answers[]` | list of {stage, answer, time} | the operator's own log; no product read is established for it here | yes |
| `gate.findings[]` | list of {id, severity, action, path, line, description} | `stages[].report.findings` (`internal/findings/report.go:57`, `internal/findings/finding.go:215-291`) | yes |
| `gate.usage[]` | per stage {calls, failures, duration_ns, input, output, cache_read, cache_creation} | `assistant --json runs --usage=<window>`, with a window reaching back past the run's start: the `by_run` rows whose `run` is `<run>` (`internal/machine/usage.go:63`, `:84-125`, `:186-195`); each token count is {total, reported, unreported} | yes |
| `gate.usage_scope` | {covers, excludes} | the same view's `covers`, `excludes` (`internal/machine/usage.go:76-77`) | yes |
| `gate.dollars` | null | **gap**: nothing in this build records a price (`price` is in `UsageExcludes`) | no |
| `gate.checks_basis` | `live-checks` or `no-ci-declaration` | the home's `config.yaml` `no_ci` line and `gh api repos/<mirror>/actions/permissions` (`docs/dogfood.md:152-166`) | yes |
| `origin_ci` | list of {name, conclusion} | `gh api repos/dayamjz/assistant/commits/<head>/check-runs` | CI failure, live |
| `review.head`, `review.kind`, `review.reviewer` | 40-hex, `full`, string | line 1 of `ft:reviews/<N>.md`, `# <Reviewer> review of PR #N at <40-hex> (full...)` (`ft:landing-check.sh:13-16`) | when a review exists |
| `review.verdict` | `APPROVE`, `CHANGES REQUESTED` or `none` | `ft:review-findings.py --verdict ft:reviews/<N>.md`, checked against the `.verdicts` line | yes |
| `review.findings[]` | list of {severity, location, title} | `ft:review-findings.py ft:reviews/<N>.md` (`ft:review-findings.py:10-18`) | when a review exists |
| `review.block_present` | bool | false when that command prints its `NO_BLOCK` line (`ft:review-findings.py:261`) | when a review exists |
| `review.record_commit` | 40-hex | the fleet-tools commit the review files were copied from | when a review exists |
| `frozen_commit`, `raw[]` | 40-hex, list of {file, sha256} | the freeze commit (section 2.3) | yes |
| `items[]` | list of {item, kind, gate_id, review_index, order} | collator; `kind` is `matched`, `missed`, `extra`, `hold-not-made` or `pass-refused` | yes |
| `planted_match` | gate finding id or null | collator, split by any voter as a match is (section 2.2) | planted |
| `outcome_rulings[]` | list of {voter, ruled_at, correct, reason} | each voter: whether `gate.outcome` and `gate.reason` are right under PRD sections 5 and 11 | race, recovery |
| `rulings[]` | list of {item, voter, ruled_at, ruling, severity, reason, evidence, posted} | each voter, one row per item; `posted` is the comment URL or file the ruling was recorded in | per disagreement |
| `decided[]` | list of {item, ruling, severity, how} | from `rulings[]`, by the split-vote rule (open question 1) | per disagreement |

**Example record.** Illustrative scenario: every value is invented to show the
shape, and no token count or time here was measured. Token counts are shown
unreported, which is not the same as zero.

```json
{"head_id": "h07", "head": "<40-hex>", "case": "live", "provenance": "live",
 "enrolled_at": "2026-11-20T17:02:11Z", "pr": 960, "planted_truth": null, "review_base": "<40-hex>",
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
  "how": "<split-vote rule>"}]}
```

## 5. Promotion criteria (proposed, not signed)

**Proposed, not signed.** Joel signs these or changes them. Each is computed from
section 4's fields over the compared heads, and the pilot passes only if all of
them pass.

| # | Criterion | Pass when |
| --- | --- | --- |
| C1 | Sample | At least 10 compared heads, with every case in section 3 at its minimum. |
| C2 | Activation | `gate.activated` is true for every enrolled head. |
| C3 | No missed real blocker | No `missed` or `pass-refused` item is decided `review right` with severity `blocker`. |
| C4 | Planted defects found | Every planted real-finding and hold head has a non-null `planted_match`. |
| C5 | Few false holds | At most 1 of the compared heads has a `hold-not-made` item decided `review right` with severity `not-real`, and no clean head has one. |
| C6 | CI failure seen | Every CI-failure head's `gate.checks_basis` is `live-checks` and its checks stage did not pass. |
| C7 | Race and recovery | Every race and recovery head's `outcome_rulings[]` decide `correct`, by the split-vote rule. |
| C8 | Usage reported | Every compared head has `gate.usage` and `gate.stages[].times` for each stage that ran. Totals are reported as token counts with their unreported counts, and no threshold is set on them. |

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
- **a#807.** `eject --confirm` is refused for good once a task record exists
  (`docs/dogfood.md:405-419`). The pilot creates no task: runs start by push or
  `--intent`.
- **a#808.** A held run is answered only from the registered working copy on
  the run's branch (`docs/dogfood.md:363-368`). The operator answers from there.
- **a#809.** No hold names `--cancel=<run>`. The operator ends a run that way and
  records it in `gate.answers`.
- **The runbook predates a#952.** `docs/dogfood.md:115-150` still says to keep the
  mirror's Actions off "until #827 is fixed", and a#952 closed a#827. With Actions
  off the checks stage settles on a declaration, and the CI-failure case cannot
  be measured (open question 3).
- **No dollars.** The gate records token counts and no price: `price` and
  `worker-turns` are in `UsageExcludes` (`internal/machine/usage.go:76-77`).
  Usage is reported as recorded token usage only.
- **No review-side cost.** No source for the review's own tokens or time was
  verified for this document, so the schema has none.
- **First-round findings.** At the default `fix_rounds.review` of 1
  (`docs/prd.md:3432`), a `fix` finding gets an automatic round, and a later read
  may return the re-review's report. Nothing found for this document keeps the
  first report but the best-effort stage log (open question 7).
- **No intent on a pushed run,** so the scope lens does not run
  (`docs/dogfood.md:291-299`, open question 8).
- **Prose reviews.** A review with no findings block is read from its prose
  (`review.block_present` false), which is less reliable than the block.
- **Activation is by hand.** The operator pushes each head, so C2 measures the
  operator as much as the gate.

## 7. Open questions for Joel and the voters

This draft answers none of these. The complete protocol must answer every
question here before the freeze. Row 12's rulings are the voters' (Joel, ft#545
6954624923, answer 4), and anything that feeds section 5's criteria is Joel's.
Where #A1's full review at `5d38835f` (a#955 comment 6965786742) set a
requirement, it is quoted as the reviewer's requirement, not as an answer.

1. **Split vote.** When the three voters do not agree, how is a disagreement
   decided? (a) Two of three decide, and a three-way split goes to Joel.
   (b) Only a unanimous ruling decides, and anything else goes to Joel.
   (c) Two of three decide, and a three-way split is recorded as undecided and
   counted against the gate in section 5.
2. **A voter who wrote the review.** When the review under comparison is a
   voter's own, does that voter (a) rule as usual, (b) recuse, leaving two
   voters for that head, or (c) recuse and be replaced by Joel for that head?
   Under (b), how do the two decide when they differ: (i) it goes to Joel,
   (ii) it is recorded as undecided and counted against the gate, or (iii)
   something else the voters name? Reviewer's requirement: "define how a
   two-person panel decides after recusal".
3. **The mirror's Actions.** Should the mirror (a) keep Actions off, as
   `docs/dogfood.md` says, which leaves the CI-failure case unmeasured, or
   (b) turn Actions on now that a#952 landed, at the private repository's
   billed minutes? Whether a#952's first-run path passes on the mirror is not
   verified here. Reviewer's requirement: "preserve the required CI-failure
   case instead of dropping it to make C6 pass".
4. **Bases.** Should the operator (a) set the mirror's `main` to each head's
   merge base before pushing it, which moves the mirror's `main` backwards, or
   (b) keep the mirror current as the runbook says, and flag `base_mismatch` on
   the heads it affects? Does a flagged head count toward the ten and toward
   section 5's criteria, and if so how? Reviewer's requirement: "ensure
   base-mismatched heads do not silently count as comparable".
5. **Race and recovery.** Does "race" mean (a) a gate-side event, such as a
   second push to the branch while its run is in flight, or `main` moving while
   the run waits on checks (PRD section 5, "A conflict after the push"), or
   (b) a head whose diff changes concurrent code? Does "recovery" mean
   (a) a service restart while the run is mid-stage (PRD section 11), or (b) a
   head that is a fix round after a `CHANGES REQUESTED` verdict?
6. **The review of a planted head.** Should a planted head get (a) a full review
   through the usual review path, on a PR that exposes the planted commit,
   (b) a full review of the commit outside a PR, by the same reviewers, or (c)
   no review, with the gate compared with `planted_truth` alone?
7. **Fix rounds.** Should the pilot run the gate (a) at the default
   `fix_rounds.review: 1`, which is the gate as it would ship, or (b) at `0` in
   the home's `config.yaml`, which sends every review finding to a hold and
   keeps the first report for comparison?
8. **Intent.** Should each head's run be started (a) by push, with no intent, or
   (b) with `--intent` set to the PR's goal sentence?
9. **Thresholds.** Are C3's zero and C5's one-head allowance the bars Joel wants,
   and should C8 carry a token threshold, or stay report-only?
10. **Where the records live.** Should each head's frozen raw files be committed
    to (a) `dayamjz/fleet-tools`, beside `reviews/`, or (b) a private repository
    of their own?
