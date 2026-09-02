// Package config is the configuration schema, its defaults, its merge, and its
// validation. Its contract is PRD section 10; this comment restates the parts
// that are load-bearing so a reader of the code does not have to open the PRD
// to know what must stay true.
//
// The package is pure. It reads no files, runs no git, and fetches nothing: it
// turns bytes plus a stated origin into a typed configuration, or into a
// refusal that names the key and the value it refused.
//
// # Two layers
//
// A global document lives in the operator's home and a repository document
// lives at the repository root. The repository layer overrides the global one
// key by key, never section by section: a repository that sets
// "fix_rounds.review" keeps the global values for the other fix round limits.
// Parse records which keys a document actually wrote, which is what makes that
// possible and what keeps an explicitly empty value distinct from an absent
// one. Setting "ignore_patterns" to an empty list overrides a global list;
// omitting the key inherits it.
//
// Absence has two shapes and they are not the same. A file that is not there
// is a valid layer that sets nothing, built with Absent. A file that exists and
// parses to nothing is a present layer that sets nothing, built by Parse from
// empty input. They resolve to the same configuration and Present tells them
// apart. A file that exists and cannot be read or parsed is neither: it is a
// refusal, and for a trusted document PRD section 10 requires the caller to
// stop the run before launching anything rather than fall back to defaults.
//
// # Trust is a property of the source, so it must be stated
//
// P7 says configuration that executes code or selects which agent process
// starts is read from the default branch at a freshly fetched commit, never
// from the pushed branch. Enforcing where the bytes came from needs git and
// belongs to the gate. What lives here is the classification, and it is
// mandatory rather than advisory: Parse requires an Origin, Absent requires an
// Origin, the zero Layer has none, and Resolve refuses a layer without one.
// Resolve also refuses either layer standing in the other's position, since
// the operator's own file and a repository file are different documents with
// different standing. A configuration therefore cannot be assembled without
// every field's trust class being decided.
//
// The limit of that is worth stating plainly: this package cannot verify that
// an origin was reported honestly. A caller that reads a pushed branch and
// labels it OriginTrusted gets a trusted layer, and one that labels its own
// repository file OriginGlobal gets a global layer. What the package
// guarantees is that nobody can avoid making the claim, and that a claim of
// OriginPushed or of a repository origin is then honored key by key.
//
// Each key has one class, assigned once in the key table:
//
//   - TrustPushed keys can neither execute anything nor weaken a check, so a
//     contributor may set them: the ignore list, the fix round limits, and the
//     commit message template.
//   - TrustCommands keys run shell or choose which process starts with the
//     operator's credentials: the three commands and the agent list. A pushed
//     branch may set one only when KeyAllowPushedCommands is set, and that key
//     is itself TrustTrusted, so a branch cannot enable itself.
//   - TrustTrusted keys shape what review or documentation demands or declare
//     a check unnecessary: the path-scoped review rules, document ownership,
//     instruction suppression, the no-CI declaration, and the run budget. A
//     pushed branch may never set one.
//   - TrustGlobal keys are the operator's preferences about how their own
//     machine behaves rather than facts about a repository: the checks timeout
//     and session reuse. Only the operator's own file may set one, and a
//     repository file carrying one is refused when it is parsed.
//
// The fourth class is this package's reading rather than the PRD's wording,
// and it is the larger of two such readings. PRD section 10 names three
// classes and its trust diagram does not place "checks_timeout" or
// "session_reuse" at all. They are global-only here because they are operator
// preferences rather than repository facts, so a repository should not be able
// to set the owner's idle timeout or session policy even harmlessly. That
// makes section 10 short a class, and the amendment is a separate task: a
// disagreement between this code and the PRD is a finding to raise, not a
// documentation gap to close, so do not treat this comment as the contract.
// The opt-out key name is the other reading, because section 10 describes the
// opt-out in prose without giving it a schema row; that name is settled and
// what the PRD still owes is the row.
//
// A key a layer set but its origin may not set is not an error, with the one
// exception above. It is dropped, the key falls back to the global layer or
// its default, and the drop is reported as a Rejection so an author can be
// told their setting had no effect. It is still validated first, because PRD
// section 10 requires an invalid value to fail at parse time even on a branch
// whose fields are otherwise ignored. A global-only key in a repository file
// is the exception because the file has no standing to carry it at all: that
// is refused where its author will see it rather than dropped quietly.
//
// # Failing at parse time
//
// Every refusal names the offending key and its value, and nothing falls back
// to a default silently. An unrecognized key is refused rather than ignored,
// a null is refused rather than treated as absent, a duration is a string
// because a bare number would leave the unit to be assumed, and a reserved
// agent flag is refused rather than dropped. Document ownership refuses a
// second claim on a subject, which is P14 enforced in configuration rather
// than stated in prose.
//
// A document also has to mean one thing. A dotted key is written as nested
// objects and the flat spelling is refused, and a member name repeated within
// one object is refused at any depth, rather than either being resolved to the
// value that happens to win. Both would otherwise let a file read one way to
// whoever reviews the diff and take effect another way, and this configuration
// selects commands that run with the operator's credentials.
//
// Keys are validated in sorted order, so a document with several problems
// always reports the same one and a fix makes visible progress. A refusal that
// came from inside a list says which element, and carries that element rather
// than the whole list, so a fault in one of sixty-four review rules does not
// arrive with the other sixty-three attached.
//
// # The path matcher
//
// One matcher serves ignore patterns and path-scoped review rules, so a path
// means the same thing to both. Its rules are documented on Pattern, and the
// one PRD section 10 states outright is that a wildcard never crosses a path
// separator on any platform. That holds here because matching is performed one
// segment at a time and a separator is never inside a segment, and because a
// backslash in a path being matched is treated as a separator rather than as
// an ordinary character.
//
// The matcher is also bounded, because the ignore list is one of the keys a
// pushed branch may set. ParsePattern documents the limits and what they do
// and do not bound; the part that matters here is that no pattern can make one
// match cost more than the pattern's segment count times the path's.
//
// # What this package does not do
//
// It does not resolve an agent against what is runnable, check that an agent
// has a verified instruction-suppression mechanism, decide whether a finding
// is eligible for an automatic fix round, or run any command it holds. Those
// need a process, a filesystem, or a network, and they belong to the callers
// that have them. The fix round limit for review is the clearest case: this
// package stores the number, and PRD section 14 decision 5, that the round
// covers fix findings only and never an ask finding, is the review stage's
// rule to keep.
package config
