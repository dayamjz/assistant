# Working agreement for this repository

`assistant` runs a fleet of coding agents in parallel and puts a validation gate
in front of your remote, so no change reaches a shared branch until it has been
independently reviewed, tested, documented, and linted.

## The specification is checked in

`docs/prd.html` is the product requirements, and it is the contract this code
answers to. Read the section you are working in before you change behavior.

The PRD's section 4 lists fourteen numbered principles, `P1` through `P14`. They
are acceptance criteria, not aspirations. Each one exists because the failure it
describes actually happened in a system this design draws from. A change that
violates one is wrong even when it works and even when its tests pass.

When the code and the PRD disagree, that is a finding to raise, not a
documentation gap to close. Do not edit the PRD to match the code.

Section 5 reserves two words that used to be interchangeable. A **hold** waits
on a person; a **park** is a bound stopping the run. `internal/findings` owns
the hold predicate (`Holds`, `Held`, `HasHeld`) and counts no bounds, so
nothing there parks. `internal/graph` owns the three parks, and
`graph.Status.Stopped` is named after neither because it is the union of both.
Prose and identifiers both say this now, so a reader may take either literally.
Keep it that way: a wait on a person is a hold everywhere, and only a bound is a
park.

## The principles that most often get violated by accident

- **P3.** A finding with a missing, empty, or unrecognized action becomes `ask`.
  This is defined behavior, not an error path to log and move past.
- **P4.** Reviewing and fixing are separate roles with separate memory. A review
  never resumes the session that prescribed the fix it is checking.
- **P6.** Never lose work. Anchor every history-rewriting update to a commit the
  run actually observed, never to a tip read immediately before pushing, which
  always succeeds and therefore protects nothing. When a safety fact cannot be
  verified, refuse and report.
- **P7.** Configuration that executes code is read from the default branch, never
  from a pushed commit. If the trusted copy cannot be read and parsed, stop
  before launching anything rather than falling back to defaults.
- **P8.** An append-only event log is not current state. Reading its last line to
  decide what is true now is always wrong.
- **P14.** Every fact has one owner. Everything else points at it.

## What review keeps catching

These sit alongside `P1`-`P14` as a reading lens, not a replacement for them.
Each has cost this repository more than one round of review.

- **Claim only what a reader can verify from this repository.** A doc comment is
  a contract, so it may not promise more than the mechanism enforces.
  `internal/graph` and `internal/vcs` both shipped claims of protection nothing
  implemented. When a measure is best-effort, say so and name the residual gap.
- **Do not assert how an external system behaves internally.** Say what a measure
  buys this package instead. `internal/vcs` lost four rounds to sentences about
  git's internals, checkable only by probing git and falsified by the next round.
- **Watch for a strict stance quietly relaxed in one path.** `internal/findings`
  failed toward the human but substituted the schema example, `internal/config`
  accepted nothing silently but let a duplicate key win last, and
  `internal/graph` reported an exhausted guard set as a completed run. The
  invisible exception is the part that would have shipped.
- **A fake may not produce a shape the real mechanism cannot.** `internal/safety`
  shipped a guard against a reference that peels to another object which could
  never fire, because the read behind it did not ask for the peeled line and so
  could only ever come back with the two fields it compares equal. Its test
  passed because the fake stated `vcs.Ref` values directly and stated one that
  read cannot produce. Mutation testing does not surface this, since deleting
  the guard does fail the test. Model what the mechanism puts on the wire and
  derive the values from it the way the real parser does.
- **Check a confession as hard as a promise.** `internal/pipeline` came within a
  fix round of disclosing that a configuration change could defeat its
  convergence bound, a failure the mechanism cannot produce; the round-limit
  half of the same disclosure was true, which is what made the false half read
  as plausible. A doc comment is a contract in both directions, so verify a
  stated gap against the mechanism before writing it down.

## Code

- Go, formatted with `gofmt`. `make check` is what CI runs and what the gate
  runs; keep it green.
- `.no-mistakes.yaml` carries the gate's own review instructions, and it is
  executable configuration the gate reads from the default branch under P7, so
  a line there steers every future run in this repository. An instruction may
  claim only what the mechanism enforces, and where a guarantee is partial it
  says what it does not cover: one that overstates manufactures confident wrong
  findings for as long as it stands. Change it on a branch of its own, never at
  a document gate, where review is already past and no reviewer would see it.
  Its schema has no repository-wide review key, so a rule that holds everywhere
  is the `path: "*"` block rather than a copy in each package block, which is
  also the cheaper of the two.
  The gate caps the whole section by an upper bound it checks before a run
  starts, and the section is close enough to that cap that a new block cannot
  fit at all and an addition to an existing block has well under a rule's worth
  of room. Measure with `no-mistakes`' own `ReviewPathInstructionsBytes` before
  writing anything: that function is the accounting, and
  `docs/upstream-review-instruction-bounds.md` owns what it charges, how much
  room is left, and the method behind each of those figures.
  A section over the cap is refused rather than truncated, and the gate
  validates the pushed copy too, so a branch that overfills it fails its own run
  at start and cannot merge through the gate. Reaching later runs takes a commit
  that lands on the default branch without a gate run.
  Do not make room by deleting a rule. The deleted rule's defect starts
  recurring, and a diff that removes one instruction and adds another reads as
  an edit rather than as the regression it is. A channel with no room left is a
  decision to raise, not one to settle at the point of use, and
  `docs/upstream-review-instruction-bounds.md` is that ask; no local move
  changes it materially.
- This file is not an overflow channel for that section, and moving a review
  rule here to make room is not a fix. It differs in trust: the gate reads
  `.no-mistakes.yaml` from the default branch, while this file reaches a
  reviewer only through the resolved agent CLI's project-doc discovery in the
  pushed working copy, so a rule moved here is deletable by the branch it was
  written to review. Its reach is also adapter-dependent and nothing here
  settles it, so a rule moved here can stop applying with no error and nothing
  reported; `docs/upstream-review-instruction-bounds.md` owns which mechanisms
  decide that and what was searched to establish each one. Do not confuse the
  gate's `disable_project_settings` with `suppress_project_instructions`, which
  is this project's own key for the same idea and decides nothing about the
  gate.
- Every exported symbol carries a contract, so give it a doc comment that states
  the contract rather than restating the name.
- Prefer a small, testable pure core with the side effects at the edges. The
  execution engine in `internal/graph` has no git, no network, and no agent
  calls in it, and it should stay that way.
- A refusal is a typed result a caller must handle, never a logged warning that
  execution continues past.
- The configuration schema has one owner: the key table in
  `internal/config/key.go`. A key's default, trust class, decoding, and where it
  lands in a `Config` are one row. Add a key by adding a row, never by adding a
  second list of keys somewhere else.
- `internal/graph` owns execution and all three loop bounds. A bound only
  survives a resume with the thing it is compared against, so `Counters` carries
  the run's step budget and a digest of the edge vector its per-edge counts are
  indexed against, and a graph whose edges differ is refused rather than
  resumed. Add a bound by adding it there, never beside the counter that reads
  it. The budget is the one bound a caller may change on a live run, and only
  through `Executor.AdoptBudget`, which writes the change into the run's
  history; a difference nobody asked for is `ErrBudgetChanged`. Read its
  `doc.go` before changing any of that, and `identity.go` for what the digest
  covers and what it deliberately leaves out.
- `internal/vcs` is the only package that invokes git. Do not shell out to git
  anywhere else; add a typed operation there instead. Its package comment states
  the two rules it applies to every invocation and the residual gaps in them.
  `Incorporated` is defined by its result, not by how git reaches it, and a
  non-nil error from it is never a clean `false`; `Inventory` is the work set a
  cleanup compares and excludes remote-tracking refs because a proof's own fetch
  writes them. Neither decides anything and nothing there removes a clone.
  `StripRedirecting` is the one list of what points git at another repository,
  and `Observe` is taken under this package's own environment, so a
  `vcs.Observation` says what a copy on disk is and never what a worker's
  environment would make git see there; a check on that environment derives
  from `StripRedirecting` rather than carrying a second list. `Clone` promises
  the remote set the caller named and refuses a destination that exists.
- `internal/store` is the only package that opens the database and the only one
  that writes SQL. Add a typed accessor there rather than a query elsewhere. Its
  driver is pure Go on purpose, so `make check` needs no cgo on any platform.
  The cleanup decision's two records beside `task_cleanup` are migration 18's:
  `run_cleanup`, on the same terms plus a refusal column that is the one
  state a row is revised out of, and `task_report`, written once by
  `StoreTaskReport` in the transaction that registers the holds derived from
  it, with the parsed report opaque and the hold keys the one fact the row
  holds about it. `RecordTaskCleanup` takes the shape it was decided for and
  refuses `ErrTaskShapeMoved` in the claiming transaction, and
  `TransitionTaskShape`, promotion's one write, flips shape, repository and
  branch with the `promoted` entry in one anchored transaction, one way
  only, and refuses `ErrCleanupRecorded` by reading the claim the same way,
  so the two writes have exactly one loser in either order.
  `ForgetHoldForTest` takes the test and is a counterfeit builder for the hold
  rule's refusal case, never a production path.
  A schema change is a new migration appended to `schema.go`, never an edit to
  one that has shipped; the tests number what they append from the shipped list
  rather than spelling a version out. It also owns who resolved a hold:
  `Resolver` is a closed set, and it is a struct with an unexported field rather
  than the defined string type every other vocabulary here uses, so no text a
  caller was handed decodes into one. `ResolvedByPerson` has no producer in this
  repository on purpose, because no surface here witnesses a person; the record
  understates who decided rather than overstating it. It records and does not
  gate, so nothing may branch on the value to decide whether a resolution may
  proceed. `Acknowledger` is the same shape for the wake queue, with no member
  meaning a person and, in this build, no producer: the ipc rows that would
  derive it wait on #91's worker set. The queue is `wake`, a table and never a
  directory, appended and sealed so acknowledging is a row's one revision;
  `AppendWakeWithMarkers` is the only way a wake lands and it advances the
  task's `wake_marker` row in the same transaction, deriving the change epoch
  there, so a kind is a word it records and `internal/supervisor` owns. Both
  it and `AdvanceWakeMarkers` take an `Incarnation`, which only
  `BeginIncarnation` produces, and refuse one that is not the current row of
  `supervisor_incarnation` with `ErrStaleIncarnation` inside the same
  transaction: the store owns which loop incarnation is current, and that is
  the fence `internal/service`'s repair in place rests on. A
  drain is a read, delivery is at least once, and `OpenReadOnly` is the open
  a reader with no service takes, answering `ErrUnreadable` for what it
  cannot read. It also holds the run checkpoint history the durability layer needs,
  which is the only record of a run's position: opaque payloads indexed by run
  and sequence, with the anchor a property of the request that no column holds.
  The run process record (`RecordProcess`, migration 19) says which agent group
  a run started and not whether it runs now (P8), so nothing reads it as
  liveness.
  The one-row `checkpoint` migration 1 created was the second record the PRD
  forbids, and it now has no accessor at all. Do not give it one. Its table is
  still declared because a migration may not remove a table, so migration 6
  seals it with a trigger instead; a drop would mean weakening `verifyAdditive`,
  which is what protects every other table's rows. Read its `doc.go` before
  changing any of that.
- `internal/safety` owns two questions: whether a branch update may proceed and
  on what anchor, and whether a set of commits is already incorporated in a tip,
  which is the git half of P12's content proof and fails closed through its own
  `IncorporationRefusal` matching `ErrNotEstablished` and never `ErrRefused`.
  `internal/vcs` stays mechanism only, so a lease, an incorporation check, or a
  force decision belongs in `internal/safety` even when it would be shorter to
  write at the git call. Its anchor is an `Observation`, and only `Guard.Observe`
  or `RestoreObservedFromCheckpoint` produces one. The wrong anchor is not
  unrepresentable: a restored anchor's provenance rests on the checkpoint it came
  out of, not on the type. Read its `doc.go` for why, and for the residual gaps
  that leaves.
- `internal/forge` is the only package that talks to a code host. Everything
  provider-specific stays behind `Provider`, and `internal/vcs` still owns git,
  so a forge adapter never shells out to it. Its checks model is the part that
  is not a thin wrapper: an empty check list is not green, only a `no_ci`
  declaration makes it so, a cancelled check is settled, and an unrecognized
  state deliberately keeps the caller waiting. A `Provider` addresses exactly
  one repository, so it is never the build-scoped thing: `Host` holds what is
  settled once and `Host.Open` takes the repository, which reaches a run as
  `pipeline.KeyForgeRepository`, derived in `internal/service`'s `begin` from
  the run's own repository row. Three measures keep a write on the repository
  the run named, and each answers the same fact that a specifier on a command
  line carries no host: `GitHubRepository` reads one only out of a remote on
  `GitHubHostname`, every invocation is given that host in `GH_HOST`, and
  `Open` and `UpdateBody` first confirm the provider resolves the specifier to
  itself. Read its `doc.go` before changing any of that, and for the residual
  gaps: the confirmation precedes a write and not a read, it is not a lock
  against a later rename, and a GitHub Enterprise upstream yields no specifier.
  Tests outside the package reach a code host through
  `internal/forge/forgetest`, a stand-in `gh` answering over the wire and
  read by the real adapter, scripted with only shapes gh produces, on
  `internal/judge/judgetest`'s terms; the package's own tests keep their
  private stand-in for the contract cases it models and `forgetest` does not.
- `internal/gate` owns the local bare repository a push is validated through:
  where it lives, what it is born with, its hooks, and its identity across a
  move or a copy. It composes `internal/vcs` and builds no command lines, and it
  composes `internal/store` for the ownership index without opening a database.
  It also owns the hook protocol it defined, which is what makes a push start a
  run: the installed hook writes this binary's absolute path, `--gate` and
  `--home`, so the pushing environment chooses none of the three;
  `ParseRefUpdates` reads the reference update lines the hook puts on that
  command's standard input; and `WorkingCopyFor` answers the question a hook
  arrives with, because a gate is filed under a hash of its working copy's path
  and that hash cannot be inverted.
  Two things there are mechanism rather than rule, because this package wrote
  both rules down and then broke them. No operation that changes a gate takes a
  gate's path, only the working copy it is asked about: one unexported seam
  resolves the gate, settles who it belongs to, and seals it, and an operation
  that cannot obtain a handle cannot skip any of that; `WorkingCopyFor` is the
  one exported operation outside the seam, and it is confined to a read that
  seals nothing. And nothing this package produced counts as evidence of
  ownership, so neither the `assistant` remote nor the path hash is weighed;
  the question is one shared rule both askers run, over the store's ownership
  index and the gate's own record, and every answer either gives is checked
  against the working copy actually standing there. Read its `doc.go` before
  changing any of that, and for the residual gaps: a path that outlives its
  working copy, `core.hooksPath`, and letter case in a path.
- `internal/findings` is the vocabulary every stage speaks, and the only place
  a report is parsed, normalized, validated, or refused. P3 lives there: an
  action it cannot read becomes `ask`. The review stage answers for one rule
  more, PRD section 5's "what a review report has to carry", and
  `ParseReviewReport` is that rule with `Demand.Guidance` as the text a reviewer is
  held to. Two orderings hold it together and both are structural: the binding
  runs before `Normalize`, on the action the reviewer stated, which is what
  keeps demoting an unsupported finding from reaching P3; and it is reachable
  only through that one entry point, so a report already normalized, stored, or
  built by hand cannot be bound. Read its `doc.go` before changing any of that,
  and for the residual gaps: the evidence set is the reviewer's own claim and no
  path is resolved against a filesystem, and citing is voluntary by the PRD's
  own design, so the wholesale-declaration forfeit bites only a finding that
  volunteers its reach through a location or a citation.
- `internal/agents` is the only package that starts an agent process. It owns
  the process tree, the per-invocation environment, and what is recorded about
  a call. P4 lives in its type split rather than in a rule callers follow:
  `Runner.Run` cannot be given a session and `Fixer.Apply` cannot be given a
  purpose, so keep any new entry point on one side of that line. The same split
  answers the adapter that has no sessions at all: `Runner` has no `Fixer`
  method, only `SessionRunner` does, and `OpenFixer` is the only route to one
  this package offers a caller holding a `Runner`. `SessionRunner` is exported,
  so `Resolve` rather than the type is what keeps an assertion on it from
  finding an undeclared session mechanism. An adapter declares what it supports
  through `Runner.Capabilities`, the set is the table in `capability.go`, and
  undeclared means unavailable. Add a capability by adding a row there and a
  conformance probe in `capability_test.go`; a capability an adapter declares
  and no probe checks fails the conformance suite, which is what keeps a
  declaration from being a comment. `Resolve` refuses an adapter whose
  declaration and type disagree in either direction. `Shape` rides on the shared
  `Invocation` and so sits outside the P4 split, which is why
  `Invocation.ValidateForFixer` refuses `ShapeReview` at the fixer with
  `ErrReviewInFixerSession` before anything starts; put a new shape on one side
  of the line the same way. A task's worker is the third entry point and sits
  on the fixer side by its types: `WorkerRunner.Start` takes a `WorkerSpec`
  with no purpose and no shape, `OpenWorker` is the only route to one from a
  `Runner`, the `Worker` handle is a process and no route back, and its
  environment is the one builder's output passed through
  `vcs.StripRedirecting`, which owns what redirects git. The handle reports
  its process group and registers it nowhere; `internal/service`'s
  `containRunner` has no worker arm, because a worker's push is what the gate
  admits. No shipped adapter declares `worker_sessions`; `ScriptedFactory` is the
  declarer the conformance suite holds to the mechanism, and
  `standin.NewWorker` is how a test gets a runner built from it. Reading agent output is `internal/findings`' job and
  never a second parser here; the one choice this package makes is `Shape`, and
  `ShapeReview` is the only one carrying a `findings.Demand`, which
  `Invocation.Validate` requires there and refuses everywhere else. It also owns
  the one PID-reuse check, `Identity`, which `internal/session` and the run
  process record carry rather than reading a start time of their own, and
  `EndGroup`, the only way to end a group it started from outside the
  invocation: nothing is signalled until `Identity.Presence` verifies the
  leader, and then only by group identifier, never by pattern. `Sweeper` is
  the identity-based sweep: it ends what runs in one copy `home.CopyAt`
  accepts, matched by working directory and never by command line, and lists
  only on Linux; elsewhere `ErrSweepUnsupported` means "could not prove",
  and its zero `SweepResult` is not `Clear`, so never read it as "none
  found". Read its `doc.go` for the residual gaps: a capability whose row
  has no probe is taken at its word, an identity narrows PID reuse rather
  than closing it, and a sweep sees neither a process that left the copy nor
  one whose working directory it may not read, so an unreadable one blocks
  `Clear` unless it is an ancestor or another user's process it may not
  signal.
- `internal/agents/standin` is the scripted agent the tests outside
  `internal/agents` run against. It implements no `agents` interface and builds
  no `agents.Result`: it prints bytes and exits with a status, and the real
  adapter reads that, so a shape the adapter cannot produce is unsayable
  there. Started without `--print` it is a worker, reading lines and answering
  as `Script.Worker` says, and `NewWorker` is how a test gets the adapter that
  starts one. A consuming package's `TestMain` must call `standin.Main()`, and the
  first `New` in a process refuses loudly when it did not. Read its
  `doc.go` for what a recorded `Call` does and does not prove about P4, and for
  the two residual gaps.
- `internal/ipc` owns the local protocol: the method table, the event
  taxonomy, and the bounded stream. Two rules there are load-bearing rather
  than stylistic. An event class decides what overflow may discard, and an
  unrecognized type is state, so a type added later is retained; adding a type
  means adding a row to the class table in `event.go`. Authority over a request
  comes from `Credentials`, read off the socket, and never from `Marker`, which
  a caller writes for itself. A third follows from the second: no frame here has
  a field for who resolved a hold, and `Request.HoldResolver` derives the answer
  from the surface rather than reading it, so a caller cannot claim a person
  decided; `Request.WakeAcknowledger` is the same rule for the wake queue.
  Whether a peer is a task's worker is a second relation over the same
  `Credentials`, decided by the handler from `internal/session`'s worker group
  set and never a third access class: the rows whose answer turns on it
  (`task.event`, `wakes.*`, `supervision.assert`, `gate.admit`) say so, and
  `ErrWorker` and `ErrNotWorker` are its two refusals. Read its `doc.go` for
  where state may still be collapsed into a gap marker and why that is not the
  strict rule relaxed. Four state types have a
  producer, all in `internal/service`; two the table names have none, and the
  table keeps them because a row is not a promise of a producer.
- `internal/pipeline` is the nine stages as a graph definition, not a second
  executor: `internal/graph` owns execution, halting, and all three bounds. P2
  is structural there rather than checked. A `Stages` is nine named fields, so
  another order, a missing stage, and an added one are unsayable, and the order
  itself is the unexported stage table in `stage.go`, which also says which
  five stages may take fix rounds. The state schema is the key table in
  `key.go`, on the same terms as `internal/config`: a key's kind, its merge
  rule, and whether a stage may write it are one row, and a stage that names
  anything else fails to build. It also refuses a path the resolved adapter has
  not declared: `Options.Adapter` is the declaration, an `Implementation` and a
  `Fixer` name what they need in `Requires`, and `capability.go` gathers every
  requirement in one place. For `resumable_sessions` that is the early half of
  the rule and the half that cannot be forgotten is `internal/agents`; for
  `suppress_project_instructions` there is no second half, because
  `internal/agents` implements no suppression and so has nothing to refuse at,
  which leaves `New`'s check the only enforcement of PRD section 10's refusal
  before launch. A fixer reports nothing that can clear a stage: the one
  finding it may return is an `ask` in `FixOutput.Holds`, anything else is
  `ErrFixerFinding`, and the stage node adds those holds to the stage's next
  report itself, so no stage body can drop one. A hold's `fix` answer is a
  second bounded cycle through the stage's request node, its `FixRequest`
  riding as the hold's `Halt.Detail`, and it always holds again after. It
  exists only at the stage table's requestable stages, the ones before push,
  and its edge back is `graph.Edge.Answered`, exempt from convergence and
  from no other bound. Read its
  `doc.go` before changing the loop or the holds, and for the residual gaps: no
  review reads what a person left unselected, and convergence is over the whole
  state.
- `internal/runs` owns two things: the table of a run's status changes, and the
  one durable fixer session a run keeps. Both are anchored rather than
  convention. A status move is `store.TransitionRun`, which reads and writes in
  one transaction against the origin set the caller names, and which statuses
  are terminal is read off that table rather than listed beside it. The session
  reference is on the run's own row and must never move into `internal/pipeline`
  graph state: that state is the convergence fingerprint, and a reference that
  changed across a resume would silently disable the bound.
  `TestConvergenceFiresWithADurableSessionAndNotWithOneInGraphState` runs the
  real loop both ways so the second half of that sentence is demonstrated
  rather than asserted. P4 rides on `internal/agents`: `runs.Fixer` is an
  `agents.Fixer`, so no entry point here takes a purpose, and the session-free
  mode asks `Invocation.ValidateForFixer` itself so a review shape is refused in
  both modes. Read its `doc.go` before changing any of that, and for the
  residual gaps: one fixer per run is bounded by one service, and a handle
  already handed out is not revoked. A run's position across a restart is not
  this package's: that is `internal/checkpoints`.
- `internal/taskstate` resolves a task's state from the records that own its
  facts, on every read, and stores nothing: `Resolve` is the PRD section 6
  precedence and `kind.go` is the one owner of what an event kind means. The
  `task_state` table is sealed (migration 10) and must never get an accessor.
  `internal/store` only returns the facts, through `TaskSnapshot`, and learns
  no meaning. The terminal tier, `SourceRecord`, outranks every run and is
  derived: `Landed` from the merge record of an attributed run and `Complete`
  from that and the cleanup record's removal, for a delivery task; nothing
  there reads a code host or writes a record, and no event kind is named
  `landed` or `complete`. `Complete` for an investigation task is not
  resolved: `internal/store` now holds the stored report and its holds, and
  `internal/tasks` verifies them before the cleanup record, but the snapshot
  does not carry the report and the tier declines rather than approximating,
  which `doc.go` says. Read its `doc.go` for the residual gaps, and for why PRD
  section 8's "task state" row is read as a resolution rather than a row.
- `internal/tasks` is PRD section 8's tasks module as far as this build takes
  it: intake, queueing, the P11 assertion, the P12 cleanup decision and
  promotion, with dispatch and the copy in `internal/service` and the launch
  in `internal/session`. `Expect` computes
  a task's `Expectation` and its second-check shell line from one call, and
  `AssertIsolated` compares a real `vcs.Observation` against it by file
  identity, never by string, with an exact remote set per shape and a typed
  `*IsolationError` naming the comparison; only it sets `Isolation`'s
  unexported field, `Refusal` is the one mapping from a comparison to
  `taskstate`'s refusal token, `internal/session`'s `Create` is what takes an
  `Isolation` to start a worker and `internal/service`'s launch is what
  reaches it, with a proof asserted again at launch; its own P11 citation
  claims the assertion refuses and nothing about launch, and
  `internal/service`'s claims the launch edge. Until
  #91's worker set exists, "own branch, only through the gate" is
  instruction text and nothing enforces which ref a worker's push may move;
  its `doc.go` carries that sentence. Every lifecycle move there is an appended event, never a state
  column: `Create` writes the disposition through `store.CreateTask`, anchored
  to the newest task row it saw on the branch and under the service's branch
  exclusion, and `Release` appends `dispatched` through
  `store.AppendTaskEventAfter` anchored to the sequence the resolution it
  decided from had read. It learns a state only through `taskstate`, so the
  live set and the clear condition (`Live`, `Clears`) are predicates over a
  `State`, `Unknown` is live, `Release` re-runs the floor before it
  dispatches (an earlier queued task on a branch holds a later one, so one
  pass releases one task per branch), and a queued task whose claim a higher tier
  overrode is refused with `*OutrankedError` rather than appended past; it
  reads that off `Resolution.Superseded`, never the history's tail. Ids are
  `home`'s rule, the shape is `store.TaskShape` decoded at the edge, and the
  copy path is `home.TaskCopy`, recorded as a reservation. `Create` has no
  caller in this build, by design: PRD section 9 has no verb that creates a
  task, and adding one is a finding. `Release` is called from
  `internal/service` when a run finishes and at open, except where a push
  supersedes a run, since that push creates the branch's next run under the
  same exclusion and the branch stays live. Read its
  `doc.go` before changing the floor or the clear condition, and for the
  residual gaps: Ready never clears and only a merge record moves a blocker
  past it (`Clears` is Failed, Reported, Landed and Complete, and the two
  finishes in `internal/service` that write the record both call `Release`);
  Failed is not terminal, so a relaunch must re-run the floor; a
  person's push on a task's branch is outside it.
  It is also P12's decision and the only writer of the cleanup record:
  `Cleaner.CleanUp` over a task's copy and `CleanUpRun` over a run's, one
  decision with two removals, a clone's directory removed by this package
  behind the proof and a worktree removed through the non-discarding
  `vcs.RemoveWorktree`. `Removal` is the proof, with the field only the
  decision sets and both removal functions require; the three proofs in
  `proof.go` read the upstream fresh, fetch the tip by identifier, and record
  what they examined (`store.CleanupProof`, with `CleanupProofScratch` the
  one case that is no proof of work: an unchanged investigation copy); an
  inconclusive proof is a `ProofAttempt` on the `*CleanupRefusal`, never a
  proof, and no remedy names a flag. The record is written before the
  removal and marked after, anchored to the shape, so a second call
  completes a crashed one. An investigation copy answers to PRD section 11
  first through `StoreReport`, which parses with `findings.ParseReport`,
  registers every ask as a hold in the row's transaction, and keeps a fix
  finding as evidence that reaches no fixer. A task copy is examined only
  once its terminal probed `Dead`, and `internal/service` passes the
  liveness its own task reader resolved through the session provider, so a
  landed task whose worker runs keeps its copy with the live refusal; its
  `doc.go` lists the gaps: a stated liveness is believed, the window after
  the read is covered by the inventory re-read alone, the discarding
  worktree removal stays reachable, ignored files are invisible, and a
  promoted copy holding nothing main lacks passes the content proof like a
  fresh delivery copy.
  `Promote` is PRD section 5's promotion, a library entry on `Create`'s
  terms (no verb, no method: #95 line E) and the one place this package
  writes into a copy. The flip is first and is `store.TransitionTaskShape`
  anchored to the resolution's event sequence, under the branch exclusion,
  from the closed table `Promotable`; branch and repository come from the
  request and are held to intake's branch rule and the floor, which refuses
  `*BranchHeldError` rather than queueing a running task; the report is a
  pointer to its row; the copy is then asserted (so a copy reaching the
  primary is never written to), shaped with the gate remote and the branch
  at HEAD, and asserted again, with a refusal recorded as
  `isolation-refused` anchored to the `promoted` entry. Read `promote.go`'s
  opening comment and `doc.go` before changing the order, and for the gaps:
  the copy side is never repeated, the environment asserted is the
  request's, and the `promoted` note resolves Unknown at the event tier.
- `internal/session` is PRD section 8's terminal session provider, and the
  word means a terminal session, never the conversation session
  `internal/agents` keeps for a fixer: `Provider` is the five operations and
  nothing else, there is no capability table here, and an optional behaviour
  a later provider has is a row in `internal/agents`' table with its probe
  and declarer. `Pipe` is the reference provider and starts nothing: `Create`
  refuses an unproven `tasks.Isolation` with `ErrUnproven` and then asks the
  resolved runner for a worker through `agents.OpenWorker`, so only the
  scripted adapter answers it in this build and the real adapter's worker
  mode is #95 line K. Its one creating caller is `internal/service`'s
  `launch.go`, which hands it a proof asserted again at launch rather than
  dispatch's; `Create` itself re-derives nothing. Its one observing caller
  is that package's `terminals.go`, which probes through `Probe` and
  captures through `Pipe.CaptureReference`, the capture addressed by the
  record's reference out of the same held set `Probe` answers `Live` from,
  so the reference to handle lookup has one owner. Three things there are
  mechanism rather than rule.
  `Probe` answers `taskstate.Liveness`, that package's word, with a typed
  `Reason`, and the `reasons` table is the one place a reason's liveness is
  decided: `Live` only through a handle this provider holds whose process
  has not exited, `Dead` only where absence is proven (exited, no such
  process, start time differs), everything else `Unobserved`, a probe error
  among it; the test applies that rule to the table and to a counterfeit.
  The identity is the attempt's session reference, a `Reference` of home
  marker, task, pid and start time encoded in one closed format, recorded by
  the caller through `store.SetAttemptSession` and nowhere else, with no
  file under the home; another home's reference probes `Unobserved` here.
  `Capture` is the worker's bounded tail plus `Digest` over `Normalize`, an
  output stream and not a screen, with the rules derived from what the
  stand-in emits and the real adapter's format stated unknown; the
  comparison is the supervisor's. `Workers` is the set of live worker groups,
  keyed by the process group a worker leads and naming its task, and `Pipe`
  is its one producer: `Create` joins the group and the membership ends with
  the process or with `Close`, the join is unexported, and `Member` reads the
  worker's done signal, so nothing a caller states is a membership. It is
  the authority input every worker-to-program path derives a task from and it
  is not `internal/service`'s containment registry, which answers a different
  question; its doc says why one map may not answer both. Read its `doc.go`
  for the restart answer
  and the residual gaps: nothing ends an orphan, the record-first window
  leaves a process no record names until #112's sweep, the route walk does
  not see behind an interface-typed field, and on a platform without a
  start-time read an unheld reference is never `Dead`.
- `internal/supervisor` is the pure core of PRD section 5's supervision:
  `Classify` is the wake flowchart as a function and `Assess` is the stop
  guard's predicate, both over typed inputs the caller computed, with no
  clock, key, file or record read here; its direct imports are
  `internal/taskstate`, `internal/home` for its lock vocabulary, and the
  standard library, and neither function performs I/O. The loop, the queue, the beacon and the hook are
  elsewhere. Three things there are mechanism rather than rule. Evidence is
  `taskstate.Resolution.Progress` and `EvidenceOf` reads that field alone,
  so nothing here re-derives from `Basis`. An event wake exists only through
  `Event`, which reads the decision verb off `taskstate.Lookup`, and `Of`
  refuses the two event kinds, so a caller cannot state that an event carries
  no decision verb and an unrecognized verb surfaces as such. A
  `StopVerdict`'s `Allowed` is derived from its reason, and the unknown
  in-flight and lock readings are the zero values, so nothing unfilled
  allows. The matrix test's oracle is written from the flowchart and not from
  the kind table, enumerates the kinds so a row without an oracle fails, and
  runs two counterfeit classifiers to show it can fail. It cites P9 for the
  table only and never P10: `Assess`'s inputs are stated there, not produced
  by the real lock and files. `inflight.go` is the one owner of which states
  owe a supervision cycle: a closed table over `taskstate.States()` that its
  test derives from that function, so a new state fails until placed, and
  `InFlightOf` reads it beside `store.UnacknowledgedWakes`, because a hold
  nobody has been told about is in flight whatever its state says. Read its
  `doc.go` for the residual gaps: the
  check-result kind has no producer, the repetition count is read for the
  unchanged terminal alone, and freshness blocks falsely across a suspend or a clock
  jump by design.
- `internal/checkpoints` is the durable `graph.CheckpointStore`, and the only
  place outside `internal/graph` that serializes a `graph.Checkpoint`. It
  exists as its own package because `internal/store` must not learn what a
  checkpoint means and `internal/graph` must not learn about a database, so the
  adapter belongs to neither. The anchor is never decided here: `Write` hands
  it to `store.AppendGraphCheckpoint`, which decides it in the transaction that
  assigns the sequence, because a check made here and a write made there is
  exactly the interleaving P6 exists to stop. Honouring the anchor is required
  of every implementation, so the behavioural suite in `store_test.go` runs
  against this one and `graph.MemoryStore` both, and a behaviour checked
  against only one is not checked. Read its `doc.go` before changing any of
  that, and for the residual gaps: the encoder is `encoding/json` over the
  exported type rather than `internal/graph`'s own, and a fork reads its source
  outside the transaction that claims its destination.
- `internal/scope` is the review stage's scope lens, not a tenth stage: every
  changed line should trace to the recorded intent, and P2 fixes the list at
  nine. It ships on and has no off switch, which is why it is shipped guidance
  rather than the default of `review.path_rules`: a repository layer replaces a
  list, so that default would be erased by any repository that set the key.
  Its findings are `note` by construction, and a change that is also wrong is
  reported for being wrong through the ordinary review path. Read its `doc.go`
  before changing what fires, and for the residual gaps: the granularity is the
  path rather than the line, and a trace's reason is recorded, not verified.
- `internal/home` owns the on-disk layout PRD section 8 gives one root, the
  exclusive lock that makes a home have exactly one service, its probe
  `LockState`, and the instance file on both sides, `WriteInstance` and
  `ReadInstance`. No other package spells a path under the root, with one
  deliberate exception: `internal/gate` composes `<home>/repos/<id>.git`
  itself, so this package does not; the beacon's path is here and its content
  is `internal/supervisor`'s. A task's paths (`Task`, `TaskCopy`,
  `TaskInstructions`, `TaskReport`) are all composed here from an identifier
  `ValidTaskID` accepted, and the report's evidence copy sits beside the copy
  so cleanup leaves it. The wake
  queue is not in the layout at all: it is `internal/store`'s table, so no
  path to a `queue/` is composed and `Create` makes none, while an older home
  keeps an empty one nothing reads. The lock is
  an operating-system lock rather than a file naming a process, because the
  property PRD section 8 asks for is that it cannot go stale after a hard kill.
  Read its `doc.go` for the residual gaps: an advisory lock binds only the
  processes that ask for it, and two roots naming one directory are two homes.
- `internal/redact` is the one owner of credential removal, which
  `internal/store` refuses to open without and `internal/vcs`,
  `internal/forge`, and `internal/stages` take. It recognizes a credential in
  a URL's userinfo and a bearer or Authorization value and nothing else, and
  says so; adding a shape means adding it there, never a second remover at a
  call site.
- `internal/judge` is the decision-model client for PRD section 5's "Checks a
  decision model may add": the two fixed questions, the pinned version, the
  cutoffs and their measurement. A `Result` is a probability or a failure and
  never both, so a failed call is never read as an answer. The key comes only
  from `ASSISTANT_DECISION_MODEL_KEY` in the operator's environment, and the mode is the
  global-only `decision_model` key. The review stage turns answers into
  findings in `internal/stages`' `decision.go`, which may only add. The screen
  is a tripwire for plain attempts, not a security boundary. Tests reach the
  provider over the wire through `internal/judge/judgetest`, never live. Read
  its `doc.go` for the measurement and the gaps: the record does not yet keep
  what the run did with a path afterwards, and nothing proposes turning a
  check off.
- `internal/supervisor` is the pure core of PRD section 5's supervision and
  performs no I/O: the wake classifier, `Assess`, the in-flight table, the
  beacon's fixed-size record and its codec, and the two durations derived from
  the one configured interval, `config.KeySupervisionBeaconInterval`, through
  `FreshnessFor` and `FirstBeaconWait`; staleness and the first-beacon wait are
  named multiples and never rows of their own. `GuardOf` is the pull guard's
  predicate and expects the instance file's identity and the beacon's own
  epoch, which its doc states; `ArmOf` is arm verification's decision over
  one look; `IsFresh` is the one freshness predicate, which the readers and
  `internal/service`'s watchdog share. Its subpackage `feed` is the I/O edge:
  the beacon `Writer`, whose one caller is `internal/service`'s loop,
  `ReadBeacon` with its one retry on a torn
  read, `Observe` over the files, the lock and `store.OpenReadOnly`, `Guard`,
  and `VerifyArm`. P10 is cited in `feed`'s test, through a lock a
  second process holds, the real writer and the real instance file, and in
  `internal/service`'s wedge test for the loop replaced in place. The
  `supervision.assert` ipc row waits on #91's worker set and is not in the
  method table. Read both `doc.go` files for the residual gaps: no epoch is
  compared outside the stop hook, the checksum authenticates nothing, and the
  in-flight read outside the service resolves with no probe and no advancing
  answer.
- `internal/machine` owns the shapes a structured answer takes, the three exit
  codes, the outcome vocabulary, and the P13 phrase table in `phrases.go`: one
  table of `Phrase` rows, each an outcome, a consequence and a decision in
  three fields so the required wording is structural, listed by
  `TablePhrases`. Its rows are the pull guard's three (`SupervisionOf`), one
  per `taskstate.State` with the domain derived from that package
  (`TaskPhrase`, which renders Paused as `Task.Waiting` and never the word, and
  a program note's Unknown as `NoWordYet`), one per `Outcome` with executing
  split three ways on the facts `NextActionOf` reads (`RunPhrase`,
  `Run.Phrase`, `RecordPhrase` for a record alone), the hold rows, the cleanup
  rows, the stream's rows and the status rows. No task row carries an outcome
  word. The forbidden set its test holds every row to is derived from the
  owners by `internal/machine/vocabtest`, which `internal/cli` and
  `internal/journey` run over what was printed, and a caller pairs `Assert`
  with `Control`. Add a phrase by adding a row there, never a sentence at a
  renderer. It composes records rather than restating
  them: a run is a `store.Run`, a report is a `findings.Report`, so no wire
  shape becomes a second owner of a record. `Outcome` is a closed set, and one
  translation answers for a run: `Run.Decide` takes a `Standing`, which is the
  record, where execution stopped, and whether a segment is advancing the run,
  and writes the outcome, that fact and the next action together. The action is
  unexported for that reason, so a surface elsewhere has no assignment site for
  one that disagrees with the outcome beside it; build an answer through
  `Decide` rather than filling the fields in. `OutcomePassed` has one
  producer: a passed record beside PRD section 8's merge record,
  `store.MergeRecord`, which only `internal/runs`' two pull request moves
  write, once, and which `Standing` carries as the end read off it. It
  does not own the outcome set: the PRD's outcome row does, and
  `internal/outcomes` fails the build when the two come apart, so the constant
  block's order is that row's order rather than a free choice.
- `internal/service` is the background service PRD section 8's process model
  puts at the centre of a home. It decides nothing a run validates:
  `internal/graph` executes, `internal/pipeline` is the topology,
  `internal/runs` owns the record, `internal/checkpoints` makes the position
  durable, and this wires them. What it does decide is which build-scoped
  dependencies a stage body gets, because it is the one place holding the home,
  the resolved configuration and the resolved agent at once; `doc.go` owns why,
  including why the agent reaches a body wrapped. Five things there are
  load-bearing. It takes the home's lock before recovery and
  before binding the socket, in that order. It reconciles every unfinished run
  against its checkpoint on open, because a record saying running against a
  checkpoint saying halted is a run nobody can answer. Containment is a
  process group this service registered, never anything a caller says about
  itself, and it has a producer now: `containRunner` wraps the resolved agent
  so every invocation and fix round registers its group for exactly the
  process's lifetime, while the configured commands and the provider command
  still register nothing, which `doc.go` states rather than implies. Each such
  registration also writes the run process record, and `recover` reaps from it
  after the runs are reconciled: decided from the run's reconciled status, a
  group ended only through `agents.EndGroup`, and anything it cannot verify
  reported and left alone rather than refusing to serve; `reap.go` owns the
  outcomes and `doc.go` the gaps, task terminals and copy removal being other
  owners'. A task's
  worker is placed by the other relation over the same credentials, the
  worker group set `internal/session` holds, and `workers.go` owns that
  derivation (credentials, then the peer's process group read as containment
  reads it, then the set) and the four methods that turn on it: `task.event`
  admits only a worker and holds the kind to `taskstate`'s writer column,
  `wakes.list`, `wakes.acknowledge` and `supervision.assert` refuse one.
  `admit_workers.go` owns the rule a worker's push is held to - only its
  task's branch, forward, never a deletion - and why that is admission's
  decision and not `internal/safety`'s; a person's push meets it nowhere, and
  on Windows no peer is placed at all, which both files state as a limit. Three
  state events have producers here and `publish.go` owns two: `task.state` is
  published from the supervisor loop's pass, the one reader that resolves
  every task, once per change by `taskstate.Basis` equality and not per pass,
  and `hold.state` at the two sites a hold is written, as the store returned
  it; both draw on the one revision counter `run.state` draws on, and
  `taskView` is the one composition of a task's answer for the publisher and
  the surface alike. A push is the one
  caller that does not wait: `gate.notify` records the runs and walks each on a
  goroutine of the service's, and a new push supersedes the branch's run in the
  record, which is the whole of what that word buys: `doc.go` says nothing
  bounds how long the displaced run goes on executing. Both gate methods take an
  identifier and never a working copy, so a caller cannot attach a push to
  another repository's runs. It is also the caller of `internal/tasks`'
  cleanup decision and decides nothing of it: `reclaimCopy` asks `CleanUpRun`
  when a run finishes and returns the typed refusal, which the decision
  recorded on the run's cleanup record and `report` composes into the run's
  answer as `machine.Run.Cleanup`; `cleanup.go` calls `CleanUp` for the
  delivery task a merge record landed and for an investigation task whose
  report `storeTaskReport` stored, passing the terminal's liveness as the
  service's own task reader resolved it, never a second read, and no verb
  or method reaches any of it. And whether anything is advancing a run is a third
  fact neither the record nor the checkpoint holds: it lives in the slot a run
  advances in, `report` reads it rather than inferring it, and it travels as
  `machine.Run.Advancing`. It is also where PRD section 11's step 3 lives:
  `checkshold.go` re-reads a run's pull request before the hold the checks
  stage raised at `checks_timeout` elapse is resumed, at recovery and at a
  response, and only that hold, which `stages.ChecksTimeoutHeld` identifies
  from the recorded report; it is the one producer of
  `store.ResolvedByProgram`, and a test there counts every identifier
  reference to it. The
  live wait's own re-read is the checks body's, and `settle` turns what that
  body wrote under `pipeline.KeyPullRequestEnd` into the record's move and
  its merge record, so no stage body decides a run's status; the resolution
  of that hold and the move are one store transaction. A segment that ends without settling leaves a run
  something can resume and nothing is resuming, which is the stall PRD section 9
  calls worse than an error; `carryOn` is what stops that persisting, and it
  decides from what ended the segment rather than from the run. It also reads
  the repository's own configuration document per run - the trusted copy at a
  fresh fetch of the default branch, the pushed copy at the submitted commit,
  composed by `config.ResolveRun` - and refuses the run before anything
  launches when the trusted copy cannot be read or the composed document pins
  a different agent than the operator's. Read `doc.go` for that, including
  how a resume resolves fresh and what decides its resumability. It builds
  every run an isolated copy, a worktree of the gate's bare repository at the
  run's own `refs/assistant/runs/<id>`, which `create` writes right after the
  row: from the gate's own object for a push, by a fetch from the working copy
  for a start or a rerun. `ensureCopy` reads that reference and never the
  working copy, and `copy.go`'s header says why the fetch moved there. It is
  also where a task is dispatched, in `dispatch.go`: the attempt row is the
  claim, the copy is a `vcs.Clone` of the primary shaped for the task's
  shape, `tasks.AssertIsolated` is run under `agents.WorkerEnvironment`, both
  snapshots are recorded, a refusal is the `isolation-refused` event carrying
  `taskstate`'s token, and a proof is held in the service's memory for
  `launch.go`, which follows a proven dispatch in the same pass: it observes
  and asserts the copy again under the same environment, refusing exactly as
  dispatch does, creates the terminal session through `session.Create` over
  the service's `session.Pipe` - built in `driverFor` on the resolved agent
  before `containRunner` takes its worker arm away - and records
  `Session.Reference()` on the attempt through `store.SetAttemptSession`.
  Every task reader is built through `reader`, probing through the
  service's `Terminals`, the one seam a terminal is observed through, which
  `Open` leaves to `terminals.go`'s `heldTerminals` over the service's own
  provider unless a test passes a double: liveness has one producer, the
  provider's probe, reached through that seam by the task surface, the
  queue, reconciliation, the loop and the cleanup sites alike, so a launched
  task reads Running from the process tier while its worker runs and what
  precedence gives with `Dead` once it exits, and nothing writes an event to
  say either; the provider is built in `driverFor` when first asked for, so
  a service that cannot resolve an agent observes nothing, and
  `ErrTerminalsUnavailable`'s one producer is a capture on such a service.
  An adapter without `worker_sessions` dispatches and never launches, with
  the `*agents.CapabilityError` logged and nothing recorded, so such a task
  reads Dispatched from the record; and `Close` ends only the sessions this
  service created. A path already standing at the
  copy's path is asserted and never removed. The environment half of P11 is
  only as strong as every launch path building its environment through
  `agents.WorkerEnvironment`, which every assertion and the worker's start
  do; no second strip list exists or should. A launched task is never
  dispatched again: what a restart does about it is `reconcile.go`'s, PRD
  section 11's fleet reconciliation inside `recover`, after the runs and
  before `releaseQueued`. The table `reconcileRow` is keyed on the resolved
  state, its source and the probed liveness, and its test derives the
  domain from `taskstate.States`, `Sources` and `Livenesses`; only Unknown
  from the process tier and Failed relaunch, and only with a dead terminal.
  The synchronous half is bounded, per probe and per read, and takes the
  anchored attempt claim; the launch goes to a goroutine under `startWork`.
  `relaunch.go`'s `relaunchTask` is the one relaunch: it decides from its
  own read, re-runs `tasks.HoldsBranch`, re-probes before the claim, claims
  through `store.BeginTaskAttempt`, appends `relaunched` then `dispatched`,
  asserts the standing copy through the `assertCopy` dispatch and launch
  share and never cuts one, and acknowledges as the program only the
  terminal-gone wakes about the session it replaced, matched through the
  loop's own marker composer. `relaunch_attempts` is a global-only row read
  only there, a task-level bound and not a loop bound, and `key.go` says
  why. Reconciliation writes no wake and no marker: the queue and the
  markers are the loop's, under its fence, and the loop's terminal-gone row
  is what tells the person about a terminal this pass did not relaunch.
  `reconcile.go` states what a dead terminal does not prove about the copy,
  since no sweep exists.
  It is also where PRD section 5's supervisor loop lives, in `supervise.go`,
  started by `Open` after recovery and before the caller serves. The loop
  runs as incarnations fenced by `store.BeginIncarnation`: every wake append
  and marker advance is decided against the current epoch in its own
  transaction, and the beacon, written only from the loop's own `select`
  through `feed.Writer`, is written under the same mutex the epoch is bumped
  under. A pass reads the records, then the slot, then the provider, through
  the same `Terminals`, so an unchanged live worker is absorbed on the
  handle's evidence, a dead one surfaces as terminal gone, and a survivor
  or another home's reference surfaces as unobservable; classification is
  `supervisor.Classify` over inputs `decide` composes from the markers, and a
  capture reaches a marker as its digest only, through `internal/redact`. The
  watchdog reads the beacon the guards read, replaces an incarnation whose
  beacon is stale or whose goroutine returned after one interval's delay, and
  never writes the beacon; its delay is not one of `internal/graph`'s bounds
  and carries no counter, so do not add one. `Close` waits `CloseGrace` for the
  supervision and then abandons it with `ErrSupervisionAbandoned`, the one
  exception to its wait on `work`, which the fence makes safe. Read
  `supervise.go`'s header before changing any of that, and for the residual
  gaps: an abandoned goroutine runs on in the process, a wedged watchdog is
  detected by readers and not repaired, provider-wide surfaces once per
  outage only while the newest affected task stays the same, and the verdict's
  level is not recorded on the wake row.
- `internal/cli` is the command surface, and its verb table is PRD section 9's
  table and nothing else. A verb that section does not describe is a finding to
  raise against the specification, not a row to add: `cli_test.go` holds both
  halves of that, the commands it names and the plausible ones it does not.
  Answering a hold and running the service in the foreground are flags on the
  commands that section does name. A verb that acts on a run calls the service;
  a verb whose job includes reporting that the service is down does not.
  Two commands stand outside that table, and together they are not a
  precedent for a third: each is the interface something external requires,
  so each is dispatched beside the table rather than added to it.
  `assistant gate` is what `internal/gate`'s installed hooks invoke, and its
  two subcommands are the `gateHooks` table in `gate.go`. `assistant host` is
  what the coordinator's host invokes from the hook it fires at turn end, its
  one subcommand is the `hostHooks` table in `host.go`, and it is PRD section
  5's stop guard with no service: it feeds `supervisor.Assess` through
  `internal/supervisor/feed` and never a second predicate, reads the re-entry
  signal from `HostReentryVar` and stores nothing, and maps the verdict onto
  the host's block convention in `cli`, never in `internal/machine`, which owns
  the verdict's shape and section 9's three codes. Nothing installs the hook
  until #95 line D is answered, and `host.go` says what the command cannot
  tell about its caller.
  What a person reads is rendered through the `renderings` table in
  `render.go`, which `readOut` dispatches through and `Renderings` enumerates:
  eight renderers are phrased through `internal/machine`'s table and the rest
  are enumerated as not, `doc.go` names which and why, and
  `internal/journey`'s P13 row is held to that enumeration. `eventLine` speaks
  the same table. Do not put a sentence a person reads in a renderer; put a
  row in the table.
  `cmd/assistant` is the process boundary and holds no behaviour, so a test
  drives `cli.Run` with its own streams rather than a subprocess. The push
  tests reach the surface the way a push does: the test binary answers the
  gate hook verbs from its own `TestMain`, so a real `git push` through the
  hooks `internal/gate` wrote drives `cli.Run`.
- `internal/stages` is where the nine stage bodies go. A stage with no body is
  `Pending`, which reads nothing and reports one `ask` finding, so P3 holds the
  stage for a person and no stage reports a pass it did not establish. The one
  owner of which stages have a body is the `written` table there: `All` places
  implementations from it and `Implemented` reports it, so adding a body is
  adding an entry. `Fix` is the fix path's body: it asks the run's fixer to
  resolve a stage's findings in the isolated copy, commits what changed, and
  reports the new head, which is the write the convergence bound reads. PRD
  section 5's judge rule lives in `judge.go`: a round after review whose commit
  touches `judge_paths` raises a hold, decided from the commit's paths through
  `internal/vcs` and never from the fixer's reply, and review reports every
  judge path the change touched, unfiltered by the pushed ignore list. A body
  is handed a `StageDeps` at construction, which carries the adapters that do
  not vary with the run; a fact that does vary is a declared state key in
  `internal/pipeline` instead, because one `All` serves every run of a service.
  Lifetime decides which, and `deps.go` has that argument and the P4 reason
  `Agent` is a `StageAgent`. A fix body is handed a `FixDeps` instead, and the
  two are separate types for the same reason: it carries a route to the run's
  fixer, and a stage body that could reach one could fix what it is about to
  report on. Do not merge them.
  Three things about the intent stage generalize. PRD section 5's "this stage never blocks a
  run" is owed by the implementation and not by `internal/pipeline`, which
  refuses to enforce it structurally because a stage that could not hold would
  have to drop an ask finding; every finding it reports is a note, and the test
  runs every path it has and is itself checked against a report that blocks, so
  the assertion cannot pass vacuously. A body landing can move where a run
  stops, so a test may not name the stage it expects a hold at: the helpers in
  `internal/cli` and `internal/service` derive a run's stops from
  `stages.Holding`, which owns which stages hold under a configuration -
  having a body and holding are different questions, and the test stage holds
  with a body wherever no test command is configured - and `internal/journey`
  names both its body-less stages and the stages a run stops at in
  declarations checked against the build, so landing a body means writing it
  down there too. A body refusing what its run cannot give it cannot be
  walked past either, because it fails rather than holding, so
  `internal/service`'s answer-to-the-end test skips such a stage for the one
  run instead of the body softening: the pull request stage fails on a record
  naming no repository on the code host. The review stage is skipped there on
  different terms: its body launches the resolved agent over the run's
  isolated copy, and the tests that walk a run script that agent to answer a
  clean review, so a walk that takes the stage rests on what the script says
  rather than on the configuration's holds.
  It is also where a body reads another stage's record:
  `pipeline.StageResultKeys` declares the keys and `pipeline.ReadStageResult`
  decodes them, so `internal/pipeline` stays the one owner of the report
  encoding. And a stage implements the part of its PRD section the
  phase list has reached and ships no seam for the rest: the intent stage reads
  supplied intent and does not infer, because inference is deferred, and what
  that deferred work inherits is a note in the package documentation rather
  than an unwired interface. Exported surface answers to a consumer: one that
  exists today, or specified work whose absence would otherwise have each of
  several consumers re-cut the same file. A seam is the second case, and it
  names in its own doc which consumers it answers to, so the claim is checkable
  rather than asserted. Surface added because deferred work might plug into it
  answers to nobody - it grows whether or not that work arrives and nothing
  breaks if it never does - and it is dropped in review.
- `internal/fixture` builds the adversarial subject repository the end-to-end
  harness validates against, and records beside each planted condition what it
  must produce, down to the substrings the message has to carry. It is one of
  the two documented exceptions to the rule above that `internal/vcs` is the
  only package invoking git, `internal/journey` being the other: a fixture built
  with the code under validation cannot show that code wrong, so it runs git
  directly the way `internal/vcs`'s own test helpers do. Two rules there are
  load-bearing. A condition is reached by the path the product takes to it, so
  the two states that only exist partway through a run are deferred to
  `AdvanceRemoteOutOfBand` and `CopyGatedWorkingCopy` rather than assembled.
  And "nothing executed" is checked, not assumed: every planted executable
  appends to the scenario's tripwire file, and the package's own
  tests run one to prove the tripwire fires. Nothing here decides how a harness
  drives a condition; what it ran into is in `OpenQuestions`. Build it with
  `scripts/build-fixture.sh DIR`, and read `doc.go` first.
- `internal/principles` fails the build when a principle the PRD lists has no
  test claiming it, and claiming is all a citation is: it says a test says it
  checks that principle, never that it does. Nothing built on it may describe
  it as coverage. A test cites with `principles.Cite(t, principles.P6)` inside
  its own body and only that counts, so a comment naming a principle is not a
  citation, which is the rule's point rather than a limitation of it. The PRD
  owns the list, the constants are pinned to it in both directions, and where
  nothing claims a principle the gap is a row in `unclaimed.go` rather than an
  absence nobody can see. Read its `doc.go` before changing the rule, and for
  the residual gaps: a citing test may check nothing, and the scan reads source
  rather than a built test binary.
- `internal/outcomes` is that package's sibling and not a generalization of it:
  it fails the build when the PRD's outcome row and `internal/machine`'s set
  stop declaring the same six values, in the same order, in the same two
  groups. The row is the owner in the mechanism and not only in the prose: the
  whole extraction rule is a `data-outcome` attribute on a `<code>` inside
  `<tr id="outcome-set">`, and a marker anywhere else in the document is a
  refusal rather than a seventh outcome. Where the row lives is the separate
  question: it has to sit inside `<section id="surfaces">`, named by that id
  because a section inserted earlier renumbers the section and not the id. A
  set the rule cannot read is a refusal too, never a short list. Agreement is
  not correctness, and the gaps are worth knowing before relying on it: nothing
  here reads what the row says an outcome *means*, nothing pins
  `machine.OutcomeOf`'s translation from a run's state, a set both sides get
  wrong agrees, and a field or a second next action added to the answer passes.
  Read its `doc.go` before changing what fires.
- `internal/journey` is the end-to-end harness, and it is organized by PRD
  principle rather than by feature: PRD section 13 turns each principle into a
  test and that is its structure. It drives the real binary as a process,
  against `internal/fixture`'s subject, with the binary taken from
  `ASSISTANT_BINARY` when that is set so a shipped artifact can be validated
  rather than a checkout. It is the second documented exception to the rule
  that `internal/vcs` is the only package invoking git: `Git` and `GitWith`
  build git command lines and run them, under the isolation the fixture built
  its subject with, for the reason `internal/fixture` takes the same exception.
  A harness that confirmed the product's own git operation by asking the
  package under validation would be reporting that package agreeing with
  itself. The rule is not weakened by either: it has one owner and two named
  exceptions, and a third is a finding rather than a precedent. Two things
  there are mechanism rather than rule. A check is a `Check[O]`: a list of
  `Clause[O]` over a typed observation plus
  the counterfeit observations it must reject, `Verify` runs both halves on
  every invocation, and a check naming no counterfeit is refused, as is one
  carrying a clause no counterfeit reaches, so neither a check nor a part of
  one that nobody has shown can fail may ship. The predicate is a list because
  a check answered as a whole hides the assertion that discriminates nothing.
  A counterfeit is a mutation of the real observation rather than one written
  from nothing, for the reason `internal/agents/standin` gives about fakes.
  Starting from a real observation is all `Verify` enforces, since `Break` is
  an unconstrained `func(O) O`; keeping the mutation inside a shape the product
  could have produced is a rule the writer keeps, and the residual gap is that
  nothing there can tell a counterfeit that broke it from one that did not. A
  clause asserting an absence declares itself one and carries `Possible`,
  because a mutation of the model cannot say whether the thing could have been
  there in the subject, and that is the one gap counterfeits never close. And
  three tables account for everything in both directions: `Coverage` against
  the principles its own tests cite, `Drives` against the planted catalog, and
  `Settlements` against the questions `internal/fixture` left open, which this
  package owns and answers rather than editing that one. Read its `README.md`
  first: green there says the machinery behaves on inputs we chose and says
  nothing about review quality, and every row carries whether it was reached
  through the binary or
  through the package that owns the mechanism, because a run there still
  reaches no code host, and review and push only in the three walks that reach the push stage: `internal/service` builds every
  run an isolated copy and resolves its repository configuration now, so what
  keeps the reviewing agent out is the walks themselves - every one but those three asks to
  skip the review stage, whose body would launch the agent the harness
  scripts to answer nothing - while the pull request body fails because the
  run's record names no repository on the code host, so every walk skips that
  stage too, and the test body holds wherever the resolved configuration
  names no command. The one exception is the P7 branch walk, whose trusted
  document does name commands: its test stage executes the trusted command,
  the failure takes a fix round through the scripted stand-in, and the
  convergence bound parks the run. It
  takes both of the platform guards
  `internal/cli` and `internal/service` carry, on their terms: a check that
  drives a run skips where `internal/ipc` reads no local socket peer
  credentials, and a check whose service did not come up skips where there is
  no local socket transport to serve the protocol over, which is the wider of
  the two because a check needs a service before it can drive anything. The
  second is taken wherever a service was expected to come up and never wherever
  one is started, because a check whose subject is a service it arranged to
  fail would otherwise be skipped on the arranged failure and establish nothing
  there. Either skip is
  recorded as a limit in that `README.md` and in the `Coverage` note of every
  principle it takes down with it, because a skipped check that reads as a pass
  is what this package exists to refuse. Neither limit is recorded as a list of
  the checks it takes down: an enumeration goes stale the next time one is
  added, so both say what a green run there establishes instead.

## Tests

- Test through an executable interface or a typed model of the behavior. A test
  whose only evidence is that the source text matches a pattern proves nothing
  and will be rejected in review.
- Construction-time rules get construction-time tests: an unbounded cycle must
  fail to build, and an undeclared shared state key must fail to build.
- Concurrency rules need the race detector, so `make test` runs it and CI runs
  it on every platform.
- A test that establishes a PRD principle cites it with `principles.Cite` as
  well as saying so in its doc comment. The comment is what a reader learns
  from; the call is what `internal/principles` can count, and a principle no
  test cites fails `make check`.
- A test that needs a coding agent scripts `internal/agents/standin` rather
  than writing its own `Runner`. A double that states typed values directly is
  how this repository shipped a dead guard once; one that answers over the wire
  and is read by the real adapter cannot repeat it.
- A guard that a caller *cannot reach* something is asked of the type graph,
  never of one assertion. `any(x).(T)` answers whether `x` is a `T`, not whether
  a caller can obtain one, and that gap is where the guarantee goes:
  `agents.StageAgent` given a `Runner()` accessor hands a body a live `Runner`
  while every such assertion stays green and P4 is gone.
  `internal/agents/route` walks what a caller outside the package can reach
  instead, exported fields and the results of exported methods, transitively,
  so it catches a route nobody enumerated. It is a package rather than a helper
  in one test because two packages ask it and two copies of a rule drift. Test
  P4 that way.
- A guard needs a positive control or it can pass by looking at nothing. That
  walk is also run against `agents.Resolution`, which really does expose a
  `Runner`, so a walk that stopped inspecting anything fails instead of
  reporting a guarantee it no longer checks.

## Commits and pull requests

- Conventional commits: `type(scope): description`. Use `feat` and `fix` for
  anything user-facing.
- Never add an agent as a commit co-author.
- Say what changed and why. The reviewer was not present for the work.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
