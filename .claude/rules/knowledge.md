---
paths: ["internal/knowledge/**"]
---

# internal/knowledge

- The store keeps the revision a launch was told (`SetAttemptKnowledge`,
  migration 45) and never the text.

Read `doc.go` for where the bytes are read from, the bound, the refusals and
the gaps.
