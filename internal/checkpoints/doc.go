// Package checkpoints makes a run's position durable. It is the default
// implementation of the durability layer PRD section 7 puts behind
// [graph.CheckpointStore]: the four operations internal/graph declares,
// answered out of the local embedded database internal/store owns, so a run
// survives a restart as a position and not only as a record.
//
// # Why the adapter is its own package
//
// internal/store is the only package that opens the database and writes SQL,
// and internal/graph is a pure core with no substrate in it, so the layer
// between them belongs to neither. internal/store's own comment says it does
// not interpret what it stores, because internal/graph owns the checkpoint
// vocabulary and a store that also understood it would be a second owner of
// the same contract. This package is where that understanding lives: it
// serializes a checkpoint and reads one back, and internal/store holds the
// bytes, orders them, and assigns the sequence.
//
// The record it holds them in is new alongside internal/store's Checkpoint
// rather than an extension of it, because that record is one row per run
// rewritten in place with a revision, and two of the four operations here are
// a run's whole history and a fork from a point in it, which such a row cannot
// answer. PRD section 8 names this history as the one record of where a run
// stands, and lists no row-per-run checkpoint beside it.
//
// # Where the anchor is decided
//
// P6 lives at this boundary: an update is anchored to what the caller actually
// observed, never to a tip read a moment before writing, which always matches
// and therefore protects nothing.
//
// Write hands the anchor to store.AppendGraphCheckpoint, which decides it in
// the same transaction on the same connection that assigns the sequence. That
// is the whole of the decision, and it is why there is no read of the run's tip
// in this package before a write: a check made here and a write made there
// could interleave, and the interleaving is exactly the defect the anchor
// exists to prevent. What this package does with the answer is translate it,
// so a caller sees graph.ErrRunExists and graph.ErrStaleAnchor rather than
// this substrate's names for them.
//
// The anchor is never stored. It reaches the database as two arguments to one
// accessor and no column holds either, which is what keeps a later read from
// handing back a checkpoint that claims to know what it was decided against.
//
// # It does not diverge from the in-memory store
//
// Honouring the anchor is required of every implementation of the interface,
// not a description of any one of them, so this package is written to answer
// what graph.MemoryStore answers, in the same wording, for every condition the
// interface names. The tests are the part that holds it: the behavioural suite
// in store_test.go runs against both, so a divergence fails the suite rather
// than waiting for a caller to find it.
//
// Two differences remain, and neither is a condition the interface names. Both
// are held where they are by a test rather than left to be found:
//
// A checkpoint that can neither be encoded nor anchored is refused by both and
// writes nothing in either, but they report different halves of it. The payload
// has to exist before the accessor that decides the anchor is called, so this
// package reaches the encoding failure first where graph.MemoryStore checks the
// anchor before it encodes. Reaching the anchor sooner would mean deciding it
// outside the write, which is the one thing this boundary may not do.
//
// A run named only whitespace is held by graph.MemoryStore, which refuses only
// the empty name, and refused by internal/store, which refuses a blank name
// everywhere it takes one. Nothing here relaxes that, so such a run has no
// durable history. graph.Executor admits the name, so it is reachable rather
// than theoretical.
//
// # Serialization
//
// A checkpoint is stored as its own JSON shape, which PRD section 7 requires to
// be data only: graph.Checkpoint's UnmarshalJSON is the decoder internal/graph
// publishes, and it refuses an unknown field and an unrecognized enumeration.
// A checkpoint restored here is still validated against the graph it claims to
// belong to before a run resumes from it, which is graph.Executor's own read
// path and not this package's.
//
// # Residual gaps
//
// internal/graph's encoder is unexported, so what this package writes is
// json.Marshal over the exported type rather than that encoder itself. The two
// cannot silently disagree about which fields exist, because the conversion in
// graph's own decoder holds the wire struct and Checkpoint to identical field
// sets at compile time, and a field whose wire name drifted would make a
// checkpoint written here fail to decode rather than decode into a different
// checkpoint. What that leaves is when it would be found: on the way back in,
// at a resume, rather than at the write. TestACheckpointReadsBackAsItWasWritten
// is what moves that to make check, and it runs against both implementations.
//
// Fork reads its source outside the transaction that claims its destination.
// The contract asks for the destination claim to be atomic with the copy, and
// it is; the source read is separate and answers the same bytes the copy
// writes, because nothing revises an entry once appended and a fork copies only
// the prefix up to its point. A source still being written may not have reached
// that point yet, and the fork is refused with graph.ErrNoSuchCheckpoint, which
// is what the in-memory store answers at the same moment.
//
// A run's position has one owner, and it is this history. store.WriteCheckpoint
// is still there and still writes the one-row record PRD section 8 does not
// name, and nothing stops a caller from writing both for one run and ending up
// with two answers. Nothing in this repository writes or reads it outside
// internal/store's own tests, which is an absence rather than a mechanism, and
// dropping the table is a migration that has not been made.
//
// Nothing prunes. A run's history grows by a row for every node it executes and
// one more for every segment that claims it, and the three bounds
// internal/graph owns bound the run rather than the table.
//
// What survives a restart is the position, not the work in flight. A process
// that died partway through a node left no checkpoint for it, so the node runs
// again when something resumes the run; that something is still a caller, and
// nothing here notices that a process went away.
package checkpoints
