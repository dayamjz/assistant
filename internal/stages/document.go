package stages

import (
	"context"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
)

// documentProjectionBytes bounds the command output that travels in the stage's
// report. The whole output goes to the run's document evidence file, and what a
// person or an agent is shown is a bounded projection of it carrying an
// explicit marker for what was left out, which is what PRD section 8 asks of
// anything travelling in findings, streams, and prompts.
const documentProjectionBytes = 8 << 10

// Document is the document stage: it updates documentation the change made stale.
//
// # What it establishes
//
// PRD section 5 asks this stage to update documentation the change made stale,
// report only what it could not resolve, and limit its scope to what this
// change affected. The updates happen "in pass" per the PRD's fix rounds
// column, meaning the command makes changes during execution rather than in
// separate fix rounds.
//
// What a passing run establishes is that the configured command exited zero
// against the commit in the run's isolated copy. A non-zero exit reports what
// could not be resolved as a note finding - informational rather than blocking.
//
// # Where the command comes from
//
// The command is read from StageDeps.Config and from nothing else, and this
// body opens no configuration file in the isolated copy at all. So the command
// reaches this stage from the trusted layer, unless the operator set
// allow_pushed_commands: commands.document is config.TrustCommands rather than
// config.TrustTrusted, and PRD section 10 makes that opt-out the sanctioned
// way to let a pushed branch set commands. The opt-out is itself trusted-only,
// so a branch cannot enable it for itself.
//
// # The three answers
//
// A command that exits zero passes. The report carries the pass and no
// finding, and the run advances.
//
// A command that exits non-zero reports what it could not resolve. The finding
// is a note rather than a fix: it is informational about unresolved
// documentation gaps, not a blocking error. This aligns with the PRD's "in pass"
// fix model where updates happen during execution.
//
// A command that did not report an exit status of its own establishes nothing
// either way, and neither does a run with no command configured at all. Both
// are the case PRD section 5 calls a stage that could not gather enough
// evidence, so both are ask findings and both hold the stage for a person,
// which is P3's fail-closed direction.
//
// # Evidence
//
// PRD section 8 puts documentation evidence at evidence/<run>, deliberately
// outside the isolated copy. This body writes there and never into the copy,
// and it writes while the command runs, so the record survives a command this
// build gives up waiting on.
//
// The record is redacted rather than verbatim. PRD section 8 has
// internal/redact called at every persistence boundary and carves out no
// exception for evidence.
//
// Filing that record is never allowed to decide the verdict. A command that
// answered has answered, so a record that could not be opened, could not be
// written, or was cut short before all of the output reached it is reported as
// a note, the report then names no evidence and claims none, and the stage
// still reports what the check said.
//
// # Where the line between an error and a finding falls
//
// A body returns an error only when the stage could not run at all, and the
// line is drawn at the command. The run's own state, the isolated copy and the
// commit in it are this build's machinery: a failure in any of them means no
// check can run and none of it is the change's fault, so those are errors. A
// cancelled run is an error for the same reason and is returned as the
// context's own, so a caller can recognize it.
//
// The command's own failure is the other side of that line. It ran, or it
// could not be started, and either way the stage ran and has something to
// report, so it reports rather than stopping the run.
//
// # The residual gaps
//
// Whether the configured command is targeted at this change is not checked.
// The key is documented as documentation updates and this stage runs what it is
// given.
//
// A command that leaves a process behind leaves it running, and nothing bounds
// how long the command itself runs. commandGrace says what that bounds and
// what it does not.
//
// Redaction removes the one shape internal/redact recognizes, a credential in
// a URL's userinfo, and nothing else.
func Document(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{pipeline.KeyRepository, pipeline.KeyRun},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return runDocumentCommand(ctx, deps, in)
			}
		},
	}
}

// configuredDocumentCommand returns the documentation command cfg names for this
// stage and whether it names one at all, decided after trimming: a value that is
// all whitespace configures nothing. It is one read with two consumers -
// runDocumentCommand answers a run with it, and Holding predicts the hold with
// it - so the body and the prediction cannot drift apart.
func configuredDocumentCommand(cfg config.Config) (string, bool) {
	command := strings.TrimSpace(cfg.Commands.Document)
	return command, command != ""
}

// runDocumentCommand is the stage body: it runs the configured documentation
// command in the run's isolated copy and turns what the command answered into
// the stage's report.
func runDocumentCommand(ctx context.Context, deps StageDeps, in pipeline.Input) (pipeline.Output, error) {
	command, configured := configuredDocumentCommand(deps.Config)
	if !configured {
		return pipeline.Output{Report: noDocumentCommandConfigured()}, nil
	}

	repositoryID, runID, err := readDocumentState(in.State)
	if err != nil {
		return pipeline.Output{}, err
	}
	copied, err := deps.Copy(ctx, repositoryID, runID)
	if err != nil {
		return pipeline.Output{}, err
	}
	commit, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return pipeline.Output{}, fmt.Errorf(
			"stages: reading the commit the %s stage would check in %s: %w", in.Stage, copied.Path(), err)
	}

	record := openDocumentEvidence(deps.Home.EvidenceLog(runID, in.Stage.String()), deps.redact)
	record.header(command, commit, copied.Path())
	result := runCommand(ctx, commandSpec{
		command:    command,
		dir:        copied.Path(),
		record:     record,
		projection: documentProjectionBytes,
		grace:      commandGrace,
		redact:     deps.redact,
	})
	record.footer(result)
	record.cutShort(result.short)
	record.close()
	if err := ctx.Err(); err != nil {
		return pipeline.Output{}, err
	}
	return pipeline.Output{Report: documentReport(deps.redact, command, commit, record, result)}, nil
}

// readDocumentState reads the two facts this stage needs of the run's state:
// which repository it validates and which run it is. They are what locate the
// isolated copy and the evidence directory, and neither is derivable from the
// other.
func readDocumentState(state pipeline.Reader) (repositoryID, runID string, err error) {
	repositoryValue, err := state.Get(pipeline.KeyRepository)
	if err != nil {
		return "", "", err
	}
	runValue, err := state.Get(pipeline.KeyRun)
	if err != nil {
		return "", "", err
	}
	repositoryID, _ = repositoryValue.Text()
	runID, _ = runValue.Text()
	return repositoryID, runID, nil
}

// openDocumentEvidence opens the run's document evidence file for appending,
// creating the run's evidence directory if this is the first thing to write there.
//
// It returns a usable evidence either way: one that could not be opened
// records the reason and swallows every write, so a caller writes to it
// without asking first.
func openDocumentEvidence(path string, r redact.Redactor) *testEvidence {
	return openTestEvidence(path, r)
}

// documentReport is what the stage found. The three cases are what a command can
// answer: it did not report a status at all, it exited zero, or it exited
// something else. Whether it reported a status is asked first, because a
// status is what the other two read and a command that gave none carries the
// same -1 as one this build could not start.
//
// The report leaves through redactReport, so no composition site here has to
// remember the redactor and the fields below may be written from the raw
// command line and error text.
func documentReport(r redact.Redactor, command, commit string, record *testEvidence,
	result commandResult) findings.Report {
	report := findings.Report{Revision: commit}
	if result.exited {
		report.Tested = []string{command}
	}
	if record.recorded() {
		report.Evidence = []findings.Evidence{{
			Label: "this run's documentation evidence: the output of the attempts at the configured " +
				"document command that could be recorded",
			Path: record.path,
		}}
	}
	switch {
	case !result.exited:
		report.Summary = fmt.Sprintf(
			"The configured document command did not report an exit status against %s, so nothing was "+
				"established about documentation updates either way.", commit)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "document-not-settled",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf(
				"The configured document command did not report an exit status of its own, so it "+
					"established neither success nor unresolved gaps. Approving carries the run past a check "+
					"that answered nothing; skipping records the stage as skipped; cancelling ends the "+
					"run.\n\ncommand: %s\ncommit: %s\n%s\n\n%s",
				command, commit, documentNotSettledReason(result), documentOutputSection(result, record)),
		})

	case result.code == 0:
		report.Summary = fmt.Sprintf(
			"The configured document command passed against %s: %q exited zero in the run's isolated copy.",
			commit, command)

	default:
		report.Summary = fmt.Sprintf(
			"The configured document command reported unresolved documentation gaps against %s: "+
				"%q exited %d in the run's isolated copy.",
			commit, command, result.code)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "document-gaps",
			Severity: findings.SeverityInfo,
			Action:   findings.ActionNote,
			Description: fmt.Sprintf(
				"The configured document command reported what it could not resolve. This is "+
					"informational: documentation gaps are noted but do not block the run.\n\n"+
					"command: %s\nexit status: %d\ncommit: %s\n\n%s",
				command, result.code, commit, documentOutputSection(result, record)),
		})
	}
	if !record.recorded() {
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "document-evidence-unrecorded",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionNote,
			Description: fmt.Sprintf(
				"The command's full output could not be recorded, so this report names no evidence "+
					"file: a path offered as the full output has to hold it. What the command answered "+
					"is unaffected and is reported above; what is lost is whatever the command printed "+
					"beyond the bounded extract in this report.\n\nwhat went wrong: %v", record.err),
		})
	}
	return redactReport(r, report)
}

// documentNotSettledReason renders what os/exec reported about a command that
// never gave a status, and says so plainly when it reported nothing.
func documentNotSettledReason(result commandResult) string {
	if result.err != nil {
		return "what happened instead: " + result.err.Error()
	}
	return "what happened instead: it was ended by something other than its own exit"
}

// documentOutputSection renders the command's output for a finding: the bounded
// tail, preceded by a marker naming what was left out and where the whole of it
// is, and followed by the evidence path.
func documentOutputSection(result commandResult, record *testEvidence) string {
	var b strings.Builder
	if result.omitted > 0 {
		if record.recorded() {
			fmt.Fprintf(&b, "[%d bytes of earlier output omitted; the whole of it is at %s]\n",
				result.omitted, record.path)
		} else {
			fmt.Fprintf(&b, "[%d bytes of earlier output omitted, and the record that would hold "+
				"them is not whole, so this report points at no file for them]\n", result.omitted)
		}
	}
	switch {
	case result.tail != "":
		b.WriteString(result.tail + "\n")
	case result.omitted == 0:
		b.WriteString("[the command wrote nothing]\n")
	}
	if record.recorded() {
		fmt.Fprintf(&b, "\nfull output: %s", record.path)
	}
	return strings.TrimRight(b.String(), "\n")
}

// noDocumentCommandConfigured is the report for a run whose configuration names
// no document command.
//
// PRD section 10 makes commands.document empty by default, so this is the
// ordinary state of a repository that has not configured one rather than a
// malfunction. It is still a stage that established nothing, and the finding is
// an ask rather than a note for that reason: a note would let a run advance past
// a stage that never checked anything, which is the one thing this stage may not
// do.
//
// It reads no state and touches no filesystem, so a run with no command
// configured holds here whatever else is wrong with the machine. That is
// deliberate: the answer does not depend on the isolated copy, so failing to
// open one would replace a hold a person can act on with an error they cannot.
func noDocumentCommandConfigured() findings.Report {
	return findings.Report{
		Summary: "No document command is configured, so this stage ran no command and established nothing " +
			"about documentation updates.",
		Findings: []findings.Finding{{
			ID:       "document-no-command",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: "No document command is configured, so the document stage had nothing to " +
				"run and could not update documentation or verify that it is current. " +
				"Setting commands.document in this machine's own configuration file is what gives this " +
				"stage something to run in this build: the trusted repository layer is not read " +
				"yet, so committing the key to the default branch does not reach here. The key " +
				"executes shell, so the branch under validation may name it only where " +
				"allow_pushed_commands is set, and that opt-out is refused from a pushed branch, " +
				"so a branch cannot turn it on for itself. Approving carries the run past a stage " +
				"that checked nothing; skipping records it as skipped; cancelling ends the run.",
		}},
	}
}
