# Branch Cleanup Report
**Date:** September 9, 2026  
**Agent:** cursor/branch-cleanup-0507

## Summary

- **Started with:** 49 branches
- **Deleted:** 17 branches (old and redundant)
- **Remaining:** 32 branches (need manual review/rebase)

---

## ✅ Deleted Branches (17)

### Redundant (Changes Already Merged)
1. `fm/assistant-stage-pr` - 0 files different from main
2. `fm/assistant-stage-test` - Test stage already implemented in main

### Old (Sept 2-3, 2026 - Too Outdated)
3. `fm/assistant-agents` - 359 files, 7 commits
4. `fm/assistant-agents-md-review-lens` - 376 files, 1 commit
5. `fm/assistant-config` - 383 files, 8 commits
6. `fm/assistant-findings` - 395 files, 7 commits
7. `fm/assistant-forge` - 344 files, 3 commits
8. `fm/assistant-gate` - 350 files, 10 commits
9. `fm/assistant-gate-ownership-index` - 318 files, 18 commits
10. `fm/assistant-gate-path-instructions` - 376 files, 5 commits
11. `fm/assistant-graph-engine` - 397 files, 19 commits
12. `fm/assistant-ipc` - 324 files, 13 commits
13. `fm/assistant-prd-amendments` - 376 files, 1 commit
14. `fm/assistant-safety` - 350 files, 12 commits
15. `fm/assistant-safety-path-instructions` - 318 files, 1 commit
16. `fm/assistant-store` - 369 files, 7 commits
17. `fm/assistant-vcs` - 376 files, 10 commits

---

## 🔍 Remaining Branches (32) - Status by Priority

### High Priority (Small, Recent - Easier to Salvage)

| Branch | Date | Files | Commits | Status |
|--------|------|-------|---------|--------|
| `fm/assistant-forge-provider` | Sept 8 | 44 | 5 | Has conflicts, but small scope |
| `fm/assistant-stage-review` | Sept 8 | 58 | 7 | Documentation updates |
| `fm/assistant-journey-harness` | Sept 8 | 74 | 36 | CI fixes, many commits |
| `fm/assistant-review-instruction-capacity` | Sept 8 | 106 | 15 | Review instructions |
| `fm/assistant-stage-rebase` | Sept 7 | 108 | 15 | Documentation |
| `fm/assistant-pin-outcome-set` | Sept 7 | 120 | 8 | Outcome documentation |

### Medium Priority (Recent, Moderate Size)

| Branch | Date | Files | Commits | Notes |
|--------|------|-------|---------|-------|
| `fm/assistant-stalled-run-renders-working` | Sept 7 | 130 | 10 | Bug fix |
| `fm/assistant-gate-hook-verbs` | Sept 7 | 136 | 7 | Documentation |
| `fm/assistant-prd-mermaid-br` | Sept 7 | 156 | 1 | PRD fix |
| `fm/assistant-review-rules-null-and-class` | Sept 7 | 156 | 2 | Documentation |
| `fm/assistant-stage-intent` | Sept 7 | 158 | 9 | Documentation |
| `fm/assistant-prd-running-outcome` | Sept 7 | 159 | 6 | PRD documentation |
| `fm/assistant-command-surface` | Sept 6 | 161 | 13 | Command implementation |

### Lower Priority (Older or Larger)

| Branch | Date | Files | Commits | Notes |
|--------|------|-------|---------|-------|
| `fm/assistant-principle-test-coverage` | Sept 6 | 213 | 4 | Test coverage |
| `fm/assistant-drop-one-row-checkpoint` | Sept 6 | 225 | 4 | Checkpoint cleanup |
| `fm/assistant-fixture-p3-review-path` | Sept 6 | 230 | 11 | Fixture updates |
| `fm/assistant-prd-park-hold-vocabulary` | Sept 6 | 233 | 1 | Terminology |
| `fm/assistant-park-hold-rename` | Sept 6 | 234 | 3 | Renaming |
| `fm/assistant-prd-checkpoint-records` | Sept 6 | 247 | 2 | Documentation |
| `fm/assistant-gate-instructions-refresh` | Sept 6 | 247 | 3 | Gate docs |
| `fm/assistant-durable-checkpoints` | Sept 6 | 247 | 3 | Checkpoints |
| `fm/assistant-run-service` | Sept 6 | 254 | 3 | Service documentation |
| `fm/assistant-hold-resolver` | Sept 6 | 265 | 4 | Hold resolver |
| `fm/assistant-finding-evidence` | Sept 5 | 272 | 8 | Findings |
| `fm/assistant-adapter-capabilities` | Sept 5 | 283 | 5 | Adapter capabilities |
| `fm/assistant-fixture-repo` | Sept 5 | 291 | 9 | Fixture |
| `fm/assistant-budget-in-checkpoint` | Sept 5 | 305 | 5 | Budget tracking |
| `fm/assistant-fake-agent` | Sept 5 | 309 | 5 | Test agent |
| `fm/assistant-review-scope-lens` | Sept 5 | 311 | 5 | Review scope |
| `fm/assistant-prd-amend-2026-09` | Sept 5 | 312 | 1 | PRD amendment |
| `fm/assistant-pipeline` | Sept 4 | 312 | 28 | Pipeline |
| `fm/assistant-daemon` | Sept 4 | 352 | 14 | Daemon (unshipped) |

---

## ⚠️ Challenges Encountered

### Rebase Conflicts
All tested branches had significant merge conflicts when rebasing onto current `main`:

1. **`fm/assistant-forge-provider`** - 44 files, conflicts in:
   - `internal/forge/doc.go`
   - `internal/forge/github.go`
   - `internal/forge/repository.go`
   - `internal/service/service.go`
   - `internal/stages/deps.go`

2. **`fm/assistant-stage-test`** - Had conflicts in test stage files (already in main)

### Root Cause
- Main has evolved significantly since Sept 2-8
- Many branches implement features that have since been merged differently
- `go.mod` versions diverged (though some branches already updated to 1.25.0)

---

## 💡 Recommendations

### For Salvaging Branches:

**Option 1: Cherry-Pick Approach** (Recommended)
- For branches with good documentation or specific fixes
- Cherry-pick specific commits onto fresh branches from main
- Resolve conflicts commit-by-commit

**Option 2: Fresh Implementation**
- Extract the key ideas/documentation from old branches
- Implement fresh on current main
- Reference old branches in commit messages

**Option 3: Manual Rebase** (Time-Intensive)
- Clone repository locally
- Manually resolve conflicts for high-value branches
- Test thoroughly before pushing

### Priority Order:
1. **Start with smallest branches** (forge-provider, stage-review: 40-60 files)
2. **Focus on documentation branches** (many have "document" in commit messages)
3. **Leave large/complex branches** (pipeline, daemon) for last or fresh implementation

---

## 📋 Next Steps

1. **Review remaining 32 branches** - identify which have valuable unique content
2. **Cherry-pick good commits** - extract useful docs/fixes from old branches
3. **Create fresh PRs** - for features that need re-implementation
4. **Document decisions** - note which branches were salvaged vs abandoned

---

## 🔧 Technical Notes

- All deletions pushed to `origin` (dayamjz/assistant)
- Local cleanup branches created: `cursor/rebase-*-0507`
- Branch analysis script saved: `/tmp/branch-analysis.sh`

**Current main status:**
- `go 1.25.0`
- 4 stages implemented: Intent, Review, Test, PullRequest
- 5 stages pending: Rebase, Document, Lint, Push, CI
