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
// redactedText: the summary, revision, risk and its rationale, every read path,
// every trace's path and reason, every entry of Tested, every evidence label
// and path, and every field of every finding that holds text.
//
// Every string rather than the fields an agent is likely to write in, because
// a stage body may put any text in any of them and nothing here can tell which
// came from an agent. A field findings.Report or findings.Finding gains later
// has to be added here; TestARecordedReportCarriesNoCredentialInAnyField fills
// every string field the two types have by reflection, so a new one left out
// of this fails that test rather than being recorded as written.
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
	report.Summary = redactedText(report.Summary)
	report.Revision = redactedText(report.Revision)
	report.Risk = findings.Risk(redactedText(string(report.Risk)))
	report.RiskRationale = redactedText(report.RiskRationale)
	report.Read = redactedStrings(report.Read)
	if report.Traced != nil {
		traced := make(findings.Traces, len(report.Traced))
		for i, trace := range report.Traced {
			trace.Path = redactedText(trace.Path)
			trace.Reason = redactedText(trace.Reason)
			traced[i] = trace
		}
		report.Traced = traced
	}
	report.Tested = redactedStrings(report.Tested)
	if report.Evidence != nil {
		evidence := make([]findings.Evidence, len(report.Evidence))
		for i, artifact := range report.Evidence {
			artifact.Label = redactedText(artifact.Label)
			artifact.Path = redactedText(artifact.Path)
			evidence[i] = artifact
		}
		report.Evidence = evidence
	}
	report.Findings = redactedFindings(report.Findings)
	return report
}

// redactedFindings is redactedReport for a list of findings, which is the
// shape a fix round's holds take.
func redactedFindings(fs []findings.Finding) []findings.Finding {
	if fs == nil {
		return nil
	}
	out := make([]findings.Finding, len(fs))
	for i, f := range fs {
		f.ID = redactedText(f.ID)
		f.Severity = findings.Severity(redactedText(string(f.Severity)))
		f.Action = findings.Action(redactedText(string(f.Action)))
		f.Location.Path = redactedText(f.Location.Path)
		f.Cites = redactedStrings(f.Cites)
		f.Description = redactedText(f.Description)
		out[i] = f
	}
	return out
}

// redactedStrings is redactedText over a list, copied.
func redactedStrings[S ~[]string](list S) S {
	if list == nil {
		return nil
	}
	out := make(S, len(list))
	for i, s := range list {
		out[i] = redactedText(s)
	}
	return out
}
