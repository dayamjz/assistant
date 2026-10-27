---
paths: ["internal/tasks/**"]
---

# internal/tasks

- PRD section 8's tasks module as far as this build takes it: intake,
  queueing, the P11 assertion, the P12 cleanup decision and promotion, with
  dispatch and the copy in `internal/service` and the launch in
  `internal/session`.
- Intake and promotion record a delivery task's delivery posture from
  `Deps.Posture`, the operator's layer tightened by the project's trusted copy
  as `internal/service` resolves them (the key is `config.TrustGlobalLoosens`:
  a repository file may tighten it and a looser one is a
  `*config.LoosenError`). They refuse with `ErrPostureUnreadable` when that
  read fails rather than assuming one, and let a request tighten it and never
  loosen it (`*PostureError`); a floor (`Request.DeliveryFloor`, a carry
  on's) records the stricter of itself and the project's, decided in the
  one read, and is never a refusal.
- A local task's cleanup proof is the content check against the working
  copy's default branch and nothing else.
- The remote-branch and merged-pull-request proofs follow a commit the gate
  rebased only through a `store.RunRebase` of a run the snapshot attributes
  to the task, never by searching for a commit with the same content, and
  only when integrating the copy's commit into the record's published head
  would change nothing; `proof.go` owns it.
- This package's own P11 citation claims the assertion refuses and nothing
  about launch; `internal/service`'s claims the launch edge.
- It is P12's decision and the only writer of the cleanup record. An
  inconclusive proof is a `ProofAttempt` on the `*CleanupRefusal`, never a
  proof, and no remedy names a flag.
- Every repository the `Cleaner` opens is read without replacement
  references or a graft file (`vcs.WithoutReplaceObjects`, set in
  `NewCleaner`), so neither a cleanup proof nor a discard's loss reads a
  commit's parents as anything but what its object records; a copy that
  has either, or is shallow or a partial clone, is refused by
  `supportedLayout` before any loss reads it, a land's or a discard
  preview's.
- It is also the one writer of the kept work record: `Keep` reads what a
  kept copy holds from the copy (the entry's identity), reads what of it
  the default branch, fresh and fetched by identifier, lacks (`lacking` in
  `kept.go`; a stash is uncommitted state and not compared; an unreadable
  branch is recorded as unestablished, never guessed, and established by a
  later `Keep` of the unchanged copy while the entry is open), reads the
  entry already standing for the copy state before deciding anything, and
  records the entry or `NothingKept`, which sees what `vcs.Inventory` sees
  and so not an ignored file; an entry established with nothing lacking
  stays open until answered or until a cleanup removes the copy on a
  proof. `Unheld` is the one reading of what of its entry's holds a copy
  no longer holds, by what the copy names now (`copyNames`, HEAD, root
  references and reflog values included, and the trees and blobs a named
  commit's history records), which `internal/service`'s carry on asks
  before it claims, moves or adopts the copy and last right before it
  marks, and `Queue.Promote` asks for
  each entry its flip answers (`*KeptWorkUnheldError`). And
  `CleanupRefusal.Unlanded` (with `KeptOf`) is the one statement of which
  refusals are a copy kept for its work; `kept_test.go` places every reason.
- `kept_discard.go` is the copy's half of PRD section 6's discard as far
  as this build takes it, the preview (removal on a confirmed discard is
  deferred to AGE-486, on the branch feat/age-407-discard), and `Loss` is
  also what `internal/service`'s land answer decides from: `Loss`
  reads what removing the copy would lose: the commits that anything the
  removal destroys names reaches (HEAD, a reference, a root reference such
  as ORIG_HEAD, a reflog entry by either of its values, `vcs.ReflogValues`;
  for a run's worktree only its own and their logs' values,
  `vcs.ReflogValuesOf`, and the stash entries it made, `ownStash`, never
  the gate's shared references or their reflogs, which the removal leaves
  and which hold nothing either) and
  nothing held reaches, the default branch and what the upstream holds
  now: every object the copy names that it advertises, by identity, and
  the commit its branch of the same name as one of the copy's branches or
  remote-tracking references stands on, fetched by identifier
  (`upstreamHeld`; a remote-tracking reference holds nothing of itself),
  and every other object such a name or value
  records that is not itself held, read from the copy now (`lostFrom`),
  every path whose content or deletion nothing else holds (`kept_lost.go`,
  decided from the copy against HEAD's tree, the index and what git writes
  into a new repository, a file by the blob git would store for it,
  `vcs.StoredObjects`, ignored files and deletions included) and the
  inventory's three indicators, with a digest over exactly what is shown
  and nothing else (`seal`: HEAD, the commits and objects lost, each path
  lost with what it holds, decided from the copy and never from what git
  reports changed, and the indicators; the header names what it does not
  cover), refusing an unestablished entry. Nothing in this package
  removes a copy on a discard answer. A run's record in the gate is taken
  from the gate's own registry (`vcs.WorktreeRecords`, read from the gate
  and held to git's listing) and never by following the copy's `.git`,
  the copy refused when its files disagree with it (`copyRecord`). `Loss`
  supports one repository layout and refuses every other before reading
  anything (`supportedLayout` in `kept_layout.go`, `ErrLayoutUnsupported`):
  an unusual layout is refused, never handled.
  Its first step is `Cleaner.VerifyKeptCopy`, the one call into
  `internal/safety`'s verification that a kept copy is the one its registry
  recorded, which carry on in `internal/service` asks too, and promotion
  through the same body (`verifyKeptCopy`) before its flip answers the
  copy's entries and again before it writes into the copy.
- `kept_journal.go` is a confirmed discard's own directory beside its copy
  (`home.Home.Discarding`) and its journal, written through `writeWhole`
  before anything is set aside: `beginDiscard` makes it, refusing one that
  stands (`ErrAsideStands`), and `standingDiscard` reads one a discard
  left, clearing a directory that holds only a partial first journal and
  refusing, never removing, anything else. No discard answer calls it
  yet; the set-aside that does follows in AGE-486's stack.
- Intake writes the instructions file whole and never through a link,
  `writeWhole` (an exclusively created partial of a fresh name, then a
  rename; a path standing as a link is `*InstructionsLinkError`), so a file
  under the name is never partial, and `CompleteIntake` is intake's second
  step for a row a stop left without its file, removing stale partials as
  names; `InterruptAfterRecordForTest` is the seam between the two.

Read `doc.go` before changing the floor or the clear condition, and for the
residual gaps; read `promote.go`'s opening comment and `doc.go` before
changing promotion's order, and for its gaps.
