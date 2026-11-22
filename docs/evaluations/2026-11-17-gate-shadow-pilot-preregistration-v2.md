# Gate shadow pilot pre-registration, version 2 (no voters), 2026-11-22

**Status: Draft, not signed.** Joel is this text's only signer. It freezes when
he signs it at the merge commit of the docs PR that adds it, in the form J4
rules (section 11). Nothing is enrolled and no shadow run happens before then.
No pilot head has run.

Notation is the original's (its lines 26-34): `a#N` is `dayamjz/assistant`,
`ft#N` is `dayamjz/fleet-tools`, a path prefixed `ft:` is in fleet-tools, and a
bare 10-digit number is an issue comment id
(`gh api repos/dayamjz/fleet-tools/issues/comments/<id>`). "The original" is
`docs/evaluations/2026-11-17-gate-shadow-pilot-preregistration.md` at commit
`fd80055c522ea2d6b135a30d596e830e63132b97`, and "line N" without a file is a
line of that text. "O1" to "O5" are the oracle rules of section 5, "J1" to "J5"
the owner questions of section 11.

## 1. Status and authority

**What this supersedes.** The original, frozen at merge commit
`fd80055c522ea2d6b135a30d596e830e63132b97` (a#977), tree
`3cedae2b774fa6f553a228034eefeb5ddcc6013f`, blob
`32424c209b2bc43cd18ba653c7d2091edefc5dcc`, 902 lines, SHA-256 of its bytes
`f73d7888ded374378df72dbb62e06caa75ba25a69f4e6da5d97b70e99880cd5a`. It was
signed 4 of 4 at that commit: #claude 7050551843, #juno 7051086352, Joel
7051299262 (posted for him by #claude from chat) and #A1 7051313673. Its line 3
still reads "Status: Draft, not frozen". Its freeze came from those signatures
(lines 6-11), not from that line. This text leaves the original byte-identical
and supersedes it section by section: a section this text changes is restated
here in full, and a section it keeps is incorporated by reference to blob
`32424c20` by exact line range, never retyped (section 2).

**Authority.** Joel, 07:47 PT Nov 21, relayed verbatim by #claude in 7065147005:

> Yes drop the no objection and redesign so no voters are needed. Include this
> in the coordinator improvements, get sign off and prepare to implement

The same relay: "This is an owner ruling, so the four-signature amendment rule
in the frozen text no longer governs the change. The redesign still lands as a
reviewed PR under the full gates." Joel, 07:40 PT Nov 21 (7065065542), adds that
no item may depend on the voters existing. The change was agreed as ft#762
row 8 (7064759390, proposed in 7065147005), with #A1's acceptance condition B
(7065454500), #juno's notes (7065464664) and #claude's acceptance (7065548378).

**Signing.** Joel is the only signer. This text freezes when he signs it at its
merge commit, in the form J4 rules. The four-signature rule of lines 6-11 does
not govern this text. Nothing is enrolled before Joel's signature exists and
every precondition of section 9 holds.

## 2. What is kept

These parts of blob `32424c20` are incorporated by reference, by line range, as
written there. Where section 3's table removes or replaces a line inside one of
these ranges, the table wins.

- **Terms**, lines 38-42 and 51-52 (head, gate run, the review, item,
  disagreement, hold, park). The voter, collator, compared-head and
  reduced-voter terms (lines 43-50) are not kept; section 5 defines what v2
  uses.
- **Section 1, what is compared**, lines 54-94. **The planted-head review stays
  Codex, the non-voter (lines 72-83), unchanged:** a blinded full review of the
  exact commit outside a pull request, through the adapter `ft:pilot-review.sh`.
- **Section 2.2's definitions of a disagreement**, lines 121-130 (missed,
  extra, hold the review did not make, pass the review refused) and lines
  135-139 (setup holds are excluded). How two findings match (lines 131-133) is
  replaced by O5.
- **Section 3, case mix and enrollment**, lines 267-352.
- **Section 4's capture fields**, lines 354-396 (the field table from
  `head_id` to `review.record_commit`). Rows 397-405 and the example record
  (lines 407-448) are not kept.
- **Section 5's prerequisites**, lines 485-488.
- **Section 6, known limits**, lines 490-526.
- **Section 8, what does not change**, lines 873-902.
- **Section 7**, lines 528-872, stays as the record of the rulings the kept
  sections cite. It binds nothing in v2 except through those sections.

Sections 2.1, 2.3, 2.4 and 2.5 and the criteria table of section 5 are
superseded. Sections 4 to 8 below restate what replaces them.

## 3. Removed voter steps

One row per non-blank original line in the ranges row 8's brief names, and one
row for every hit of
`grep -n -i -E 'voter|recus|#juno|#A1|#claude|ruling|split'` over lines 1-527.
A row that is not a voter step says "kept, quote only". Each quote is that
line's exact text, trimmed, with `|` written as `\|`. `ft:pilot-preflight.py`
check 3 compares this table with that grep and with the original's bytes.

| original line | quote | v2 replacement |
| --- | --- | --- |
| 5 | listed as open, has an owner ruling, and sections 7.11 and 7.12 own those | kept, quote only: names the owner rulings v1 folded in |
| 6 | rulings. The text freezes only when Joel and the voters (#juno, #A1, #claude) | replaced: v2 freezes when Joel, its only signer, signs it at its merge commit (v2 section 1) |
| 9 | rulings (Joel 7044130931, 7046850989). Merging is not what freezes it; the four | replaced: no four signatures; Joel's one signature freezes v2 (v2 section 1) |
| 18 | disagreement gets a ruling (gate right, review right, both, neither) under rules | replaced: live-head disagreements are counted by kind, not ruled (O5); a ruling exists only under J1 option 2 |
| 21 | (`e10e7059`). This text folds in the section 7 rulings, the owner rulings on | kept, quote only: history of v1's drafting |
| 22 | the items the draft left open (section 7.11), and the owner rulings on the three | kept, quote only: history of v1's drafting |
| 43 | - **Voters:** #juno, #A1 and #claude. **Collator:** whoever prepares a head's | removed: no voters; the collator's packet is replaced by pilot-score.py after the end condition (v2 sections 5 and 8) |
| 49 | - **Reduced-voter head:** a head on which two voters recuse, so that the | removed: no reduced-voter heads, since nobody recuses |
| 50 | remaining eligible voter and Joel's seat rule it (section 2.5). | removed: no reduced-voter heads, since nobody recuses |
| 70 | #A1, a voter, reviewing PRs was a one-off (section 7.11, the live-PR row). A | kept, quote only for its first sentence; the recusal sentence that starts here is removed (line 71) |
| 71 | live head a voter did review still gets that voter's recusal (section 2.5). | removed: no recusal in v2 |
| 73 | blinded full review of the exact commit, outside a PR, by Codex, the non-voter | kept, quote only: Codex stays the non-voter planted-head reviewer |
| 80 | non-voter reviewer is available, the planted-head part does not run, and no | kept, quote only: no voter stands in, and none exists |
| 81 | voter stands in. The review is kept apart from `.verdicts` and creates no | kept, quote only: no voter stands in, and none exists |
| 100 | draft's, plus the owner rulings on 7.12 items 1 and 3 (section 7.12). Section | removed: v1 sections 2.1, 2.3, 2.4 and 2.5 are superseded (v2 sections 3 to 8) |
| 101 | 2.5 carries Q1 and Q2, the owner rulings on items 1, 2 and 6 (section 7.11), and | removed: v1 sections 2.1, 2.3, 2.4 and 2.5 are superseded (v2 sections 3 to 8) |
| 104 | ### 2.1 The four-way ruling | removed: no four-way ruling (J1 option 1 default); under J1 option 2 Joel alone rules within the frozen budget and order |
| 106 | Each disagreement gets exactly one ruling. "Real" means the described defect | removed: no four-way ruling (J1 option 1 default); under J1 option 2 Joel alone rules within the frozen budget and order |
| 110 | \| Ruling \| Means \| Worked example \| | removed: no four-way ruling (J1 option 1 default); under J1 option 2 Joel alone rules within the frozen budget and order |
| 112 | \| **Gate right** \| The gate's position holds and the review's does not. \| The gate raises that a refusal in `internal/cli` is reached by a path the review did not read; the voters reproduce it at the head. The review approved. \| | removed: no four-way ruling (J1 option 1 default); under J1 option 2 Joel alone rules within the frozen budget and order |
| 117 | A matched item is not a disagreement and gets no ruling. A new defect found | removed: no four-way ruling (J1 option 1 default); under J1 option 2 Joel alone rules within the frozen budget and order |
| 131 | Two findings match when they name the same defect at overlapping locations. The | replaced: two findings match by location only, same path and line within K (O5); not v1's same-defect match |
| 132 | collator proposes matches. Any voter may split a proposed match, which turns it | removed: no collator proposal and no voter split; O1 and O5 are pure functions of recorded bytes |
| 133 | into one missed and one extra item, each ruled. | removed: no collator proposal and no voter split; O1 and O5 are pure functions of recorded bytes |
| 158 | 4. **Ruling.** Each voter rules from the packet, the diff at the head and the | removed: no voter ruling step |
| 159 | repository at the head, and records the ruling before reading another | removed: no voter ruling step |
| 160 | voter's ruling on that head. | removed: no voter ruling step |
| 161 | 5. **Unblinding.** The side behind A and B is revealed only after all three | replaced: unblinding is pilot-unblind.py after the declared end condition (v2 section 8) |
| 162 | rulings for a head are recorded. On a reduced-voter head it is revealed only | replaced: unblinding is pilot-unblind.py after the declared end condition (v2 section 8) |
| 163 | after the remaining voter's ruling and then Joel's independent ruling are | replaced: unblinding is pilot-unblind.py after the declared end condition (v2 section 8) |
| 164 | recorded (7.12 item 3). Planted truth and the side mapping stay sealed from | replaced: unblinding is pilot-unblind.py after the declared end condition (v2 section 8) |
| 165 | the voters until then (Q10). | replaced: unblinding is pilot-unblind.py after the declared end condition (v2 section 8) |
| 197 | **Joel and the sealed records (7.12 item 3, #juno's addition).** Joel holds a | kept and restated in v2 section 6: applied to Joel as the sole signer, and as the sole ruler under J1 option 2 |
| 198 | ruling seat and has admin access to fleet-tools. He does not open sealed records | kept and restated in v2 section 6: applied to Joel as the sole signer, and as the sole ruler under J1 option 2 |
| 199 | before his rulings on those heads are recorded. Any earlier access is logged and | kept and restated in v2 section 6: applied to Joel as the sole signer, and as the sole ruler under J1 option 2 |
| 203 | write differently; step 4's order is a rule the voters keep, not something a | removed: step 4 (voter rulings) does not exist in v2 |
| 206 | permission enforces (#juno 7044817921), so nothing in this protocol detects an | kept and restated in v2 section 6 (residual gaps) |
| 213 | Each voter assigns one of `blocker`, `should-fix`, `nit` or `not-real` to every | replaced: severity is UNMEASURED; the raw gate and review labels are reported |
| 218 | ### 2.5 Who rules, and how a ruling is decided | removed: no voters rule disagreements |
| 220 | The voters #juno, #A1 and #claude rule every disagreement, each ruling recorded | removed: no voters rule disagreements |
| 221 | with its voter and time (Joel, ft#545 6954624923, answer 4). This replaces the | removed: no voters rule disagreements |
| 222 | "owner-recorded" ruling of `ft:docs/critique/03-juno-response.md:167` for row 12. | removed: no voters rule disagreements |
| 223 | Joel signs the promotion criteria and decides row 13. | kept, quote only: Joel signs the promotion criteria (J2) and decides row 13 |
| 225 | **Split vote (Q1).** Two of three voters decide the four-way ruling. When all | removed: no split vote; severity is UNMEASURED |
| 226 | three rulings differ, the item goes to Joel. Every dissent is recorded with its | removed: no split vote; severity is UNMEASURED |
| 227 | evidence. Severity is ruled separately from the four-way ruling. When the | removed: no split vote; severity is UNMEASURED |
| 228 | voters assign different severities, the item is held at the higher severity | removed: no split vote; severity is UNMEASURED |
| 229 | until the voters reconcile or Joel rules. | removed: no split vote; severity is UNMEASURED |
| 231 | **A three-way split where Joel holds a seat (item 6).** When Joel holds a | removed: no seats |
| 232 | recused voter's seat and all three rulings differ, Joel's vote decides. | removed: no seats |
| 234 | **Recusal (Q2).** A voter recuses from a head's gate-versus-review | removed: no recusal |
| 235 | disagreements when either holds: | removed: no recusal |
| 237 | - the voter wrote the compared review; or | removed: no recusal |
| 238 | - the voter's model family ran the gate's review or fix stage on that head. | removed: no recusal |
| 240 | Joel takes a recused voter's seat. With the gate's review and fix stages pinned | removed: no recusal |
| 241 | to Claude (section 7, the pin), #claude recuses from gate-versus-review | removed: no recusal |
| 242 | disagreements on every head whose review or fix stage ran on Claude, and Joel | removed: no recusal |
| 243 | takes that seat. | removed: no recusal |
| 245 | **When a recusal is registered (item 2).** Reviewer-based recusals are | removed: no recusal registration; recusals[] has no v2 counterpart |
| 246 | registered at enrollment. Stage-model recusals are registered by the collator | removed: no recusal registration; recusals[] has no v2 counterpart |
| 247 | from the run's own record, after the run and before anyone reads a result or | removed: no recusal registration; recusals[] has no v2 counterpart |
| 248 | opens the blinded packet. Each records its time and source in the head's | removed: no recusal registration; recusals[] has no v2 counterpart |
| 249 | `recusals[]`. A conflict found later means exclusion and re-adjudication, never | removed: no recusal registration; recusals[] has no v2 counterpart |
| 250 | a quiet edit. | removed: no recusal registration; recusals[] has no v2 counterpart |
| 252 | **Two recusals on one head (item 1).** On a reduced-voter head the remaining | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 253 | eligible voter rules with Joel in the second seat. Joel gives his own recorded | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 254 | ruling and is never an automatic tie-break. Every disagreement on a | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 255 | reduced-voter head goes to Joel with both sides' arguments set out, and he rules | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 256 | after weighing them, even where his seat would otherwise decide. If the two | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 257 | split, Joel's ruling controls, the head is labeled owner-decided (the item's | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 258 | `decided[].how` is `joel`), and both positions stay on record. If Joel cannot | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 259 | rule, the head stays sealed and adjudication pending, is not a compared head, | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 260 | and is left out of the final C1 count, with that count disclosed (7.12 item 3). | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 261 | Its sides are unblinded only after both rulings are recorded (section 2.3). | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 262 | Reduced-voter heads are marked in their record (`reduced_voter`, section 4), | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 263 | and the report gives C1 both with and without them (section 5). No disagreement | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 264 | is settled by an automatic tie-break. Any other head on which independence | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 265 | still cannot be met is adjudication pending (Q2). | removed: no reduced-voter heads and no adjudication pending; v2 C1 counts compared heads (v2 section 5) |
| 296 | reviews them as the non-voter (section 1). | kept, quote only: Codex reviews the C4 heads as the non-voter |
| 397 | \| `recusals[]` \| list of {voter, reason, stage_model, seat_taken_by, registered_at, source} \| `reason` is `wrote-review` (registered at enrollment) or `model-family` (registered by the collator from the run's own record, after the run and before anyone reads a result or opens the blinded packet); `source` is what the recusal was registered from (section 2.5) \| when a voter recuses \| | removed: recusals[] and reduced_voter have no v2 counterpart |
| 398 | \| `reduced_voter` \| bool \| true when two voters recuse on the head (section 2.5) \| yes \| | removed: recusals[] and reduced_voter have no v2 counterpart |
| 399 | \| `adjudication` \| `compared`, `pending`, `base_mismatch` or `not-compared` \| `pending` when independence cannot be met (section 2.5); `not-compared` when there is no full verdict (section 1) or the pin check fails (section 3) \| yes \| | replaced: compared is computed by pilot-score.py (v2 section 5); pending does not exist |
| 400 | \| `frozen_commit`, `raw[]` \| 40-hex, list of {file, sha256} \| the freeze commit (section 2.3) \| yes \| | kept, quote only: frozen_commit and raw[] are the freeze ledger line's body (v2 section 7) |
| 401 | \| `items[]` \| list of {item, kind, gate_id, review_index, order} \| collator; `kind` is `matched`, `missed`, `extra`, `hold-not-made` or `pass-refused` \| yes \| | replaced: items and planted_match are computed by pilot-score.py by O1 and O5; no voter splits a match |
| 402 | \| `planted_match` \| gate finding id or null \| collator, split by any voter as a match is (section 2.2) \| planted \| | replaced: items and planted_match are computed by pilot-score.py by O1 and O5; no voter splits a match |
| 403 | \| `outcome_rulings[]` \| list of {voter, ruled_at, correct, reason} \| each voter: whether `gate.outcome` and `gate.reason` are right under PRD sections 5 and 11, and whether the manifest's `injection.expected` held \| race, recovery \| | removed: race and recovery correctness is UNMEASURED (C7, J2) |
| 404 | \| `rulings[]` \| list of {item, voter, ruled_at, ruling, severity, reason, evidence, posted} \| each voter, one row per item; `posted` is the comment URL or file the ruling was recorded in \| per disagreement \| | removed: no rulings[] and no decided[]; under J1 option 2 Joel's rulings are a separate endpoint |
| 405 | \| `decided[]` \| list of {item, ruling, severity, how} \| from `rulings[]` by section 2.5; `how` is `unanimous`, `two-of-three` or `joel` (a three-way split, or a split on a reduced-voter head), and a severity still held at the higher value says so \| per disagreement \| | removed: no rulings[] and no decided[]; under J1 option 2 Joel's rulings are a separate endpoint |
| 410 | Claude, so #claude recuses and Joel takes that seat (section 2.5); Codex | removed: v1's example record is not incorporated |
| 411 | wrote the review, so no other voter recuses and the head is not reduced-voter. | removed: v1's example record is not incorporated |
| 421 | "recusals": [{"voter": "#claude", "reason": "model-family", "stage_model": "claude <model>", | removed: v1's example record is not incorporated |
| 422 | "seat_taken_by": "Joel", "registered_at": "2026-11-20T17:20:00Z", "source": "gate.stage_models[]"}], | removed: v1's example record is not incorporated |
| 423 | "reduced_voter": false, "adjudication": "compared", | removed: v1's example record is not incorporated |
| 443 | "rulings": [{"item": "I1", "voter": "#juno", "ruled_at": "2026-11-21T16:40:00Z", | removed: v1's example record is not incorporated |
| 444 | "ruling": "gate right", "severity": "should-fix", "reason": "...", | removed: v1's example record is not incorporated |
| 445 | "evidence": "internal/cli/runs.go:512 at the head", "posted": "<comment URL>"}], | removed: v1's example record is not incorporated |
| 446 | "decided": [{"item": "I1", "ruling": "gate right", "severity": "should-fix", | removed: v1's example record is not incorporated |
| 447 | "how": "two-of-three"}]} | removed: v1's example record is not incorporated |
| 452 | **Every criterion is Joel's ruling.** C3, C5 and C8 are his Q9 ruling. C1, C2, | kept, quote only: the criteria stay Joel's (J2) |
| 461 | \| C1 \| ruled (item 7) \| Sample \| At least 10 compared heads, with every case in section 3 at its minimum. A `base_mismatch` or adjudication-pending head does not count. \| | replaced: v2 C1 drops adjudication pending (v2 section 5) |
| 467 | \| C7 \| ruled (item 7) \| Race and recovery \| Every race and recovery head's `outcome_rulings[]` decide `correct`, by section 2.5's split-vote rule. \| | replaced: C7 is UNMEASURED unless J2 rules otherwise (v2 section 5) |
| 472 | **Reported beside the criteria.** C1 is reported both with and without | removed: no reduced-voter heads, so C1 has one count |
| 473 | reduced-voter heads (item 1, section 2.5). A reduced-voter head Joel cannot | removed: no reduced-voter heads, so C1 has one count |
| 474 | rule is left out of the final C1 count, with that count disclosed (7.12 item | removed: no reduced-voter heads, so C1 has one count |
| 475 | 3). The two C4 candidates are planted real-finding heads, and their results are | removed: no reduced-voter heads, so C1 has one count |

## 4. The endpoint change

- **v1's endpoint.** Blind, independent four-way rulings (gate right, review
  right, both, neither) by three voters on every disagreement (original 2.1,
  2.5), with severity set by the voters (2.4). C3, C5 and C7 are computed from
  those rulings (lines 463, 465, 467).
- **v2's endpoint.** Three things, each computed by a script from recorded
  bytes:
  - deterministic scoring of planted heads against the sealed truth (O1 to O4);
  - counts by kind of live-head and replayed-head disagreements, matched by
    location (O5);
  - for every dimension v1 left to judgment, a stated measure or UNMEASURED
    (section 5).
- **v2 is not the same experiment as v1.** Its results are not comparable with
  v1's C3, C5 outside clean heads, or C7, and they are never pooled with any v1
  result.
- **Comparability with row 11.** Row 11, the caliper Q1 agentic eval
  (`ft:notes/row11-design-a896-6953845110.md`), also scores against an oracle
  the agent under test cannot read (its review hook and prompt are in
  `sandbox.forbidden_files`, note line 92). v2's planted-head scores are
  oracle-scored in that same sense, but on a different subject and case mix,
  so no rate carries over between them.
- **Adjudication: none.** `found_in_adjudication` (lines 117-119) has no v2
  counterpart, and it is UNMEASURED.

## 5. Oracle rules

Each rule is a pure function of recorded bytes. Each names its inputs and the
condition under which it is UNMEASURED. None asks for any agent's, voter's or
person's judgment. `ft:pilot-score.py` implements them, and section 7 names the
files it reads.

**Terms.** The **operator** drives each gate run and writes the captures. The
**manifest** of the original's section 3 is, in v2, the ledger's `enroll`
lines (section 7). A **planted head** is one whose `enroll` names a sealed
record (`sealed_id`). A **compared head** is an enrolled, frozen head whose
review verdict is `APPROVE` or `CHANGES REQUESTED`, whose
`base_check.base_mismatch` is `false`, and whose `pin_check` is `pass`
(section 7). There is no adjudication pending and no reduced-voter head.

**K = 3.** A finding within 3 lines of a changed line counts as at that line:
3 is git's default unified context, so a finding on any line the diff itself
shows around a change counts, and one beyond what the diff shows does not.

- **O1, planted location.**
  - Inputs: the sealed record `<sealed_id>.md`, the head's gate capture and its
    tree list.
  - Truth: the set of (path, new-side line range) of the added or changed lines
    in the record's one fenced unified diff. A fenced block is a diff when its
    body has a line starting `@@ `. An added line is truth at its new-side
    line. A removed line is truth at the new-side line it was removed before. A
    file the diff deletes adds no truth. Consecutive truth lines of one path
    form one range.
  - Match: a gate finding matches when its `path` is equal and its `line` lies
    in a range widened by K lines on each side.
  - `planted_match` is the first matching gate finding, ordered by its
    `stage`'s position in `gate.stages[]` (a finding naming no listed stage
    comes after all), then by finding id, shorter first, then bytewise. Only a
    finding with a string `id` takes part. It is `null` when no gate finding
    matches, and `null` means a miss.
  - UNMEASURED: when the record has no fenced diff, or more than one, or its
    diff does not parse (a hunk whose line counts disagree with its header, or
    no file with a new side), or a truth path is absent from the head's tree
    list. Then `planted_match` is `unmeasured` with that reason, never `null`.
- **O2, clean heads.** A head whose `enroll` case is `clean` has a false hold
  when `gate.holds[]` has an entry whose `stage` is `review`. This is C5's
  clean-head part. UNMEASURED when the gate capture is missing.
- **O3, hold heads.** A hold head is found when `gate.holds[]` has a `review`
  entry whose finding ids include one that O1 matches. UNMEASURED when O1 is.
- **O4, CI failure.** C6 is unchanged (line 466): the head's
  `gate.checks_basis` is `live-checks` and its `checks` stage's `outcome` is
  not `passed` (`internal/pipeline/outcome.go:22`). UNMEASURED when the
  basis is `no-ci-declaration` (the original's line 278) or the stage is
  missing.
- **O5, live and replayed heads.**
  - Inputs: the gate capture and the review record. Review findings are read
    with `ft:review-findings.py`'s findings block, never a second parser.
  - A review finding whose location is `path:N` or `path:N-M` matches the
    first gate finding not yet matched, in O1's order, with the same `path`
    and a `line` within K of that location. Review findings are taken in their
    block order.
  - A review finding located as a file only, or with no location, or from a
    review with no findings block, is `unlocatable` and matches nothing. A
    malformed findings block makes the head's O5 counts UNMEASURED.
  - Counts: `matched`, `missed` (a locatable review finding with no match),
    `extra` (a gate finding with no match), `hold-not-made` (a `review` hold
    while the verdict is `APPROVE`), `pass-refused` (the `review` stage's
    `outcome` is `passed` while the verdict is `CHANGES REQUESTED`) and
    `unlocatable`.
  - This is a location match, not v1's "same defect" match (line 131).
  - The rulings on these items are UNMEASURED (J1 option 1), or Joel's alone
    under J1 option 2.
- **UNMEASURED by script:** severity (2.4; the raw gate and review labels are
  reported); race and recovery correctness (C7), unless J1 or J2 rules
  otherwise; the C4 specs' own prose scoring rules in s02 and s08 (O1 still
  applies to the heads built from them); and `found_in_adjudication`.

**Criteria.** A criterion that is UNMEASURED cannot pass. C8 has no pass mark.
The pilot passes only when C1 to C7 all PASS. Whether it can pass with any
criterion UNMEASURED is J2; until Joel rules otherwise, it cannot.

| # | v1 definition | v2 definition | Decided by |
| --- | --- | --- | --- |
| C1 | line 461 | At least 10 compared heads, with every case of the original's section 3 at its minimum among them. | script |
| C2 | line 462 | `gate.activated` is true for every enrolled head. A head with no gate capture is not activated. | script |
| C3 | line 463 | UNMEASURED: "decided review right with severity blocker" needs a ruling. | UNMEASURED, or Joel (J1, J2) |
| C4 | line 464 | Every planted real-finding and hold head has a `planted_match` that is a finding id (O1). FAIL on any `null`; UNMEASURED when none is `null` and any is `unmeasured`. | script |
| C5 | line 465 | FAIL when any clean head has a false hold (O2). Otherwise UNMEASURED, since the rest of C5 needs a ruling. | script for clean heads; the rest UNMEASURED, or Joel (J2) |
| C6 | line 466 | O4 on every CI-failure head. FAIL on any failure; UNMEASURED when none fails and any is unmeasured, or when no such head exists. | script |
| C7 | line 467 | UNMEASURED: correctness of a race or recovery outcome needs a ruling. | UNMEASURED, or Joel (J1, J2) |
| C8 | line 468 | Report only, with no pass mark: the count of heads missing `gate.usage` or stage times. | script, report only |

**If the criteria are not met,** the gate is not promoted, and each head behind
a failed criterion is filed as its own issue on this repository with its scores,
ordered by the number of failed criteria it appears in, then by head id. The
original's lines 479-483 order by decided severity, which v2 does not have.

## 6. Storage and blinding under c251 option A

- **Where.** The sealed records and the sealed ledger stay in
  `/Users/pilotseal/pilot`, owned by the macOS user `pilotseal`, under c251
  option A (Joel 7065228773; plan 7065248343, amended 7065409328). This
  replaces the original's "private fleet-tools pilot directory" (Q10(a), lines
  168 and 622): option A is the authority for the change.
- **Who can read them.** Only pilotseal can read them. The evidence so far is
  the different-uid denied-read test as uid 501 (7065533093) and the completed
  option A checks (7066230158). Enrollment needs a fresh denied-read record
  made after this text merges (section 9, precondition 6), in the record shape
  of row 5's brief 5b (7065714265, rule 3).
- **The sealed records' hashes.** The original's s01-s08 table, lines
  182-191, copied byte for byte (preflight check 2 compares them). Each hash
  is SHA-256 over the record file's exact bytes, and each file begins with a
  `nonce:` line (lines 176-180):

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

- **Who runs the scripts.** Scoring and unblinding run only as pilotseal, and
  only when Joel invokes them, as
  `sudo -u pilotseal /Users/pilotseal/bin/<script>` (7065248343 item 3). No
  agent session runs as pilotseal, and no sudoers entry is recommended.
- **Joel and the sealed records.** The original's rule (lines 197-200) is kept
  and applied to Joel as the only signer, and as the only ruler if J1 is
  option 2: he does not open a sealed record before the end condition of
  section 8 holds and `pilot-unblind.py` has published it. Any earlier access
  is logged as an `access` line in the ledger, and the head is marked.
- **Residual gaps.** The original's (lines 202-207) are kept, except that
  step 4's voter order no longer exists. One is added: root, or Joel through
  `sudo`, can read the records or rewrite the ledger, and only the posted chain
  heads of section 7 detect a rewrite.

## 7. Records

**The ledger.** `ft:pilot-ledger.py` keeps an append-only, hash-chained ledger.

- JSONL, one object per line, keys in exactly this order:
  `{"v":1,"seq":N,"prev":"<64-hex>","at":"<RFC 3339 UTC>","kind":"<kind>","body":{...}}`.
  `prev` is the SHA-256 of the previous line's exact bytes, newline included;
  for `seq` 0 it is 64 zeros. Bodies are canonical JSON (sorted keys, no
  spaces, ASCII), so the bytes are reproducible.
- Kinds are a closed set: `enroll`, `capture`, `freeze`, `close`, `score`,
  `unblind`, `unblind-refused`, `score-refused`, `denied-read`, `preflight`,
  `install`, `access`, `correction`. `enroll` and `freeze` are unique per
  `head_id`; `close`, `score` and `unblind` are unique per ledger. A `freeze`
  needs an earlier `enroll` of its `head_id`. Nothing but a `correction` may
  follow `unblind`.
- An append locks, verifies the whole chain, writes one line with `O_APPEND`
  and syncs it. No mode rewrites or truncates a ledger. A mistake is fixed by a
  `correction` line that names the bad `seq`.
- `head <ledger>` prints `head <seq> <sha256 of the last line>`.

**Bodies.**

- `enroll`: `head_id` (`hNN`), `case` (`clean`, `real-finding`, `hold`,
  `ci-failure`, `race`, `recovery` or `live`), `provenance` (`live`,
  `replayed` or `planted`) and `sealed_id` (`sNN` for a planted head, else
  `null`), plus the original's manifest fields (lines 306-330).
- `freeze`: `head_id`, `frozen_commit` (40-hex) and `raw[]` of
  `{file, sha256}`, one per capture file of the head.
- `preflight`: what `ft:pilot-preflight.py check` verified, including this
  text's merge commit and the parameters below. The end rule and K are read
  from the last `preflight` line before the first `enroll`, so they are fixed
  before any head is enrolled.
- `close`, `score`, `unblind`, `unblind-refused`, `score-refused`,
  `denied-read`, `install`, `access`, `correction`: as their scripts write them
  (section 10).

**Captures.** For each head the operator writes, under `<captures>/<head_id>/`:
`gate.json` (the original's section 4 fields, lines 361-396, plus `pin_check`,
`pass` or `fail`, the item 3 pin check of line 332), `review.md` (the review
record, for a head that has one) and `tree.txt` (`git ls-tree -r --name-only
<head>`). Each is listed in that head's `freeze` `raw[]`, and the scripts read
no capture whose bytes differ from it.

**Streams and chain heads.** The sealed ledger is
`/Users/pilotseal/pilot/ledger.jsonl`. Whether uid-501 captures go to a second,
public ledger is coordinator open item 2 (section 11). The coordinator posts the
chain head (`head`'s line, which reveals no content) at each `freeze`,
`close`, `score` and `unblind`. Pilot-op's proposal (`cl-pilotop` REPORT.md,
step 6) suggested posting each `frozen_commit` the same way.

**Parameters.** The scripts read these from this block, through the `preflight`
line, never from a flag. A value that is Joel's stays `UNRULED` until he rules.

```pilot-v2-params
k: 3
end_rule: pr-sequence
pr_count: UNRULED J3 (5-10)
j1: UNRULED J1
j1_budget: UNRULED J1
j1_order: UNRULED J1
j4: UNRULED J4
```

`k` is a whole number; `end_rule` is `pr-sequence` (J3, section 11); `pr_count`
is a whole number from 5 to 10, the length of that sequence, and it stays
`UNRULED` until Joel gives it; `j1` is `option-1` or `option-2`; `j1_budget` is `<N> items` or
`<N> hours`.

## 8. End condition and unblinding

`ft:pilot-unblind.py` decides the end condition, and it fails closed: anything
missing, unreadable or unparsable is a refusal. The end condition holds only
when all of these hold:

1. The ledger verifies.
2. Exactly one `close` exists, and its `at` is not later than the clock.
3. Every `enroll` has a `freeze` with a 40-hex `frozen_commit` and a non-empty
   `raw[]` of `{file, sha256}`.
4. The end rule of section 7's parameters holds, read from the `preflight`
   line. Joel ruled it (J3): the pilot ends after one consistent sequence of
   PRs, the same kind of work, chosen to measure cost and efficiency, not on a
   calendar date. It holds once `pr_count` heads of provenance `live` are
   frozen. `pr_count` is between 5 and 10, and its exact value is J3's one
   remaining blank. While it is unset, not a whole number, or outside 5-10,
   this condition fails, so nothing is scored or unblinded.
5. For unblinding only: a `score` line exists after the `close`.

**Refusal.** A refused unblinding appends `unblind-refused` with the reason. A
refused scoring appends `score-refused`. So an early attempt is logged, as the
original's lines 199-200 require. The one refusal that cannot be logged is a
ledger that does not verify (condition 1), since nothing can be appended to a
broken chain: it is printed, and the posted chain heads show where the chain
broke. A refusal prints no record content and publishes nothing.

**Scoring.** `ft:pilot-score.py` scores only after the end condition holds, so
no score is visible mid-pilot to steer enrollment. It appends one `score` line
and prints only scores, ids and counts, never a sealed record's bytes.

**Publishing.** `pilot-unblind.py run` copies the sealed files and the ledger
into a staging directory beside the publish directory, recomputes each copy's
SHA-256 against the s01-s08 table of the original (read from blob `32424c20`,
which it checks), and refuses on any mismatch. Only then does it append
`unblind` with the published hashes, and only after that line is written does it
move the copies into the publish directory, so nothing is published unlogged. A
second run refuses with "already unblinded". After that, the records
are copied to `docs/evaluations/` in this repository, as the original's line
173 says.

## 9. Enrollment preconditions

Enrollment needs every one of these. Choosing an option is not evidence (#A1
7065454500). `ft:pilot-preflight.py check` tests them and names the first that
fails:

1. The original's blob at `fd80055c` and at assistant `origin/main` is
   `32424c209b2bc43cd18ba653c7d2091edefc5dcc`.
2. This text exists on assistant `origin/main`, and its s01-s08 rows equal
   the original's lines 182-191 byte for byte.
3. Every grep hit of section 3 has a row in its table, with the original's
   exact line.
4. Joel's signature of this text is recorded, naming its merge commit, in the
   form J4 rules. Until J4 is ruled this check fails with "J4 unruled", and once
   it is ruled, reading that form needs a reviewed change to the check.
5. J3's `pr_count` is set, a whole number from 5 to 10, under the
   `pr-sequence` end rule. J1 is ruled, and if it is option 2, this text states
   a numeric budget and an item order.
6. A denied-read record that passes brief 5b's rule 4 exists, dated after this
   text's merge: run as uid 501, against targets owned by a different uid, never
   as root, and every probe denied with `EACCES`. `pilot-preflight.py probe`
   makes it: for each target it opens it read-only and closes it without reading
   a byte, opens it as a directory without listing an entry, and stats it, and it
   reads the owner uid from the nearest ancestor it can stat. A target whose own
   stat gives the owner has no stat probe.
7. The `install` record shows that the SHA-256 of each script in
   `/Users/pilotseal/bin` equals the blob pinned in section 10 (J5).

On success it appends a `preflight` line. It never enrolls.

## 10. Scripts

| script | fleet-tools path | merge commit | blob SHA-256 |
| --- | --- | --- | --- |
| ledger | `pilot-ledger.py` | UNPINNED | UNPINNED |
| unblinding | `pilot-unblind.py` | UNPINNED | UNPINNED |
| scoring | `pilot-score.py` | UNPINNED | UNPINNED |
| preflight | `pilot-preflight.py` | UNPINNED | UNPINNED |
| review findings, used by scoring | `review-findings.py` | UNPINNED | UNPINNED |

At this text's final head each row names the fleet-tools merge commit that
landed the script and the SHA-256 of its bytes there. The copies Joel installs
in `/Users/pilotseal/bin` must hash to those values, and preflight check 7
compares them.

## 11. Owner questions for v2

This text is not signed while any of these is open. Once Joel answers, his
answers are quoted here verbatim with their comment ids, as a#977 folded in
section 7.12.

- **J1. Live-head disagreements** (#juno 7065464664). Decides whether O5's
  items are only counted or also ruled.
  - Option 1 (the default if Joel declines): report counts and kinds only, and
    declare their rulings UNMEASURED.
  - Option 2: Joel rules them alone, as an endpoint different from the frozen
    text. Then this text fixes, before any head is enrolled (#claude 7065849509
    note 3, #A1 7065881793), a budget of `____` items or `____` hours and the
    order `____` in which he rules them. Option 2 also needs a reviewed change
    that adds a ruling kind to the ledger, since its kind set is closed.
- **J2. Criteria** (the criteria are Joel's rulings, lines 452-458). Decides
  how C3, C5 outside clean heads and C7 are treated: report-only like C8,
  planted heads only, or ruled by Joel under J1. Also decides whether the pilot
  can pass with any criterion UNMEASURED. Default if he declines: all three are
  UNMEASURED, and the pilot cannot pass with one. `pilot-score.py` implements
  only this default, so any other answer needs a reviewed change to it before
  enrollment.
- **J3. The end condition. Ruled, except the count.** Joel, 09:12 PT Nov 22,
  relayed verbatim by #claude in 7082663349:

  > "End condition should be a consistent sequence of PRs that help measure
  > cost and efficiency. Maybe between 5-10"

  So `end_rule` is `pr-sequence`: the pilot ends after one consistent sequence
  of PRs, the same kind of work, chosen because it helps measure cost and
  efficiency, and not on a calendar date. The sequence is 5 to 10 PRs long.
  **Still open:** the exact count, `pr_count` in section 7, which this text
  leaves unfilled. It blocks signing, and until it is set, preflight check 5
  and end condition 4 fail closed.
- **J4. How Joel signs this text as its only signer.** Every agent and Joel post
  as `dayamjz` (#juno 7065464664), and his `fd80055c` signature was a chat
  relay posted by #claude (7051299262). Decides the form preflight check 4
  reads. Default if he declines: none, and the text stays unsigned.
- **J5. Installing and running the scripts.** As pilotseal, Joel installs the
  landed scripts of section 10 into `/Users/pilotseal/bin`, records their
  SHA-256 (the `install` record), and at the end runs scoring and unblinding by
  hand (7065248343 item 3; no sudoers entry). Decides whether he does so.
  Default if he declines: no script runs as pilotseal, and nothing is scored.

**Open before signing, for the coordinator (not owner questions).**

- Who appends `enroll` and `capture`, and to which ledger, now that the
  manifest is under `/Users/pilotseal` (open item 2 of row 8's brief).
- Where chain heads are posted, and who checks them at unblinding (open item 4).
- Whether Joel runs a parse-only check of the real sealed records, as
  pilotseal, printing only how many parse under O1 (open item 3). If none
  parses, C4 is wholly UNMEASURED.
