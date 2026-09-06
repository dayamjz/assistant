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
// so it holds for a decision and never enters that loop. ActionNote is
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
//     Held and Report.Held select the other way, on ActionAsk.
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
// # A review report is bound to what the reviewer read
//
// One stage answers for more than the above. PRD section 5, "What a review
// report has to carry", makes a review report carry the revision it read and
// the set of paths it actually read, and binds its findings to them.
// ParseReviewReport is that rule and Demand.Guidance is what the reviewer is
// told about it; no other stage answers to any of it, and a report read
// through ParseReport is untouched by it.
//
// The part worth knowing before reading either is why it is a comparison
// rather than a field. A reviewer satisfies a rule that merely asks for an
// evidence set by naming every changed file and then reading nothing but the
// diff, and the run can derive that list without asking, so on its own it
// proves nothing. So the evidence set and the paths the change touched are
// reported separately and compared, and a finding reaching past the change
// has to cite the code it reaches to and is refused unless the evidence set
// names it. Declaring the diff wholesale therefore forfeits every finding
// that reaches past the diff rather than buying a free pass, and an evidence
// set equal to the touched paths stays a permitted answer, reported rather
// than invisible.
//
// Two ordering facts hold that together, and both are structural here rather
// than remembered by a caller:
//
//   - The binding runs before Normalize, on the action the reviewer stated.
//     That is what keeps the demotion of an unsupported finding from reaching
//     P3: an action that was missing, empty, or unreadable is still the ask P3
//     makes of it, because Action.Stated is false for all three, and only an
//     action this package recognized is demoted to a note.
//   - The binding is reachable only through ParseReviewReport. There is no
//     exported way to bind a report that has been through Normalize, through
//     storage, or through a caller's own struct literal, in each of which the
//     stated action is already gone and the demotion would take a hold P3
//     fixed and turn it into a note.
//
// The rule is added to what a report already answers for and takes nothing
// away from it. A report ParseReport refuses is refused by ParseReviewReport
// with the same defects named, because the reviewer's own report is put to
// Validate before the binding rewrites any finding: the binding writes a note
// over a refused finding and appends to a demoted one, and a note it wrote
// must not stand in for a description the reviewer never wrote. So the review
// path refuses everything the ParseReport path refuses, and its extra rule can
// only refuse more.
//
// What the binding is not is a check that the reviewer read anything. The
// evidence set is the reviewer's claim about itself, and this package resolves
// no path against a filesystem: a reviewer that declares a path it never
// opened is believed, and one that declares nothing and reports nothing passes
// with an evidence note saying it declared nothing. What the binding buys is
// that a claim is refused unless the reviewer's own account of what it read
// supports it, and that the account is on the record to be read across runs.
//
// The other limit is on how far the discriminator reaches, and it is PRD
// section 5's own design rather than a shortfall against it: the PRD puts the
// duty to cite on the reviewer, so citing is voluntary. The wholesale
// declaration therefore forfeits only a finding that volunteers its reach,
// through a Location naming a path or through Cites. A reviewer that declares
// exactly the touched paths and reports a fix-eligible finding located inside
// the change, whose reasoning rests on a caller it never read and never names,
// is refused nothing, reports an empty Beyond, and is indistinguishable here
// from one that read the diff and reasoned about nothing else. Refusing it
// would mean judging what a finding's reasoning rests on, which is not in the
// text this package reads, and would hold reviewers to a stricter rule than
// the PRD states.
//
// # What this package does not promise
//
// The object extraction in ParseReport is a brace scan that tracks JSON string
// literals, not a recovery parser. Three ways the report an agent meant to
// write can fail to be a candidate at all are worth stating rather than
// discovering:
//
//   - If the agent's final object is truncated it is not balanced, so it is
//     not a candidate.
//   - Prose holding an unmatched brace before the object absorbs it into a
//     span that either never closes, and so is never offered as a candidate at
//     all, or closes around text that is not valid JSON.
//   - An object mangled badly enough to break JSON syntax exposes no keys, so
//     nothing tells it apart from prose that happened to balance its braces.
//
// All three have the same consequence, and it is the one thing this package
// cannot fail closed on. When the text holds no earlier object that validates,
// the outcome is a refusal. When it does hold one, such as a schema example
// quoted in the prose, that object is returned with no error and a caller reads
// it as the stage's report. Nothing in the text says which object the agent
// meant. Together these are the only way ParseReport returns a report the agent
// did not write, and ParseReport states the same limit where a caller meets it.
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
