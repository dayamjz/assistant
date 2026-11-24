# Gate shadow pilot pre-registration, version 2 (no voters), 2026-11-22

**Status: Draft, not signed.** Joel is this text's only signer. It freezes when
he signs it at its signing commit (section 1), with the key of section 13
(section 11, I5). Nothing is enrolled and no shadow run happens
before then. No pilot head has run. Joel's direction of Nov 23 (#A1's relay
7111628611, translated by the coordinator in 7114296700 and checked by #juno
7114355345 and #claude 7114424384) is folded into sections 5 to 17, with
#A1's further corrections of 7114817207 (r2), #juno's addition of 7115601439
(r3) and Joel's answers to the owner inputs, relayed verbatim by #claude in
7121929832 (r4).

Notation is the original's (its lines 26-34): `a#N` is `dayamjz/assistant`,
`ft#N` is `dayamjz/fleet-tools`, and a bare 10-digit number is an issue comment
id
(`gh api repos/dayamjz/fleet-tools/issues/comments/<id>`). "The original" is
`docs/evaluations/2026-11-17-gate-shadow-pilot-preregistration.md` at commit
`fd80055c522ea2d6b135a30d596e830e63132b97`, and "line N" without a file is a
line of that text. "O1" to "O5" are the oracle rules of section 5, "J1" to "J5"
the original owner questions of section 11 (now answered or narrowed), "I1" to
"I8" the owner inputs (section 11), "E1" to "E5" the exclusion rules
of section 12 and "F1" to "F6" the script follow-ups of section 17.

**Where an `ft:` path is.** In the sections kept by reference, an `ft:` path is
at fleet-tools `5b1b73d2`, as the original says (its line 28). In this text's
own sections, an `ft:` path is at fleet-tools `main`, except the four pilot
scripts. Every "as built" statement about them describes them at these
commits:

- `pilot-ledger.py` at `acdb837b0c8e297c718376c1c3dc42cb55c32e34`, ft#790's
  merge on `main`;
- `pilot-unblind.py` at `9cd73498ec4d1172bb45a5df4c2bf87bd14a6990`, ft#792's
  approved head, not yet landed;
- `pilot-score.py` at `9b2d4e2719ae17d40a1e4baa26aaeca8cfa5839c`, ft#793's
  approved head, not yet landed;
- `pilot-preflight.py` at `6adbd531c6402c0f19a8f531f2abcfaf6ced61f8`, ft#794's
  approved head, not yet landed.

None of the four exists at `5b1b73d2`. Before the signing commit, section 10
pins the landed commits, and each "as built" statement is checked again
against them and updated where it no longer holds.

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
signing commit, with the key of section 13. The four-signature rule of lines 6-11
does not govern this text, so the four signatures of `fd80055c` are not needed
again: Joel's 07:47 ruling above set that rule aside for this change, and his
07:40 ruling (7065065542) bars any item that depends on the voters existing,
which a voter signature would be. Only Joel's key signature counts. A GitHub
comment, a relay or a chat message is never that signature (section 13). This
is the agreed voter-free design, and #A1 confirms it (7114817207): this text is
signed by its owner alone. The independent review that each implementing PR
gets under the landing gates is review of code, not a signature of this text,
and it adds no voter-signature requirement.
Nothing is enrolled before the signature exists and every precondition of
section 9 holds.

**The signing commit.** The signature names one assistant `main` commit, the
signing commit. It must be a commit on `main`'s first-parent history at which
this text holds no `UNRULED` and no `UNPINNED` value, so every owner input is
answered and every pin is filled there. This text may reach `main` earlier as an
unsigned draft, and each later PR that answers an input or fills a pin is still
a draft. Preflight check 4 must verify all three conditions: the commit is on
`main`'s first-parent history, this text there has no `UNRULED` or `UNPINNED`
value, and this text at `origin/main` is byte-identical to this text there. As
built, check 4 refuses every signing form, and these checks are F3. So any
later edit leaves the text unsigned until Joel signs again. Every "after
the signing commit" in this text means after that commit's time.

**The Nov 23 direction.** Joel replied "Agree on your recommendations" to #A1's
ORDER r2 assessment, relayed by #A1 in 7111628611. Its seven points are: (1) an
eligible sequence of 10 consecutive fleet-tools reliability-fix PRs under the
size bound, frozen before enrollment (section 12); (2) Joel adjudicates
unresolved disagreements, with an order and budget frozen before enrollment
(sections 5 and 11); (3) no success while any required criterion is UNMEASURED
(section 5); (4) a locally signed owner artifact agents cannot impersonate
(section 13); (5) a protected append-only enrollment ledger with a narrowly
scoped writer (section 14); (6) deterministic scoring and unblinding under
pilotseal (sections 6 and 8); (7) reconciling the server-protected promotion
requirement with Joel's no-branch-protection decision before any promotion
(section 16). The relay is #A1's report of Joel's reply, and #claude has asked
Joel to confirm it directly (7114424384 point 3). Joel's key signature of this
text is what makes every value folded in here binding.

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
  For a live head, which is now a fleet-tools PR, section 12's "Live heads run
  on fleet-tools" replaces lines 58-60 (the mirror), 61-69 (the review record)
  and 87-91 (the base).
- **Section 2.2's definitions of a disagreement**, lines 121-130 (missed,
  extra, hold the review did not make, pass the review refused) and lines
  135-139 (setup holds are excluded). How two findings match (lines 131-133) is
  replaced by O5.
- **Section 3, case mix and enrollment**, lines 267-352, except line 281 (the
  "Live PRs" row: at least 4, in the order of `REVIEW-REQUEST full` relays),
  which section 12 replaces. Live heads are the eligible sequence, 10 of them,
  in PR creation order.
- **Section 4's capture fields**, lines 354-396 (the field table from
  `head_id` to `review.record_commit`). Rows 397-405 and the example record
  (lines 407-448) are not kept. For a live head, section 12 restates the
  sources of `origin_ci` (line 391) and of `review.head`, `review.kind`,
  `review.reviewer` and `review.verdict` (lines 392-393).
- **Section 5's prerequisites**, lines 485-488.
- **Section 6, known limits**, lines 490-526.
- **Section 8, what does not change**, lines 873-902.
- **Section 7**, lines 528-872, stays as the record of the rulings the kept
  sections cite. It binds nothing in v2 except through those sections.

Sections 2.1, 2.3, 2.4 and 2.5 and the criteria table of section 5 are
superseded. Sections 4 to 8 below restate what replaces them.

## 3. Removed voter steps

One row per non-blank original line in these ranges, named by row 8's build
brief: 6, 43, 49-50, 70-71, 131-133, 158-165, 203, 213, 218-265, 397-405,
410-411, 421-423, 443-447, 461, 467 and 472-475. Also one row for every hit of
`grep -n -i -E 'voter|recus|#juno|#A1|#claude|ruling|split'` over lines 1-527.
A row that is not a voter step says "kept, quote only". Each quote is that
line's exact text, trimmed, with `|` written as `\|`. `ft:pilot-preflight.py`
check 3 compares this table with that grep and with the original's bytes.

| original line | quote | v2 replacement |
| --- | --- | --- |
| 5 | listed as open, has an owner ruling, and sections 7.11 and 7.12 own those | kept, quote only: names the owner rulings v1 folded in |
| 6 | rulings. The text freezes only when Joel and the voters (#juno, #A1, #claude) | replaced: v2 freezes when Joel, its only signer, signs it at its signing commit (v2 section 1) |
| 9 | rulings (Joel 7044130931, 7046850989). Merging is not what freezes it; the four | replaced: no four signatures; Joel's one signature freezes v2 (v2 section 1) |
| 18 | disagreement gets a ruling (gate right, review right, both, neither) under rules | replaced: live-head disagreements are counted by kind (O5) and ruled by Joel alone within the budget (J1 option 2) |
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
| 104 | ### 2.1 The four-way ruling | replaced: no voter rules; the four-way meanings are kept only as the vocabulary of Joel's `ruling` lines, within the frozen budget and order (J1 option 2, v2 section 8) |
| 106 | Each disagreement gets exactly one ruling. "Real" means the described defect | replaced: no voter rules; the four-way meanings are kept only as the vocabulary of Joel's `ruling` lines, within the frozen budget and order (J1 option 2, v2 section 8) |
| 110 | \| Ruling \| Means \| Worked example \| | replaced: no voter rules; the four-way meanings are kept only as the vocabulary of Joel's `ruling` lines, within the frozen budget and order (J1 option 2, v2 section 8) |
| 112 | \| **Gate right** \| The gate's position holds and the review's does not. \| The gate raises that a refusal in `internal/cli` is reached by a path the review did not read; the voters reproduce it at the head. The review approved. \| | replaced: no voter rules; the four-way meanings are kept only as the vocabulary of Joel's `ruling` lines, within the frozen budget and order (J1 option 2, v2 section 8) |
| 117 | A matched item is not a disagreement and gets no ruling. A new defect found | replaced: no voter rules; the four-way meanings are kept only as the vocabulary of Joel's `ruling` lines, within the frozen budget and order (J1 option 2, v2 section 8) |
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
| 213 | Each voter assigns one of `blocker`, `should-fix`, `nit` or `not-real` to every | replaced: no voter sets severity; Joel's `ruling` lines carry this scale for the items he rules (v2 section 8); elsewhere severity is UNMEASURED and the raw labels are reported |
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
  v1's C1, C3, C5 or C7, and they are never pooled with any v1 result:
  - C1's live case is 10 fleet-tools PRs, where v1's was at least 4 assistant
    PRs;
  - C5's clean part counts any `review` hold on a clean head (O2). At
    `fix_rounds.review: 0` every review finding becomes a hold (the original's
    lines 341-342), where v1 counted only a hold decided `review right` with
    severity `not-real`;
  - C3, the rest of C5, and C7 are ruled by Joel alone, not blind (section 6),
    where v1 had three blind voters.
- **Comparability with row 11.** Row 11, the caliper Q1 agentic eval
  (`ft:notes/row11-design-a896-6953845110.md`), also scores against an oracle
  the agent under test cannot read (its review hook and prompt are in
  `sandbox.forbidden_files`, note line 92). v2's planted-head scores are
  oracle-scored in that same sense, but on a different subject and case mix,
  so no rate carries over between them.
- **Adjudication: no voters.** `found_in_adjudication` (lines 117-119) has no v2
  counterpart, and it is UNMEASURED. Joel's rulings under J1 option 2 (section
  11) are a different endpoint, not v1's three-voter adjudication.

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

**K = 3.** A finding within 3 lines of a changed line counts as at that line.
K is a fixed rule. It does not depend on how much context the sealed record's
diff shows. 3 was chosen because it equals git's default unified context.

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
    no file with a new side), or `git apply --numstat -z`, run outside any
    repository, does not accept the diff, or a truth path is not one of the
    file names git reports, or a truth path is absent from the head's tree
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
  - Joel rules these items alone (J1 option 2, relay point 2), within the
    48-hour budget and in the order of I2. An item past the budget is
    UNMEASURED, never decided by default.
- **UNMEASURED by script:** severity (2.4; the raw gate and review labels are
  reported), except where a `ruling` line sets it (section 8); race and
  recovery correctness (C7), unless Joel rules it under J1 within the budget
  (I3, answered: required); the C4 specs' own prose scoring rules in s02 and
  s08 (O1 still applies to the heads built from them); and
  `found_in_adjudication`.

**Criteria.** Every criterion is computed over the primary run set, at
`fix_rounds.review: 0` (the original's lines 340-347). Which heads each one
covers:

- **Compared heads only:** C1 counts them. C3, C5 outside clean heads and C7
  use the items of compared heads.
- **Every enrolled head of the case named:** C2 (every head), C4 (planted
  real-finding and hold heads), C6 (CI-failure heads) and C8 (every head).
- **C5's clean part:** every enrolled clean head except a `base_mismatch` one,
  since the original's line 315 leaves those out of C5. As built,
  `pilot-score.py` also applies O2 to a `base_mismatch` clean head (F4).

The original's line 315 leaves a `base_mismatch` head out of C1, C3, C5 and C7
only, which this split follows. The `default-1` run is captured apart (section
7), reported apart, never pooled, and decides nothing. This restates the
original's lines 455-457, with the per-criterion scope above in place of their
"compared heads". Each criterion is required or report-only, as the table
says. A criterion that is UNMEASURED cannot pass. The pilot passes only when
every required criterion, C1 to C7, has a measured PASS. No success is
declared while any required criterion is UNMEASURED (relay point 3, which
answers J2's second half), and no report calls the pilot successful in that
state. As built, `pilot-score.py` prints `pilot: PASS` only when C1 to C7 all
PASS and `NOT PASSED` otherwise. Its self-test row "an UNMEASURED criterion never
passes: every scripted criterion PASS, the pilot still NOT PASSED" shows it.

| # | v1 definition | v2 definition | Decided by | Required |
| --- | --- | --- | --- | --- |
| C1 | line 461 | At least 10 compared heads, with every case of the original's section 3 at its minimum among them. | script | required |
| C2 | line 462 | `gate.activated` is true for every enrolled head. A head with no gate capture is not activated. | script | required |
| C3 | line 463 | Over every `missed`, `pass-refused` and `unlocatable` item of the compared heads: FAIL when any is ruled `review right` with severity `blocker`; PASS when every one is ruled and none is; otherwise UNMEASURED. | Joel's `ruling` lines under J1 within the budget (I3), applied by script | required |
| C4 | line 464 | Every planted real-finding and hold head has a `planted_match` that is a finding id (O1). FAIL on any `null`; UNMEASURED when none is `null` and any is `unmeasured`. | script | required |
| C5 | line 465 | FAIL when any clean head has a false hold (O2), or when more than one compared head that is not clean has a `hold-not-made` item ruled `review right` with severity `not-real`. PASS when neither holds and every such item is ruled; otherwise UNMEASURED. | script for clean heads; the rest Joel's `ruling` lines under J1 within the budget (I3), applied by script | required |
| C6 | line 466 | O4 on every CI-failure head. FAIL on any failure; UNMEASURED when none fails and any is unmeasured, or when no such head exists. | script | required |
| C7 | line 467 | Over every race and recovery head's outcome item: FAIL when any is ruled not `correct`; PASS when every one is ruled `correct`; otherwise UNMEASURED. | Joel's `ruling` lines under J1 within the budget (I3), applied by script | required |
| C8 | line 468 | Report only, with no pass mark: the count of heads missing `gate.usage` or stage times. | script | report-only |

Until `pilot-score.py` reads Joel's rulings (F4), C3, C5 outside clean heads and
C7 stay UNMEASURED, so under the rule above the pilot cannot pass.

C3 includes `unlocatable` items, so a review blocker located as a file only,
or from a review with no findings block, still reaches C3: Joel rules whether
the gate covered it. v1 counted such a finding as missed under its same-defect
match (line 131).

C1's live case minimum is 10 (section 12). C4 is the original's line 464 as
written: it tests `planted_match` alone. O3 is reported for each hold head beside
C4, as `pilot-score.py` already prints it, and it is not a criterion. A hold head
whose planted finding matched but caused no `review` hold passes C4 and shows a
miss under O3.

**If the criteria are not met,** the gate is not promoted, and each head behind
a failed criterion is filed as its own issue on this repository with its scores,
ordered by the number of failed criteria it appears in, then by head id. The
original's lines 479-483 order by decided severity, which v2 has only for the
items Joel rules.

## 6. Storage and blinding under c251 option A

- **Where.** The sealed records and the sealed ledger stay in
  `/Users/pilotseal/pilot`, owned by the macOS user `pilotseal`, under c251
  option A (Joel 7065228773; plan 7065248343, amended 7065409328). This
  replaces the original's "private fleet-tools pilot directory" (Q10(a), lines
  168 and 622): option A is the authority for the change.
- **Who can read them.** Only pilotseal can read them. The evidence so far is
  the different-uid denied-read test as uid 501 (7065533093) and the completed
  option A checks (7066230158). Enrollment needs a fresh denied-read record
  made after the signing commit (section 9, precondition 6), in the record shape
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
  Scoring needs git on pilotseal's `PATH` (O1 above); with no git it refuses
  and logs `score-refused` rather than marking heads UNMEASURED. This is relay
  point 6. Both are deterministic: the early-unblinding refusal is shown by
  `pilot-unblind.py`'s self-test rows, and "no agent or voter judgment" by
  `pilot-score.py`'s rows "the dry run makes no agent or network call" and "the
  CLI dry run calls no roles.sh, claude, codex, cursor-agent, gh or curl".
- **Preflight runs the same way.** `pilot-preflight.py check`, which verifies
  Joel's signature and appends the `preflight` line, runs only as pilotseal,
  from the `/Users/pilotseal/bin` copy whose SHA-256 the trust file of section
  13 pins,
  so no agent can edit the checker that verifies the signature. Its `probe`
  mode is the one exception, by design: it runs as uid 501 to show that uid 501
  is denied (section 9, precondition 6), from a copy of the same pinned blob.
  Making `check` refuse to run as any other user is F3.
- **Joel and the sealed records.** The original's rule (lines 197-200) is kept
  and applied to Joel as the only signer, and as the only ruler under J1
  option 2: he does not open a sealed record before the end condition of
  section 8 holds and `pilot-unblind.py` has published it. Any earlier access
  is logged as an `access` line in the ledger, and the head is marked.
- **Residual gaps.** The original's (lines 202-207) are kept, except that
  step 4's voter order no longer exists. Two are added:
  - Root, or Joel through `sudo`, can read the records or rewrite the ledger,
    and only the posted chain heads of section 7 detect a rewrite.
  - **Joel's rulings are not blind.** v1's packet hid each item's side, the
    severity labels and the reviewer, and unblinded only after the rulings
    (the original's lines 154-165). v2 has no packet. Each item Joel rules
    carries its kind (`missed`, `extra`, `hold-not-made`, `pass-refused`),
    which says which side raised it, and its raw labels and reviewer. Joel also
    owns the gate under test. So a PASS of C3, of C5 outside clean heads, or of
    C7 rests on his unblinded ruling, and the report says so beside each of
    them.

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
  needs an earlier `enroll` of its `head_id`. **`close` seals enrollment and
  captures:** in the landed set, only `score`, `score-refused`, `unblind`,
  `unblind-refused`, `access` and `correction` may be appended after it, and
  F1 adds `items` and `ruling` to that list, so no head,
  freeze, capture or preflight can join after the end. Nothing but a
  `correction` may follow `unblind`.
- An append locks, verifies the whole chain, writes one line with `O_APPEND`
  and syncs it. A caller may name the chain head it expects, and the append
  then refuses, under the lock, if the ledger has moved past that head. A write
  that fails part way is truncated back to the size the append verified. No
  mode rewrites or truncates a ledger.
- **A `correction` annotates and changes no result.** It names the bad `seq`
  and says what is wrong with it. No script changes an enrollment, a case, a
  capture, the end condition, a score or a publication because of one. The
  line it names keeps its effect, and the correction is reported beside it
  (F4). So a mistake that would change a result is reported, never repaired,
  and no post-hoc line can move a result.
- `head <ledger>` prints `head <seq> <sha256 of the last line>`.

**Bodies.**

- `enroll`: `head_id` (`hNN`), `case` (`clean`, `real-finding`, `hold`,
  `ci-failure`, `race`, `recovery` or `live`), `provenance` (`live`,
  `replayed` or `planted`), `sealed_id` (`sNN` for a planted head, else
  `null`) and `work_kind` (for a live head, its kind of work, which must equal
  `pr_kind` for the head to count toward the end condition), plus the
  original's manifest fields (lines 306-330). A live head's `enroll` also
  records section 12's evidence: `pr` (its fleet-tools PR number),
  `pr_created_at` (GitHub's `created_at`), `enroll_head` (the 40-hex head it is
  enrolled at), `code_lines` (the pinned `review-size.sh` at that head),
  `incident` (the issue number and that issue's `created_at`) and `rule`
  (`eligible`).
- `exclude`: not in the landed kind set. Section 12 needs it, and F1 adds it,
  unique per `pr`, with the same evidence fields and the exclusion rule (`E1`
  to `E5`) in place of `rule`.
- `items` and `ruling`: not in the landed kind set either, and F1 adds both.
  They are allowed after `close`, since Joel rules only once enrollment is
  sealed (section 8). `items` is unique per ledger and lists the items Joel
  may rule. Each `ruling` names one item, its ruling and its place in the J1
  budget.
- `freeze`: `head_id`, `frozen_commit` (40-hex) and `raw[]` of
  `{file, sha256}`, one per capture file of the head.
- `preflight`: what `ft:pilot-preflight.py check` verified, including this
  signing commit and the parameters below. The end rule and K are read
  from the last `preflight` line before the first `enroll`, so they are fixed
  before any head is enrolled.
- `close`, `score`, `unblind`, `unblind-refused`, `score-refused`,
  `denied-read`, `install`, `access`, `correction`: as their scripts write them
  (section 10).

**Captures.** For each head the operator writes, under `<captures>/<head_id>/`:
`gate.json` (the original's section 4 fields, lines 361-396, plus `pin_check`,
`pass` or `fail`, the item 3 pin check of line 332), `review.md` (the review
record, for a head that has one) and `tree.txt` (`git ls-tree -r -z --name-only
<head>`: NUL-separated, so no path is quoted). Each is listed in that head's
`freeze` `raw[]`, and the scripts read no capture whose bytes differ from it.
The `default-1` run of a head goes under
`<captures>/<head_id>/default-1/`, with its own `gate.json`, and is listed in the
same `raw[]`. `pilot-score.py` reads only the file named `gate.json`, so it
never scores or pools the `default-1` run. Reporting that run apart is F4.

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
pr_count: 10
pr_kind: fleet-tools-reliability-fix
j1: option-2
j1_budget: 48 hours
j1_order: UNRULED I2
j4: UNRULED I5
```

`k` is a whole number; `end_rule` is `pr-sequence` (J3, section 11); `pr_count`
is 10, the number of compared live heads that ends that sequence (Joel,
7083761337; section 12); `pr_kind` is
`fleet-tools-reliability-fix`, the kind relay point 1 names, defined in section
12; `j1` is `option-2` (relay point 2); `j1_budget` is `48 hours` (Joel,
7121929832, answer 3); `j1_order` stays `UNRULED` until Joel names the order
(I2); `j4` names the signing form, which stays `UNRULED` until I5. The size
bound and the exclusion rules have no key here: they are section 12's text,
covered by Joel's signature of this text. No deadline applies until Joel says
otherwise (I4, still open), so the end rule has no date key. If he adds one
before signing, this block gains its key (F5).

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
   PRs, the same kind of work, chosen to measure cost and efficiency. It holds
   once 10 heads of provenance `live` whose `work_kind` equals `pr_kind`
   (`fleet-tools-reliability-fix`, section 12) are frozen **and compared**
   (section 5), so C1's live minimum can always be met; planted heads never
   count. As built, `pilot-unblind.py` counts frozen live heads, not compared
   ones, so F6 must land before activation (section 9, precondition 12). The
   condition fails closed if the parameters ever stop naming a kind or a
   count of 10, so nothing is scored or unblinded then. No calendar deadline
   ends the pilot until Joel says otherwise (I4, still open: he wrote "No
   deadline, maybe 48 hrs" in 7121929832, and #claude asked him which he
   means).
5. For unblinding only: a `score` line exists after the `close`.

**Refusal.** A refused unblinding appends `unblind-refused` with the reason. A
refused scoring appends `score-refused`. So an early attempt is logged, as the
original's lines 199-200 require. The one refusal that cannot be logged is a
ledger that does not verify (condition 1), since nothing can be appended to a
broken chain: it is printed, and the posted chain heads show where the chain
broke. A refusal prints no record content and publishes nothing.

**Joel's rulings come before the score.** After `close`, `pilot-score.py items`
(F4) appends the one `items` line. It lists every O5 item of every compared
head except `matched` ones, each `unlocatable` finding as its own item, planted
heads included, and the outcome of every race and recovery head
for C7. Each item gets an id. For a planted head it reads that head's captures
(`gate.json`, `review.md`), never its sealed record, so listing it opens no
sealed record and prints no score. Joel then rules items in the I2 order, one
`ruling` line each, until the budget is spent. Only then is the one `score`
line appended. It reads the `ruling` lines and applies section 5's C3, C5 and
C7 rules to them. Every item without a ruling is UNMEASURED. Since `items` and
`ruling` are allowed after `close` (F1), and the `score` comes after both,
C3, C5 outside clean heads and C7 can be measured.

**The `ruling` line.** Its vocabulary is the original's, by reference:

- `subject` is `item` (an id from the `items` line) or `classification` (a PR
  number, for section 12's disputes);
- for an O5 item, `outcome` is one of the four-way rulings, with the meanings
  of the original's lines 106-115 (`gate right`, `review right`, `both`,
  `neither`), and `severity` is one of the original's line 213 scale
  (`blocker`, `should-fix`, `nit`, `not-real`);
- for a race or recovery outcome, `correct` is `true` or `false`;
- for a classification, `class` is `eligible` or one of E1 to E5;
- `budget` records where the line falls in the J1 budget, and the script
  recomputes it from recorded bytes. The budget is 48 hours (Joel,
  7121929832, answer 3): the window opens at the `at` of the `items` line and
  lasts 48 hours. An item ruling is inside it when its `at` falls in the
  window. A classification ruling comes before that line, so it is not timed;
  it is inside, and the report counts it apart.

**What the budget costs.** Since the budget is hours, classification rulings
do not draw on the window, and O5 items are limited only by time. An item Joel
has not ruled when the window closes is UNMEASURED, so C3, C5 outside clean
heads and C7 can stay UNMEASURED and the pilot then cannot pass. Joel may
leave any dispute unruled, which costs nothing (section 12).

No voter rules, and no split vote exists. The four-way meanings and the
severity scale serve only as the vocabulary of Joel's lines.

**Scoring.** `ft:pilot-score.py` scores only after the end condition holds, so
no score is visible mid-pilot to steer enrollment. It appends one `score` line
and prints only scores, ids and counts, never a sealed record's bytes.

**What `close` fixes, and what each script is bound to.** `close` fixes the set
of heads and their captures: after it, no `enroll`, `freeze`, `capture` or
`preflight` may be appended. Lines that are meant to come after it still can:
`items` and `ruling` (F1), `score`, `score-refused`, `unblind`,
`unblind-refused`, `access` and `correction`. So the score covers every head
that was enrolled, and it also reads the `ruling` lines written after `close`.
Each of `pilot-score.py` and `pilot-unblind.py` is bound to the chain head it
read when it started. It reads that head before the entries, and appends its
`score` or `unblind` line only if the ledger is still at that head (the
expected-head append of section 7). If any line was written in between, it
refuses and logs the refusal, and unblinding publishes nothing.

**Publishing.** `pilot-unblind.py run` copies the sealed files and the ledger
into a staging directory beside the publish directory, recomputes each copy's
SHA-256 against the s01-s08 table of the original (read from blob `32424c20`,
which it checks), and refuses on any mismatch. Only then does it append
`unblind` with the published hashes and the chain head it checked, and only
after that line is written does it move the copies into the publish directory,
so nothing is published unlogged. A second run refuses with "already unblinded".
After that, the records are copied to `docs/evaluations/` in this repository,
as the original's line 173 says.

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
4. Joel's signature of this text is recorded, naming the signing commit, which
   meets section 1's three conditions, and made with the key pinned in the
   trust file of section 13 (I5). Until `j4` is ruled this check fails
   with "J4 unruled". Once it is ruled, it still fails with "has no check yet"
   until F3 lands the verification, so no enrollment can pass this check before
   then.
5. J3 is ruled: the `pr-sequence` end rule with `pr_count` 10 and a named
   `pr_kind`. J1 is ruled, and since it is option 2, this text states a numeric
   budget and an item order (I2).
6. A denied-read record that passes brief 5b's rule 4 exists, dated after this
   signing commit: run as uid 501, the uid agents run as, against targets owned by
   a different uid, never as root, and every probe denied with `EACCES`. It
   must probe `/Users/pilotseal`, `/Users/pilotseal/pilot` and
   `/Users/pilotseal/pilot/sealed/s01.md`, each with an open and a directory
   probe, and nothing outside `/Users/pilotseal`. `pilot-preflight.py probe`
   makes it: for each target it opens it read-only and closes it without reading
   a byte, and opens it as a directory without listing an entry. It then stats
   the target for its owner uid. If that stat succeeds, the owner comes from
   the target and no stat probe is recorded. If it is denied, the denied stat is
   recorded as a probe, and the owner comes from the nearest ancestor it can
   stat.
7. The `install` record shows that the SHA-256 of each script in
   `/Users/pilotseal/bin` equals the blob pinned in section 10 (J5), and the
   trust file of section 13 holds the same hashes.
8. Section 12's eligibility rule is in this text at the signing commit.
9. Each owner record of section 13 that the pilot relies on is signed with the
   pinned key, and if the private key is a file, a denied-read record as uid
   501 against its location exists (section 13).
10. A write-denied record as uid 501 against the sealed ledger and the trust
    file exists, dated after the signing commit (sections 13 and 14).
11. The pinned `review-size.sh` is in the `install` record, at the hash the
    trust file pins. No enroll script exists (section 14).
12. **No activation before review and reconciliation** (#A1 7114817207). Every
    script this text pins, and every follow-up of section 17, has landed
    through the landing gates with its review. Every landing requirement that
    changed between this text's signing and the first `enroll` has been
    reconciled explicitly: either this text is amended and re-signed to adopt
    it, or a signed owner record says it does not apply to the pilot (section
    15).

13. The fleet-tools mirror and its gate home of section 12 exist, isolated
    like the assistant mirror (the original's item 9 and lines 349-352), with
    the gate's first-run path proven on it.
14. **The pin is out of the repo and the copies match it** (#juno 7115601439).
    Preflight reads the key fingerprint and the executable hashes from the
    trust file at section 13's location (I8), never from this text. Each copy
    in this text must equal the trust file: the fingerprint in section 13's
    copy block, section 10's table, and section 12's row. Any
    mismatch, or a trust file that is missing or unreadable, refuses
    enrollment.

On success it appends a `preflight` line. It never enrolls. Items 8 to 14 are
not yet checked by `pilot-preflight.py`; F3 adds them. Until F3 lands, item 4
refuses every signing form, so no enrollment can pass preflight without them.

## 10. Scripts

| script | fleet-tools path | merge commit | blob SHA-256 |
| --- | --- | --- | --- |
| ledger | `pilot-ledger.py` | UNPINNED | UNPINNED |
| unblinding | `pilot-unblind.py` | UNPINNED | UNPINNED |
| scoring | `pilot-score.py` | UNPINNED | UNPINNED |
| preflight | `pilot-preflight.py` | UNPINNED | UNPINNED |
| review findings, used by scoring | `review-findings.py` | UNPINNED | UNPINNED |

By the signing commit each row names the fleet-tools merge commit that
landed the script and the SHA-256 of its bytes there. The copies Joel installs
in `/Users/pilotseal/bin` must hash to those values, and preflight check 7
compares them. These rows are a published copy. The authoritative hashes are
in the trust file of section 13, outside any repository an agent can write,
and the two must agree (F3).

## 11. Owner questions for v2

This text is not signed while any input below is open. Once Joel answers, his
answers are quoted here verbatim with their comment ids, as a#977 folded in
section 7.12, and his key signature of this text (section 13) is what makes
them binding.

**Answered or narrowed by the Nov 23 direction** (#A1's relay 7111628611,
translated in 7114296700):

- **J1. Live-head disagreements: option 2.** Relay point 2: Joel adjudicates
  unresolved disagreements, with the order and budget frozen before
  enrollment. So O5's items are ruled by Joel alone, as an endpoint different
  from the frozen text, and an item past the budget is UNMEASURED. The budget
  is 48 hours (I2, answered); the order is still open (I2). A disputed
  eligibility classification (section 12) is also a J1 item, ruled but not
  timed (section 8). Ruling needs a ledger kind the landed closed set lacks
  (F1).
- **J2. Criteria: answered.** Relay point 3: no success while any required
  criterion is UNMEASURED (section 5). The first half: C3, C5 outside clean
  heads and C7 are required, ruled by Joel under J1, and UNMEASURED past the
  budget (I3, answered).
- **J3. The end condition: count and kind answered.** Joel, 09:12 PT Nov 22,
  relayed verbatim by #claude in 7082663349:

  > "End condition should be a consistent sequence of PRs that help measure
  > cost and efficiency. Maybe between 5-10"

  Joel, 10:38 PT Nov 22, relayed verbatim by #claude in 7083761337, answering
  whether the count is 10 (so C1's 10-head minimum stays measurable) and
  whether the kind of work is named before signing:

  > "1.agree
  > 2. Yes"

  Relay point 1 names the kind: fleet-tools reliability-fix PRs, so `pr_kind`
  is `fleet-tools-reliability-fix` (section 12). The pilot ends at 10 eligible
  PRs (Joel, 7121929832, answer 5). Whether a deadline applies is still open
  (I4).
- **J4. How Joel signs: direction answered.** Relay point 4: a locally signed
  owner artifact that agents cannot impersonate (section 13). Which key and
  where its public key is pinned are open (I5).
- **J5. Installing and running the scripts: answered.** Relay point 6:
  deterministic scoring and unblinding under pilotseal (sections 6 and 8). As
  pilotseal, Joel installs the landed scripts of section 10 into
  `/Users/pilotseal/bin`, records their SHA-256 (the `install` record), and at
  the end runs scoring and unblinding by hand (7065248343 item 3; no sudoers
  entry).
- **v2's signers: answered, not an input.** Joel alone, with his key (section
  1, "Signing"). Requiring the four `fd80055c` signatures again would make v2
  depend on the voters, which his 07:40 ruling (7065065542) bars. #A1's r2
  (7114817207) confirms it.

**Joel's answers of Nov 23.** #claude put the inputs to Joel at 19:59 PT,
numbered as 7114296700 lists them after a first question confirming #A1's
relay, and relayed his reply verbatim in 7121929832 (20:20 PT):

> 1. Yes
> 2. 300 or less
> 3. Let’s do 48hrs
> 4. Required
> 5. No deadline, maybe 48 hrs
> 6. Recommend
> 7. I’ll add them
> 8. I can name a substitute

Answer 1 confirms #A1's relay 7111628611 as owner direction. Answers 2 to 8
are 7114296700's inputs 1 to 7, which are I1 to I7 here. Read narrowly, as
#claude did:

**Owner inputs (Joel).** These are the only questions this text asks. Each
keeps its number, answered or not.

- **I1. Size bound: answered, at most 300.** Eligible PRs have at most 300
  code lines by `review-size.sh` (answer 2, "300 or less"). This replaces the
  interim "fewer than 300" of #A1's r2 (7114817207), so the pilot bound and
  the landing cap now match.
- **I2. J1 budget and order: the budget is answered, the order is open.** The
  budget is 48 hours (answer 3), which fills `j1_budget` (section 7). The
  order Joel rules live-head disagreements and disputed classifications in is
  still open, and fills `j1_order`.
- **I3. J2 first half: answered, required.** C3, C5 outside clean heads and
  C7 are required criteria, ruled by Joel under J1 within the budget, and
  UNMEASURED past it (answer 4), so a pilot with any of them UNMEASURED is not
  a pass.
- **I4. J3 deadline: open.** The pilot ends at 10 eligible PRs. On a
  deadline Joel wrote "No deadline, maybe 48 hrs" (answer 5), which is not
  settled, and #claude asked him which he means. Until he answers, no deadline
  applies. A deadline he adds before signing goes into section 8's end
  condition and the parameters (F5).
- **I5. J4 key custody: open.** Joel asked for a recommendation (answer 6),
  which #claude is giving him directly. Which key Joel signs with is not
  chosen. Its public key is pinned in the trust file of section 13. This fills
  `j4` and the fingerprint copy.
- **I6. The writer of section 14: answered, Joel by hand.** Joel adds the
  enrollments himself, as pilotseal (answer 7). No script enrolls a head.
- **I7. The promotion conflict of section 16: direction answered, substitute
  not named.** Joel will name a local substitute for the server-protected
  required check (answer 8): a locally enforced required check plus a
  break-glass log agents cannot alter. He has not named it yet. When he does,
  this text records it as his change to ORDER r2 item 13. Until then section
  16's prerequisite is unmet and promotion stays blocked.
- **I8. Where the trust file lives: open** (#juno 7115601439). It was not
  among the questions put to Joel on Nov 23. It must meet section 13's rules:
  outside every repository and every agent's home, not writable by uid 501,
  and written only by Joel. A path under `/Users/pilotseal` would meet them,
  as would another location Joel names. This fills `trust_file` in section
  13's copy block.

**Open before signing, for the coordinator (not owner questions).**

- Who appends `capture` and `freeze`, and whether uid-501 captures go to a
  second, public ledger (open item 2 of row 8's brief). Joel appends `enroll`
  and `exclude` himself (I6).
- Where chain heads are posted, and who checks them at unblinding (open item 4).
- Whether Joel runs a parse-only check of the real sealed records, as
  pilotseal, printing only how many parse under O1 (open item 3). If none
  parses, C4 is wholly UNMEASURED.

## 12. The eligible sequence

Relay point 1, with #juno's fixes 1 to 3 (7114355345) and #claude's point 3
(7114424384). This rule is frozen at the signing commit. A PR is
left out only by a rule written here.

- **The window.** The live window opens at the `at` of the **first**
  `preflight` line in the ledger, so a later preflight never moves it. It
  closes at the `at` of the first `freeze` after which 10 enrolled live heads of
  the kind are compared heads (section 5). A PR is in the window when its
  GitHub `created_at` falls in it. Enrollment continues in order until then, so
  an enrolled PR that is never compared, for example one closed before review,
  stays enrolled and reported, and a later PR in the order makes up the 10. No
  PR is replaced: the uncompared one keeps its record.
- **The live case.** This replaces the original's line 281: the live case's
  minimum is 10 compared heads, and its heads are this sequence. As built,
  `pilot-score.py` still holds the original's live minimum of 4, so F4 must land
  before activation.
- **Live heads run on fleet-tools.** This replaces the original's lines 58-60,
  61-69 and 87-91, and the sources at lines 391-393, for a live head:
  - **Mirror.** The gate runs against a private mirror of `dayamjz/fleet-tools`,
    never `dayamjz/fleet-tools` itself, with its own gate home. The stage-model
    pin (the original's lines 324-328) and the intent sentence (lines 319-323)
    are the same as for every other head (section 9, precondition 13).
  - **Base.** The merge base of the head with fleet-tools `origin/main`. The
    operator resets the fleet-tools mirror's `main` to it before the push, and
    the original's base check (lines 310-316) applies unchanged.
  - **Review record.** The full verdict at that head under the landing rules is
    a line of `ft:reviews/ft-<N>.verdicts` of the form `--- verdict ft#<N> at
    <short sha> (full): VERDICT: ...`. When a head has several full verdicts,
    the last such line in `.verdicts` is the review, as in the original's lines
    68-69. The review it records is the file whose line 1 is
    `reviewed at <40-hex>` for that head with no `(delta from ...)`:
    `ft:reviews/ft-<N>.md` while it is the latest review, otherwise its archived
    copy `ft:reviews/ft-<N>-full-<short sha>.md`. Its line 2,
    `reviewer: <reviewer> <model> ...`, gives `review.reviewer`. The verdict is
    what `ft:review-findings.py --verdict` prints, as for any other head. If no
    such file exists, or its verdict differs from that last line, the head has
    no review, and its `review.verdict` is `none`.
  - **CI.** `origin_ci` is
    `gh api repos/dayamjz/fleet-tools/commits/<head>/check-runs`. Fleet-tools
    runs no CI workflow today, so that list is empty, and the gate's
    `checks_basis` on the mirror is `no-ci-declaration`. No live head is a
    CI-failure head, since C6's heads are planted.
- **The kind (`fleet-tools-reliability-fix`).** A `dayamjz/fleet-tools` PR
  fixes a reliability defect when its body's `Observed incident` field (the
  freeze field every fix-shaped PR carries) links a `dayamjz/fleet-tools`
  issue whose `created_at` is earlier than the PR's `created_at`.
- **Who classifies.** Joel, the writer of section 14 (I6), applies this test
  and records the evidence (the issue number and both `created_at` values) in
  the `enroll` or `exclude` line. These criteria are frozen at the signing
  commit.
- **Disputes go to Joel.** Anyone, Joel or an agent, may dispute a
  classification, and the dispute goes to Joel under J1 (section 8 says what
  it costs in the budget). A dispute never blocks enrollment. When the writer
  comes to record a PR, it follows a `classification` ruling for that PR if
  one is in the ledger and inside the budget, and otherwise applies the test
  above. So a dispute Joel has not ruled, or ruled past the budget, leaves the
  test's classification standing, and the dispute is reported. Once a PR's
  `enroll` or `exclude` line exists, its classification never changes, and no
  enrolled PR is removed or replaced by another. A later ruling is recorded
  and reported beside it.
- **Size.** Eligible PRs have at most 300 code lines (I1, answered: "300 or
  less", 7121929832), measured by the pinned `review-size.sh` at the
  enrollment head: the PR head the `enroll` or `exclude` line records, before
  its first review.
  A PR that grows past the bound after it is enrolled stays enrolled. The
  report lists it with both sizes, and it is never quietly dropped.
- **Order.** Live PRs are taken in order of GitHub's `created_at`, which is
  immutable, with ties broken by PR number, lower first. They are never
  ordered by a relay or request, since the coordinator controls when it relays
  (#juno fix 1). "Consecutive" means the eligible PRs in the window are
  enrolled in this order, none skipped, until 10 enrolled live heads are
  compared (the window above). That can be more than 10 enrolled PRs.
- **Every PR gets a record.** Every PR created in the window gets an `enroll`
  or `exclude` line before its first review. No `enroll` may be written while
  any PR earlier in that order has no record. So a PR with no record blocks the
  next enrollment.
- **Reconciliation.** Before each `enroll` and before `close`, the writer lists
  every `dayamjz/fleet-tools` PR created in the window, open or closed, from
  GitHub, and checks that each has exactly one `enroll` or `exclude` line. A PR
  with none, or with two, refuses the `enroll` or the `close`.
- **The exclusion rules.** These are the only ones. Each `exclude` line names
  its rule:
  - **E1.** It fails the kind test above.
  - **E2.** It has more than 300 code lines at the enrollment head.
  - **E3.** It changes a file the pilot pins (section 10, or this section's
    `review-size.sh`).
  - **E4.** It was closed unmerged with no review.
  - **E5.** It was reviewed before its record was written: when the writer
    comes to record it, `ft:reviews/ft-<N>.verdicts` already exists with any
    line. The record states that. The report counts E5 exclusions apart,
    since each one is a lapse in recording, so a PR can't be excluded quietly.
- **The pinned `review-size.sh`.** The fleet-tools blob current when the
  signing commit is made, filled in by then like section 10:

| script | fleet-tools path | commit | blob SHA-256 |
| --- | --- | --- | --- |
| size | `review-size.sh` | UNPINNED | UNPINNED |

## 13. Owner records and the signing key

Relay point 4, with #juno's fix 4 and #claude's point 2.

- **The key.** Joel signs with a key that no agent can read or use: not
  readable by uid 501, and not under any agent's home or `~/Projects`. Agents
  run as uid 501, so uid 501's home is ruled out too.
  Which key, for example an SSH or GPG key on a hardware token or in the
  pilotseal account, is I5. If the private key is a file, a denied-read record
  as uid 501 against its location is made in the shape of 7065533093
  (section 9, precondition 9).
- **The trust file.** The trust root is pinned outside any repository an agent
  can write (#A1 7114817207, #juno 7115601439). Where it lives is I8. It must be
  outside every repository and every agent's home, not writable by uid 501, and
  written only by Joel, as the user that owns it. It holds the public key and
  its fingerprint, the signing form, the signing commit and this text's blob
  there, and the SHA-256 of every executable the pilot runs: section 10's
  scripts and section 12's `review-size.sh`. No enroll script exists
  (section 14).
- **This text holds only copies.** The block below copies the fingerprint, and
  section 10's table and section 12's row copy the executable hashes. No
  script trusts a copy read from a repository. Preflight reads the
  trust file and refuses enrollment when any copy here differs from it
  (section 9, precondition 14). So an agent's PR that edits a copy makes
  preflight fail closed, and the PR can't move the pin.

```pilot-v2-trust-copy
trust_file: UNRULED I8
key_fingerprint: UNPINNED I5
```
- **Key rotation.** A change to the public key in the trust file is valid only
  with a statement signed by the old key that names the new key and the
  signing commit. So neither an agent's PR nor a rewrite of the trust file
  without the old key can swap it.
- **What counts as a signature.** A signature is made with the pinned key over
  the exact bytes of the record, and the record names the signing commit. A
  GitHub comment from `dayamjz`, a relay or a chat message is never one,
  since every agent posts as `dayamjz`.
- **The owner records the pilot relies on.** Each is signed with the pinned
  key, or the pilot cannot rely on it:
  - the signature of this text;
  - the J1 budget and order (I2);
  - the size bound (I1);
  - the end condition (J3 and I4);
  - the writer's identity (I6);
  - the section 16 choice (I7);
  - the trust file's location (I8).

  Each one that is folded into this text before signing is covered by the
  signature of this text. One set or changed after signing needs its own
  signed record, or an amended and re-signed text. Joel's other rulings,
  cited here by comment id, stand as they are.
- **The verifier.** `pilot-preflight.py check` verifies the signature against
  the trust file's key, as pilotseal, from the copy whose hash the trust file
  pins (section 6). F3 builds that check. Until then, check 4 refuses every
  form.

## 14. The enrollment writer

Relay point 5.

- **Where.** `enroll` and `exclude` lines are written only through
  `pilot-ledger.py append`, which hash-chains them and keeps `enroll` unique
  per head, into `/Users/pilotseal/pilot/ledger.jsonl`, which uid 501 cannot
  write.
- **Who.** Joel, by hand, as pilotseal via `sudo -u pilotseal` (I6, answered:
  "I'll add them", 7121929832). No script enrolls a head, and no enroll path
  exists that an agent can invoke: no agent can run as pilotseal, and the
  ledger is not writable by uid 501 (precondition 10). So the pilot pins no
  enroll script.
- **Scope.** Joel's enrollment does one thing: it appends `enroll` and
  `exclude` lines that meet section 12, after section 12's reconciliation, and
  planted heads' `enroll` lines under the original's section 3, following F2's
  written procedure. `pilot-ledger.py` has no mode that rewrites or truncates
  a ledger. It only cuts back its own failed partial write to the size it
  verified.
- **A known limit.** Joel writes as pilotseal, which owns the sealed records,
  so the operating system does not stop the writer from reading them. What
  keeps him from doing so is that F2's procedure opens no sealed path, and
  section 6's rule that Joel opens no sealed record before unblinding. Only a
  writer uid other than pilotseal, able to append to the ledger and nothing
  else, would make that bar a mechanism.
- **The denied-write test.** A record as uid 501, in the shape of section 9's
  denied-read record, shows that opening the sealed ledger or the trust file
  for writing is denied with `EACCES` (section 9, precondition 10).
  `pilot-preflight.py probe` opens read-only today, so the write probe is F3.
- **Chain heads.** The coordinator posts the chain head at each `freeze`,
  `close`, `score` and `unblind` (section 7).

## 15. Landing during the pilot

#juno's fix 6 and #A1's r2 (7114817207), under the relay's "preserve existing
landing controls". An enrolled PR lands alone, never in a batched landing. The
batched-landings change (Joel 7112930679, #juno 7113018815, #claude 7114168630)
does not apply to enrolled PRs during the pilot unless this text is amended and
re-signed before enrollment.

**The landing regime is frozen** so that enrolled PRs are measured under the
same rules. The first live `enroll` records `landing_regime`: the fleet-tools
`main` commit at that time, whose landing scripts and gates apply to every
enrolled PR until `close`. A landing change that lands later does not apply to
an enrolled PR, unless this text is amended and re-signed. Section 9's
precondition 12 reconciles any change made before the first `enroll`. Every
existing landing control stays. Nothing in this text
changes rule #68, delegates a waiver, changes admin bypass, enables branch
protection or promotes a gate.

## 16. The promotion prerequisite and no branch protection

Relay point 7. The original's lines 485-488, kept by section 2, require
"a server-protected required check and an auditable break-glass" before a
promotion decision, per ORDER r2 item 13. Joel's standing decision is that
there is no branch protection (7114296700, point 7). Joel will resolve the
conflict by naming a local substitute (I7, answer 8 of 7121929832): a locally
enforced required check plus a break-glass log agents cannot alter. Until he
names it and this text records it as his change to ORDER r2 item 13, that
prerequisite stays as written and is **unmet**, so no promotion decision can
be made, whatever the pilot shows. This section is a direction to resolve the
conflict. It enables no branch protection and promotes no gate.

## 17. Script follow-ups

What this text requires that the landed and held scripts (ft#790, ft#792,
ft#793, ft#794, ft#804) do not do yet. None widens a held PR. Each is its own
reviewed PR, and each pin is filled by the signing commit. Enrollment
cannot pass preflight before F3, since check 4 refuses every form until then.

- **F1. `pilot-ledger.py`:** add an `exclude` kind, unique per `pr`; an
  `items` kind, unique per ledger; and a `ruling` kind with section 8's body.
  A `ruling` whose subject is `classification` may come before or after
  `close`. One whose subject is `item` needs an earlier `items` line naming
  that id. `items` and `ruling` join the kinds allowed after `close`, and no
  `items` or `ruling` may follow `score`.
- **F2. The enrollment procedure (I6):** a written procedure Joel follows by
  hand as pilotseal. It is a document, not a script, and it applies section
  12:
  - it orders by `created_at`, then PR number;
  - it reconciles every window PR against the ledger before each `enroll` and
    before `close`, and refuses on a missing or doubled record;
  - it runs the pinned `review-size.sh` at the enrollment head;
  - it records the evidence fields, and `landing_regime` on the first live
    `enroll`.
- **F3. `pilot-preflight.py`:**
  - check 4 verifies the signing commit's three conditions (section 1), and
    preconditions 6 and 10 date their records against the signing commit, not
    against the commit that added this text, as `pilot-preflight.py` does today;
  - `check` reads the key and the executable hashes from the trust file at the
    I8 location, never from a repository, and verifies the signature,
    including the old-key rule for a rotation. It refuses to run as any user
    but pilotseal;
  - precondition 14: it refuses enrollment when the trust file is missing or
    unreadable, or when any copy in this text (the fingerprint, section 10's
    table, section 12's row) differs from it. A self-test row
    shows the refusal for a changed copy, and a mutation that skips the
    comparison turns that row red;
  - it checks preconditions 8 to 13;
  - it adds the write probes of sections 13 and 14 and a probe of the key's
    location;
  - its `install` check covers `review-size.sh`.
- **F4. `pilot-score.py`:**
  - an `items` mode that appends section 8's `items` line from captures alone;
  - the `items` line covers every compared head, planted heads included, from
    captures only, and lists each `unlocatable` finding as its own item (as
    built, `o5_counts` only counts them);
  - it computes each ruling's place in the 48-hour budget (section 8);
  - it reads Joel's `ruling` lines and applies section 5's C3, C5 and C7 rules,
    within the J1 budget (I3), and anything past the budget is UNMEASURED;
  - the live minimum of C1 becomes 10;
  - O2 skips a `base_mismatch` clean head, for C5's clean part (section 5);
  - it reports the `default-1` run apart, heads that grew past the size bound,
    disputed classifications, E5 exclusions counted apart, and every
    `correction` line beside the line it names, with no result changed by one.
- **F5. A deadline, only if Joel adds one (I4).** If he picks a deadline before
  signing, F5 adds its key to the parameters, to section 8's end condition, to
  `pilot-unblind.py`'s end condition and to preflight check 5. If no deadline
  applies, F5 is empty.
- **F6. `pilot-unblind.py`:** end condition 4 counts frozen live heads of the
  kind that are compared (section 5), reading each head's frozen captures, not
  frozen live heads alone.
