// Package knowledge decides which scoped knowledge may be injected into a
// worker prompt.
//
// The rows live in internal/store. This package does not open a database and
// does not write one. A row is not current state, and the newest row is not
// what is true: Rules reads the whole history the caller passes. Putting the
// history in internal/pipeline's graph state would make it part of the
// convergence fingerprint, so this package does not.
//
// # What becomes a rule
//
// An operator row is a rule. A task assertion is not, unless an operator row
// adopts it by identifier. Adopting is that identifier. This package does not
// parse a body to decide, and a task row that carries an adoption is ignored,
// which is the decision the store also refuses to write.
//
// AuthoredByOperator is not a person. store.ResolvedByPerson is not consulted
// here. PRD section 9 names no verb that records knowledge, and this package
// does not add one. internal/service's RecordOperatorKnowledge and
// RecordTaskAssertion are the writers a test calls.
//
// # What the prompt is
//
// Compose appends the generated section after the trusted operator document
// the caller passes, and the trusted bytes stay the prefix. The repository
// file is an input Compose does not copy into that prefix and does not
// substitute when the trusted document is absent. A repository file that is
// the trusted document is refused.
//
// Compaction drops the oldest rules first until the generated section fits
// the budget. Pinned references are passed in by the caller and are not
// dropped, including when they alone exceed the budget. The bound then
// yields and every rule is dropped. This package does not decide which holds
// are open or which tasks are unfinished.
//
// # What this does not do
//
// It does not read a checkout. The service passes assistant.json from the
// directory it names, and no other repository file. A different file in the
// checkout is not this check.
//
// It does not redact. The store redacts a body before it is stored, through
// internal/redact. A body this package is handed is already that text.
//
// The size bound on a stored body is the store's. The budget here is only
// how much generated text a prompt may carry.
package knowledge
