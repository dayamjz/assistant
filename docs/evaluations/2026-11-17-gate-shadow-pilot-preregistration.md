# Gate shadow pilot pre-registration, 2026-11-17

**Status: Draft, not frozen.** This text is the complete protocol. Every item
the draft's section 7.11 listed as open, and every item a#976's section 7.12
listed as open, has an owner ruling, and sections 7.11 and 7.12 own those
rulings. The text freezes only when Joel and the voters (#juno, #A1, #claude)
each sign this exact text at a named commit, and that commit is the freeze
point. They sign at the merge commit of the docs PR that adds section 7.12's
rulings (Joel 7044130931, 7046850989). Merging is not what freezes it; the four
signatures are. Nothing is enrolled and no shadow run happens before then. No
pilot head has run.

This is row 12 of the agreed order (ORDER r2), the a#806 shadow pilot.
Row 12 (`ft:docs/critique/03-juno-response.md:52`) reads: "At least 10
representative heads as a pilot floor, not proof, covering clean, real-finding,
hold, CI-failure, race and recovery cases and live PRs. Per head: gate
activation, matched, missed and extra findings, stage times and cost. Every
disagreement gets a ruling (gate right, review right, both, neither) under rules
frozen before the run. Promotion criteria are written before the run." Step 1,
a#827, landed as a#952 (`865db281`). Step 2, the draft, landed as a#955
(`e10e7059`). This text folds in the section 7 rulings, the owner rulings on
the items the draft left open (section 7.11), and the owner rulings on the three
items a#976's text left open (section 7.12). Step 3, the run, has its own brief
and starts only after this protocol is signed.

Notation: `a#N` is `dayamjz/assistant`, `ft#N` is `dayamjz/fleet-tools`. A path
with no repository named is in this repository at this draft's head. A path
prefixed `ft:` is in the private `dayamjz/fleet-tools` repository at `5b1b73d2`.
A bare 10-digit number is an issue comment id on ft#545
(`gh api repos/dayamjz/fleet-tools/issues/comments/<id>`). "Q1" to "Q10" are the
questions of section 7. "Item 1" to "item 11" are the items the draft's section
7.11 listed as open (a#958), ruled in section 7.11. "7.12 item 1" to "7.12 item
3" are the items a#976's section 7.12 listed as open, ruled in section 7.12. An
item of ORDER r2 is always named with "ORDER r2".

## Terms

- **Head:** one commit, named by its 40-hex sha. **Gate run:** the run the gate
  records for a head, set up as [the shadow runbook](../dogfood.md) describes.
  **The review:** section 1's full verdict at the same head.
- **Item:** one finding or one verdict-level difference. **Disagreement:** an item
  the two sides do not share (section 2.2).
- **Voters:** #juno, #A1 and #claude. **Collator:** whoever prepares a head's
  blinded packet. **Operator:** whoever drives the gate run. **Manifest:** the
  enrollment list of section 3.
- **Compared head:** a head with a review, that is not `base_mismatch`, not
  adjudication pending, and not excluded by the pin check (section 3). Section
  5's criteria count compared heads.
- **Reduced-voter head:** a head on which two voters recuse, so that the
  remaining eligible voter and Joel's seat rule it (section 2.5).
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
  review. Live PRs are reviewed by Codex per the framework (`ft:roles.json`);
  #A1, a voter, reviewing PRs was a one-off (section 7.11, the live-PR row). A
  live head a voter did review still gets that voter's recusal (section 2.5).
- **A planted head's review (Q6, item 5, item 9).** A planted head gets a
  blinded full review of the exact commit, outside a PR, by Codex, the non-voter
  reviewer. The reviewer is not shown the planted truth. The review uses the
  same review template, severity rubric and full-head identity as a live head's
  review, and its record is produced through a documented comparable review
  adapter, never a hand-written or fabricated `.verdicts` line. Before
  enrollment the adapter is tested against at least one known full-verdict
  record, and it refuses to produce a record when a field is missing. If no
  non-voter reviewer is available, the planted-head part does not run, and no
  voter stands in. The review is kept apart from `.verdicts` and creates no
  landing eligibility. Its record is one of the planted head's sealed records
  (section 2.3, 7.12 item 1).
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

Sections 2.1, 2.2 and 2.4 are unchanged from the draft. No section 7 question
ruled on them, and signing this text adopts them as written. Section 2.3 is the
draft's, plus the owner rulings on 7.12 items 1 and 3 (section 7.12). Section
2.5 carries Q1 and Q2, the owner rulings on items 1, 2 and 6 (section 7.11), and
7.12 item 3.

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
   rulings for a head are recorded. On a reduced-voter head it is revealed only
   after the remaining voter's ruling and then Joel's independent ruling are
   recorded (7.12 item 3). Planted truth and the side mapping stay sealed from
   the voters until then (Q10).

**Where a planted head's records live (7.12 item 1).** Before unblinding, a
planted head's records stay sealed in the private fleet-tools pilot directory
(Q10), and only the collator and the operator can read them. The public
protocol carries only a salted hash of each record, with the hash algorithm
named. Each record has a random nonce of at least 128 bits, so no one can guess
a label by hashing it. Public names do not reveal the label. After unblinding,
the records are copied to `docs/evaluations/` in this repository and checked
against the hashes.

The sealed records written before the freeze are these. Each hash is SHA-256
over the record file's exact bytes, and each file begins with a line
`nonce: <64 hex characters>`, a random 256-bit nonce. The ids were assigned in
random order and say nothing about a head's case. s02 and s08 are the two C4
specs (section 3).

| id | record kind | sha256 | algorithm |
| --- | --- | --- | --- |
| s01 | planted-head record | `ad93d15b7ff21b97238c299b65fee268d19785dfe730033839cb79050783d975` | SHA-256 |
| s02 | C4 spec | `113d6bdb11573425987f06387971b62b9ddc57b783ea943d7b2e1c7a3b6a38fc` | SHA-256 |
| s03 | planted-head record | `963d41995183d7557d61ac9de5079df45341e6d1ec5cacac8aecf28eefba1507` | SHA-256 |
| s04 | planted-head record | `3054623662fc4d4cf0f4dd3def488e8aed6a46cb459169794a138b61c7f5ed20` | SHA-256 |
| s05 | planted-head record | `e2204208fb421bf060909112f4dbf1de8429f066a8b56be2533d827c912190de` | SHA-256 |
| s06 | planted-head record | `143fa6e89eba814cd3a01a7a2df3ef7069d18ed0d4fa1112bc2292608918a2e0` | SHA-256 |
| s07 | planted-head record | `a737d440306cae865e9851c72527e20df4f3e50ca970dcfa7bef62aae7217589` | SHA-256 |
| s08 | C4 spec | `9b86a1d331e3a933048b16a1559b41101445c4a10e5972042e2a768045fee3a4` | SHA-256 |

A record that exists only after a run, such as a planted head's review record
or a capture (step 1), is not in this table: no such record exists at the
freeze, and each is hashed at its own time.

**Joel and the sealed records (7.12 item 3, #juno's addition).** Joel holds a
ruling seat and has admin access to fleet-tools. He does not open sealed records
before his rulings on those heads are recorded. Any earlier access is logged and
the head is marked.

Residual gaps: style can reveal a side, since the gate's agent and the reviewers
write differently; step 4's order is a rule the voters keep, not something a
mechanism enforces; a commit does not stop a privileged operator from reading a
sealed record (Q10); and Joel's rule above is a stated rule that no repository
permission enforces (#juno 7044817921), so nothing in this protocol detects an
access that nobody logs.

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

**A three-way split where Joel holds a seat (item 6).** When Joel holds a
recused voter's seat and all three rulings differ, Joel's vote decides.

**Recusal (Q2).** A voter recuses from a head's gate-versus-review
disagreements when either holds:

- the voter wrote the compared review; or
- the voter's model family ran the gate's review or fix stage on that head.

Joel takes a recused voter's seat. With the gate's review and fix stages pinned
to Claude (section 7, the pin), #claude recuses from gate-versus-review
disagreements on every head whose review or fix stage ran on Claude, and Joel
takes that seat.

**When a recusal is registered (item 2).** Reviewer-based recusals are
registered at enrollment. Stage-model recusals are registered by the collator
from the run's own record, after the run and before anyone reads a result or
opens the blinded packet. Each records its time and source in the head's
`recusals[]`. A conflict found later means exclusion and re-adjudication, never
a quiet edit.

**Two recusals on one head (item 1).** On a reduced-voter head the remaining
eligible voter rules with Joel in the second seat. Joel gives his own recorded
ruling and is never an automatic tie-break. Every disagreement on a
reduced-voter head goes to Joel with both sides' arguments set out, and he rules
after weighing them, even where his seat would otherwise decide. If the two
split, Joel's ruling controls, the head is labeled owner-decided (the item's
`decided[].how` is `joel`), and both positions stay on record. If Joel cannot
rule, the head stays sealed and adjudication pending, is not a compared head,
and is left out of the final C1 count, with that count disclosed (7.12 item 3).
Its sides are unblinded only after both rulings are recorded (section 2.3).
Reduced-voter heads are marked in their record (`reduced_voter`, section 4),
and the report gives C1 both with and without them (section 5). No disagreement
is settled by an automatic tie-break. Any other head on which independence
still cannot be met is adjudication pending (Q2).

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

**The two C4 candidates (7042280221, 7.12 item 2).** Both are enrolled as
planted real-finding heads:

- (a) a real defect that the agent's own intent calls deliberate. That excuse
  lives only in the planted head's own commit message or code, never in Q8's
  goal sentence, which is the same for every case. Its exact bytes are fixed by
  hash in the manifest, and it is never reworded after a gate result is seen.
- (b) a fake-shape-like defect, fixed the same way.

Each one's construction, expected failure mode and scoring rule are written in
the private pilot directory before the freeze, with their salted hashes in this
protocol: they are records s02 and s08 of section 2.3's table. Both are
reported separately as challenge coverage, not as live-head prevalence. Codex
reviews them as the non-voter (section 1).

**Marking planted and replayed heads apart from live PRs.** The manifest records
each head's `provenance`: `live`, `replayed` or `planted`. Every head is pushed to
the gate as `shadow/h<NN>`, where `NN` is its enrollment number, rather than
`shadow/pr-<number>` (`docs/dogfood.md:275`). Nothing in the commit, its message
or the branch name says how a head was made, so neither the gate's agent nor a
reviewer can read it there. Planted heads go to the mirror only, never to this
repository's remote (item 5).

**What the manifest records before each push.** These entries are written
before the head is pushed, so every check below rests on facts recorded
beforehand:

- **Base (Q4).** The base SHA, the SHA the mirror's `main` is reset to, and
  `git rev-parse main` on the mirror taken right after the reset. The operator
  then checks that `git merge-base` of the mirror's `main` and the head returns
  the base, and that the head's patch-id against the base matches the reviewed
  diff. If either check fails, the head is `base_mismatch`: it is left out of
  C1, C3, C5 and C7, and its activation and usage are still reported. Each case
  runs with a fresh branch/mirror state and explicit provenance (item 9).
- **Injection (Q5).** For a race or recovery head: the injection point, its
  time, and the expected outcome and invariant.
- **Intent (Q8).** Every head's run starts with `--intent` set to one
  predeclared goal sentence, identical across cases, the C4 candidates
  included (`docs/dogfood.md:301-316`, 7.12 item 2).
  The manifest holds the sentence's sha256. The intent-start path is verified
  to still exercise the same required stages (item 9).
- **The stage-model pin (item 3).** The operator writes the shadow home's
  `config.yaml` with the same roles pin as
  `evals/caliper/q1/subject-home-config.yaml`, never from repository config,
  because `roles` is of trust class `TrustCommands`. The file's sha256 goes in
  the manifest before the run.
- **Configuration (Q7).** The `fix_rounds.review` the run uses and which run
  set the head belongs to (below).

**The pin check (item 3).** Each head's actual stage model is checked against
its run record (`gate.stage_models[]`, section 4), not only against the
configuration. A pin mismatch, missing stage evidence or untrusted
configuration means the head is not a compared head, and it stays in the
exclusion count. Section 4 reads the stage model through a usage window, so a
head whose window does not hold only its own run's calls has no stage evidence
(section 6).

**Run sets (Q7, item 4).** The primary comparison runs at
`fix_rounds.review: 0` in the home's `config.yaml`, so every review finding goes
to a hold and the first report survives for adjudication. That is not the
configuration that ships (`docs/prd.md:3432` defaults it to 1), and the report
says so. One extra run at the default of 1 is approved. It is reported apart
from the primary comparison and never pooled with it. It covers every compared
head. A subset is allowed only if Joel names it in the manifest before any
outcome exists, with the denominator published up front.

**The mirror's Actions (Q3, item 9).** Actions with billed minutes go on only on
the isolated private mirror, and only after a#952's first-run path is proven
there, as part of step 3, with budget and permissions recorded. Until then the
mirror's Actions stay off.

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
| `planted_truth` | text | manifest: the defect, location and expected action, or `no defect`; sealed in the private pilot directory, readable only by the collator and the operator, until unblinding (section 2.3) | planted |
| `review_base` | 40-hex | `git merge-base origin/main <head>` | yes |
| `mirror_reset`, `mirror_main_after_reset` | 40-hex, 40-hex | manifest, before the push: the SHA the mirror's `main` is reset to, and the mirror's `git rev-parse main` right after (section 3) | yes |
| `base_check` | {merge_base, patch_id_head, patch_id_reviewed, base_mismatch} | the operator's checks before the push (section 3) | yes |
| `injection` | {kind, point, time, expected} | manifest, before the push (section 3) | race, recovery |
| `intent_sha256` | 64-hex | manifest: the sha256 of the goal sentence (section 3) | yes |
| `roles_pin_sha256` | 64-hex | manifest, before the run: the sha256 of the shadow home's `config.yaml` that carries the stage-model pin (section 3) | yes |
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
| `recusals[]` | list of {voter, reason, stage_model, seat_taken_by, registered_at, source} | `reason` is `wrote-review` (registered at enrollment) or `model-family` (registered by the collator from the run's own record, after the run and before anyone reads a result or opens the blinded packet); `source` is what the recusal was registered from (section 2.5) | when a voter recuses |
| `reduced_voter` | bool | true when two voters recuse on the head (section 2.5) | yes |
| `adjudication` | `compared`, `pending`, `base_mismatch` or `not-compared` | `pending` when independence cannot be met (section 2.5); `not-compared` when there is no full verdict (section 1) or the pin check fails (section 3) | yes |
| `frozen_commit`, `raw[]` | 40-hex, list of {file, sha256} | the freeze commit (section 2.3) | yes |
| `items[]` | list of {item, kind, gate_id, review_index, order} | collator; `kind` is `matched`, `missed`, `extra`, `hold-not-made` or `pass-refused` | yes |
| `planted_match` | gate finding id or null | collator, split by any voter as a match is (section 2.2) | planted |
| `outcome_rulings[]` | list of {voter, ruled_at, correct, reason} | each voter: whether `gate.outcome` and `gate.reason` are right under PRD sections 5 and 11, and whether the manifest's `injection.expected` held | race, recovery |
| `rulings[]` | list of {item, voter, ruled_at, ruling, severity, reason, evidence, posted} | each voter, one row per item; `posted` is the comment URL or file the ruling was recorded in | per disagreement |
| `decided[]` | list of {item, ruling, severity, how} | from `rulings[]` by section 2.5; `how` is `unanimous`, `two-of-three` or `joel` (a three-way split, or a split on a reduced-voter head), and a severity still held at the higher value says so | per disagreement |

**Example record.** Illustrative scenario: every value is invented to show the
shape, and no token count or time here was measured. Token counts are shown
unreported, which is not the same as zero. The head's review stage ran on
Claude, so #claude recuses and Joel takes that seat (section 2.5); Codex
wrote the review, so no other voter recuses and the head is not reduced-voter.

```json
{"head_id": "h07", "head": "<40-hex>", "case": "live", "provenance": "live",
 "enrolled_at": "2026-11-20T17:02:11Z", "pr": 960, "planted_truth": null, "review_base": "<40-hex>",
 "mirror_reset": "<40-hex>", "mirror_main_after_reset": "<40-hex>",
 "base_check": {"merge_base": "<40-hex>", "patch_id_head": "<40-hex>",
  "patch_id_reviewed": "<40-hex>", "base_mismatch": false},
 "intent_sha256": "<64-hex>", "roles_pin_sha256": "<64-hex>",
 "run_set": "primary", "fix_rounds_review": 0,
 "recusals": [{"voter": "#claude", "reason": "model-family", "stage_model": "claude <model>",
  "seat_taken_by": "Joel", "registered_at": "2026-11-20T17:20:00Z", "source": "gate.stage_models[]"}],
 "reduced_voter": false, "adjudication": "compared",
 "gate": {"run": "<run>", "activated": true, "base": "<40-hex>", "reviewed_revision": "<40-hex>",
  "outcome": "checks-passed", "reason": "", "park": null, "checks_basis": "no-ci-declaration", "dollars": null,
  "stages": [{"stage": "review", "outcome": "approved", "ran": true, "skip_reason": "",
   "times": [{"started": "2026-11-20T17:04:00Z", "ended": "2026-11-20T17:11:30Z"}]}],
  "stage_models": [{"stage": "review", "agent": "claude", "model": "<model>"}],
  "holds": [{"stage": "review", "findings": ["F1"], "captured_at": "2026-11-20T17:11:41Z"}],
  "answers": [{"stage": "review", "answer": "approved", "time": "2026-11-20T17:14:02Z"}],
  "findings": [{"id": "F1", "severity": "warning", "action": "ask",
   "path": "internal/cli/runs.go", "line": 512, "description": "..."}],
  "usage": [{"stage": "review", "calls": 1, "failures": {}, "duration_ns": 450000000000,
   "input": {"total": null, "reported": 0, "unreported": 1},
   "output": {"total": null, "reported": 0, "unreported": 1}}],
  "usage_scope": {"covers": ["stage-agent-calls", "jev-compaction-stats"],
   "excludes": ["worker-turns", "price"]}},
 "review": {"head": "<40-hex>", "kind": "full", "reviewer": "Codex", "verdict": "APPROVE",
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

**Every criterion is Joel's ruling.** C3, C5 and C8 are his Q9 ruling. C1, C2,
C4, C6 and C7 he ruled "Sign all as written" (item 7, 6987480649), so their
rows below are the draft's (a#958) unchanged. C8's completeness condition he
ruled "Report only, no gate" (item 8, 6987480649). Each criterion is computed
from section 4's fields over the compared heads of the primary run set (section
3). The default-1 run is reported apart and decides nothing here.

| # | Status | Criterion | Pass when |
| --- | --- | --- | --- |
| C1 | ruled (item 7) | Sample | At least 10 compared heads, with every case in section 3 at its minimum. A `base_mismatch` or adjudication-pending head does not count. |
| C2 | ruled (item 7) | Activation | `gate.activated` is true for every enrolled head. |
| C3 | ruled (Q9) | No missed real blocker | Zero: no `missed` or `pass-refused` item is decided `review right` with severity `blocker`. |
| C4 | ruled (item 7) | Planted defects found | Every planted real-finding and hold head has a non-null `planted_match`. |
| C5 | ruled (Q9) | Few false holds | Zero false holds on clean heads, and at most one across the rest. A false hold is a head with a `hold-not-made` item decided `review right` with severity `not-real`. |
| C6 | ruled (item 7) | CI failure seen | Every CI-failure head's `gate.checks_basis` is `live-checks` and its checks stage did not pass. |
| C7 | ruled (item 7) | Race and recovery | Every race and recovery head's `outcome_rulings[]` decide `correct`, by section 2.5's split-vote rule. |
| C8 | ruled (Q9, item 8) | Usage reported | Report only, with no pass mark. Token counts are reported with their unreported counts. If a head is missing gate usage or per-stage times, that is reported. It does not fail the pilot. |

The pilot passes only if every criterion with a pass mark passes. C8 has none.

**Reported beside the criteria.** C1 is reported both with and without
reduced-voter heads (item 1, section 2.5). A reduced-voter head Joel cannot
rule is left out of the final C1 count, with that count disclosed (7.12 item
3). The two C4 candidates are planted real-finding heads, and their results are
also reported separately as challenge coverage, not as live-head prevalence
(section 3).

**If the criteria are not met,** the gate is not promoted, and the shadow failures
become the prioritized defect backlog (ORDER r2 item 13,
`ft:docs/critique/03-juno-response.md:56`): every item behind a
failed criterion is filed as its own issue on this repository with its record,
ordered by decided severity and then by how many heads it appeared on.

**Prerequisites, not measured by this pilot.** ORDER r2 item 13 promotes only
"with a server-protected required check and an auditable break-glass in place".
Both must exist before a promotion decision, whatever the pilot shows. Joel
ruled them "Sign as written" (item 7, 6987480649).

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
  in time cannot be told apart there, and such a head has no stage evidence for
  the pin check (section 3).
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
- How the pilot's shadow home carries the pin is item 3 (section 7.11).

### 7.11 Owner rulings on the items the draft left open

The draft's section 7.11 (a#958) listed eleven items as open. The voters' picks
are #claude 7032538576, #A1 7033016212 and #juno 7041925922. #coordinator put
them to Joel in 7041948375, and #claude 7042006674 and #coordinator 7042059749
corrected that list: items 6, 7 and 8 were already ruled in 6987480649. Joel
ruled the rest in chat, relayed verbatim by #claude in 7042251348 and
7042280221. Where Joel's answer is "Yes" or names an item's pick, the rule is
the voters' pick as their comments state it, quoted below. Sections 1 to 5 state
each ruling as a rule; this section owns the quotes. The table's last three rows
are 7.12 items 1 to 3, ruled in 7046850989, whose quotes section 7.12 owns.

| Item | Ruling (verbatim) | Comment |
| --- | --- | --- |
| 1. Two recusals on one head | "Even if I rule disagreements should be raised to me for consideration as I might be siding with the wrong reviewer" | 7042251348 |
| 2. When a recusal is registered | "Yes" | 7042280221 |
| 3. Where the pin reaches the pilot | "Yes" | 7042251348 |
| 4. Which heads the default-1 run covers | "Yes" | 7042251348 |
| 5. The planted heads' reviewer and record | "Codex"; "Yes codex is the non voter reviewer." | 7042251348 |
| 6. A three-way split where Joel holds a seat | "My vote decides" | 6987480649 |
| 7. C1, C2, C4, C6, C7; the promotion prerequisites | "Sign all as written"; "Sign as written" | 6987480649 |
| 8. C8's completeness condition | "Report only, no gate" | 6987480649 |
| 9. #A1's four added requirements | "Adopt the 4" | 7042251348 |
| 10. The waiver band's wording | "Yes" | 7042251348 |
| 11. The four owner dispositions | "Yes" | 7042251348 |
| The two C4 candidates | "Yes" | 7042280221 |
| Who reviews live PRs | "Codex should do reviews per the framework, voters that review was just a one off case." | 7042251348 |
| 7.12 item 1. Where the planted heads' records live | "1. Yes" | 7046850989 |
| 7.12 item 2. Writing down the two C4 candidates | "2.yes" | 7046850989 |
| 7.12 item 3. When a reduced-voter head is unblinded | "3. Yes" | 7046850989 |

Owner rulings in the same comments that this protocol does not use: a1, "No
a1"; k=5 for row 11, "Decide after k=1"; a#942, "Yes, fix before k=1" (all
6987480649).

1. **Two recusals on one head. Ruled: the voters' pick, plus Joel's rule.**
   Section 2.5.
   - #claude 7042251348: "Every disagreement on a reduced-voter head goes to
     Joel with both sides' arguments set out, and he rules after weighing them.
     That holds even where his seat would otherwise decide. Nothing is settled
     by an automatic tie-break."
   - #juno 7041925922: "The remaining eligible voter rules with Joel in the
     second seat. I agree with #A1's safeguard: Joel gives his own recorded
     ruling and is never an automatic tie-break. If the two split, Joel's ruling
     controls, and the head is labeled *owner-decided* with the split kept on
     record. If Joel can't take part, the head is adjudication pending. Report
     C1 both with and without reduced-voter heads."
   - #A1 7033016212: "provided Joel actually gives an independent recorded
     ruling and is not treated as an automatic tie-break proxy"; "Mark
     reduced-voter heads distinctly and report sensitivity with/without them."
   - #claude 7032538576: "Flag every such head in the report so a reader can see
     which rulings rest on fewer voters."
2. **When a recusal is registered. Ruled: the voters' pick.** Joel answered
   "Yes" as his answer 1 in 7042280221. Section 2.5.
   - #claude 7042280221: "Reviewer-based recusals are registered at enrollment.
     Stage-model recusals are registered by the collator from the run's own
     record, after the run and before anyone reads a result or opens the blinded
     packet. Each records its time and source. A conflict found later means
     exclusion and re-adjudication, never a quiet edit."
   - #A1 7033016212: "register stage-model identity from immutable run metadata
     immediately after execution and **before unblinding**. Record timestamps,
     source and reason."
3. **Where the pin reaches the pilot. Ruled: the voters' pick.** Section 3.
   - #juno 7041925922: "The operator writes the shadow home's `config.yaml` with
     the same roles pin as `evals/caliper/q1/subject-home-config.yaml`, never
     from repository config, because `roles` is `TrustCommands`. The file's hash
     goes in the manifest before the run. Then check the stage model each head
     actually used against its run record, not only the config."; "A mismatch
     or missing evidence means the head isn't compared, and it stays in the
     exclusion count."
   - #A1 7033016212: "Pin mismatch, missing stage evidence or untrusted config
     => not a compared head; retain it in the exclusion accounting."
4. **Which heads the default-1 run covers. Ruled: the voters' pick.** Section 3.
   - #juno 7041925922: "Every compared head. A subset only if Joel names it in
     the manifest before any outcome exists, and the denominator is published
     up front."
   - #A1 7033016212: "Publish the denominator and subset-selection rule before
     outcomes are visible; no post-hoc selection."
5. **The planted heads' reviewer and record. Ruled: Codex is the non-voter
   reviewer; the rest is the voters' pick.** Section 1 and section 3.
   - #claude 7042251348: "Codex is the non-voter planted-head reviewer. The
     records directory, the record form and the sealed labels follow the voters'
     pick."
   - #juno 7041925922: "If nobody is available, this part doesn't freeze or run,
     and no voter stands in. Records go in one directory under
     `docs/evaluations/` next to the protocol, in the section 4 record form,
     produced through the item 9 Q6 adapter, never a hand-written verdict line.
     Blind labels stay sealed until adjudication. Planted heads go to the mirror
     only, never to this repository's remote."
   - That directory receives a planted head's records only after unblinding;
     before then they stay sealed in the private fleet-tools pilot directory
     (7.12 item 1). #juno 7044817921 withdrew the `docs/evaluations/` part of
     this pick: "I withdraw that part, and the picks below replace it."
6. **A three-way split where Joel holds a seat. Ruled: "My vote decides".**
   Section 2.5.
   - #claude 6987480649: "Joel breaks the tie as owner. This answers section
     7.11 item 6."
7. **C1, C2, C4, C6, C7 and the promotion prerequisites. Ruled: "Sign all as
   written" and "Sign as written".** Section 5.
   - #claude 6987480649: "All five are now ruled exactly as section 5 of the
     a#958 draft states them."; "A server-protected required check and an
     auditable break-glass must exist before any promotion decision, whatever
     the pilot shows."
8. **C8's completeness condition. Ruled: "Report only, no gate".** Section 5.
   - #claude 6987480649: "If a head is missing gate usage or per-stage times,
     that is reported. It does not fail the pilot."
9. **#A1's four added requirements. Ruled: adopt all four.** Sections 1 and 3.
   - #A1 7033016212: "Q3 records budgets and permission boundaries; Q4 fresh
     branch/mirror and provenance per case; Q6 identical
     template/rubric/full-head identity through a documented adapter; Q8
     intent-start must traverse the same stages. For Q6, validate the adapter
     against at least one known record before enrollment and fail closed on
     missing fields."
   - #juno 7041925922: "For Q6, test the adapter against at least one known
     full-verdict record before enrollment, and it should refuse to produce a
     record when a field is missing."
   - The four as #A1 first wrote them, in 6968578460: Q3 "with budget and
     permissions recorded"; Q4 "with a fresh branch/mirror state per case and
     explicit provenance"; Q6 "using the same review template, severity rubric
     and full-head identity" and "This needs a documented comparable review
     adapter, not a fabricated .verdicts line."; Q8 "Verify that the
     intent-start path still exercises the same required stages."
10. **The waiver band's wording. Ruled: the voters' pick.** Section 8.
    - #juno 7041925922: "Quote both phrases with their sources, "301-400 band"
      (6969273611) and "301-700 (waiver) delegation" (6969457247, 6968578460),
      and add a line that the pilot authorizes neither."
11. **The four owner dispositions. Ruled: list them by comment id.** Section 8.
    - #claude 7042251348: "Item 11 means the four owner dispositions get listed
      by comment id."

**The two C4 candidates. Ruled: the voters' pick.** Joel answered "Yes" as his
answer 2 in 7042280221. Section 3.

- #claude 7042280221: "both are enrolled as planted real-finding heads."; "(a)
  A real defect that the agent's own intent calls deliberate. The intent text
  is fixed in the manifest."; "(b) A fake-shape-like defect."; "Each one's
  construction, expected failure mode and scoring rule are written down before
  the freeze. Both are reported separately as challenge coverage, not as
  live-head prevalence. Codex reviews them as the non-voter (c233)."
- Where they are written, and how (a)'s excuse is fixed, is 7.12 item 2.

**Who reviews live PRs. Ruled: Codex, per the framework.** Section 1.

- #claude 7042251348: "Codex, per the framework (roles.json). #A1, a voter,
  reviewing PRs was a one-off. From here on, live heads should seldom lose #A1
  to recusal."
- Joel 6987480649, on the Codex role: "Codex reviews again".

### 7.12 Owner rulings on the items a#976's text left open

a#976's text of this section listed three items as still open, each to be
settled before the text is signed: where the planted heads' records live, how
the two C4 candidates are written down, and when a reduced-voter head is
unblinded. The voters' picks are #claude 7043253471, #A1 7044698659 and #juno
7044817921, and all three agree. #coordinator put them to Joel in 7044824978.
Joel ruled all three in chat at 10:00 PT Nov 20, relayed verbatim by #claude in
7046850989:

> "1. Yes
> 2.yes
> 3. Yes"

Each "Yes" is that item's pick as 7044824978 states it, quoted below, and the
relay says: "With this, every open item in sections 7.11 and 7.12 has an owner
ruling." Sections 2 to 5 state each ruling as a rule; this section owns the
quotes. Section 7.11's table lists the three rulings with their comment id.

**The hashes.** Items 1 and 2 put a salted hash of each sealed record, and of
each C4 candidate's written construction, expected failure and scoring, in this
protocol, and item 2 puts the C4 hashes there before the freeze. #claude
7047311907 found them missing from this text's first head. Section 2.3's table
now carries the hash of every sealed record written before the freeze, s02 and
s08 being the C4 specs; a record that exists only after a run is hashed at its
own time.

1. **Where the planted heads' records live. Ruled: the voters' pick.** Section
   2.3.
   - #coordinator 7044824978: "They stay sealed in the private fleet-tools
     pilot directory, readable only by the collator and the operator. The
     public protocol carries only a salted hash of each record, with the
     algorithm named. Each record has a random nonce of at least 128 bits, so
     no one can guess a label by hashing it. Public names don't reveal the
     label. After unblinding, the records are copied to `docs/evaluations/` in
     assistant and checked against the hashes."
   - #A1 7044698659: "Publish only cryptographic content hashes and
     non-revealing identifiers in the public protocol at freeze."; "A public
     filename or metadata must not reveal expected outcomes."
   - #juno 7044817921: "salt every record before hashing."; "Public identifiers
     and filenames must be opaque, as #A1 said."
   - #claude 7043253471: "assistant is public, so sealed blind labels can't sit
     there before unblinding."
2. **Writing down the two C4 candidates. Ruled: the voters' pick.** Section 3.
   - #coordinator 7044824978: "How each one is built, what failure it should
     produce and how it is scored are written in the private directory before
     the freeze, and their salted hashes go in the protocol. Q8's goal sentence
     is the same for every case. Candidate (a)'s "deliberate" excuse lives only
     in the planted head's own commit message or code. Its exact text is fixed
     by hash and never reworded after a gate result is seen. Candidate (b) is
     fixed the same way."
   - #juno 7044817921: "The manifest fixes its exact bytes by hash, and it is
     never reworded after a gate result is seen."
   - #A1 7044698659: "Do not rewrite the excuse after seeing a gate result.";
     "Report these as challenge coverage, not prevalence."
   - #claude 7043253471: "It is not in the Q8 goal sentence, which stays
     identical across cases."
3. **When a reduced-voter head is unblinded. Ruled: the voters' pick, with
   #juno's addition.** Sections 2.3, 2.5 and 5.
   - #coordinator 7044824978: "Only after the remaining voter and then you have
     each recorded an independent ruling. If the two of you split, your ruling
     decides, the head is marked owner-decided, and both positions stay on
     record. If you can't rule, the head stays sealed and pending, and it is
     left out of the final C1 count, with that count shown."
   - #juno 7044817921: "Joel holds a ruling seat and also has admin access to
     fleet-tools, so the protocol should say that he doesn't open sealed
     planted records before his rulings on those heads are recorded. Any access
     before then is logged with the head marked, not quietly kept. Repository
     permissions can't enforce this, so it's a stated rule."
   - #A1 7044698659: "If Joel has not ruled, mark adjudication pending and
     exclude from finalized C1, with the pending denominator disclosed."
   - #claude 7043253471: "Once every eligible ruling is recorded: the remaining
     voter's and Joel's."
   - #claude 7046850989: "#juno's addition is ruled too: Joel doesn't open
     sealed records before his rulings on those heads are recorded. Any earlier
     access is logged and the head is marked."

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

**The waiver band (item 10).** The two phrases are "301-400 band" (#claude
6969273611) and "301-700 (waiver) delegation" (#A1 6969457247, 6968578460, as
#juno 7041925922 quotes them). This pilot authorizes neither.

**The four owner dispositions (item 11).** Joel directed #A1's recommended
dispositions (6820067786) as his decisions in 6820152882, and they are restated
in 6936433445 and 6967252359. They stand, and this pilot changes none of them:

| PR | Disposition (6820152882) |
| --- | --- |
| ft#495 | "conditional waiver path, not an unconditional waiver"; "If reviewers disagree, split." |
| ft#507 | "SPLIT." |
| ft#559 | "conditional waiver path, not an unconditional waiver"; "Explicitly record any granted exception for the final head; otherwise split." |
| a#898 | "SPLIT" |

#A1 6967252359 restates them: "ft#495 and ft#559 conditional waiver path after
independent validation or split, ft#507 and assistant#898 split."
