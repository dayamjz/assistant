---
paths: ["internal/service/**"]
---

# internal/service

- It decides nothing a run validates: `internal/graph` executes,
  `internal/pipeline` is the topology, `internal/runs` owns the record,
  `internal/checkpoints` makes the position durable, and this wires them.
  What it does decide is which build-scoped dependencies a stage body gets;
  `doc.go` owns why, including why the agent reaches a body wrapped.
- Open order: it takes the home's lock before recovery and before binding
  the socket, in that order. It reconciles every unfinished run against its
  checkpoint on open; fleet reconciliation runs inside `recover`, after the
  runs and before `releaseQueued`; the supervisor loop is started by `Open`
  after recovery and before the caller serves.
- Containment and reaping: containment is a process group this service
  registered, never anything a caller says about itself. `doc.go` owns its
  producer, `containRunner`, and what still registers nothing; `reap.go` owns
  the reap outcomes and `doc.go` the gaps, task terminals and copy removal
  being other owners'.
- Worker placement: a task's worker is placed by the other relation over the
  same credentials, the worker group set `internal/session` holds.
  `workers.go` owns that derivation, the methods that turn on it, which way
  each rule goes, and `pushIntent`; `placement.go` owns the walk. A test's
  push is a person's, so a package whose tests push to a gate calls
  `standin.AtTerminal` in `TestMain`.
- Subordinate coordinators: `subordinates.go` is PRD section 9's
  `domain.delegate` and `domain.end`. It owns the placement a third way over
  the same credentials and chain (worker first, the side that claims less),
  the grant decided per method from it with `scopeTask` holding a named task
  to the domain's row, the escalation a refused `task.promote` or
  `task.respond` records, the wake route `wakeRouteOf` composes from the kind
  table, and the restart relaunch bounded by `relaunch_attempts`; its header
  owns the residual gaps.
- Steering: `steer.go` is PRD section 6's steering (message, stop,
  interrupt), and its header owns the gaps.
- Admission and local landing: `admit_workers.go` owns the rule a worker's
  push is held to and why that is admission's decision and not
  `internal/safety`'s; `local.go` owns what a local task's own push does
  instead of starting a run, the approval hold it raises, and the anchored
  fast-forward an approve answer makes; `foreground.go` owns the relation
  that hold alone is held to.
- Events: `publish.go` owns `task.state` and `hold.state`; `taskView` is the
  one composition of a task's answer for the publisher and the surface
  alike.
- Cleanup and sweep: it is the caller of `internal/tasks`' cleanup decision
  and decides nothing of it, and no verb reaches any of it. `cleanup.go` owns
  the sites, `landed.go` ending a landed task's worker and asking again, and
  `runsweep.go` the sweep of a finished run's copy: what the order
  guarantees and its gaps.
- Kept work: `cleanup.go`'s `keepWork` is the one call that turns a copy
  kept for its work into a kept work entry, from every cleanup site and from
  recovery's `reclaimAtRecovery`; `supervise.go` raises each open
  entry about a task once as a `kept-work` wake, and status and the catch-up
  read list them. `dispatch.go` holds a new task's dispatch while its
  project has an open entry, asking internal/store's one predicate
  (`KeptWorkHolding`, `BeginDispatchAttempt` at the claim, and
  `launch.go`'s `ClaimAttemptLaunch` before the worker starts);
  `releaseHeldSlot` gives the slot back and reads the hold again, and a
  removal asks again (`redispatchAfterRemoval`); `taskView` and `report`
  name the entries.
  `keptanswer.go` is the answer (`keptwork.answer`, reached by section 9's
  hold answer with `--hold kept-work/N`): every answer is claimed or
  confirmed on the open entry first and written only with its outcome, so
  an answer under way never hides the entry. Land, claimed once
  (`ClaimKeptWorkLanding`), carried out through `startPushedBranch` and
  answered with its run, the claim withdrawn when no run could start; carry on (`keptcarryon.go`), claimed first with the
  entry left open, which creates a new task through `queue.Create` with
  the identifier the claim named and adopts the kept copy, the answer
  written only with the adoption and the claim withdrawn when intake
  refuses the task; and discard (`keptdiscard.go`), shown with a digest and
  removed only on it through `internal/tasks`' `Loss` and `Discard`, the
  person's confirmation recorded first with the entry left open and the
  answer written only with the removal (two facts, so an entry is never
  answered over a copy that stands; a copy that changed has the
  confirmation withdrawn), and what "person-only" is and is not enforced
  by stated in its header, a confirmation held to `foreground.go`'s
  relation as the approval hold is; all three completed at open when a
  stopped service left them. A promotion answers its copy's entry carry on in the
  flip, which is `store.TransitionTaskShape`'s write with the resolver
  `promoteTask` passes.
- Run activity: whether anything is advancing a run is a third fact neither
  the record nor the checkpoint holds: it lives in the slot a run advances
  in, and `report` reads it rather than inferring it.
- Checks hold and settle: `checkshold.go` is PRD section 11's step 3 and the
  one producer of `store.ResolvedByProgram`; `settle` turns what the checks
  body wrote into the record's move, so no stage body decides a run's
  status; `carryOn` decides from what ended the segment rather than from the
  run.
- Configuration per run: it reads the repository's own configuration
  document per run, composed by `config.ResolveRun`, and refuses the run
  before anything launches when the trusted copy cannot be read or the
  composed document pins a different agent than the operator's. Read
  `doc.go` for that, including how a resume resolves fresh and what decides
  its resumability.
- Copies: every run's isolated copy is cut at the run's own
  `refs/assistant/runs/<id>`, which `create` writes right after the row;
  `ensureCopy` reads that reference and never the working copy, and
  `copy.go`'s header says why the fetch moved there.
- Dispatch and launch: `dispatch.go` dispatches (the attempt row is the
  claim) and `launch.go` follows a proven dispatch in the same pass. A path
  already standing at the copy's path is asserted and never removed, and
  `Close` ends only the sessions this service created.
- Terminals: every task reader is built through `reader`, probing through
  the service's `Terminals`, the one seam a terminal is observed through;
  liveness has one producer, the provider's probe, and nothing writes an
  event to say Running. Which provider a launch builds over the adapter
  `agents.Route` selected, and `driverFor` over the resolved agent, is
  `terminals.go`'s `sessionHost`, from the global layer only.
- Reconciliation and relaunch: a launched task is never dispatched again;
  what a restart does about it is `reconcile.go`'s. `relaunch.go`'s
  `relaunchTask` is the one relaunch, and `taskholds.go` owns the bound's
  hold and both no-sweep holds. Reconciliation writes no wake and no marker:
  the queue and the markers are the loop's, under its fence.
- Stopping: `shutdown.go` is P15's stop half. `Shutdown` is `Close` under
  `stop_grace`; past it, or on a forced stop, it ends every group it
  registered by force and the caller exits. Its header owns which groups and
  the gaps.
- Supervisor loop: PRD section 5's supervisor loop lives in `supervise.go`;
  read its header before changing it, and for the residual gaps.

Read `doc.go` before changing any of that, and for the residual gaps.
