// Package findings is the vocabulary every pipeline stage speaks. Its contract
// is PRD section 5, "Findings and their actions"; this comment restates the
// parts that are load-bearing so a reader of the code does not have to open the
// PRD to know what must stay true.
//
// A stage returns a Report: a summary, the findings, an optional risk level
// with its rationale, what was tested, and any evidence artifacts. A Finding
// carries a severity, a location, a description, and an Action.
//
// # The action decides who resolves the finding
//
// ActionFix is objectively wrong and mechanically fixable, so it is eligible
// for the automatic fix loop. ActionAsk touches the user's intent or judgment,
// so it parks for a decision and never enters that loop. ActionNote is
// informational and blocks nothing; a report whose findings are all notes is
// approved as it stands.
//
// # The fail-closed default, which is P3
//
// A finding that arrives with no action, an empty action, an action this
// package does not recognize, or an action that was not even a string becomes
// ActionAsk. That is defined behavior, not an error path to log and move past.
// It holds in two independent places, and both are worth knowing about:
//
//   - Normalize rewrites any unrecognized action to ActionAsk, so everything
//     downstream of ParseReport sees one of the three recognized actions.
//   - Fix eligibility is defined positively as equality with ActionFix, in
//     Finding.FixEligible, and nothing else. A Finding that never went through
//     Normalize, or one a caller built by hand with an action nobody
//     recognizes, is therefore still not fix-eligible. Fixable and
//     Report.Fixable are the only selectors that feed the fix loop, and both
//     are built on that one predicate, so neither can return an ask or a note.
//     Parked and Report.Parked select the other way, on ActionAsk.
//
// # Agent output is untrusted input
//
// ParseReport takes whatever an agent printed and either returns a normalized,
// validated Report or refuses. It tolerates the shapes agents actually
// produce: a bare JSON object, a fenced block, and a final object after prose.
// It also tolerates fields this package does not recognize, because a stage
// that added a field to its own prompt must not cause an otherwise valid set of
// findings to be thrown away.
//
// It does not tolerate ambiguity about what it read. A report with no summary
// is refused rather than read as a clean pass, since a truncated output that
// decodes to an empty object is exactly the case where a silent "no findings"
// is most dangerous. A risk word this package does not recognize is refused
// rather than mapped: unlike an action, risk has no fail-closed value that does
// not either understate the risk or manufacture alarm, so the person sees the
// raw output instead.
//
// An unreadable severity is different again, and this is this package's own
// rule rather than one PRD section 5 states. A missing or unrecognized
// severity becomes SeverityWarning. Severity orders and colors a list; it never
// decides who resolves a finding, so defaulting it cannot make anything
// fix-eligible, and refusing a whole set over a label that decides nothing
// would discard findings the person needs to see. Warning rather than info,
// because an unreadable severity must not read as harmless.
//
// # What this package does not promise
//
// The object extraction in ParseReport is a brace scan that tracks JSON string
// literals, not a recovery parser. Two consequences are worth stating rather
// than discovering:
//
//   - If the agent's final object is truncated it is not balanced, so it is
//     not a candidate, and an earlier complete object in the same text can be
//     selected instead. Nothing in the text says which object the agent meant.
//   - Prose holding an unmatched brace before the object can absorb it into a
//     span that does not decode. The result is a refusal, not a wrong answer.
//
// Identifier assignment is deterministic: the same input yields the same
// identifiers, and an identifier a finding already carries is never rewritten.
// Derived identifiers avoid identifiers already present in the same set, but
// the search for a free one is bounded at 64 attempts. Past that bound the
// search stops and returns its last candidate with the attempt count appended,
// without checking that against the identifiers already taken, so two findings
// that both reach the fallback receive the same identifier. That needs no
// digest collision: a set holding 66 findings identical in every hashed field
// reaches it by counting. Report.Validate refuses a set with duplicate
// identifiers, which is where that case surfaces.
//
// This package is pure. It performs no I/O, runs no agent, and reads no file.
// An Evidence path is a string it carries and never resolves.
package findings
