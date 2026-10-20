---
paths: ["internal/checkpoints/**"]
---

# internal/checkpoints

- The anchor is never decided here: `Write` hands it to
  `store.AppendGraphCheckpoint`, which decides it in the transaction that
  assigns the sequence.
- The behavioural suite in `store_test.go` runs against this implementation and
  `graph.MemoryStore` both, and a behaviour checked against only one is not
  checked.

Read `doc.go` before changing any of that, and for the residual gaps.
