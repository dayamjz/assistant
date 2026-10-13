// Package judge is the decision-model client: the two fixed questions, the
// pinned model version, the cutoffs and the measurement behind them, and
// request splitting. Its contract is PRD section 5, "Checks a decision model
// may add", and section 8's module row for judge: put one typed question to
// the model and return a probability or a failed call, never a guess.
//
// # What it asks, and of what
//
// The provider is TypeSafe's Jev, reached at Endpoint with the key from
// KeyVariable as a bearer token. Every request names ModelVersion, carries one
// "noul" question with its criteria, and reads back the probability of yes.
// The two questions, their criteria, and their cutoffs are the table in
// check.go and nothing configures them.
//
//   - CheckTrace puts a reviewer's trace to the model with the path's diff:
//     state {path, reason, diff}. A reason the model does not confirm - an
//     answer below 0.8 - is the check's finding.
//   - CheckScreen puts one file's diff to the model: state {path, diff}. An
//     answer at or above 0.3 flags the file.
//
// # The measurement behind the cutoffs
//
// Both cutoffs were measured on jev-1.13.0, against labeled sets kept outside
// this repository and drawn from this repository's own data; the sets are not
// here, so the figures below are a record of that measurement and not
// something a reader can re-run from this tree.
//
//   - Trace reason, cutoff 0.8: area under the ROC curve 0.991; at 0.8, 94% of
//     true reasons pass and 3% of false ones do.
//   - Screen, cutoff 0.3: area under the ROC curve about 0.99, about 90% recall
//     on subtle payloads, and about 2% false positives.
//
// What those figures cover is narrower than they read. The screen's labeled
// payloads were written by the people building it, so its recall is measured
// on plain attempts somebody thought to write and says nothing about text
// written to evade this question. And the trace set's false reasons were
// written by a model, not collected from reviewers. The screen is a tripwire
// for plain attempts to address the reviewer, not a security boundary and
// not a defense against prompt injection: a file it does not flag is not
// shown to be free of such text, and nothing here keeps the reviewer from
// reading what it flags.
//
// The same measurement is why the screen asks about one file per request:
// bundling many files into one request lowered recall. A file too large for
// one request is cut into consecutive pieces of whole lines rather than
// truncated, and each piece is its own request; how the pieces come to one
// result is Screen's to state. Whether a piece fits is the provider's answer,
// not an estimate here: a piece refused as over its token limit is halved and
// asked again, down to a floor below which the refusal stands as a failure.
// The trace check is never cut, because its question is about the reason
// against the path's change as a whole, so a diff too large for one request
// fails that check rather than being checked in part.
//
// A change to a question, the version, or a cutoff has to beat the one it
// replaces on held-out examples, on the terms PRD section 5 sets; this package
// owns where those values live, and that section owns the rule.
//
// # A failed call is never an answer
//
// A Result is a probability or a Failure, and its fields are unexported so
// nothing outside this package can build one that is both. A missing key, a
// transport error, a timeout, a status that is not a success, a body that is
// not the provider's answer shape, an answer missing, of another type, or
// outside zero to one, and an answer from a model other than ModelVersion are
// all failures, and Fired reports false for every one of them. What a caller
// does with a failure is the caller's: the review stage reports it and holds
// nothing.
//
// A client with no key sends nothing at all. That is the consent rule: the key
// is read from the operator's environment and from no file, so a run with no
// key in the environment has no request leave the machine whatever its mode.
// The variable is KeyVariable, ASSISTANT_DECISION_MODEL_KEY, and never the
// provider's generic TYPESAFE_API_KEY, with no fallback between them: the
// checks run by default where a key is present, so a key set for some other
// purpose must not start sending changed lines. The PRD does not name the
// variable; KeyVariable is its one owner.
//
// # The key stays out of text
//
// The key is written into one request header and into nothing else this
// package builds: no error sentence quotes a header, a Result's sentence
// passes internal/redact before it is returned, and Client formats without
// its key. internal/redact is the one owner of credential removal and is
// where the bearer shape is recognized.
//
// # What it does not do, and the residual gaps
//
//   - It decides no finding. It answers; the review stage builds notes and
//     asks from its answers, and owns what each mode does with them.
//   - It records nothing itself. Record forwards to the Recorder the client
//     was built with, and the review stage records every answer and every
//     failed call it got; internal/service builds its one client with the
//     recorder that writes the store's decision-model record, so in this
//     build no request leaves the machine without its answer being handed to
//     that record. Writing the row is best-effort: a row the store refuses is
//     not retried, and the record then understates, which the review stage
//     names in a note on the run's report.
//   - The model's answer is a judgment with no evidence behind it. A screen
//     that does not flag a file says nothing about the file being safe, and
//     the screen runs beside the review rather than before it, so the
//     reviewer has already read what it flags.
//   - A review's fan-out is bounded by MaxRequestsPerReview, and one file's
//     screen by MaxRequestsPerFile, only for a client from ForReview; a
//     client serving no review is bounded per file and not in total. Which
//     paths go unasked when a review reaches its bound follows the order
//     requests were taken, not a ranking of the paths.
package judge
