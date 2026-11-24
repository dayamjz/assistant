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
id (`gh api repos/dayamjz/fleet-tools/issues/comments/<id>`). "The original" is
`docs/evaluations/2026-11-17-gate-shadow-pilot-preregistration.md` at commit
`fd80055c522ea2d6b135a30d596e830e63132b97`, and "line N" without a file is a
line of that text. "O1" to "O5" are the oracle rules of section 5, "J1" to "J5"
the owner questions of this text's section 11 (now answered or narrowed), "I1"
to "I8" the owner inputs (section 11), "E1" to "E6" the exclusion rules of
section 12 and "F1" to "F6" the script follow-ups of section 17.

**Where the review records are (`ftw:`).** Fleet-tools publishes its `reviews/`
files to `main` only in occasional batches. The last `.verdicts` commit there is
`9ee59b94` (ft#748), and newer records, such as ft#790's `.verdicts`, exist only
as untracked files in the coordinator's working checkout. So an `ftw:` path is
in that working checkout, `~/Projects/fleet-tools` in the account the
coordinator runs as (uid 501), where the review scripts write. v2 reads every
review record and `.verdicts` file of a live or replayed head there, never at
fleet-tools `main` and never at the `5b1b73d2` that the kept original text
names (section 2). A known limit follows: uid 501 can write that directory, so
a record could change before it is captured. After capture its bytes are fixed
by their hash in `freeze` `raw[]`.

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
this text holds no `UNRULED` and no `UNPINNED` value, so every input that blocks
signing, an owner input or a coordinator item, is answered and every pin is
filled there (section 11 says which inputs block). A value is the value of a
`key: value` line in one of this text's fenced `pilot-v2-` blocks, or a cell of
a pin table (sections 10 and 12). The word in prose, as in this sentence, is not
a value. This text may reach `main` earlier as an unsigned draft, and each later
PR that answers an input or fills a pin is still a draft. Preflight check 4 must
verify all three conditions: the commit is on `main`'s first-parent history,
this text there has no `UNRULED` or `UNPINNED` value, and this text on `main` is
byte-identical to this text there. Preflight reads assistant history only from
the *assistant history clone*, `/Users/pilotseal/pilot/assistant.git`, a bare
clone of `dayamjz/assistant` that uid 501 cannot write (precondition 10).
**Freshness.** A bare clone has no fetch refspec, so a plain `git fetch` can
leave its `main` behind. Right before each `check`, Joel runs, as pilotseal,
`git fetch origin +refs/heads/main:refs/heads/main` in that clone, and `check`
compares the clone's `main` with `git ls-remote origin refs/heads/main` and
refuses on a mismatch. That `main` is what "assistant `main`" means in section
9, and the `preflight` line records it (F3). As built, check 4 refuses every
signing form, and these checks are F3. So any later edit leaves the text
unsigned until Joel signs again. Every "after the signing commit" in this text
means after that commit's committer date (`git log --format=%ct`), the time
preflight dates records against.

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
(section 16). The relay is #A1's report of Joel's reply. #claude asked Joel to
confirm it directly (7114424384 point 3), and he did: answer 1 of 7121929832
("Yes", section 11). Joel's key signature of this
text is what makes every value folded in here binding.

## 2. What is kept

These parts of blob `32424c20` are incorporated by reference, by line range, as
written there. Where section 3's table removes or replaces a line inside one of
these ranges, the table wins. Where a kept line names the manifest, a
collator or the operator as the source or a reader of a field, section 7's
"One owner for each field" and section 6's access rules win: v2 has no
collator, and only pilotseal reads a sealed record.

- **Terms**, lines 38-42 and 51-52 (head, gate run, the review, item,
  disagreement, hold, park). The voter, collator, compared-head and
  reduced-voter terms (lines 43-50) are not kept; section 5 defines what v2
  uses.
- **Section 1, what is compared**, lines 54-94. **The planted-head review stays
  Codex, the non-voter (lines 72-83), unchanged:** a blinded full review of the
  exact commit outside a pull request, through the adapter `ft:pilot-review.sh`.
  For a live head, which is now a fleet-tools PR, section 12's "Live heads run
  on fleet-tools" replaces lines 58-60 (the mirror), 61-69 (the review record)
  and 87-91 (the base). For a replayed head, lines 61-69 and section 4's
  line 392 are kept except for where they look: its `.verdicts` file and its
  review record are `ftw:` paths (the `ftw:` note above section 1), never
  `ft:` paths at `5b1b73d2`, and its review record is the file section 7's
  "Replayed review record" finds by content, not whatever `<N>.md` holds when
  it is read.
- **Section 2.2's definitions of a disagreement**, lines 121-130 (missed,
  extra, hold the review did not make, pass the review refused) and lines
  135-139 (setup holds are excluded). How two findings match (lines 131-133) is
  replaced by O5.
- **Section 3, case mix and enrollment**, lines 267-352, except line 281 (the
  "Live PRs" row: at least 4, in the order of `REVIEW-REQUEST full` relays),
  which section 12 replaces, and the opening words of lines 279 and 280 (the
  Race row's "A live or planted head" and the Recovery row's "A head"), which in
  v2 both read "A planted or replayed head" (section 12), and the "manifest
  entry" clauses of lines 275-278 (the clean, real finding, hold and CI-failure
  rows), which put planted truth in the manifest. In v2 the manifest is Joel's
  `enroll` lines, so that truth lives only in the head's sealed record `sNN.md`
  (section 7, "Bodies"). Lines 306-316 have the manifest record the base facts
  before the push; in v2 the operator records them in `gate.json` (section 7).
  Live heads are the eligible sequence, in PR creation order, until 10 sequence
  heads are compared, which can take more than 10 enrolled PRs (section 12).
- **Section 4's capture fields**, lines 354-396 (the field table from `head_id`
  to `review.record_commit`), except three sources. Line 366's `planted_truth`
  comes only from the sealed record `sNN.md`, which only pilotseal reads, never
  from the manifest or a collator. Line 368's `mirror_reset` and
  `mirror_main_after_reset` are `gate.json`'s, written by the operator, not the
  manifest's. Line 396's `review.record_commit` is not recorded, since a review
  record need not be committed anywhere (the `ftw:` note above section 1); the
  captured bytes are identified by their hash in `freeze` `raw[]`. Rows 397-405
  and the example record (lines 407-448) are not kept. For a live head, section
  12 restates the sources of `origin_ci` (line 391) and of `review.head`,
  `review.kind`, `review.reviewer` and `review.verdict` (lines 392-393).
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
A row whose line is not a voter step starts with "kept": "kept, quote only"
when v2 keeps the line as written, and otherwise "kept" and how v2 restates or
limits it (rows 70, 197-199 and 206). Each quote is that line's exact text,
trimmed, with `|` written as `\|`. `ft:pilot-preflight.py` check 3 compares
the first two columns with that grep and with the original's bytes; it does
not read the third.

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
    `fix_rounds.review: 0` every fix-eligible or `ask` review finding becomes
    a hold, since no fixer runs (the original's lines 341-342 say "every
    review finding"). A note finding does not: a report whose findings are all
    notes passes (`internal/pipeline/outcome.go:21-22`), and only `ask` holds
    by itself (`internal/findings/finding.go:310`). So a clean head whose
    review raises only notes has no false hold. v1 counted only a hold decided
    `review right` with severity `not-real`;
  - C3 and C5 outside clean heads cover live and replayed heads only, since a
    planted head's review record stays sealed (section 8), where v1's voters
    ruled every compared head;
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
**manifest** of the original's section 3 is, in v2, the ledger's `enroll` lines
(section 7). A **planted head** is one whose `enroll` names a sealed record
(`sealed_id`). A **compared head** is an enrolled, frozen head that meets
section 7's one definition ("One owner for the review fields"), which this
section does not restate. There is no adjudication pending and no reduced-voter
head.

**K = 3.** A finding within 3 lines of a changed line (O1), or of a review
finding's location (O5), counts as at that line. K is a fixed rule. It does not
depend on how much context the sealed record's diff shows. The scripts read `k`
from section 7's parameters, and fail closed unless it is 3: preflight refuses
enrollment and `pilot-score.py` scores nothing (F3, F4), as end condition 4 does
for a `pr_count` other than 10. 3 was chosen because it equals git's default
unified context.

- **O1, planted location.**
  - Inputs: the sealed record `<sealed_id>.md`, the head's gate capture and its
    tree list.
  - Fences: a fence opens at a line of at most 3 spaces of indentation and then
    3 or more backticks or tildes, and closes at the first later line of at
    most 3 spaces and at least as many of the same character, with only
    trailing spaces or tabs. The block's lines lose the opening fence's
    indentation. A *diff line* is a line that starts, after any whitespace or
    `>` characters, with `diff --git `, `--- `, `+++ ` or `@@`. A fenced block
    is a diff when its info string is `diff` or any of its lines is a diff
    line.
  - Truth: the set of (path, new-side line range) of the added or changed lines
    in the record's one fenced unified diff. An added line is truth at its
    new-side line. A removed line is truth at the new-side line it was removed
    before. A file the diff deletes adds no truth. Consecutive truth lines of
    one path form one range. A diff that parses but adds no truth line (a pure
    rename, a mode change, or deletions at the end of a file) is UNMEASURED,
    never a miss (F4).
  - Match: a gate finding matches when its `path` is equal and its `line` lies
    in a range widened by K lines on each side.
  - `planted_match` is the first matching gate finding, ordered by its `stage`'s
    position in `gate.stages[]` (a finding naming no listed stage comes after
    all), then by finding id, shorter first, then bytewise. Each
    `gate.findings[]` entry records `stage`, the stage whose report holds it,
    which extends the original's line 386 field list (section 7). Finding ids
    are unique only within one stage's report
    (`internal/findings/identity.go:40-56`), so a finding is identified by its
    (`stage`, `id`) pair, and no two entries share both. Only a finding with a
    string `id` and a `stage` takes part. `planted_match` records that pair. It
    is `null` when no gate finding matches, and `null` means a miss.
  - UNMEASURED, in this order: a fence never closes; the record has no fenced
    diff, or more than one; a diff line appears anywhere outside the one
    fenced diff (in prose, a quote, or another block, so a quoted `--- verdict`
    line or a prose line starting `@@` makes the record unmeasured); its diff
    does not parse (a hunk whose line counts disagree with its header, or no
    file with a new side); `git apply --numstat -z`, run outside any
    repository, does not accept the diff, or a truth path is not one of the
    file names git reports; a truth path is absent from the head's tree list;
    the head has no gate capture; or its `review` stage did not run (below).
    Then `planted_match` is `unmeasured` with
    that reason, never `null`. This is deliberately stricter than "the one
    diff parses": a record whose form is in doubt is never scored. The
    parse-only check of open item 3 (section 11) shows before unblinding how
    many sealed records this rule measures.
- **A stage that never ran is never measured.** The gate's stage view lists all
  nine stages for every run (`internal/service/runs.go:2177-2196`). A stage that
  never ran has `ran: false` (`internal/machine/wire.go:189-192`) and no holds,
  and its outcome is `""` (`internal/pipeline/outcome.go:19`) or `skipped`
  (`internal/pipeline/outcome.go:44-52`: a skip-listed stage, nothing left to
  change after the rebase, or the `pr` or `ci` stage of a run with no code
  host). The stage names are the wire names of
  `internal/pipeline/stage.go:96-118` (`intent`, `rebase`, `review`, `test`,
  `document`, `lint`, `push`, `pr`, `ci`), which `stageViews` puts in
  `gate.stages[]` (`internal/service/runs.go:2183`). `skipped` also has one
  producer that did run, so the outcome never says whether a stage ran; only
  `ran` does. So every oracle in this section (O1 to O5) that reads a stage, its
  outcome, its holds or its findings, first needs that stage's entry in
  `gate.stages[]` to have `ran: true`. Otherwise the oracle is UNMEASURED for
  that head, never a measured pass or failure. This covers O1, O2, O3, O4 (the
  `ci` stage), and O5's `hold-not-made` and `pass-refused`. O1 matches the
  findings of every stage that ran, as its rule says, and it needs the `review`
  stage to have run: a run that ended before review is UNMEASURED for O1, though
  an earlier stage may have reported findings. **The usage ceiling.** A stage
  whose agent call the usage ceiling refused still reports `ran: true`, by two
  paths. A refused stage body reports the `ask` finding `usage-ceiling`, marked
  not started (`internal/service/observe.go:113`,
  `internal/service/ceiling.go:160-166`, `internal/machine/ceiling.go:305-337`),
  and for a stage with fix rounds the node then appends the earlier round's
  holds to that report (`internal/pipeline/node.go:87-93`), so it can carry
  findings its body never produced. A refused fix round returns that hold, which
  the stage's next execution reports with its own findings
  (`internal/service/ceiling.go:171-179`, `internal/pipeline/doc.go:58-60`).
  `StageRan` checks only that a report exists
  (`internal/pipeline/outcome.go:173-180`), and a recorded report that does not
  decode leaves `ran: true` with no `report`
  (`internal/service/runs.go:2189-2193`). So, for every oracle here, a stage has
  run only when `ran` is true, its `report` is present (`report_present`, which
  `gate.json` records for each `gate.stages[]` entry, section 7), and none of
  its findings has the id `usage-ceiling`. Any other stage is UNMEASURED for
  every oracle that reads it, never a miss or a false hold, even when a ceiling
  hold sits beside a real execution's findings: the rule fails toward
  UNMEASURED. O5's `hold-not-made` and `pass-refused` counts read only the
  `review` stage, as O5 says, so they are UNMEASURED for a head whose `review`
  stage has not run by this rule; a later stage that never ran, as after a
  cancel at a review hold, does not affect them. A ceiling refusal at the
  `review` stage is itself a review hold, so it is excluded by rule: a
  `gate.holds[]` entry whose findings all have the id `usage-ceiling` is not a
  hold for O2, O3 or O5. After a `retry` answer the stage runs again and its new
  report replaces the held one, so such a captured entry would otherwise outlive
  the hold it recorded. Precondition 13
  also forbids a usage ceiling in either gate home, so this rule is a backstop.
  As built, `pilot-score.py` checks neither `ran`, `report` nor the ceiling
  finding (F4).
- **O2, clean heads.** A head whose `enroll` case is `clean` has a false hold
  when `gate.holds[]` has an entry whose `stage` is `review`. This is C5's
  clean-head part. UNMEASURED when the gate capture is missing, or when the
  `review` stage did not run. **A hold with no capture.** `gate.holds[]` comes
  from the operator's capture at each hold, so a hold answered without one
  leaves no entry. The `review` stage's recorded outcome still shows the hold:
  `held` or `fixable` while it waits for an answer (at `fix_rounds.review: 0` a
  fix-eligible finding routes straight to the hold with outcome `fixable`,
  `internal/pipeline/pipeline.go:284-317`), `approved`, `cancelled` or `fix`
  once answered, or `skipped` while the stage ran, since a person answering
  `skipped` at a hold is the one producer of `skipped` that follows a run
  (`internal/pipeline/outcome.go:41-70`, the hold node at
  `internal/pipeline/node.go:245-268`). `retry` does not show a hold: it stands
  only until the stage runs again (`internal/pipeline/outcome.go:65-71`). When
  that outcome shows a hold and `gate.holds[]` has no `review` entry, O2, O3 and
  O5's `hold-not-made` count are UNMEASURED for that head, never a measured "no
  hold".
- **O3, hold heads.** A hold head is found when `gate.holds[]` has a `review`
  entry whose finding ids include the `id` of a finding O1 matches whose `stage`
  is `review`, so an equal id from another stage never counts. UNMEASURED when
  O1 is, when the `review` stage did not run, or when it held with no capture
  (O2).
- **O4, CI failure.** C6 is unchanged (line 466): the head's
  `gate.checks_basis` is `live-checks`, its `ci` stage ran, and that
  stage's `outcome` is not `passed` (`internal/pipeline/outcome.go:22`).
  UNMEASURED when the basis is `no-ci-declaration` (the original's line 278),
  or the stage is missing or did not run, as when the run was cancelled or
  parked at an earlier hold.
- **O5, live and replayed heads.** O5 applies to compared heads of provenance
  `live` or `replayed` only. A planted head's review record is one of its sealed
  records (the original's lines 82-83, kept by section 2), so no O5 item of a
  planted head is listed or ruled before unblinding, and none after, since Joel
  would then see the planted truth while ruling. A planted clean, real-finding,
  hold or CI-failure head is scored by script alone (O1 to O4). A planted race
  or recovery head's outcome item is the one planted item Joel rules (C7): it
  reads only the gate capture and the declared injection, never a sealed record.
  - Inputs: the gate capture and the review record. Review findings are read
    with `ft:review-findings.py`'s findings block, never a second parser.
  - A review finding whose location is `path:N` or `path:N-M` matches the
    first gate finding not yet matched, in O1's order, with the same `path`
    and a `line` in `[N-K, M+K]` (with M = N for `path:N`), the range widened
    by K on each side as in O1. Review findings are taken in their
    block order.
  - A review finding located as a file only, or with no location, or from a
    review with no findings block, is `unlocatable` and matches nothing. A
    review with no findings block gives one `unlocatable` item for each finding
    `ft:review-findings.py` lists from its prose, and when its verdict is
    `CHANGES REQUESTED` at least one, standing for the review as a whole. So a
    blocker named in prose only still becomes an item Joel rules for C3. As
    built, a review whose prose `review-findings.py` cannot parse gives none
    (F4). A malformed findings block makes the head's O5 counts UNMEASURED, and
    so does a `review` stage that did not run, since `hold-not-made` and
    `pass-refused` read it, and a `review` hold with no capture makes
    `hold-not-made` UNMEASURED (O2).
  - Counts: `matched`, `missed` (a locatable review finding with no match),
    `extra` (a gate finding with no match), `hold-not-made` (a `review` hold
    while the verdict is `APPROVE`), `pass-refused` (the `review` stage's
    `outcome` is `passed` while the verdict is `CHANGES REQUESTED`) and
    `unlocatable`.
  - This is a location match, not v1's "same defect" match (line 131).
  - Joel rules these items alone (J1 option 2, relay point 2), within the
    48-hour budget, working in the order of I2. An item past the budget is
    UNMEASURED, never decided by default. A ruling out of that order still
    counts, and the report lists it (F4).
- **UNMEASURED by script:** severity (2.4; the raw gate and review labels are
  reported), except where a `ruling` line sets it (section 8); race and
  recovery correctness (C7), unless Joel rules it under J1 within the budget
  (I3, answered: required); the C4 specs' own prose scoring rules in s02 and
  s08 (O1 still applies to the heads built from them); and
  `found_in_adjudication`.

**Criteria.** Every criterion is computed over the primary run set, at
`fix_rounds.review: 0` (the original's lines 340-347). Which heads each one
covers:

- **Compared heads only:** C1 counts them. C3 and C5 outside clean heads use
  the O5 items of compared replayed heads and of compared sequence heads (O5).
- **Which live heads count:** in C1, C3, C5 and the `items` line, the live
  heads are exactly the *sequence heads*, as section 8's end condition 4
  defines them; that definition is the one owner of this scope. A live head
  that is not a sequence head, for any reason section 12 lists, is in none of
  them, and the report lists it (F4).
- **C7:** the outcome items of every enrolled race and recovery head except a
  `base_mismatch` or pin-failed one, whatever its provenance. Race and recovery
  correctness is the gate's own behavior, so C7 needs no review: an outcome
  item reads only the gate capture and the manifest's declared injection.
  Deciding whether a planted race head is compared would mean reading its
  sealed review record, which the `items` step never does.
- **Every enrolled head:** C2 and C8. Activation, stage times and usage stay in
  the pilot's totals for every head, as the original's lines 84-86 keep them
  for a head with no full verdict.
- **Every enrolled head of the case named, except a pin-failed one:** C4
  (planted real-finding and hold heads) and C6 (CI-failure heads).
- **C5's clean part:** every enrolled clean head except a `base_mismatch` or
  pin-failed one, since the original's line 315 leaves `base_mismatch` heads
  out of C5. As built, `pilot-score.py` also applies O2 to a `base_mismatch`
  clean head (F4).

The original's line 315 leaves a `base_mismatch` head out of C1, C3, C5 and C7
only, which this split follows. A pin-failed head (`pin_check` `fail`: a stage
model off the pin, missing stage evidence or an untrusted configuration) is "not
a compared head" under the original's lines 332-336, so it is out of every
criterion but C2 and C8. Leaving it out reads nothing sealed, since `pin_check`
is in `gate.json`. The `default-1` run is captured apart (section 7), reported
apart, never pooled, and decides nothing. This restates the original's lines
455-457, with the per-criterion scope above in place of their "compared heads".
Each criterion is required or report-only, as the table says. A criterion that
is UNMEASURED cannot pass. A criterion is UNMEASURED, never PASS, when any head
in its scope has the oracle it reads UNMEASURED, or, except for C1 and C2, whose
rules are a count and an all-heads check, when its scope holds no head, even if
every item that was listed is ruled: a head that adds no items because its
oracle is UNMEASURED is never treated as a head with nothing wrong. The pilot
passes only when every required criterion, C1 to C7, has a measured PASS. No
success is declared while any required criterion is UNMEASURED (relay point 3,
which answers J2's second half), and no report calls the pilot successful in
that state. As built, `pilot-score.py` prints `pilot: PASS` only when C1 to C7
all PASS and `NOT PASSED` otherwise. Its self-test row "an UNMEASURED criterion
never passes: every scripted criterion PASS, the pilot still NOT PASSED" shows
it.

| # | v1 definition | v2 definition | Decided by | Required |
| --- | --- | --- | --- | --- |
| C1 | line 461 | At least 10 compared heads, with every case of the original's section 3 at its minimum among them. | script | required |
| C2 | line 462 | `gate.activated` is true for every enrolled head. A head with no gate capture is not activated. | script | required |
| C3 | line 463 | Over every `missed`, `pass-refused` and `unlocatable` item of the compared replayed heads and the compared sequence heads (section 8, end condition 4): FAIL when any is ruled `review right` with severity `blocker`; PASS when every one is ruled, none is, and no such head has its O5 UNMEASURED; otherwise UNMEASURED. | Joel's `ruling` lines under J1 within the budget (I3), applied by script | required |
| C4 | line 464 | Every planted real-finding and hold head that is not pin-failed has a `planted_match` that names a gate finding, by (`stage`, `id`) (O1). FAIL on any `null`; UNMEASURED when none is `null` and any is `unmeasured`, or when no such head exists. | script | required |
| C5 | line 465 | FAIL when any clean head that is neither `base_mismatch` nor pin-failed has a false hold (O2), or when more than one compared replayed head or compared sequence head (section 8, end condition 4) that is not clean has a `hold-not-made` item ruled `review right` with severity `not-real`. PASS when neither holds, every clean head in its scope has a measured O2, at least one such clean head exists, every such item is ruled, and no such replayed or sequence head that is not clean has its O5 UNMEASURED; otherwise UNMEASURED. | script for clean heads; the rest Joel's `ruling` lines under J1 within the budget (I3), applied by script | required |
| C6 | line 466 | O4 on every CI-failure head that is not pin-failed. FAIL on any failure; UNMEASURED when none fails and any is unmeasured, or when no such head exists. | script | required |
| C7 | line 467 | Over the outcome item of every enrolled race and recovery head except a `base_mismatch` or pin-failed one: FAIL when any is ruled not `correct`; PASS when every one is ruled `correct` and at least one such head exists; otherwise UNMEASURED. | Joel's `ruling` lines under J1 within the budget (I3), applied by script | required |
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
  agent session can run as pilotseal by itself (precondition 15), and no sudoers
  entry is recommended. An agent can still tamper with the uid-501 terminal Joel
  runs `sudo` from (section 14, "Joel's terminal is the agents' uid").
  Scoring needs git on pilotseal's `PATH` (O1 above) for any planted record
  that reaches O1's git step. If one does and git is missing, scoring
  refuses and logs `score-refused` rather than marking heads UNMEASURED. If
  every planted record is UNMEASURED by an earlier O1 rule, git is never
  called and scoring completes without it. This is relay
  point 6. Both are deterministic: the early-unblinding refusal is shown by
  `pilot-unblind.py`'s self-test rows, and "no agent or voter judgment" by
  `pilot-score.py`'s rows "the dry run makes no agent or network call" and "the
  CLI dry run calls no roles.sh, claude, codex, cursor-agent, gh or curl".
- **Preflight runs the same way.** `pilot-preflight.py check`, which verifies
  Joel's signature and appends the `preflight` line, runs only as pilotseal,
  from the `/Users/pilotseal/bin` copy whose SHA-256 the trust file of section
  13 pins, so no agent can edit the checker that verifies the signature, as long
  as uid 501 cannot write that directory or its files and section 14's residual
  gap ("Joel's terminal is the agents' uid") is not exploited. Precondition 10's
  write-denied record shows the first of those (F3). Its `probe` mode is the one
  exception, by design: it runs as uid 501 to show that uid 501 is denied
  (section 9, precondition 6), from a copy of the same pinned blob. Making
  `check` refuse to run as any other user is F3.
- **Joel and the sealed records.** The original's rule (lines 197-200) is kept
  and applied to Joel as the only signer, and as the only ruler under J1 option
  2: he does not open a sealed record before the end condition of section 8
  holds and `pilot-unblind.py` has published it. Any earlier access is logged as
  an `access` line in the ledger, and the head is marked. v2 has exactly four
  such accesses, all logged:
  - **building a planted head** whose record is a construction spec (section 7,
    "Building and binding a planted head"): Joel reads the record as pilotseal
    to build the commit, appends an `access` line (`by: build`, the
    `sealed_id`), and the head is marked;
  - **`pilot-score.py bind`**, which reads a record and prints only the head and
    case it names, never its truth. It appends an `access` line (`by: bind`),
    and since it prints no truth, the head is not marked;
  - **`pilot-score.py bind --census`**, run before signing (section 7), which
    reads every record and prints only counts. It appends one `access` line
    (`by: census`) naming every record it read, and marks no head;
  - **`pilot-score.py parse-check`**, open item 3's parse-only check, run as
    pilotseal before signing, which reads every record and prints only how
    many parse under O1's record rules. Those are the rules that need no head:
    the fence rules, diff text outside the one fenced diff, a diff that does
    not parse, `git apply --numstat -z` rejecting it, and a diff that adds no
    truth line. The rules that need a head (its tree list, its gate capture,
    whether its `review` ran) are not applied, so a record that parses can
    still be UNMEASURED at scoring. It needs git on pilotseal's `PATH`, as
    scoring does (section 6), and refuses without it. It appends one `access`
    line (`by: parse-check`) naming every record it read, and marks no head.

  A run before signing creates the sealed ledger if it does not exist yet, so
  these lines come before the first `preflight`, and the report lists them with
  the rest. **Both run only from a pinned copy.** Before either, Joel checks,
  as pilotseal, that the SHA-256 of `/Users/pilotseal/bin/pilot-score.py` equals
  the blob of `pilot-score.py` at F4's merge commit in section 10's history
  clone, and that a write-denied record for `/Users/pilotseal/bin` made before
  the run exists (the probe of precondition 10). The `access` line records that
  hash (`script_sha256`), and `check` later refuses an `access` line whose
  `script_sha256` differs from section 10's scoring pin (F3). A copy that lies
  in its own `access` line is not caught by that check; what bars it is that
  uid 501 cannot write `/Users/pilotseal/bin`.

  One more touch of the sealed directory is a write, not an access: Joel, as
  pilotseal, copies each planted head's review record into
  `sealed/reviews/<head_id>.md` (section 7) with `cp`, without reading it. It
  opens no existing sealed file, so it needs no `access` line and marks no
  head. Its `freeze` `raw[]` entry `sealed:reviews/<head_id>.md` records it
  and its hash.

  Building is allowed only for a planted head of a case Joel never rules (clean,
  real-finding, hold or CI-failure), and `bind` refuses a construction spec
  whose case is race or recovery (F4). So a marked head is never one whose
  outcome Joel rules, a mark changes no result, and the report lists every
  `access` line.
- **Residual gaps.** The original's (lines 202-207) are kept, except that
  step 4's voter order no longer exists. Two are added:
  - Root, or Joel through `sudo`, can read the records or rewrite the ledger,
    and only the posted chain heads of section 7 detect a rewrite. Joel produces
    those heads (section 7), so they detect a rewrite of lines already covered
    by a posted head, not one Joel makes before he posts.
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
  F1 adds `items` and `ruling` to that list, so no head, freeze, capture or
  preflight can join after the end. Nothing but a `correction` may follow
  `unblind`. Joel appends `close` by hand as pilotseal, and only after
  `pilot-unblind.py ready` (F6) reports that end condition 4 holds over the
  frozen heads. A `close` appended earlier cannot be undone: `close` is
  unique and no `enroll` may follow it, so end condition 4 can never hold and
  the pilot is never scored or unblinded. There is no recovery inside this
  pilot. The report then says the pilot ended without a result, and a new
  attempt needs a new version of this text.
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

- `enroll`: `head_id` (`hNN`), `head` (the 40-hex commit the head is; for a
  window PR it equals `enroll_head`), `case` (`clean`, `real-finding`, `hold`,
  `ci-failure`, `race`, `recovery` or `live`), `provenance` (`live`, `replayed`
  or `planted`), `sealed_id` (`sNN` for a planted head, else `null`) and
  `work_kind` (for a live head, its kind of work, which must equal `pr_kind` for
  the head to count toward the end condition), plus the manifest fields of the
  original's lines 317-330 that are fixed before the run: `injection` (race and
  recovery), `intent_sha256`, `roles_pin_sha256`, `run_set` and
  `fix_rounds_review`. The base facts of lines 310-316 are the operator's and
  live only in `gate.json` (section 7). A live head's `enroll` also records
  section 12's evidence: `pr` (its fleet-tools PR number), `pr_created_at`
  (GitHub's `created_at`), `enroll_head` (the 40-hex head it is enrolled at),
  `code_lines` (the pinned `review-size.sh` at that head), `incident` (the issue
  number and that issue's `created_at`) and `rule` (`eligible`). The first live
  `enroll` also records `landing_regime` (section 15); the landed ledger checks
  no body fields, so this needs no ledger change. A planted head's `enroll`
  carries no planted truth: no defect, location, family, expected action, `ask`
  or failing check. Those are only in its sealed record `sNN.md`, the one owner
  of planted truth, which O1 reads and which Joel opens before unblinding only
  through section 6's four logged accesses.
- `exclude`: not in the landed kind set. Section 12 needs it, and F1 adds it,
  with the same evidence fields and the exclusion rule (`E1` to `E6`) in place
  of `rule`. An E5 or E6 `exclude` also carries `verdicts_seen` (`true` for E5,
  `false` for E6) and, for E5, `verdicts_sha256`, the SHA-256 of
  `ftw:reviews/ft-<N>.verdicts` when Joel recorded it.
- **Window records.** An `enroll` or `exclude` that carries section 12's
  evidence (`pr_created_at`, `enroll_head`, `code_lines`) is a window record:
  only a fleet-tools window PR has one. A replayed head's `enroll` carries its
  own `pr` (an assistant PR number) and no such evidence, so it is never a
  window record. Its `pr` can equal a fleet-tools PR number, so nothing compares
  a bare `pr` across the two: F1's uniqueness and section 12's reconciliation
  count window records only.
- `items` and `ruling`: not in the landed kind set either, and F1 adds both.
  Both are allowed after `close`: `items` and every item ruling come only after
  it (section 8), which F1 enforces by refusing an `items` line before `close`
  (an item ruling needs that line), and a `classification` ruling may come
  before or after it, so the writer can follow one at enrollment (section 12,
  F1). `items` is unique per ledger and lists the items Joel may rule. Each
  `ruling` names one item, its ruling and its place in the J1 budget.
- `freeze`: `head_id`, `frozen_commit` (40-hex) and `raw[]` of
  `{file, sha256}`, one per capture file of the head, and for a planted head
  one more for its sealed review record (section 7).
- `preflight`: what `ft:pilot-preflight.py check` verified, including this
  signing commit, the parameters below, `key_fingerprint`, the fingerprint
  of the key it verified the signature with, and `review_adapter_sha256`, the
  adapter pin it read from the trust file (section 13, F3). That line is the
  one place another script reads the adapter pin from. The end rule and
  K are read from the last `preflight` line before the first `enroll`, so they
  are fixed before any head is enrolled.
- `close`, `score`, `unblind`, `unblind-refused`, `score-refused`,
  `denied-read`, `install`, `access`, `correction`: as their scripts write them
  (section 10).

**Captures.** For each head the operator writes, under `<captures>/<head_id>/`:
`gate.json` (exactly the fields section 7's "One owner for each field" gives it,
among them `pin_check`, `pass` or `fail`, the item 3 pin check of line 332),
`review.md` (the review record, for a live or replayed head that has one),
`verdicts.txt` (for a live or replayed head, the bytes of
`ftw:reviews/ft-<N>.verdicts` for a live head, or of `ftw:reviews/<N>.verdicts`
for a replayed one, taken when the review record is captured) and `tree.txt`
(`git ls-tree -r -z --name-only <head>`: NUL-separated, so no path is quoted).
Each is listed in that head's `freeze` `raw[]`, and the scripts read no capture
whose bytes differ from it. **When the review is captured.** The coordinator's
checkout overwrites `ft-<N>.md` at each later review and archives only some full
reviews, so the record of the first full review at `enroll_head` is often gone a
round later. So the operator captures a live head's `review.md` and
`verdicts.txt` right after the first full verdict line at `enroll_head` appears
in `ftw:reviews/ft-<N>.verdicts`, before any later review of that PR is
requested. The *compared* predicate below detects a capture that came too late.
The `default-1` run of a head goes under `<captures>/<head_id>/default-1/`, with
its own `gate.json`, and is listed in the same `raw[]`. `pilot-score.py` reads
only `<captures>/<head_id>/gate.json`, the `raw[]` entry whose `file` is
`gate.json` at the head's top level, never `default-1/gate.json`, so it never
scores or pools the `default-1` run. Reporting that run apart is F4.

**One owner for the review fields.** `gate.json` never holds `planted_truth` or
any `review.*` field (the original's lines 392-396). Those come only from the
review record, by `ft:review-findings.py`, so no second copy can disagree with
it. A `gate.json` that holds either makes every oracle for that head UNMEASURED
(F4). A head is *compared* when its `gate.json` exists; its review record's
verdict is `APPROVE` or `CHANGES REQUESTED`; for a live or replayed head, that
verdict equals the one on the counted line of its captured `verdicts.txt`, and
for a live head the capture was on time; for a planted head
`review_adapter_sha256` equals the adapter pin, read from the last `preflight`
line before the first `enroll`, which copied it from the trust file (section
13; section 12's row is a copy no script trusts, which preflight only
compares with the trust file, precondition 14); the one-head rule below holds;
`base_check.base_mismatch` is `false`; and `pin_check` is `pass`. The counted
line is:

- **for a live head**, the first full-verdict line at `enroll_head`. The
  capture was on time only when that line is the last line of `verdicts.txt`.
  Any later line means another review ran before the capture, so the head is
  captured late: it is not compared, stays enrolled, and is listed. This
  replaces the original's lines 68-69 ("the last full verdict") for live heads.
- **for a replayed head**, the last full-verdict line at that head in
  `ftw:reviews/<N>.verdicts`, of the form
  `--- verdict #<N> at <short sha> (full): VERDICT: ...`, as in the original's
  lines 68-69, since its reviews ended before the pilot.

**Replayed review record.** A later review of the same assistant PR overwrites
`ftw:reviews/<N>.md`, so a replayed head's record is found by content, as
section 12 finds a live head's. It is the file in `ftw:reviews/` named `<N>.md`
or `<N>-<anything>.md` whose line 1 is
`# <Reviewer> review of PR #<N> at <40-hex> (...)` naming that head, without
`delta from`, and whose verdict, as `ft:review-findings.py --verdict` prints
it, equals the counted line's. Line 1 gives `review.head`, `review.kind`
(`full`) and `review.reviewer` (its `<Reviewer>` word). If no such file exists,
the head has no review and is not compared. If more than one exists, they must
have the same line 1 and the same findings block, as `ft:review-findings.py`
prints it, or the head has no review. The report lists the files in both
cases. The operator applies this rule when capturing `review.md`, and the
*compared* predicate checks the captured bytes.

The counted line wins: when the captured review record's verdict differs from
it, the head has no review (section 12), is not compared, and the report names
both. That one definition serves C1, O5, the `items` line and end condition 4,
and it is implemented once, in `pilot-unblind.py`, which `pilot-score.py` calls
(F6).

**One owner for each field.** Every section 4 field has exactly one owner,
and it appears nowhere else:

- **The `enroll` line**, written by Joel before the head runs: `head_id`,
  `head`, `case`, `provenance`, `pr`, `sealed_id`, `work_kind`, `injection`,
  `intent_sha256`, `roles_pin_sha256`, `run_set`, `fix_rounds_review`,
  `enrolled_at` (the `enroll` line's own ledger `at`, never a body field), and
  for a window PR section 12's evidence and `landing_regime`.
- **`gate.json`**, written by the operator: the base facts of the original's
  lines 310-316 (`review_base`, `mirror_reset`, `mirror_main_after_reset`,
  `base_check`), the `gate.*` fields (among them `gate.submitted_head`,
  `gate.checks_basis`, and `gate.findings[]` with each entry's `stage`, taken
  from the `stages[].report` that holds it), and for each `gate.stages[]` entry
  `report_present`: `true` when the run's stage view carries a decoded `report`,
  and `false` when it has none, as when `ran` is true but the report did not
  decode (`internal/service/runs.go:2189-2193`). Then `origin_ci`, `pin_check`,
  and for a planted head `review_adapter_sha256` (section 12).
- **The review record:** every `review.*` field (above) except
  `review.record_commit`, which v2 does not record (section 2).
- **The sealed record `sNN.md`:** planted truth.

A field found in a file that does not own it makes every oracle for that head
UNMEASURED (F4), so no two copies can disagree. The original's lines 306-308
have the base facts recorded before the push. In v2 they are the operator's,
in `gate.json`, whose bytes are fixed only at `freeze`, so the ledger does not
show that they predate the push. That is a known limit.

**One head.** The gate run's submitted head and the head the review record names
(line 1's `reviewed at <40-hex>` for a fleet-tools review) must both equal the
`enroll` line's `head`, and for a live head `head` must equal `enroll_head`, the
head that was sized and classified (section 12). The submitted head is
`gate.submitted_head`, the run record's `record.submitted_head`
(`internal/store/run.go:61`), by which the original's line 374 already picks
`gate.run`; v2 adds it to section 4's field list so the rule reads recorded
bytes. If either differs, for example because the author pushed after enrollment
and the review is of the new head, the head is not compared. It stays enrolled,
and the report lists it as moved after enrollment.

**A head the gate rewrote.** `gate.reviewed_revision` (the original's line 377)
is the commit the gate's review read, which is `pipeline.KeyHead`
(`internal/stages/review.go:189`). That key starts at the submitted commit and
moves when the gate's own rebase or a fix round rewrites it
(`internal/pipeline/key.go:95-97`, `internal/stages/rebase.go:284-321`). So a
`reviewed_revision` that differs from `gate.submitted_head` is the gate's
rewrite, not an author's push. It never makes a head uncompared. The report
lists every such head as "rewritten by the gate", since its findings' line
numbers refer to the rewritten commit and may sit off the submitted one's.

**Building and binding a planted head.** A planted record either names an
existing commit, or it is a construction spec that a head is built from, as s02
and s08, the C4 candidates, are (the original's lines 283-296).

- **Building:** a head built from a spec is built by Joel, as pilotseal, reading
  the spec under section 6's logged `access` (`by: build`). No uid-501 process
  ever reads a spec. He commits the result in a pilotseal-owned repository and
  hands the commit to the operator, who pushes it like any planted head. Its
  40-hex commit becomes the planted head's `head`. The commit is code the gate
  has to read anyway; the spec's expected failure mode and scoring rule stay
  sealed.
- **Binding:** `pilot-score.py bind <sealed_id>` (F4), run by Joel as pilotseal
  and logged as an `access` (`by: bind`), reads the record and prints only the
  case it names and, for a record that names a commit, that commit. Joel copies
  those values into the planted head's `enroll`; for a built head, `head` is the
  commit he built. A record that names no case, or names neither a commit nor a
  construction, cannot be bound, so that planted head is not enrolled.
- **Before signing:** `pilot-score.py bind --census`, as pilotseal, appends an
  `access` line (`by: census`, section 6) and prints only counts: how many
  records name a commit, how many are construction specs, and how many name a
  case. It is run before signing with open item 3's parse-only check (section
  11), so a pilot whose planted cases can't meet C1's minimums is caught before
  any live enrollment.

At scoring, the script checks again that each planted `enroll`'s `case`, and its
`head` where the record names a commit, equal what its record names, and a
planted head that differs is UNMEASURED for every oracle (F4). The planted
commit and its case are not sealed: in the original the case is a manifest field
(line 363), and only `planted_truth` (line 366) is sealed. Knowing them does not
bias Joel, whose only rulings are on the O5 items of live and replayed heads, on
race and recovery outcomes, and on classifications (section 8).

**Where a planted head's review record lives.** A planted head's review record
is a sealed record (O5). **A stated limit:** the operator runs the pinned
adapter as uid 501, so its output first exists in uid-501 space. There an agent
can read it, as the original's line 169 let the operator read sealed records,
and can also rewrite it before Joel copies it. `review_adapter_sha256` is
written by the operator too, so nothing ties the sealed bytes to a real adapter
run. Joel copies, not moves, the file: pilotseal cannot delete from uid-501
space, so the agent's copy stays. The effect is that a planted head's review
verdict, which decides whether it is compared, rests on bytes an agent could
have changed. That can move C1's planted-case counts. It cannot move C4, whose
scope is every planted real-finding and hold head whether compared or not
(section 5). **What closes it:** Joel runs the pinned adapter himself, as
pilotseal, writing straight into the sealed directory, so no uid-501 process
holds the output; that needs the adapter's reviewer to run from the pilotseal
account. Joel copies the file, as pilotseal, to
`/Users/pilotseal/pilot/sealed/reviews/<head_id>.md`, never into `<captures>`,
and listed by hash in that head's `freeze` `raw[]`, as the entry whose `file` is
`sealed:reviews/<head_id>.md`. The `sealed:` prefix names
`/Users/pilotseal/pilot/sealed/`, so readers open that entry there, never under
`<captures>` (as built, `read_captures` opens every entry under `<captures>`,
F4). It has two readers, both in `pilot-unblind.py` and both run as pilotseal:
the *compared* predicate (F6), which reads only its verdict and the head its
line 1 names (the one-head rule), whether `pilot-unblind.py` calls it or
`pilot-score.py` does; and `pilot-unblind.py run`, which publishes it at
unblinding with s01 to s08 (F6). Nothing else opens it, and the `items` step
never reads it.

**Streams and chain heads.** The sealed ledger is
`/Users/pilotseal/pilot/ledger.jsonl`. Whether uid-501 captures go to a second,
public ledger is coordinator open item 2 (section 11). The coordinator, as uid
501, cannot read the sealed ledger (section 6, precondition 6), so it cannot
compute a chain head. Joel does: right after the first `preflight` and after
each `freeze`, `close`, `score` and `unblind`, he runs the installed
`pilot-ledger.py head` on the sealed ledger as pilotseal (`sudo -u pilotseal`)
and passes the printed line (`head`'s line, which reveals no content) to the
coordinator, who posts it as given. That line passes through Joel's uid-501
terminal, so section 14's residual gap applies to it. Pilot-op's proposal
(`cl-pilotop` REPORT.md, step 6) suggested posting each `frozen_commit` the same
way.

**Parameters.** The scripts read these from this block, through the `preflight`
line, never from a flag. A value that is Joel's stays `UNRULED` until he rules.

```pilot-v2-params
k: 3
end_rule: pr-sequence
pr_count: 10
pr_kind: fleet-tools-reliability-fix
j1: option-2
j1_budget: 48 hours
j1_order: open-window-first-then-oldest-posted-then-lower-pr
j4: ssh-keygen-Y-sign-ed25519-sk-touch
```

`k` is a whole number; `end_rule` is `pr-sequence` (J3, section 11); `pr_count`
is 10, the number of compared sequence heads that ends it (section 8; Joel,
7083761337; section 12); `pr_kind` is
`fleet-tools-reliability-fix`, the kind relay point 1 names, defined in section
12; `j1` is `option-2` (relay point 2); `j1_budget` is `48 hours` (Joel,
7121929832, answer 3); `j1_order` is the order Joel agreed on Nov 24 (I2,
section 11); `j4` names the signing form Joel agreed on Nov 24: `ssh-keygen -Y
sign` with an ed25519 SSH key on a hardware token that requires a touch (I5,
section 13). The size bound and the exclusion rules have no key here: they are
section 12's text, covered by Joel's signature of this text. The 48-hour
deadline (I4, answered Nov 24) has no key here either, since this block's keys
are frozen: its one home is the `deadline` line of section 11's open-inputs
block, which preflight copies into the `preflight` line and the scripts read
from there (F5), as they read the adapter pin.

## 8. End condition and unblinding

`ft:pilot-unblind.py` decides the end condition, and it fails closed: anything
missing, unreadable or unparsable is a refusal. The end condition holds only
when all of these hold:

1. The ledger verifies.
2. Exactly one `close` exists, and its `at` is not later than the clock.
3. Every `enroll` has a `freeze` with a 40-hex `frozen_commit` and a non-empty
   `raw[]` of `{file, sha256}`.
4. The end rule of section 7's parameters holds, read from the `preflight` line.
   Joel ruled it (J3): the pilot ends after one consistent sequence of PRs, the
   same kind of work, chosen to measure cost and efficiency. It holds once 10
   *sequence heads* are frozen **and compared** (section 5), or once 48 hours
   have passed since the window opened, whichever comes first (the window's
   close, below). A sequence head is an enrolled head whose `case` and
   `provenance` are both `live`, whose `work_kind` equals `pr_kind`
   (`fleet-tools-reliability-fix`, section 12), and whose `pr_created_at` is in
   the window: at or after its opening, the `at` of the first `preflight` line,
   and not after its close (section 12), and whose `enroll` line's `at` is not
   later than the window's close. A head enrolled after the close cannot be
   frozen before it, so this condition never moves where the window closes.
   **The window's close** is the earlier of two times (J3, Joel 7135308036 and
   7135401609): the `at` of the first `freeze` after which 10 sequence heads are
   compared, and the window's opening plus 48 hours (the `deadline` of section
   11, read from the `preflight` line). The rule holds once the window has
   closed, by count or by time. The sequence-head definition above is the one
   owner of which live heads C1, C3, C5 and the `items` line cover (section 5).
   C1's live minimum counts compared sequence heads, so it is met when the
   window closes by count. When it closes by time with fewer than 10 compared
   sequence heads, C1 FAILs and the pilot is not a pass; the report says the
   window closed by time and how many sequence heads were compared. Every window
   PR is enrolled with `case` `live` (section 12), so no eligible PR can leave
   the sequence by its case; planted heads never count. As built,
   `pilot-unblind.py` counts frozen heads of provenance `live`, not compared
   sequence heads, so F6 must land before signing (section 9, precondition 12).
   The condition fails closed if the parameters ever stop naming a kind or a
   count of 10, so nothing is scored or unblinded then. As built,
   `pilot-unblind.py` has no deadline, so F5 must land before signing too
   (precondition 12).
5. For unblinding only: a `score` line exists after the `close`.

**Refusal.** A refused unblinding appends `unblind-refused` with the reason. A
refused scoring appends `score-refused`. So an early attempt is logged, as the
original's lines 199-200 require. Three refusals are printed but cannot be
logged. A ledger that does not verify (condition 1) takes no append, and the
posted chain heads show where the chain broke. A second `pilot-unblind.py run`
refuses with "already unblinded" before it reaches its logging step. And any
refusal after `unblind` cannot be logged, since only a `correction` may follow
it (section 7). A refusal prints no record content and publishes nothing.

**Joel's rulings come before the score.** After `close`, `pilot-score.py items`
(F4) appends the one `items` line. It lists every O5 item of every compared
replayed head and every compared sequence head (end condition 4) except
`matched` ones, each `unlocatable` finding as its own
item, and the outcome of every enrolled race and recovery head except a
`base_mismatch` or pin-failed one for C7. Each item gets an id. It never reads a
planted head's review record, which is sealed and kept out of `<captures>`
(section 7), and for a race or recovery outcome it reads only the gate capture
and the manifest's declared injection, so listing opens no sealed record and
prints no score. Joel then rules items, working in the I2 order, one `ruling`
line each, until the budget is spent. The order guides his work and decides
nothing: a ruling made out of it still counts, and the report lists each such
ruling, computed from the `ruling` lines and `j1_order` (F4). Only then is the
one `score` line appended: `pilot-score.py` refuses before an `items` line
exists or before its budget period ends (F4). It reads the `ruling` lines and
applies section 5's C3, C5 and C7 rules to them. Every item without a ruling is
UNMEASURED. Since `items` and `ruling` are allowed after `close` (F1), and the
`score` comes after both, C3, C5 outside clean heads and C7 can be measured.

**The `ruling` line.** Its vocabulary is the original's, by reference:

- `subject` is `item` (an id from the `items` line) or `classification` (a PR
  number, for section 12's disputes);
- for an O5 item, `outcome` is one of the four-way rulings, with the meanings
  of the original's lines 106-115 (`gate right`, `review right`, `both`,
  `neither`), and `severity` is one of the original's line 213 scale
  (`blocker`, `should-fix`, `nit`, `not-real`);
- for a race or recovery outcome, `correct` is `true` or `false`;
- for a classification, `class` is `eligible` or one of E1 to E6;
- `budget` records where the line falls in the J1 budget. The script
  recomputes it from recorded bytes, and the recomputed value wins; a recorded
  value that differs is reported beside the line. The budget is 48 hours (Joel,
  7121929832, answer 3): the *budget period* opens at the `at` of the `items`
  line and lasts 48 hours. It is not section 12's live window. An item ruling
  is inside the budget when its `at` falls in the budget period. A
  classification ruling is not tied to the `items` line, and F1 lets it come
  before or after `close`, so it is not timed; it is inside, and the report
  counts it apart.

**What the budget costs.** Since the budget is hours, classification rulings are
not timed and do not draw on the budget period, and O5 items are limited only by
time. An item Joel has not ruled when the budget period ends, 48 hours after the
`items` line's `at`, is UNMEASURED, so C3, C5 outside clean heads and C7 can
stay UNMEASURED and the pilot then cannot pass. Joel may leave any dispute
unruled, which costs nothing (section 12).

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

**Publishing.** `pilot-unblind.py run` copies the sealed files s01 to s08 into a
staging directory beside the publish directory, recomputes each copy's SHA-256
against the s01-s08 table of the original (read from blob `32424c20`, which it
checks), and refuses on any mismatch. Only then does it append `unblind` with
those hashes (as built, `published` covers s01 to s08, not the ledger; F6 adds
the planted heads' sealed review records) and the chain head it checked. After
that line is written it copies the ledger into the staging directory, checks
that the copy ends in that `unblind` line, and only then moves the staging
directory into the publish directory, so nothing is published unlogged. A second
run refuses with "already unblinded". After that, the records are copied to
`docs/evaluations/` in this repository, as the original's line 173 says.

## 9. Enrollment preconditions

Enrollment needs every one of these. Choosing an option is not evidence (#A1
7065454500). `ft:pilot-preflight.py check` tests them and names the first that
fails:

1. The original's blob at `fd80055c` and at assistant `main`, read from the
   assistant history clone (section 1), is
   `32424c209b2bc43cd18ba653c7d2091edefc5dcc`.
2. This text exists on assistant `main` in that clone, and its s01-s08 rows
   equal the original's lines 182-191 byte for byte.
3. Every grep hit of section 3 has a row in its table, with the original's
   exact line.
4. Joel's signature of this text is recorded, naming the signing commit, which
   meets section 1's three conditions, and made with the key pinned in the
   trust file of section 13 (I5). `j4` is ruled (I5), so as built this check
   fails with "has no check yet" until F3 lands the verification, so no
   enrollment can pass this check before then.
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
   stat. Listing is not reaching: a directory can deny listing and still allow
   traversal (precondition 10). So the record must also show that uid 501
   cannot reach anything below `/Users/pilotseal`:
   - **traversal probes:** for each of `/Users/pilotseal`,
     `/Users/pilotseal/pilot`, `/Users/pilotseal/pilot/sealed` and
     `/Users/pilotseal/pilot/sealed/reviews`, a stat of a name inside it that
     need not exist (`<dir>/.pilot-traverse-probe`), denied with `EACCES`. An
     `ENOENT` means the directory can be traversed, and fails the record;
   - **read probes:** a read-only open, denied with `EACCES`, of the sealed
     ledger `/Users/pilotseal/pilot/ledger.jsonl` and of every sealed record
     `/Users/pilotseal/pilot/sealed/s01.md` to `s08.md`;
   - **no stat below the top:** a stat that succeeds on any target other than
     `/Users/pilotseal` fails the record, since it shows a directory above that
     target can be traversed.

   This is what sections 6 and 7 rest on when they say uid 501 cannot read the
   ledger, the sealed records or `sealed/reviews/`. As built, `probe` records
   neither traversal probes nor these targets (F3).
7. The `install` record shows that the SHA-256 of each script in
   `/Users/pilotseal/bin` equals the blob pinned in section 10 (J5), and the
   trust file of section 13 holds the same hashes.
8. Section 12's eligibility rule is in this text at the signing commit.
9. Each owner record of section 13 that the pilot relies on is signed with the
   pinned key. If the private key is a file, a denied-read record as uid 501
   against its location exists. Whatever the key, a use-denied record exists,
   dated after the signing commit and made from an agent session as uid 501:
   an attempt to sign a test message with the pinned key fails, through every
   agent the key could be reached by (for example `ssh-agent` or
   `gpg-agent`), or, for a token, fails without a physical touch (section
   13).
10. A write-denied record as uid 501 against the sealed ledger, the trust file,
    the checker's directory, section 10's history clone and section 1's
    assistant history clone exists, dated after the signing commit (sections 6,
    13 and 14). For `/Users/pilotseal/bin` it shows that creating a file in the
    directory, and opening each installed script there for writing, are both
    denied with `EACCES`, and for the history clones
    `/Users/pilotseal/pilot/fleet-tools.git` and
    `/Users/pilotseal/pilot/assistant.git` that creating a file in either is
    denied the same way. Read probes alone do not show it: a directory can deny
    listing and still allow traversal and writes by group.
11. The pinned `review-size.sh` and `pilot-review.sh` are in the `install`
    record, at the hashes the trust file pins. No enroll script exists (section
    14).
12. **No activation before review and reconciliation** (#A1 7114817207). Every
    script this text pins, and every script follow-up of section 17 (F1, F3, F4,
    F5 and F6), has landed through the landing gates
    with its review before the signing commit, so the pin section 10 fills there
    is a blob that contains it. Section 10's `pilot-v2-followups` block names
    each follow-up's PR and merge commit, and the trust file holds the same
    values; preflight checks them (F3) against the history in section 10's
    history clone, as section 10 says. A follow-up that lands after signing is
    not in the pinned blob, so it needs this text amended and re-signed. F2 is
    part of this text and needs no landing of its own. Every landing requirement
    that changed between this text's signing and the first live `enroll` has
    been reconciled explicitly, comparing the trust file's landing baseline
    (section 13) with `landing_regime` at that `enroll`: either this text is
    amended and re-signed to adopt it, or a signed owner record says it does not
    apply to the pilot (section 15).

13. The fleet-tools mirror and its gate home of section 12 exist, isolated like
    the assistant mirror (the original's item 9 and lines 349-352), with the
    gate's first-run path proven on it, and the home's `config.yaml` declares
    `no_ci: true` (section 12, "CI"). Neither that home nor the assistant gate
    home that runs planted and replayed heads sets any `usage_ceiling.*` key
    (`internal/config/key.go:313-354`), and the preflight record shows both
    `config.yaml` files with none (F3).
14. **The pin is out of the repo and the copies match it** (#juno 7115601439).
    Preflight reads the key fingerprint and the executable hashes from the
    trust file at section 13's location (I8), never from this text. That
    location is built into the checker itself: it is a constant in the landed
    `pilot-preflight.py` whose blob the pilot pins, installed in
    `/Users/pilotseal/bin`, which uid 501 cannot write (precondition 10). The
    `trust_file` value in section 13's copy block is only a copy of that
    constant, and preflight refuses when they differ. Each copy
    in this text must equal the trust file: the fingerprint in section 13's
    copy block, section 10's table, and section 12's rows. Any
    mismatch, or a trust file that is missing or unreadable, refuses
    enrollment.
15. **No agent session can use sudo to reach pilotseal.** Joel's account is uid
    501, the uid agents run as, so the `sudo -u pilotseal` Joel runs starts from
    the agents' own uid. A sudo-denied record, dated after the signing commit
    and made from an agent session as uid 501, shows that
    `sudo -n -u pilotseal /usr/bin/true` is refused: no `NOPASSWD` rule lets uid
    501 reach pilotseal, and no sudo ticket usable from that session is cached.
    uid 501 cannot read the sudoers policy (`/etc/sudoers` is readable only by
    root and `wheel`), and the text relies on no uid-501 view of it. The
    policy comes from a second record, the *sudoers record*: in his own
    terminal Joel uses `/usr/bin/sudo` to copy, as root, the bytes of
    `/etc/sudoers` and of every file it includes (each `#include`,
    `@include`, `#includedir` and `@includedir` target, recursively), with each
    path, and the output of `visudo -c`, into one file owned by pilotseal. The
    record must show `visudo -c` reporting every file parsed OK, a global
    `Defaults timestamp_type=tty` or `Defaults timestamp_type=ppid` line, and
    no other line that sets `timestamp_type` in any scope: none for a user
    (`Defaults:`), host (`Defaults@`), runas user (`Defaults>`) or command
    (`Defaults!`). The text relies on that explicit line, not on sudo's
    default. `check`, as pilotseal, validates both records and records both
    hashes in its `preflight` line (F3). No field of either is typed in. The
    sudoers record passes through Joel's uid-501 terminal, so section 14's
    terminal gap applies to it. The sudo-denied record shows only that an agent
    session cannot use sudo by itself. It does not show that an agent cannot
    tamper with the terminal Joel uses, which section 14 states as a residual
    gap.

**A residual gap: uid-501 records attest themselves.** The denied-read,
use-denied, write-denied and sudo-denied records of preconditions 6, 9, 10 and
15 are files written by a process running as uid 501, the uid they restrain.
`check` validates their fields and hashes them, but nothing ties a record to a
probe that really ran: an agent whose session can in fact reach `ssh-agent` or a
sudo ticket could write a well-formed record by hand. What narrows it: Joel runs
each probe himself, under section 14's terminal practice, and checks that the
hash `check` records is the hash of the file he saw written. What closes it:
running each probe as uid 501 from the separate account of section 14 (`sudo -u`
from there) and signing each record with the pinned key, so a record an agent
wrote fails its signature. Until then this is a stated residual gap.

On success it appends a `preflight` line. It never enrolls. As built
(`6adbd531`), `pilot-preflight.py` checks items 1 to 7 only in part: item 4
refuses every signing form, item 6 dates its record against the commit that
added this text, not the signing commit, and item 7 compares the installed
hashes with section 10 of this text, not with the trust file. Items 8 to 15
are not checked at all. F3 adds every one of these. Until F3 lands, item 4
refuses every signing form, so no enrollment can pass preflight without them.

## 10. Scripts

| script | fleet-tools path | merge commit | blob SHA-256 |
| --- | --- | --- | --- |
| ledger | `pilot-ledger.py` | UNPINNED | UNPINNED |
| unblinding | `pilot-unblind.py` | UNPINNED | UNPINNED |
| scoring | `pilot-score.py` | UNPINNED | UNPINNED |
| preflight | `pilot-preflight.py` | UNPINNED | UNPINNED |
| review findings, used by scoring | `review-findings.py` | UNPINNED | UNPINNED |

By the signing commit each row names a fleet-tools `main` merge commit and the
SHA-256 of the script's bytes there. For a script with a follow-up (F1 for
`pilot-ledger.py`, F3 for `pilot-preflight.py`, F4 for `pilot-score.py`, F6 for
`pilot-unblind.py`, and F5 for `pilot-unblind.py` and `pilot-preflight.py`),
that commit is the merge of the script's latest
follow-up, as the block below records it.

```pilot-v2-followups
f1: UNPINNED F1
f3: UNPINNED F3
f4: UNPINNED F4
f6: UNPINNED F6
f5: UNPINNED F5
```

Each value becomes `ft#<N> <40-hex merge commit>`, the follow-up's PR and the
merge commit that landed it.

**Containment, checked by preflight (F3).** Preflight reads fleet-tools history
only from the *history clone*, `/Users/pilotseal/pilot/fleet-tools.git`, a bare
clone of `dayamjz/fleet-tools` that uid 501 cannot write (precondition 10's
write probes cover it), kept fresh as section 1 keeps the assistant clone:
fetched with `+refs/heads/main:refs/heads/main` right before each `check`, and
compared with `git ls-remote origin refs/heads/main`, refusing on a mismatch.
For each script with a follow-up it checks that:
- the row's merge commit equals the merge commit of the script's latest
  follow-up in the block, and that commit is on the clone's `main`
  first-parent history;
- the script's blob at that commit hashes to the row's SHA-256 and to the trust
  file's, so the pinned bytes are exactly the bytes that follow-up landed. No
  later change can be in the pin, so a later merge that reverts part of the
  follow-up is never pinned;
- when a script has two follow-ups (F5 with F3 or F6), the earlier one's merge
  commit is an ancestor of the later one's. The later PR was reviewed on a base
  that held the earlier one, and that review is what keeps the earlier change in
  it.

A commit id is a hash that covers its tree, so a fetched history that someone
rewrote cannot give a recorded merge commit different bytes: it can only lack
the commit, and then the check refuses.

The copies Joel installs in `/Users/pilotseal/bin` must hash to the rows'
SHA-256 values, and preflight check 7 compares them. These rows are a published
copy. The authoritative hashes are in the trust file of section 13, outside any
repository an agent can write, and the two must agree (F3).

## 11. Owner questions for v2

This text is not signed while any input below that blocks signing is open. Each
one that blocks has an `UNRULED` or `UNPINNED` value (section 1): `j1_order`
`trust_file` (I8) and `key_fingerprint` in section 13's copy block,
`freeze_writer` (open item 2) and `chain_head_checker` (open item 4) in the
block below, and the follow-up merges in section 10's `pilot-v2-followups`
block. `key_fingerprint` is filled once Joel makes the key of I5 (answered).
Once Joel answers, his answers are quoted here verbatim with their comment
ids, as a#977 folded in section 7.12, and his key signature of this text
(section 13) is what makes them binding.

```pilot-v2-open-inputs
deadline: 48 hours
freeze_writer: UNRULED open item 2
chain_head_checker: UNRULED open item 4
```

`deadline` is I4's answer: the window closes 48 hours after it opens if 10
sequence heads are not compared first (section 8, end condition 4). This line
is its one home; preflight copies it into the `preflight` line (F5).

**Answered or narrowed by the Nov 23 direction** (#A1's relay 7111628611,
translated in 7114296700):

- **J1. Live-head disagreements: option 2.** Relay point 2: Joel adjudicates
  unresolved disagreements, with the order and budget frozen before
  enrollment. So O5's items are ruled by Joel alone, as an endpoint different
  from the frozen text, and an item past the budget is UNMEASURED. The budget is
  48 hours and the order is Joel's Nov 24 agreement (I2, answered). A disputed
  eligibility classification (section 12) is also a J1 item, ruled but not timed
  (section 8). Ruling needs a ledger kind the landed closed set lacks (F1).
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
  is `fleet-tools-reliability-fix` (section 12). The count of 10 is Joel's
  (7083761337, above). The pilot ends once 10 sequence heads are compared,
  which can take more than 10 enrolled PRs (sections 8 and 12), or once 48
  hours have passed since the window opened, whichever comes first (I4).
- **J4. How Joel signs: direction answered.** Relay point 4: a locally signed
  owner artifact that agents cannot impersonate (section 13). The key is
  answered (I5): an ed25519 SSH key on a hardware token with touch required,
  signing with `ssh-keygen -Y sign`.
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
- **I2. J1 budget and order: answered.** The budget is 48 hours (answer 3),
  which fills `j1_budget` (section 7). The order is Joel's Nov 24 agreement
  (below), which fills `j1_order`.
- **I3. J2 first half: answered, required.** C3, C5 outside clean heads and
  C7 are required criteria, ruled by Joel under J1 within the budget, and
  UNMEASURED past it (answer 4), so a pilot with any of them UNMEASURED is not
  a pass.
- **I4. J3 deadline: answered, 48 hours.** On Nov 23 Joel wrote "No deadline,
  maybe 48 hrs" (answer 5); on Nov 24 he wrote "Stop at 48 hours" (below). The
  pilot ends at 10 compared sequence heads or 48 hours after the window opens,
  whichever comes first. This fills `deadline` (above) and section 8's end
  condition, and F5 builds it.
- **I5. J4 key custody: answered.** Joel agreed on Nov 24 (below): an ed25519
  SSH key on a hardware token, a YubiKey or similar, with touch required, so
  the private key is never on disk; its public key pinned outside the repo in a
  file pilotseal owns, with a copy here; a pin change needs a signature from
  the old key; signing uses `ssh-keygen -Y sign`. This fills `j4`. The
  fingerprint copy is filled once the key is made.
- **I6. The writer of section 14: answered, Joel by hand.** Joel adds the
  enrollments himself, as pilotseal (answer 7). No script enrolls a head.
- **I7. The promotion conflict of section 16: answered.** Joel does the local
  substitute check for the server-protected required check himself. If he has
  not done it within 48 hours of its being due, #claude (the arbiter) does it
  (Nov 24, below). Section 16 records this, and this text's signature covers
  it.
- **I8. Where the trust file lives: open** (#juno 7115601439). It was not
  among the questions put to Joel on Nov 23. It must meet section 13's rules:
  outside every repository and every agent's home, not writable by uid 501,
  and written only by Joel. A path under `/Users/pilotseal` would meet them,
  as would another location Joel names. This fills `trust_file` in section
  13's copy block.

**Answered on Nov 24** (#claude's relays 7135308036, 06:41 PT, and
7135401609, 07:03 PT, on fleet-tools#545). To the four open items, Joel wrote,
verbatim:

> 1. Stop at 48 hours
> 2. Show me a proposed order
> 3. I will be the substitute, else the arbiter
> 4. If safe close

and to #claude's three proposals that followed, verbatim:

> Agreed on proposals

Read narrowly, as #claude relayed them:

- **J3 (I4):** the pilot ends at 10 eligible PRs or 48 hours, whichever comes
  first (section 8, end condition 4).
- **J1 order (I2):** "First, any disagreement on a head whose window is open.
  Then the rest, oldest first by the time the disagreement was posted. Ties go
  to the lower PR number. The 48-hour budget per ruling is unchanged, and
  anything past it is UNMEASURED." In this text every item is posted at once,
  by the one `items` line, and every item ruling's window is that line's budget
  period (section 8). So every item's window is open together, every item was
  posted at the same time, and the order comes down to the lower PR number
  first, by item id within one head. Past the budget period an item is
  UNMEASURED. As section 8 says, the order guides Joel's work and decides
  nothing, and the report lists a ruling made out of it.
- **(7) substitute (I7):** Joel does the local substitute check for the
  server-protected required check; if he has not done it within 48 hours of its
  being due, #claude (the arbiter) does it (section 16).
- **J4 key custody (I5):** the key of section 13.
- Item 4 (an idle pane) is not about this text.

None of these enrolls a head, promotes a gate, or changes rule #68 or a landing
control.

**Open before signing, for the coordinator (not owner questions).**

- Who appends `capture` and `freeze`, and whether uid-501 captures go to a
  second, public ledger (open item 2 of row 8's brief). Joel appends `enroll`
  and `exclude` himself (I6). This blocks signing: `freeze_writer` above is
  `UNRULED` until it is named.
- Where chain heads are posted, and who compares them with the ledger, at
  unblinding and for section 13's rotation detection (open item 4). This blocks
  signing: `chain_head_checker` above is `UNRULED` until it is named.
- Whether Joel runs a parse-only check of the real sealed records, as pilotseal,
  printing only how many parse under O1 (open item 3), together with
  `bind --census` (section 7). If he does, it is `pilot-score.py parse-check`,
  which logs an `access` line like every other read of a sealed record (section
  6). If none parses, C4 is wholly UNMEASURED, and if too few records can be
  bound, C1's planted minimums can't be met. This does not block signing and has
  no value: it only tells in advance whether C4 and C1 can be measured, and
  skipping it can only leave them UNMEASURED, never produce a PASS.

## 12. The eligible sequence

Relay point 1, with #juno's fixes 1 to 3 (7114355345) and #claude's point 3
(7114424384). This rule is frozen at the signing commit. A PR is
left out only by a rule written here.

- **The window.** The live window opens at the `at` of the **first** `preflight`
  line in the ledger, so a later preflight never moves it. It closes at the
  window's close of section 8, end condition 4: the `at` of the first `freeze`
  after which 10 sequence heads are compared heads (section 5), or 48 hours
  after the opening, whichever comes first. A PR is in the window when its
  GitHub `created_at` falls in it, at or after the opening and not after the
  close. A PR created before the window opens is not a window PR: it gets no
  record, is never reconciled, and F1 refuses a live `enroll` for it, so a PR
  reviewed before signing cannot be picked into the sequence. Enrollment
  continues in order until then, so an enrolled PR that is never compared, for
  example one closed before review, stays enrolled and reported, and a later PR
  in the order makes up the 10, unless the 48 hours run out first. No PR is
  replaced: the uncompared one keeps its record.
- **After the window closes.** No live `enroll` is written once the window has
  closed. Before each live `enroll`, the writer runs `pilot-unblind.py window`
  (F6), which checks end condition 4 alone, computing the window's close from
  the ledger and the frozen captures, and writes no `enroll` once it says the
  window has closed. A PR created in the window that still has no record then
  gets an `exclude` under the first of E1 to E6 that applies (the precedence
  below), so E5 or E6 only when E1 to E4 do not. A PR created after the window's
  close gets no record and is never reconciled. If a live head whose
  `pr_created_at` is after the close is enrolled anyway, or one created before
  the opening gets past F1, or a live `enroll` for any PR is written after the
  window's close (its `at` later than the close), it is not a
  sequence head by end condition 4, so it is in none of C1, C3, C5 or the
  `items` line (section 5), and the report lists it (F4). The last case takes
  the place of the PR's E5 or E6 record, so it also marks the pilot "possibly
  selected".
- **A window PR's case.** Every PR in the window that is enrolled is enrolled
  with `case` `live` and `provenance` `live`. This replaces the original's lines
  279 and 280 for v2: a race or recovery head is planted or replayed, never a
  window PR, so the writer cannot move an eligible PR out of the sequence by
  giving it another case, or another kind. F1 makes the ledger refuse a window
  `enroll` (section 7, "Window records") whose `case` or `provenance` is not
  `live`, or whose `work_kind` differs from the `pr_kind` of the last
  `preflight` line before the first `enroll` (section 7); and an `enroll` of
  provenance `live` that is not a window record. A window PR that fails the kind
  test gets an `exclude` under E1, never an `enroll` with another kind.
- **The live case.** This replaces the original's line 281: the live case's
  minimum is 10 compared heads, and its heads are this sequence. As built,
  `pilot-score.py` still holds the original's live minimum of 4, so F4 must land
  before signing (precondition 12).
- **Live heads run on fleet-tools.** This replaces the original's lines 58-60,
  61-69 and 87-91, and the sources at lines 391-393, for a live head:
  - **Mirror.** The gate runs against a private mirror of `dayamjz/fleet-tools`,
    never `dayamjz/fleet-tools` itself, with its own gate home. The stage-model
    pin (the original's lines 324-328) and the intent sentence (lines 319-323)
    are the same as for every other head (the original's section 3 items,
    kept by section 2), and `pin_check` checks the pin for each head
    (section 7).
  - **Base.** The merge base of the head with fleet-tools `origin/main`. The
    operator resets the fleet-tools mirror's `main` to it before the push, and
    the original's base check (lines 310-316) applies unchanged.
  - **Review record.** The full verdict at that head under the landing rules is
    a line of `ftw:reviews/ft-<N>.verdicts` of the form
    `--- verdict ft#<N> at <short sha> (full): VERDICT: ...`. The counted line
    is the first such line at the enrolled head, and a timely capture leaves it
    as the last line of `verdicts.txt` (section 7, "compared"; this replaces the
    original's lines 68-69 for live heads). The review it records is found by
    content, never by file name, since archived copies are named in more than
    one way (for example `ft-787-full-ff23425.md` and
    `ft-791-claude-full-a787e4f.md`). It is the file in `ftw:reviews/` named
    `ft-<N>.md` or `ft-<N>-<anything>.md` whose line 1 is exactly
    `reviewed at <40-hex>` naming that head, with no `(delta from ...)`, and
    whose verdict, as `ft:review-findings.py --verdict` prints it, equals the
    counted line's. That line 1 gives `review.head` (the 40-hex), and the
    absence of `(delta from ...)` gives `review.kind`, which is `full`. Its line
    2, `reviewer: <reviewer> <model> ...`, gives `review.reviewer`. If no such
    file exists, the head has no review, and its `review.verdict` is `none`. If
    more than one exists, they must have the same line 2 and the same findings
    block, as `ft:review-findings.py` prints it, so any of them gives the same
    `review.findings[]`. If they differ in either, the head has no review, and
    the report lists the files.
  - **CI.** `origin_ci` is
    `gh api repos/dayamjz/fleet-tools/commits/<head>/check-runs`. Fleet-tools
    runs no CI workflow today, so that list is empty. An empty list is not a
    pass: the run waits in the `ci` stage until `checks_timeout` unless the
    repository declares `no_ci` (`docs/dogfood.md:145-152`,
    `docs/prd.md:3450`). So the fleet-tools gate home's own `config.yaml` sets
    `no_ci: true` (precondition 13), and that declaration, as the original's
    line 390 sources it, is what makes the gate's `checks_basis` on the mirror
    `no-ci-declaration`. No live head is a
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
  one is in the ledger, and otherwise applies the test above. A
  classification ruling is not timed and is always inside the budget (section
  8). So a dispute Joel has not ruled leaves the test's classification
  standing, and the dispute is reported. Once a PR's
  `enroll` or `exclude` line exists, its classification never changes, and no
  enrolled PR is removed or replaced by another. A later ruling is recorded
  and reported beside it.
- **Size.** Eligible PRs have at most 300 code lines (I1, answered: "300 or
  less", 7121929832), measured by the pinned `review-size.sh` at the
  enrollment head: the PR head the `enroll` or `exclude` line records, at the
  time the record is written. For a PR recorded before its first review, as
  every PR should be, that is before the review. For an E5 candidate, tested for
  E2 first by the precedence below, it is the head at the time Joel records it,
  which is after a review. A PR that grows past the bound after it is enrolled
  stays enrolled. The report lists it with both sizes, and it is never quietly
  dropped.
- **Order.** Live PRs are taken in order of GitHub's `created_at`, which is
  immutable, with ties broken by PR number, lower first. They are never
  ordered by a relay or request, since the coordinator controls when it relays
  (#juno fix 1). "Consecutive" means the eligible PRs in the window are
  enrolled in this order, none skipped, until 10 sequence heads are compared
  (the window above). That can be more than 10 enrolled PRs.
- **Every PR gets a record.** Every PR created in the window gets an `enroll`
  or `exclude` line before its first review. No live `enroll` may be written
  while any PR earlier in that order has no record. So a PR with no record
  blocks the next live enrollment. A planted head's `enroll` has no place in
  the PR order and is never blocked by it.
- **Reconciliation.** Before each live `enroll` and before `close`, the writer
  lists every `dayamjz/fleet-tools` PR created in the window, open or closed,
  from GitHub, and checks the window records (section 7), never counting a
  replayed head's `enroll`, whose `pr` is an assistant number. Before a live
  `enroll`, every PR earlier in the order than the one being enrolled must have
  exactly one record, and the one being enrolled must have none. Before
  `close`, every PR created in the window must have exactly one. A PR with none
  where one is due, or with two, refuses the `enroll` or the `close`. A planted
  `enroll` is not reconciled, as "Every PR gets a record" says. Joel does this
  by hand under F2's procedure, and the ledger enforces the "two" half (F1): it
  refuses an `enroll` or `exclude` naming a PR that already has a record.
- **The exclusion rules.** These are the only ones. Each `exclude` line names
  one rule. When more than one applies, the first in the order E1, E2, E3, E4,
  E5, E6 is recorded, except that between E5 and E6 the rule in E6 decides. So a
  PR that fails the kind, size or pinned-file test is excluded for that, never
  as E5, and only an otherwise eligible PR can count as an E5 selection lever:
  - **E1.** It fails the kind test above.
  - **E2.** It has more than 300 code lines at the enrollment head.
  - **E3.** It changes a file the pilot pins (section 10, or this section's pin
    table: `review-size.sh` and `pilot-review.sh`).
  - **E4.** It was closed unmerged with no review.
  - **E5.** It was reviewed before its record was written: when the writer comes
    to record it, `ftw:reviews/ft-<N>.verdicts` already exists with at least
    one line. An empty file is not a review.
    The record states that, with `verdicts_seen: true` and `verdicts_sha256`
    (section 7). The report counts E5 exclusions apart and lists each with its
    `created_at`, the `at` of its `exclude` line and that hash, so a PR can't be
    excluded quietly.
  - **E6.** The window closed before it was recorded: it was created in the
    window, and when the window closed (above) it had no record, so it can no
    longer be enrolled. The writer records it under E6 before `close`, so
    reconciliation can pass. The report counts E6 exclusions apart and lists
    each with its `created_at`. E6 applies only once the window has closed, so
    it never removes a PR from the sequence that could still be enrolled. No
    review time is trusted: the `.verdicts` lines carry none, and uid 501 can
    write their files. So the split uses only what Joel sees when he records.
    Right after `pilot-unblind.py window` first reports the close, he records
    every unrecorded window PR by the precedence above: first E1 to E4, and only
    a PR none of those fits gets E5 or E6. Among those, one whose
    `ftw:reviews/ft-<N>.verdicts` already exists then with at least one line
    gets E5
    (`verdicts_seen: true`), and the rest get E6 (`verdicts_seen: false`). F2
    runs `window` right after every live `freeze`, and again when the 48 hours
    run out, so the gap between the window's close and Joel's record is only
    his own delay, which the report
    shows as the difference between the two lines' `at`. A PR reviewed in that
    gap counts as E5. That over-reports the lever, never under-reports it.
  - **Residual selection levers.** The agents decide when reviews are
    requested, and they author, push and close the PRs, so they can drop a
    chosen eligible PR from the compared ten. That is the lever #juno's fix 1
    took away from ordering. The levers are:
    - an E5 exclusion (a review requested before Joel records the PR);
    - a live head captured late (another review requested before the
      capture, section 7);
    - a live head moved after enrollment (a push before its first full review,
      section 7's one-head rule), for example a merge of `main` into the PR
      branch pushed to get it dropped; a rewrite by the gate's own rebase is not
      a move and drops no head (section 7);
    - any other enrolled live head that ends not compared, for example a PR
      closed before its first full review;
    - an E4 exclusion: a PR closed unmerged before any review, before Joel
      records it;
    - an E1, E2 or E3 exclusion: an agent leaves out or alters the PR body's
      `Observed incident` field, pushes the PR past 300 code lines, or touches a
      pinned file.

    Nothing here prevents them. What limits them is that each is listed with its
    reason and times, and a pilot with any of the first five is reported as
    possibly selected, beside its result. E1 to E3 exclusions are counted and
    listed too, but they do not set the mark: those rules say what the PR is,
    its kind, size and files, which its author always decides by writing it, and
    a PR they exclude is not the work the pilot measures.
- **The pinned `review-size.sh` and planted-review adapter.** The fleet-tools
  blobs current when the signing commit is made, filled in by then like section
  10. The adapter `pilot-review.sh` produces every planted head's review
  (section 2), so a planted head's `gate.json` records `review_adapter_sha256`,
  the SHA-256 of the adapter copy that produced its review, and a planted head
  whose value differs from this pin is not compared (section 7):

| script | fleet-tools path | commit | blob SHA-256 |
| --- | --- | --- | --- |
| size | `review-size.sh` | UNPINNED | UNPINNED |
| adapter | `pilot-review.sh` | UNPINNED | UNPINNED |

## 13. Owner records and the signing key

Relay point 4, with #juno's fix 4 and #claude's point 2.

- **The key.** Joel signs with a key that no agent can read or use, within
  section 14's residual gap ("Joel's terminal is the agents' uid"): not readable
  by uid 501, and not under any agent's home or `~/Projects`. Agents run as uid
  501, so uid 501's home is ruled out too. The key is I5's answer (Joel, Nov
  24, 7135401609): an ed25519 SSH key on a hardware token, a YubiKey or
  similar, with touch required, so the private key is never on disk where an
  agent running as Joel's user could read or use it. Joel signs with
  `ssh-keygen -Y sign`, and F3 verifies with `ssh-keygen -Y verify` against the
  pinned public key. Reading is not the only risk: a key served to uid 501 by
  `ssh-agent` or `gpg-agent`, or a token with no touch policy, can be used by an
  agent without being read. So whatever the key, precondition 9's use-denied
  record shows that an agent session cannot sign with it.
- **A residual gap: a touch-required token.** If the key is on a token that
  needs a physical touch per signature, an agent can still start a signing
  request that Joel touches by mistake. What bars that is Joel touching the
  token only for a signature he started and read. He reads it with uid-501
  tools, though, which an agent can change (section 14, "Joel's terminal is the
  agents' uid"). A key held by a user other than uid 501, used from an account
  no agent can write, would close both gaps.
- **The trust file.** The trust root is pinned outside any repository an agent
  can write (#A1 7114817207, #juno 7115601439). Where it lives is I8. It must be
  outside every repository and every agent's home, not writable by uid 501,
  owned by pilotseal (Joel, 7135401609), and written only by Joel acting as
  pilotseal. It holds the public key and
  its fingerprint, every earlier key with its rotation statement (the key
  rotation rule below), the signing form, the signing commit and this text's
  blob there, the landing baseline at signing (the fleet-tools `main` commit
  when the signing commit is made, and the path and blob of each landing script
  and gate the coordinator runs there), and the SHA-256 of every executable the
  pilot runs: section 10's scripts and section 12's `review-size.sh` and
  `pilot-review.sh`, and section 10's follow-up PRs and merge commits. No enroll
  script exists (section 14).
- **This text holds only copies.** The block below copies the fingerprint, and
  section 10's table and section 12's rows copy the executable hashes. No script
  trusts a copy read from a repository. Only preflight opens the trust file;
  another script that needs a trust-file value, such as the adapter pin the
  *compared* predicate needs, reads it from the `preflight` line, which copied
  it (section 7). Preflight reads the trust file and refuses enrollment when any
  copy here differs from it (section 9, precondition 14). So an agent's PR that
  edits a copy makes preflight fail closed, and the PR can't move the pin.

```pilot-v2-trust-copy
trust_file: UNRULED I8
key_fingerprint: UNPINNED key not yet made
```
- **Key rotation.** The anchor is the `key_fingerprint` in the ledger's first
  `preflight` line (section 7), which is hash-chained and covered by the chain
  head Joel posts right after it, so once that head is posted, moving the
  anchor breaks the chain against that head. The trust file
  keeps every key since that one, in order, each later key with a statement
  signed by the key before it that names the new key and the signing commit.
  `check` verifies that chain from the ledger's anchor to the trust file's
  current key, and refuses when a link is missing or its signature fails (F3).
  An agent's PR cannot swap the key, since no script trusts a copy in a
  repository. A rewrite of the trust file without the old key is refused unless
  the ledger's anchor is rewritten too, and once the head is posted that second
  rewrite is detected only by comparing the ledger with the posted heads, which
  `check` does not do (it reads the anchor from the ledger it checks); who makes
  that comparison is open item 4. So the posted head gives detection, not
  prevention (section 6, residual gaps). Before it is posted there is no
  anchor outside the trust file and the ledger, which the same user can rewrite
  together (section 6, residual gaps), so a key change then is only as safe as
  the trust file itself: only Joel writes it, and uid 501 cannot.
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
  "I'll add them", 7121929832). No script enrolls a head, and no agent session
  can enroll by itself: the ledger is not writable by uid 501 (precondition 10),
  and an agent session cannot use sudo to reach pilotseal without Joel's
  password (precondition 15). An agent can still tamper with the uid-501
  terminal Joel enrolls from (the residual gap below). So the pilot pins no
  enroll script.
- **Scope.** Joel's enrollment does one thing: it appends `enroll` and
  `exclude` lines that meet section 12, after section 12's reconciliation, and
  planted heads' `enroll` lines under the original's section 3, following F2's
  written procedure. `pilot-ledger.py` has no mode that rewrites or truncates
  a ledger. It only cuts back its own failed partial write to the size it
  verified.
- **A known limit.** Joel writes as pilotseal, which owns the sealed records, so
  the operating system does not stop the writer from reading them. What keeps
  him from doing so is that F2's procedure opens a sealed path only through
  section 6's four logged accesses, and section 6's rule that any other earlier
  access is logged and marks the head. Only a writer uid other than pilotseal,
  able to append to the ledger and nothing else, would make that bar a
  mechanism.
- **A residual gap: Joel's terminal is the agents' uid.** Joel's account is uid
  501, so the terminal he runs `sudo` and his signing tools from runs uid 501's
  shell startup files (`~/.zshrc`, `~/.zprofile` and the rest), its `PATH` and
  its tools, all of which agents can write. A `sudo` function, or a `sudo`
  wrapper earlier on `PATH`, could capture his password or add its own command
  to his. A live sudo ticket can be used from his terminal. A signing or viewing
  tool on that `PATH` could show him one record while signing another.
  Precondition 15 cannot show that none of this happened. Every claim in this
  text that only pilotseal can read, write or run something, or that no agent
  can use the key, holds only while this gap is not exploited, since an agent
  with Joel's captured password acts as pilotseal.
  - **What narrows it:** Joel runs `/usr/bin/sudo -u pilotseal` by absolute
    path, from a shell started with no startup files (`/bin/zsh -f`), ends the
    ticket with `sudo -k` when he is done, and reads what he signs with tools
    outside uid 501's writable paths. This only narrows it: a shell that was
    already started with tampered files can intercept what is typed into it.
  - **What closes it:** a separate account for Joel's `sudo` and signing,
    other than uid 501, whose home, startup files and `PATH` no agent can
    write. Until he has one, this is a stated residual gap, and the report
    says so beside the result.
- **A gap until F1 and F6: one record per PR.** By hand, nothing refuses a
  doubled record. The landed ledger keeps `enroll` unique per head only, so a PR
  enrolled twice as two heads, or both enrolled and excluded, is accepted, and
  `pilot-unblind.py` as built counts `enroll` lines, so one PR could count twice
  toward the 10. F1 makes the ledger refuse a second window record naming a PR,
  and F6 counts distinct PRs. Both must land before signing (precondition
  12).
- **The denied-write test.** A record as uid 501, in the shape of section 9's
  denied-read record, shows that opening the sealed ledger or the trust file
  for writing is denied with `EACCES` (section 9, precondition 10).
  `pilot-preflight.py probe` opens read-only today, so the write probe is F3.
- **Chain heads.** Joel produces the chain head as pilotseal after the first
  `preflight` and at each `freeze`, `close`, `score` and `unblind`, and the
  coordinator posts the line he passes it (section 7).

## 15. Landing during the pilot

#juno's fix 6 and #A1's r2 (7114817207), under the relay's "preserve existing
landing controls". An enrolled PR lands alone, never in a batched landing. The
batched-landings change (Joel 7112930679, #juno 7113018815, #claude 7114168630)
does not apply to enrolled PRs during the pilot unless this text is amended and
re-signed before enrollment.

**The landing regime is a rule, recorded and reported, not enforced.** So that
enrolled PRs are measured under the same rules, no change to the landing scripts
or gates should land between the first live `enroll` and `close`, unless this
text is amended and re-signed (#A1 7114817207). The first live `enroll` records
`landing_regime`: the fleet-tools `main` commit at that time, and the path and
blob of each landing script and gate the coordinator runs there. Nothing in
fleet-tools stops a later change from landing: an enrolled PR may itself be a
reliability fix to a landing script. What makes a change visible is the report
(F4). For each enrolled PR that landed, it compares those blobs on fleet-tools
`main` at the PR's merge, its first parent, with `landing_regime`, and flags
each PR that landed under a changed regime. Such a PR stays enrolled and is
never dropped, and no result changes because of the flag. Section 9's
precondition 12 reconciles any change made between the trust file's landing
baseline at signing and the first live `enroll`. Preflight runs before that
`enroll`, so it can see only changes up to its own run; Joel's F2 procedure
makes the final comparison at the first live `enroll` itself. Every existing
landing control stays. Nothing in this text changes rule #68, delegates a
waiver, changes admin bypass, enables branch protection or promotes a gate.

## 16. The promotion prerequisite and no branch protection

Relay point 7. The original's lines 485-488, kept by section 2, require "a
server-protected required check and an auditable break-glass" before a promotion
decision, per ORDER r2 item 13. Joel's standing decision is that there is no
branch protection (7114296700, point 7). Joel resolved the conflict with a
local substitute (I7: answer 8 of 7121929832, and 7135308036 and 7135401609 on
Nov 24): Joel does the local substitute check for the server-protected required
check himself, and if he has not done it within 48 hours of its being due,
#claude (the arbiter) does it. This is his change to ORDER r2 item 13, and this
text's signature records it. Until this text is signed, the prerequisite stays
as the original wrote it and is **unmet**, so no promotion decision can be made,
whatever the pilot shows. This section is a direction to resolve the
conflict. It enables no branch protection and promotes no gate.

## 17. Script follow-ups

What this text requires that the landed and held scripts (ft#790, ft#792,
ft#793, ft#794, and ft#804, the open fix that makes `pilot-ledger.py append`
refuse a ledger it cannot open for writing with one line, not a traceback) do
not do yet. None widens a held PR. F1, F3, F4, F5 and F6 are script changes,
each its own reviewed PR. F2 is the bullets below, in this text, so this text's
review and signature cover it and it lands with this text. Each pin is filled
by the signing commit. Enrollment cannot pass preflight before F3, since check 4
refuses every form until then.

- **F1. `pilot-ledger.py`:** add an `exclude` kind; make window records (section
  7) unique per `pr`, so the ledger refuses a second window record naming a PR
  that already has one (section 12), with a self-test row and a mutation that
  turns it red; refuse a window `enroll` whose `case` or `provenance` is not
  `live`, whose `work_kind` differs from the `pr_kind` of the last `preflight`
  line before the first `enroll` (section 7), or
  whose `pr_created_at` is before the `at` of the first `preflight` line, and
  an `enroll` of provenance `live` that is not a window record, while a replayed
  head's `enroll` keeps its `pr` (section 12); an `items` kind, unique per
  ledger and refused unless a `close` line precedes it, with a self-test row
  and a mutation that turns it red; and a `ruling` kind with section 8's
  body. A `ruling` whose subject is
  `classification` may come before or after `close`. One whose subject is `item`
  needs an earlier `items` line naming that id, so it too can only follow
  `close`. `items` and `ruling` join the
  kinds allowed after `close`, and no `items` or `ruling` may follow `score`.
- **F2. The enrollment procedure (I6):** the procedure Joel follows by hand as
  pilotseal. Its binding steps are the bullets below, in this text, so the
  signature covers them and no separate document needs a pin. A written copy may
  exist for Joel's use, and when it differs from these bullets, these bullets
  win. It applies section 12:
  - it opens a sealed path only through section 6's four logged accesses:
    building a planted head from its spec, `pilot-score.py bind`,
    `bind --census` and `parse-check`. Its one
    write there is section 6's unlogged copy of a planted head's review record
    into `sealed/reviews/<head_id>.md`, made without reading it. Otherwise
    it reads the ledger only through `pilot-ledger.py` and the captures, never
    `/Users/pilotseal/pilot/sealed/` (section 14);
  - it orders by `created_at`, then PR number;
  - it reconciles the window PRs against the ledger before each live `enroll`
    (every PR earlier in the order has exactly one record, the one being
    enrolled has none) and before `close` (every window PR has exactly one),
    and refuses on a missing or doubled record (section 12);
  - it runs the pinned `review-size.sh` at the enrollment head;
  - right after each live head's `freeze` is appended, and before each live
    `enroll`, it runs `pilot-unblind.py window`. Once that says the window has
    closed, it writes no more live `enroll` and records every unrecorded window
    PR at once, under the first of E1 to E4 that applies, and otherwise under
    E5 when its `.verdicts` file exists then with at least one line, and under
    E6 otherwise, an empty file included (section 12);
  - it records the evidence fields, and `landing_regime` on the first live
    `enroll`, after comparing it with the trust file's landing baseline and
    refusing that `enroll` while a difference is not reconciled (precondition
    12).
- **F3. `pilot-preflight.py`:**
  - check 4 verifies the signing commit's three conditions (section 1), and
    preconditions 6 and 10 date their records against the signing commit, not
    against the commit that added this text, as `pilot-preflight.py` does today;
  - `check` reads the key and the executable hashes from the trust file at the
    I8 location, which is a constant in its own code, never read from this
    text or any repository, and refuses when section 13's `trust_file` copy
    differs from that constant;
  - it verifies the signature and records the key's fingerprint as
    `key_fingerprint` in its `preflight` line. On every run after the first
    `preflight` line it verifies section 13's rotation chain from that line's
    `key_fingerprint` to the trust file's current key, and refuses when a link
    is missing or fails. It refuses to run as any user but pilotseal;
  - precondition 14: it refuses enrollment when the trust file is missing or
    unreadable, or when any copy in this text (the fingerprint, section 10's
    table, section 12's rows) differs from it. A self-test row
    shows the refusal for a changed copy, and a mutation that skips the
    comparison turns that row red;
  - it refuses enrollment when the parameters' `k` is not 3 (section 5);
  - it reads assistant history only from section 1's assistant history clone,
    for check 4 and preconditions 1 and 2, and records that clone's `main`
    commit in its `preflight` line;
  - it validates precondition 15's sudo-denied record and its sudoers record,
    made through sudo as root: `visudo -c` OK, one global `timestamp_type` of
    `tty` or `ppid`, and no `timestamp_type` line in any other scope; it records
    both hashes in its `preflight` line;
  - it compares each history clone's `main` with `git ls-remote` of its remote
    and refuses on a mismatch (sections 1 and 10);
  - it refuses an `access` line from `bind --census` or `parse-check` whose
    `script_sha256` differs from section 10's scoring pin (section 6);
  - it checks section 10's containment rules against the history clone: each
    row's merge commit is its script's latest follow-up merge from the
    `pilot-v2-followups` block and the trust file, the script's blob there
    hashes to the pin, and an earlier follow-up of the same script is an
    ancestor (precondition 12);
  - it records `key_fingerprint` and `review_adapter_sha256`, the latter read
    from the trust file, in its `preflight` line (section 7);
  - it records precondition 6's traversal probes and read probes of the ledger
    and s01 to s08, and refuses a record whose stat of a target below
    `/Users/pilotseal` succeeds;
  - it checks preconditions 8 to 13 and 15, including that neither gate home's
    `config.yaml` sets a `usage_ceiling.*` key;
  - it adds the write probes of precondition 10 (the sealed ledger, the trust
    file, `/Users/pilotseal/bin` and each installed script there, and the
    two history clones), a probe of
    the key's location, precondition 9's signing probe (an attempt from an
    agent session to sign a test message with the pinned key through each
    agent the key could be reached by, `ssh-agent` and `gpg-agent`), and
    precondition 15's sudo probe;
  - each probe writes a record file in the shape `probe` writes today: a
    `probes[]` list of entries with the fields `tester_uid`, `target`,
    `target_owner_uid`, `owner_from`, `command`, `exit`, `errno` and `date`,
    where `date` is what every "dated after the signing commit" check reads, a
    top-level `isolation_broken` list, and, new in F3, a `kind` of
    `denied-read`, `denied-write`, `denied-sign` or `denied-sudo`. `check` takes
    each record by its own flag, validates it, and records its SHA-256 in the
    `preflight` line, as it does the denied-read record today
    (`denied_read_sha256`). The record files are their one home. v2 writes no
    ledger line for them, so F1 adds no kind, and the landed `denied-read` kind
    stays unused: a `denied-read` line, if one appears, decides nothing and the
    report lists it;
  - its `install` check covers `review-size.sh` and `pilot-review.sh`.
- **F4. `pilot-score.py`:**
  - a `bind <sealed_id>` mode that, as pilotseal, appends an `access` line
    (`by: bind`) and prints only the case and any commit a sealed record names,
    a `bind --census` mode that appends an `access` line (`by: census`) and
    prints only counts, a `parse-check` mode that appends an `access` line
    (`by: parse-check`) and prints only how many records parse under O1's
    record rules, refusing without git on `PATH` (section 6), each of the two
    recording the `script_sha256` Joel checked, a
    refusal to bind a construction spec whose case is race or recovery, and a
    check at scoring that every planted `enroll` agrees with its record (section
    7, "Building and binding a planted head");
  - an `items` mode that appends section 8's `items` line from captures alone,
    and refuses, appending `score-refused`, unless the ledger already has a
    `close` line, so no item list, and no near-score, exists mid-pilot and the
    budget period cannot start early;
  - the `items` line covers the O5 items of every compared replayed head and
    every compared sequence head (section 8, end condition 4), and the outcome
    of every enrolled race and recovery head except a `base_mismatch` or
    pin-failed one, never a planted head's review record, and lists each
    `unlocatable` finding as its own item (as built, `o5_counts` only counts
    them);
  - it computes each ruling's place in the 48-hour budget (section 8);
  - it scores nothing, appending `score-refused`, when the frozen parameters'
    `k` is not 3 (section 5);
  - it scores nothing, appending `score-refused`, before an `items` line exists
    or before its budget period has ended, 48 hours after that line's `at`
    (section 8), so no item can be cut off early;
  - every oracle that reads a stage needs that stage to have run by section
    5's rule, all three conditions: `ran: true`, `report_present: true`, and no
    finding with the id `usage-ceiling`; otherwise it is UNMEASURED, whatever
    the outcome says (as built, no oracle checks any of them: not O1, O2, O3,
    O4 or O5's `hold-not-made` and `pass-refused`);
  - O4 reads the `ci` stage (as built, `stage_outcome(gate, "checks")` looks
    for a stage that `gate.stages[]` never contains, so C6 is always
    UNMEASURED);
  - it decides compared only by calling `pilot-unblind.py`'s compared predicate
    (F6), which reads `verdicts.txt` with the `.verdicts` line winning, never by
    a second implementation; it reads each field only from its one owner
    (section 7), makes a head UNMEASURED when a field appears in a file that
    does not own it, and does not compare a head that fails the one-head rule on
    `gate.submitted_head`, and lists a head whose `gate.reviewed_revision`
    differs from it as rewritten by the gate (as built, `score_head` reads the
    verdict from `review.md` alone, with no such checks);
  - a review with no findings block gives an `unlocatable` item per prose
    finding, and at least one when its verdict is `CHANGES REQUESTED`
    (section 5, O5);
  - a planted diff that parses but adds no truth line is UNMEASURED, never a
    miss (O1);
  - a live head whose `pr_created_at` is outside the window, before its
    opening or after its close, or whose `enroll` line's `at` is later than the
    window's close, is not a sequence head, so it is in none of C1,
    C3, C5 or the `items` line (section 5); it is listed, and the last case
    marks the pilot possibly selected (section 12);
  - it lists each item ruling made out of the `j1_order` order (section 8);
  - it lists every enrolled live head that is not compared, with its reason
    (captured late, moved after enrollment, no review, or another), every E5
    exclusion, and every E4 exclusion, and marks the pilot possibly selected
    when any exists; it counts and lists E1 to E3 exclusions without the mark
    (section 12);
  - it never opens a `raw[]` entry whose `file` starts `sealed:`, and never
    reads a planted head's review record itself: the compared predicate it
    calls does (F6, section 7);
  - it identifies a gate finding by (`stage`, `id`), `planted_match` records
    that pair, and O3 counts only a matched finding whose `stage` is `review`
    (as built, `planted_match` holds the id alone and O3 compares ids alone);
  - it takes review fields only from the review record, and makes a head
    UNMEASURED when its `gate.json` holds `planted_truth` or a `review.*` field
    (section 7);
  - it reads Joel's `ruling` lines and applies section 5's C3, C5 and C7 rules,
    within the J1 budget (I3), and anything past the budget is UNMEASURED;
  - the live minimum of C1 becomes 10;
  - O2 skips a `base_mismatch` clean head, for C5's clean part, and C4, C5's
    clean part, C6 and C7 leave out a pin-failed head (section 5; as built,
    none of them checks `pin_check`);
  - it reports, for each enrolled PR that landed, whether the landing scripts
    and gates on `main` at its merge differ from `landing_regime` (section
    15);
  - it reports the `default-1` run apart, heads that grew past the size bound,
    disputed classifications, E5 exclusions counted apart, and every
    `correction` line beside the line it names, with no result changed by one.
- **F5. The 48-hour deadline (I4, answered).** Preflight copies section 11's
  `deadline` into its `preflight` line and refuses unless it is `48 hours`
  (check 5). `pilot-unblind.py` computes the window's close of section 8, end
  condition 4, as the earlier of the count close and the opening plus that
  deadline, read from the `preflight` line, in its `window`, `ready` and `run`
  modes and in the sequence-head test. `pilot-score.py`'s report says whether
  the window closed by count or by time.
- **F6. `pilot-unblind.py`:** end condition 4 counts distinct PRs whose sequence
  head (section 8: `case` and `provenance` both `live`, `work_kind` equal to
  `pr_kind`, `pr_created_at` at or after the first `preflight` line's `at` and
  not after the window's close, `enroll` not after the window's close) is
  frozen and compared (section 5), reading each head's frozen captures, not
  `enroll` lines or frozen heads of provenance `live` alone, so a doubled record
  can never count twice. It owns the *compared* predicate of section 7,
  including the on-time capture check for a live head (as built, nothing checks
  it), the adapter pin, read from the last `preflight` line before the first
  `enroll` and never from the trust file or this text, and the reading of a
  planted head's sealed review record, opened from its `sealed:` `raw[]` entry
  under `/Users/pilotseal/pilot/sealed/` and checked against its hash: one
  function, which `pilot-score.py` calls and never reimplements, so end
  condition 4 and C1 can never disagree on a head. It also adds a `window` mode
  that checks end condition 4 alone and appends nothing, which the writer runs
  before each live `enroll` (section 12), and a `ready` mode that checks end
  conditions 1, 3 and 4 without a `close` and appends nothing, so Joel appends
  `close` only when it says the end rule holds (section 7). Its `run` mode also
  publishes each planted head's sealed review record from
  `/Users/pilotseal/pilot/sealed/reviews/`, with its hash in `published` checked
  against that head's `freeze` `raw[]` (section 7).
