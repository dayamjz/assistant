# Usage and cost reporting rule

Applies to README examples, docs, release notes, PR descriptions, dashboards, and any generated report that describes Assistant usage or cost.

1. **Name the measurement.** Describe `assistant runs --usage` as *recorded agent invocation usage* or *token usage*, not as total financial cost. When quoting product output verbatim, preserve its exact wording but explain what it covers.
2. **State scope next to the numbers.** As implemented, usage reports cover validation-stage agent calls (`stage-agent-calls`). They exclude task-worker turns and monetary price (`worker-turns`, `price`). Do not claim end-to-end, whole-fleet, coordinator, or worker billing coverage from these totals.
3. **Use reported values faithfully.** Show input, output, cache read, and cache creation separately; distinguish unavailable/unreported counts from measured zero. Never extrapolate missing counts.
4. **Keep budgets distinct.** `run_usage_budget.output_tokens` applies to one run's reported output tokens; `usage_ceiling` applies to configured counters over a window. Neither is a dollar spending limit or a complete accounting of worker activity. Say whether a bound parks a run or holds starts.
5. **Do not invent dollars.** No dollar figures, token-to-dollar conversion, ROI, savings, or bill estimates presented as product telemetry unless a verified pricing source, exact model, billing plan, cache rates, measured covered usage, calculation and exclusions are explicitly supplied. If billing is subscription-based or otherwise not reducible to token rates, state that limitation.
6. **Label provenance.** Tag examples `Recorded run` (with command and capture context) or `Illustrative scenario`. Never mix synthetic data into a recorded total. A scenario may demonstrate roles and routing but must not invent token totals or financial costs without clearly stated assumptions.
7. **Optimize for readers.** Prefer a concise summary table with model, role, stage, calls, duration, token categories and relevant limit. Put long verbatim terminal output in a collapsible `<details>` block, and link to the operator guide for full semantics.
8. **Check source of truth.** Before updating wording, verify `internal/machine/usage.go` (`UsageCovers`, `UsageExcludes`), adapter reporting and `docs/operators.md`. If instrumentation expands, update this rule and the example together only after the implementation lands.

Review checklist: Is the example recorded or illustrative? Is coverage explicit? Are unreported counts distinguished from zero? Are limits labeled as tokens rather than currency? Does any financial figure have defensible pricing and billing scope? If not, correct it before publishing.
