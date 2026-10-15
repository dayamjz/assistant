package findings

// ParseReportThrough is ParseReport with every text field of the report
// passed through text after the report is decoded and before it is
// normalized or validated. Everything ParseReport documents about which object
// in the output is the report holds unchanged, because the candidate scan and
// the keys that decide what is a report are read from raw as it arrived.
//
// It exists for a caller that must not keep some part of what an agent wrote,
// such as internal/tasks, which passes internal/redact's remover so a
// credential a worker wrote into its report is gone before anything is
// derived from it. The order is the point: Normalize derives an identifier
// for a finding that arrived without one from the finding's text, and
// Validate quotes what it refuses, so applying text here rather than to the
// returned report means a derived identifier digests the rewritten text and a
// refusal quotes it. Applying text to raw instead would rewrite JSON syntax
// as well as the strings in it, and a rewrite that is not confined to a
// string literal can break the report it was meant to clean.
//
// text runs over the fields Report.Rewritten names and over nothing else; in
// particular a refusal that quotes raw itself, ErrNoReport's, quotes raw as it
// arrived, because no report was decoded for text to run over. text must not
// turn a non-empty string into an empty one, or a report valid as written can
// be refused here as one with a missing summary or description.
func ParseReportThrough(raw string, text func(string) string) (Report, error) {
	report, _, err := parseThrough(raw, nil, text)
	return report, err
}

// Rewritten returns a copy of the report with every string it carries passed
// through text: the summary, revision, risk and its rationale, every read
// path, every trace's path and reason, every entry of Tested, every evidence
// label and path, and every finding's identifier, severity, action, location
// path, citations and description.
//
// Every string rather than the fields an agent is likely to write in, because
// the caller asking cannot tell which of them carry what it is removing. A
// field Report, Finding, Trace, Evidence or Location gains later has to be
// added here; TestRewrittenReachesEveryStringField fills every string field
// by reflection, so a new one left out of this fails that test.
//
// It does not normalize, so an identifier already carried is rewritten and
// kept rather than derived again. The slices are copied rather than written
// through, and a nil slice stays nil.
func (r Report) Rewritten(text func(string) string) Report {
	r.Summary = text(r.Summary)
	r.Revision = text(r.Revision)
	r.Risk = Risk(text(string(r.Risk)))
	r.RiskRationale = text(r.RiskRationale)
	r.Read = rewrittenStrings(r.Read, text)
	if r.Traced != nil {
		traced := make(Traces, len(r.Traced))
		for i, trace := range r.Traced {
			trace.Path = text(trace.Path)
			trace.Reason = text(trace.Reason)
			traced[i] = trace
		}
		r.Traced = traced
	}
	r.Tested = rewrittenStrings(r.Tested, text)
	if r.Evidence != nil {
		evidence := make([]Evidence, len(r.Evidence))
		for i, artifact := range r.Evidence {
			artifact.Label = text(artifact.Label)
			artifact.Path = text(artifact.Path)
			evidence[i] = artifact
		}
		r.Evidence = evidence
	}
	if r.Findings != nil {
		fs := make([]Finding, len(r.Findings))
		for i, f := range r.Findings {
			f.ID = text(f.ID)
			f.Severity = Severity(text(string(f.Severity)))
			f.Action = Action(text(string(f.Action)))
			f.Location.Path = text(f.Location.Path)
			f.Cites = rewrittenStrings(f.Cites, text)
			f.Description = text(f.Description)
			fs[i] = f
		}
		r.Findings = fs
	}
	return r
}

// rewrittenStrings is text over a list, copied.
func rewrittenStrings[S ~[]string](list S, text func(string) string) S {
	if list == nil {
		return nil
	}
	out := make(S, len(list))
	for i, s := range list {
		out[i] = text(s)
	}
	return out
}
