---
paths: ["internal/outcomes/**"]
---

# internal/outcomes

- It fails the build when the PRD's outcome row and `internal/machine`'s set
  stop declaring the same values, in the same order, in the same two
  groups.
- The row is the owner in the mechanism and not only in the prose: the whole
  extraction rule is a `data-outcome` attribute on a `<code>` in the table
  row carrying `<a id="outcome-set">`, and a marker anywhere else in the
  document is a refusal rather than a further outcome.
- The row has to sit inside the section headed `## Surfaces`, named by that
  heading because a section inserted earlier renumbers the section and not
  the heading; `principles.PRDSection` owns where a section begins and ends.
- A set the rule cannot read is a refusal too, never a short list.
- Agreement is not correctness.

Read its `doc.go` before changing what fires, and for the residual gaps.
