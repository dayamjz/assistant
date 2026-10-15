package pipeline

import (
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/redact"
)

// recorder is the remover every text this package records from a stage or a
// fixer passes through. It is internal/redact's and not this package's: what
// counts as a credential is decided there, and this file decides only where
// the call is made.
//
// It is not taken from Options, so no caller can build a pipeline that records
// without it, on the terms internal/store refuses to open without one.
var recorder = redact.New()

// redactedText returns s as this package records it: with every credential
// internal/redact recognizes replaced by redact.Marker.
func redactedText(s string) string { return recorder.Redact(s) }

// redactedReport returns report with every string it carries passed through
// redactedText. Which strings those are is findings.Report.Rewritten's to
// say, because internal/findings owns the report's fields: a field the report
// gains later is added there once, and its own reflection test fails until it
// is, rather than this package and every other one that removes text from a
// report each keeping a list of fields.
//
// It runs before findings.Report.Normalize, so a derived finding identifier is
// a digest of the redacted text rather than of the credential. An identifier
// already carried is kept as it reads after redaction: a review report arrives
// normalized from findings.ParseReviewReport, so its identifiers were derived
// from the reviewer's text before this runs, and what is recorded of a
// credential there is that it contributed to a twelve-digit digest. Two
// identifiers that differed only inside a credential are the same after it,
// and Validate refuses that report as it refuses any other duplicate.
//
// The slices are copied rather than written through, because they are shared
// with whatever the stage body returned.
func redactedReport(report findings.Report) findings.Report {
	return report.Rewritten(redactedText)
}

// redactedFindings is redactedReport for a list of findings, which is the
// shape a fix round's holds take.
func redactedFindings(fs []findings.Finding) []findings.Finding {
	return findings.Report{Findings: fs}.Rewritten(redactedText).Findings
}
