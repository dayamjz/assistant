# REPORT: #767 declare and serve task.interrupt for remaining adapters

## Handoff (coordinator 00:05 resume; context high)

### Done this resume
1. Merged `origin/main` up the stack (operators conflicts resolved each layer):
   - #847 `feat/interrupt-adapters-767` → `592339bb0ad874449cf545bd1367c2ebab56fbd0` (pushed)
   - #848 `feat/interrupt-adapters-767-cursor` → `890201d338ac16e56996a629c0f79a3114147b58` (pushed)
   - #855 `feat/interrupt-adapters-767-claude` → `9d247c8a6f9838b01c17e34d1de76f737a65037b` (pushed; contains `origin/main`)
2. #855 CHANGES from `briefs/855-review-r4.md` (review at `c7ad2eb2`) already fixed on tip before this merge:
   - `TaskInterrupted` wire docs name tmux key and pipe protocol proofs (`internal/machine/wire.go`)
   - `TestSchemaClaudeStreamInterruptFramesSettleTheWorker` reads `testdata/claude-stream-interrupt-control-request.jsonl`

### Ready lines already printed (pre-main-merge; need re-review after merge)
```
ready: #847 APPROVE at 073e1465cf7900c5645084c71da6cafd1ecb0e95
ready: #848 APPROVE at 7b86bed8129007b5b9b5f3863fcdaa1ad71deab2
```
Do not treat those as current. After main merge each needs a fresh APPROVE at the new tip.

### Next steps (in order)
1. Wait `gh pr checks` green for #847, #848, #855 (stress is not branch protection; required are check×2, lint, repeat, re-runs).
2. Bottom-up self-reviews (`briefs/self-review-rule-20261106.md`):
   - #847: delta from prior APPROVE sha `073e1465…` if a prior review brief exists; else full. Archive prior to `briefs/847-review-rN.md` as needed.
   - #848: delta from `7b86bed8…` after #847 APPROVE.
   - #855: delta from `c7ad2eb2` with `briefs/855-review-r4.md` (or from `reviews/855.done` if newer). Verify r4 findings resolved.
3. On each APPROVE print exactly: `ready: #<pr> APPROVE at <full 40-char sha>`
4. Do not merge. Stack: 847 base main → 848 base 847 → 855 base 848. Last PR body `Closes #767`.

### Prior #855 review rounds (for context)
- r1 `3af6e393` CHANGES: Capture-growth race, PRD deferred bullet, steer residual, capability §12 cite
- r2 `3b73c996` CHANGES: 5s soft return after init; Capture-growth markIdle
- r3 `b351115d` CHANGES: observe markIdle on active_goal; waitCapture Capture-quiet
- r4 `c7ad2eb2` CHANGES: TaskInterrupted tmux-only contract; unread Claude fixture → fixed in `c4ade966`, retained through `9d247c8a`

### Worktree
`~/Projects/wt-interrupt767` on `feat/interrupt-adapters-767-claude`. REPORT.md untracked.

## Session cost
- Original start: 2026-11-08 10:56 PT
- Coordinator resume: 2026-11-09 00:05 PT
- Stack main merge push: 2026-11-09 ~00:10 PT
- End: handoff (reviews pending CI)
