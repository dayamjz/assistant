# Fleet implementation foundation

Checked in after main `629b16b` (PRs #113–#118). This document is the monitoring
evaluation of where the product stands and the build order an implementing agent
should follow. It does **not** amend [`prd.html`](prd.html). When code and the
PRD disagree, raise a finding (#95); do not edit the PRD to match the code.

**Contract:** claim only what the mechanism enforces. Partial fleet cores are
not a kickoff-capable product.

---

## 1. One-line verdict

The **gate is production-shaped and complete**. The **fleet is no longer an
empty folder** — intake, a pure supervisor core, and a worker process API exist —
but it is **not kickoff-capable**: nothing creates a task from outside the
library, nothing clones or proves isolation, nothing starts a real worker in
production, and nothing runs the supervisor loop.

| Metric | Value |
|---|---|
| Stage bodies | 9/9 |
| Runnable product | Gate (push → admit → nine stages → hold/park → merge_record → passed) |
| Fleet packages on main | `tasks`, `supervisor`, `taskstate`, `agents` (`worker_sessions`), home/store prereqs |
| End-to-end fleet kickoff | Impossible until Create surface + isolate + dispatch + loop |
| Principles unclaimed | P10–P13 (P9 cited by classifier matrix only) |

---

## 2. Maturity by subsystem

| Area | Status | Evidence / next |
|---|---|---|
| Nine-stage gate | Shipped | `stages.written` |
| P3 / P4 / P6 / P7 | Shipped | findings; agents split + route; Observation; trusted config |
| Checks wait + merge → passed | Shipped (#113) | `merge_record` + `HoldClosure`; `OutcomePassed` producer |
| Store fleet tables | Shipped (#115) | attempts, blockers, cleanup; snapshot carries merge records |
| Home fleet layout | Shipped (#114) | `ValidTaskID`, `TaskCopy`, LockState, instance/beacon paths, `TaskInstructions` |
| taskstate | Shipped | Resolve-on-read; Advancing → Progress; Landed/Complete still #107 |
| Worker entry | Shipped (#116) | `OpenWorker` / stand-in; no shipped adapter declares `worker_sessions`; GIT_DIR strip only (P11 half) |
| Supervisor pure core | Shipped (#117) | `Classify` + `Assess`; no I/O loop |
| Tasks intake + queue | Shipped (#118) | `Create` / `Release` + service release sites; **Create has no production caller** |
| Session provider | Open #91 | |
| Isolation + dispatch | Open #98 / #104 | |
| Wake queue + loop + P10 host | Open #99 / #100 / #105 / #106 | |
| Landed/Complete / cleanup / P12 | Open #107 / #108 / #93 | Ready never clears until Landed |
| P13 rendering | Open #110 | |
| README honesty | Amended in this PR | Opening paragraph and layout match this document |

### Principles (claim vs reality)

| P | Reality |
|---|---|
| P3–P8 | Gate-side mechanisms hold; taskstate resolve-on-read; intake/release anchors |
| P9 | Cited by `supervisor` matrix (table half). Loop half is #100; journey remains `ReachNone` for binary reach |
| P10 | Unclaimed — `Assess` exists; real beacon/lock/hook unbuilt |
| P11 | Unclaimed — env strip shipped; Dir isolation proof unowned (#98) |
| P12 | Unclaimed — cleanup record shape shipped; no CleanUp / incorporation |
| P13 | Unclaimed — `assistant tasks` lists; human vocabulary not claimed |
| P14 | Schema ownership held so far (merge_record, attempt, cleanup) |

---

## 3. Industry frame (for prioritization)

- **Agent quality:** Cursor / Claude Code / Devin win; this product wraps a configured adapter.
- **Ship safety:** The gate is the differentiator — admission before the remote moves, hold≠park, three loop bounds, honest `passed`.
- **Fleet supervision:** Partial cores now exist. Do not dilute the gate lead with a fleet that launches before P11 / P12 / P9’s loop half are real.

---

## 4. Build order for the implementing agent

Do not invent person-facing verbs. PRD section 9’s table is closed; #95.A is the
Create-surface finding. Prefer thick vertical slices over ten half-seams.

### Minimum path to first kicked-off task

1. **Settle #95.A** — how the coordinator reaches `tasks.Create` (coordinator-only IPC or equivalent). Do not add a person `create` verb without a PRD decision.
2. **#98** — `vcs` Clone/Observe and the pure isolation assertion (P11 Dir half). Refuse launch when Dir is not an isolated copy.
3. **#91** — session provider + worker set (and worker event path; Writer column already on taskstate kinds).
4. **#104** — dispatch: honour Dispatched → isolate → `OpenWorker` → deliver instructions; wire Release → dispatch for queued successors. Run-start fetch from the gate as the PRD requires.
5. **#99 → #100 → #105 → #106** — durable wake queue, supervisor loop/watchdog, beacon/pull/arm, `assistant host stop`. Without these, workers are fire-and-forget and P10 is a document.

### Parallel / follow-on (after kickoff path exists)

| Issue | Role |
|---|---|
| #107 | Landed/Complete tier (Ready blockers can clear) |
| #108 / #93 | Cleanup proofs / incorporation (P12) |
| #94 | Reconcile + relaunch |
| #109 | Promotion investigation → delivery |
| #110 | P13 human rendering + publishers |
| #111 / #112 | Orphan reaping / Sweep |
| #119 | stages TempDir/git maintenance flake (not fleet scope) |

### Explicit non-goals for early fleet PRs

- Claiming P10–P13 in `unclaimed.go` without the mechanism.
- Declaring `worker_sessions` on the Claude adapter without a real probe.
- Calling `releaseQueued` from a superseding push that still replaces the run (#118 already forbids that).
- A second merge record or a state column on `task` (P8 / P14).

---

## 5. Onboarding and kickoff (operators vs implementers)

### Short answer

**Operators cannot kick off fleet work through the product today.**  
`tasks.Create` is a library entry: no CLI verb, no IPC method, no coordinator
caller (`internal/tasks` package doc; #95 line A). `assistant tasks` only lists.

### What works now (gate)

1. `assistant init` in the subject repository.
2. Start the service; home lock + socket.
3. Operator + trusted default-branch configuration (P7).
4. `git push assistant BRANCH` → admit → nine-stage run.
5. Drive holds with `assistant status` / `assistant runs`.  
   See [operators.md](operators.md).

### What the PRD expects for fleet kickoff (future)

1. You talk to a **coordinator agent** (judgment + conversation).
2. Coordinator calls the **program** to Create (shape, project, instructions, repo/branch, overlap claim).
3. Program: disposition → isolate → start worker → supervise → worker pushes to `assistant` → gate validates → Landed/Complete from merge_record + cleanup.

### Present scaffolding vs kickoff blockers

| Present | Package | Alone enough? |
|---|---|---|
| Task tables / attempts / cleanup | `store` | No |
| Paths / instructions file | `home` | No |
| Resolve + Progress | `taskstate` | No |
| Create / Release + floor | `tasks` | No (no caller) |
| Worker handle API | `agents` | No (undeclared in shipped adapters; unwired) |
| Classify / Assess | `supervisor` | No (no loop) |
| List tasks | `cli` | Read-only |

| Blocker | Issue |
|---|---|
| Create surface | #95.A |
| Isolation | #98 |
| Session / worker set | #91 |
| Dispatch + launch | #104 |
| Queue / loop / beacon / host stop | #99 / #100 / #105 / #106 |

### Interim (tests only)

Harnesses may call `tasks.Create` / `store.CreateTask`. Journey cannot create a
task through the binary. Do not document SQL inserts as onboarding.

### Operator checklist once kickoff exists

1. Gate working for the repo.
2. Host hooks `assistant host stop` at turn end (#106).
3. Service up; beacon/loop healthy.
4. Ask the coordinator for work; it calls Create.
5. `assistant watch` / `assistant tasks`; answer holds; workers push to `assistant`.
6. merge_record → Landed; cleanup proof → Complete.

---

## 6. Residual gaps already named in code (do not “fix” by deleting)

- Two live attempts on one branch share attribution — intake floor’s job.
- Ready never clears successors until #107.
- Person push on a task’s branch is outside the floor (#118 residual).
- `KindCheckResult` has no producer (#117).
- Classifier matrix ≠ loop half of P9 (#100 must cite when it lands).
- Partial `Send` / Capture interleave on workers (#116 docs).

---

## 7. Bottom line for implementers

Ship the **Create → isolate → launch → supervise** spine next. Keep every PR’s
claims inside what its mechanism enforces. Protect the gate’s fail-closed lead;
a fleet demo that skips P11/P12/P9’s loop is a regression even when it looks
alive.
