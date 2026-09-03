# internal/gate: state at handover

This package is substantially correct and does not merge as it stands. It is
the starting point for the ownership-index task rather than work to redo: nine
review rounds are recorded in the commits on this branch, and the findings they
produced are worth as much as the code.

`make check` is green. Every guard described below is mutation-checked, meaning
it was removed once and a test was watched to fail.

Read `doc.go` first for what the package guarantees. This file is only what is
still open, and it is deliberately separate: `doc.go` states contracts, and an
open defect is not a contract.

## Open safety items

Four, in the order a reader should care about them. The first is the one to fix
before this package is used for anything real.

### 1. The assistant remote is treated as evidence this package did not write

`ownGateBinding`'s recordless branch returns `!remoteAgrees`, weighing the
`assistant` remote as a second fact alongside the path hash. It is not a second
fact when this package wrote the remote itself. This is the same
self-manufactured-evidence shape that was closed on the record branch in the
last fix round, still open on the recordless one, and it ends with a working
copy able to take over and then delete a gate whose history belongs to another.

The record branch shows the shape of the fix: a fact is only a second fact when
this initialization did not produce it.

### 2. The no-open-gate invariant is held by `Initialize` only

`doc.go` states it for the package: a gate never accepts a push that admission
has not seen. `Initialize` holds it by construction, noting each gate it learns
about and sealing them in a deferred sweep on the way out, so a future early
return is covered without anyone remembering to add a call. `Remove` has no
equivalent sweep.

This is an incomplete guarantee rather than a regression, because `Remove`
cannot itself create a gate with no admission hook. It matters because the
invariant is written without qualification and a reader will expect both
operations to hold it.

### 3. The seal writes on refusal paths documented as writing nothing

`ErrInvalidSpec` says "The refusal happens before anything is created" and
`ErrGateClaimed` says "Nothing has been created, written, or deleted when it is
returned." The deferred sweep can create `<repo>/hooks` and write a
`pre-receive` before either is returned, so both sentences are now false on the
`Initialize` side.

The behavior is intended; the contracts were not updated with it. Either is
defensible to change, but they must agree, and per the working agreement a doc
comment may not promise more than the mechanism enforces.

### 4. A failed seal discards a successful initialization

The deferred sweep turns a completed `Initialize` into an error when sealing an
unrelated gate fails. By that point the gate exists, its hooks are installed,
its record is written, and the working copy's remote has been repointed. The
sweep also holds the gate the working copy previously pointed at, which on the
copy path belongs to somebody else, so another working copy's unreadable hooks
directory can fail an initialization that fully succeeded.

## Deferred, not resolved

Two findings were reviewed, accepted as real, and deliberately left. They are
not closed and their absence from the code is not evidence that they were
judged unimportant.

- **`ErrGateBindingInferred` names a remedy that does not deliver.** The
  message says initializing after detaching gives this working copy a gate of
  its own. For the path-hash shape of an inferred binding the asker's own path
  hashes to that same gate, so the initialization lands back on it. With the
  mark now correctly persisting, following that remedy leads to a removal that
  refuses again, which is the dead end `doc.go` forbids.
- **Two `ErrMalformedRecord` producers name no action**, in `alreadyNamedGate`
  and `ownGateBinding`, while `errors.go` and `doc.go` both promise every one
  of them names the step that succeeds. Only the messages built in `readRecord`
  carry it.

## The finding this branch is most worth reading for

`doc.go` carries a rule this package wrote about itself two rounds before the
end: whatever a guard or a message owes an operator on one side of an
operation, its sibling owes too, and the side that is easy to forget is usually
the one where being wrong cannot be undone.

It has now been violated five times, including twice after it was written down:

1. Ownership checked on adoption, not on removal.
2. Hooks guarded on creation, not on repair.
3. The cost of an action stated on the destructive side, not the constructive.
4. `ErrNotAGate` naming a step in two producers, not the third.
5. The no-open-gate invariant held by `Initialize`, not by `Remove`.

A rule violated twice after being documented is not a rule anyone is failing to
read. It is a rule with no mechanism behind it, and writing it more clearly a
sixth time will not change that. Two mechanisms would, and this repository has
already solved this class of problem twice with types: `internal/agents` puts
P4 in a type split rather than in a rule callers follow, and `internal/safety`
makes an anchor's provenance a constructor rather than a checked value.

**A typed gate handle every operation must obtain.** Today `Initialize` and
`Remove` each take a path and each separately resolve the gate, decide
ownership, and (in one case) seal. Instead, one unexported seam should resolve
the repository, apply the ownership decision, and guarantee the seal, returning
a handle that carries the verified record and the ownership verdict. The
operations take that handle rather than a path. Every obligation is then
discharged before any operation begins, and a third operation added later
cannot skip them, because it cannot obtain a gate without going through the
seam. Items 1, 2 and 5 above are all instances of an obligation living at a
call site instead of in the thing every caller must pass through.

**A refusal constructor that requires a remedy.** Items 3 and 4 are the same
defect in the operator-facing half: a refusal was built without the action it
owes the reader. Make refusals go through one constructor taking the state and
a required remedy, so a refusal with no action is unrepresentable rather than
merely discouraged. That also gives the remedy text one owner, which is where
the "does this remedy actually complete" test belongs.

These are complementary to the ownership index, not an alternative to it. The
index makes the ownership question answerable directly instead of inferred from
one probe of one path, which is what `Adopted` exists to paper over. The seam
makes every operation ask it. The index without the seam still lets the next
operation forget, and the seam without the index still has to infer.

## On `Adopted`

The assessment reached twice during review, and unchanged at the end: it is the
wrong long-term shape. It is a durable fact about history standing in for a
question about the present, namely whether another working copy is still bound
to this gate. Three defects across the final rounds were interactions of that
mismatch rather than of the underlying risk, and the field's doc comment grew
longer than the mechanism it describes.

The root cause is that a gate is filed under a hash of a path, so this package
can never enumerate who points at a gate. Every ownership question is answered
from one record plus one probe of one path. An ownership index in the assistant
home, mapping working path to gate, owned by `internal/store`, makes the real
question answerable and `Adopted` unnecessary.
