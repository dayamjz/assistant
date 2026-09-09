package stages

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
)

// testProjectionBytes bounds the command output that travels in the stage's
// report. The whole output goes to the run's test evidence file, and what a
// person or an agent is shown is a bounded projection of it carrying an
// explicit marker for what was left out, which is what PRD section 8 asks of
// anything travelling in findings, streams, and prompts.
const testProjectionBytes = 8 << 10

// Test is the test stage: it validates this change with the check the
// configuration names, and reports what that check answered.
//
// # What it establishes, and what it does not
//
// PRD section 5 asks this stage for targeted validation of this change and
// this intent rather than a full suite, and PRD section 10 puts the check
// itself in commands.test, described there in those same words. So the
// targeting lives in the configured command, and this stage runs that command
// rather than deriving one: a stage that appended paths to a command line it
// cannot parse would be inventing a second targeting rule, quietly, out of a
// value whose whole shape is one line of shell.
//
// What a passing run therefore establishes is that the configured command
// exited zero against the commit in the run's isolated copy. The report says
// that and no more. Whether that command covers the intent is a judgement this
// build does not make, and the residual gaps below say so rather than letting
// a pass read as more than it is.
//
// # Where the command comes from, and where it must not come from
//
// The command is read from StageDeps.Config and from nothing else, and this
// body opens no configuration file in the isolated copy at all. So the command
// reaches this stage from the trusted layer, unless the operator set
// allow_pushed_commands: commands.test is config.TrustCommands rather than
// config.TrustTrusted, and PRD section 10 makes that opt-out the sanctioned
// way to let a pushed branch set commands. The opt-out is itself trusted-only,
// so a branch cannot enable it for itself, which is the guarantee that does
// hold here. What does not hold is a categorical one: with the opt-out on, the
// value the operator gets is the one the branch named.
//
// That is the half of P7 this stage owns. The other half is that what it was
// handed was itself resolved from a trusted commit, and that half belongs to
// whoever resolves the configuration; StageDeps.Config says where this build's
// value comes from and what section 10 it is short of. This stage cannot
// narrow that gap and does not claim to. What it can do is refuse to be a
// second way in, which is what reading only what it was given amounts to.
//
// # The three answers, and which of them holds
//
// A command that exits zero passes. The report carries the pass and no
// finding, and the run advances.
//
// A command that exits non-zero fails, and the finding is a fix. It is
// objectively wrong and mechanically fixable in PRD section 5's sense, so it
// is eligible for this stage's automatic fix rounds, config.FixRounds.Test.
// No fixer is written, and internal/cli wires PendingFixer, so such a finding
// reaches that and the run fails there naming what is missing. That is the
// honest end of this path, it is documented on PendingFixer rather than
// softened here, and it is what this stage's tests hold the built pipeline to.
//
// A command that did not report an exit status of its own establishes nothing
// either way, and neither does a run with no command configured at all. Both
// are the case PRD section 5 calls a stage that could not gather enough
// evidence, so both are ask findings and both hold the stage for a person,
// which is P3's fail-closed direction. PRD section 5 offers three answers when
// no targeted check can establish the intent - write one, verify manually with
// recorded evidence, or report that sufficient evidence is not possible - and
// this build always takes the third. The first two need an agent authoring a
// check and a person recording a verification, and neither is built; a stage
// that took either silently would be reporting a pass it did not establish.
//
// # Evidence
//
// PRD section 8 puts test evidence at evidence/<run>, deliberately outside the
// isolated copy, so that a test artifact never becomes part of the change
// being validated. This body writes there and never into the copy, and it
// writes while the command runs, so the record survives a command this build
// gives up waiting on.
//
// The record is redacted rather than verbatim. PRD section 8 has
// internal/redact called at every persistence boundary and carves out no
// exception for evidence, so what reaches the file is the command's output
// with credentials in URL userinfo removed, not the bytes the command printed;
// the file says so in each attempt's header, and the same redaction covers the
// bounded projection and the report's own text. Evidence that has been altered
// has to say so, and this paragraph and that header line are where it does.
//
// That file is the run's test evidence and nothing more. PRD section 8's
// authoritative per-stage log is logs/<run>/<stage>.log, home.StageLog owns
// that path and that role, and this stage neither writes it nor claims it.
//
// The report names the evidence file, and the finding carries a bounded tail
// of the output with a marker saying how much earlier output it leaves out and
// where the whole of it is. That marker is what PRD section 8 requires of
// anything travelling in findings, streams, and prompts.
//
// Filing that record is never allowed to decide the verdict. A command that
// answered has answered, so a record that could not be opened, could not be
// written, or was cut short before all of the output reached it is reported as
// a note, the report then names no evidence and claims none, and the stage
// still reports what the check said. The alternative is a gate that stops
// validating when a disk fills, and a report that points at a file not holding
// what it says it does.
//
// # No agent, and therefore no evidence binding
//
// Nothing here starts an agent. The findings are built from the toolchain's
// own exit status rather than parsed out of anything an agent wrote, so there
// is no report to bind and no evidence set to bind it to: PRD section 5's
// binding rule and findings.ParseReviewReport answer for a review's claims
// about code, and this stage makes none. A later body that did ask an agent
// whether a check establishes the intent would be making such a claim and
// would owe that rule; it would not inherit an exemption from here.
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
// The key is documented as a targeted check and this stage runs what it is
// given, so a repository configuring a full suite gets a full suite, slowly,
// and this stage does not notice.
//
// Whether the configured command covers the intent is not checked either. The
// run's intent is not read here, because nothing here could compare a line of
// shell against it; a build that judges that needs an agent, and this one does
// not have that stage.
//
// A command that leaves a process behind leaves it running, and nothing bounds
// how long the command itself runs. commandGrace says what that bounds and
// what it does not.
//
// Redaction removes the one shape internal/redact recognizes, a credential in
// a URL's userinfo, and nothing else. An API key echoed by a failing
// assertion, a token in an environment dump, and every other secret a command
// might print go into the record and the report as the command wrote them, and
// a whitespace-free token longer than redactingWriter's bound can carry even
// the recognized shape past it, which that writer's doc owns. What redaction
// buys here is that this persistence boundary is no worse than the others in
// this build, not that the record is safe to publish.
func Test(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{pipeline.KeyRepository, pipeline.KeyRun},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return runTargetedCheck(ctx, deps, in)
			}
		},
	}
}

// configuredTestCommand returns the targeted check cfg names for this stage
// and whether it names one at all, decided after trimming: a value that is
// all whitespace configures nothing. It is one read with two consumers -
// runTargetedCheck answers a run with it, and Holding predicts the hold with
// it - so the body and the prediction cannot drift apart.
func configuredTestCommand(cfg config.Config) (string, bool) {
	command := strings.TrimSpace(cfg.Commands.Test)
	return command, command != ""
}

// runTargetedCheck is the stage body: it runs the configured check in the run's
// isolated copy and turns what the check answered into the stage's report.
func runTargetedCheck(ctx context.Context, deps StageDeps, in pipeline.Input) (pipeline.Output, error) {
	command, configured := configuredTestCommand(deps.Config)
	if !configured {
		return pipeline.Output{Report: noTestCommandConfigured()}, nil
	}

	repositoryID, runID, err := readTestState(in.State)
	if err != nil {
		return pipeline.Output{}, err
	}
	copied, err := deps.Copy(ctx, repositoryID, runID)
	if err != nil {
		return pipeline.Output{}, err
	}
	// The commit is read from the copy rather than from state, because the
	// copy's head moves during a run - a rebase moves it and a fix round
	// commits to it - so the commit the run started from is not the tree this
	// stage checked. It is read immediately before the command starts, which
	// is as close to it as a read and a process launch get; nothing here makes
	// the two one, so a copy something changed in between would be reported
	// under the commit that was read.
	commit, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return pipeline.Output{}, fmt.Errorf(
			"stages: reading the commit the %s stage would check in %s: %w", in.Stage, copied.Path(), err)
	}

	// deps.Copy refused a nil home above, so the evidence path can be asked
	// for here without checking again. internal/home names the file, including
	// its leaf, so this body spells no path under the home root. One file per
	// run rather than one per attempt: a stage that takes fix rounds runs more
	// than once, and appending keeps every attempt's output rather than
	// letting a later one replace an earlier one.
	record := openTestEvidence(deps.Home.EvidenceLog(runID, in.Stage.String()), deps.redact)
	record.header(command, commit, copied.Path())
	result := runCommand(ctx, commandSpec{
		command:    command,
		dir:        copied.Path(),
		record:     record,
		projection: testProjectionBytes,
		grace:      commandGrace,
		redact:     deps.redact,
	})
	record.footer(result)
	record.cutShort(result.short)
	record.close()
	if err := ctx.Err(); err != nil {
		return pipeline.Output{}, err
	}
	return pipeline.Output{Report: testReport(deps.redact, command, commit, record, result)}, nil
}

// readTestState reads the two facts this stage needs of the run's state: which
// repository it validates and which run it is. They are what locate the
// isolated copy and the evidence directory, and neither is derivable from the
// other.
func readTestState(state pipeline.Reader) (repositoryID, runID string, err error) {
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

// testEvidence is the run's test evidence file, and what went wrong if it
// could not be written.
//
// Its Write never reports an error, which is the whole reason it exists. The
// command's output goes to this and to the report's bounded tail through one
// io.MultiWriter, and a MultiWriter stops at the first writer that fails, so a
// file that could not be written would otherwise cut the command's output
// short and end its run with an error - turning a full disk into a verdict.
// The first failure is kept here instead and reported as a note.
type testEvidence struct {
	// path is where the record was to be written, and is what the report
	// names when there is a record to name.
	path string
	// redact is applied to the section marks this type composes before they
	// are written. The command's own output arrives through Write already
	// redacted, by runCommand's writer, which is the layer that can see a
	// whole line; redacting again here would see each write's fragment and
	// miss a credential split across two, so Write deliberately does not.
	redact redact.Redactor
	// file is the open file, nil when it could not be opened.
	file *os.File
	// err is the first thing that went wrong: opening it, writing to it,
	// closing it, or the command's output being cut off before all of it was
	// written to it. It is nil when the record is whole.
	//
	// The last of those is the one this type cannot see for itself. A write
	// that never arrives is not a write that failed, so runCommand reports it
	// and cutShort is how it gets here.
	err error
}

// openTestEvidence opens the run's test evidence file for appending, creating the
// run's evidence directory if this is the first thing to write there.
//
// Appending rather than replacing is what keeps a fix round from erasing the
// attempt before it. The directory is created here rather than by
// internal/home, which creates the evidence root and leaves what goes under it
// to whoever writes it.
//
// It returns a usable evidence either way: one that could not be opened
// records the reason and swallows every write, so a caller writes to it
// without asking first.
func openTestEvidence(path string, r redact.Redactor) *testEvidence {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &testEvidence{path: path, redact: r,
			err: fmt.Errorf("making the evidence directory %s: %w", dir, err)}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return &testEvidence{path: path, redact: r, err: fmt.Errorf("opening %s: %w", path, err)}
	}
	return &testEvidence{path: path, redact: r, file: file}
}

// Write implements io.Writer and never fails. It reports every byte consumed
// whatever became of them, so the writer it is combined with sees the whole
// output.
func (e *testEvidence) Write(p []byte) (int, error) {
	if e.file == nil || e.err != nil {
		return len(p), nil
	}
	if _, err := e.file.Write(p); err != nil {
		e.err = fmt.Errorf("writing %s: %w", e.path, err)
	}
	return len(p), nil
}

// mark writes one formatted section mark, redacted, because a mark carries the
// command line and error text and both can hold a credentialed URL.
//
// It discards what Write answered because Write never fails: a failure is kept
// on the record itself and read off recorded, so there is nothing at this call
// to handle.
func (e *testEvidence) mark(format string, args ...any) {
	_, _ = io.WriteString(e, e.redact.Redact(fmt.Sprintf(format, args...)))
}

// header opens one attempt's section, so a reader of a file holding several
// attempts can tell which command ran against which commit in which directory.
// Its last line is for the reader of the file: the record is redacted rather
// than verbatim, so what stands here is not byte-identical to what the command
// printed.
func (e *testEvidence) header(command, commit, dir string) {
	e.mark("=== test stage\n=== command: %s\n=== commit:  %s\n=== copy:    %s\n"+
		"=== record:  redacted, not verbatim: credentials in URL userinfo are removed\n",
		command, commit, dir)
}

// footer closes one attempt's section with what the command answered.
func (e *testEvidence) footer(result commandResult) {
	switch {
	case result.exited:
		e.mark("\n=== exit status: %d\n\n", result.code)
	case result.err != nil:
		e.mark("\n=== no exit status: %v\n\n", result.err)
	default:
		e.mark("\n=== no exit status\n\n")
	}
}

// cutShort records that the command's output was cut off before all of it
// reached this file, which is what commandResult.short reports. Nothing this
// file does can notice that: the bytes never arrive, so every write it saw
// succeeded and it would otherwise call itself whole.
//
// It takes a nil to mean nothing was cut off, so a caller passes what it has
// rather than asking first. A record already broken keeps its first reason,
// on the same terms as every other failure here.
func (e *testEvidence) cutShort(err error) {
	if err == nil || e.err != nil {
		return
	}
	e.err = fmt.Errorf("the command's output was cut off before all of it reached %s: %w", e.path, err)
}

// close finishes the record, keeping a failure to flush on the same terms as a
// failure to write.
func (e *testEvidence) close() {
	if e.file == nil {
		return
	}
	err := e.file.Close()
	e.file = nil
	if err != nil && e.err == nil {
		e.err = fmt.Errorf("closing %s: %w", e.path, err)
	}
}

// recorded reports whether the whole of the command's output reached the file.
// A report names the file only when it did, because a path offered as the full
// output has to hold it.
func (e *testEvidence) recorded() bool { return e.err == nil }

// testReport is what the stage found. The three cases are what a command can
// answer: it did not report a status at all, it exited zero, or it exited
// something else. Whether it reported a status is asked first, because a
// status is what the other two read and a command that gave none carries the
// same -1 as one this build could not start.
//
// Tested is set on the same condition, and only there. findings.Report.Tested
// is what the stage actually checked, so a command that never reported a
// status of its own - one that could not be started, or that something else
// ended - does not belong in it. The command is still named in the summary, in
// the finding, and in the evidence header, so nothing about it is lost; what
// is withheld is the claim that it checked anything.
//
// The report leaves through redactReport, so no composition site here has to
// remember the redactor and the fields below may be written from the raw
// command line and error text.
func testReport(r redact.Redactor, command, commit string, record *testEvidence,
	result commandResult) findings.Report {
	report := findings.Report{Revision: commit}
	if result.exited {
		report.Tested = []string{command}
	}
	if record.recorded() {
		report.Evidence = []findings.Evidence{{
			Label: "this run's test evidence: the output of the attempts at the configured test " +
				"command that could be recorded",
			Path: record.path,
		}}
	}
	switch {
	case !result.exited:
		report.Summary = fmt.Sprintf(
			"The configured test command did not report an exit status against %s, so nothing was "+
				"established about this change either way.", commit)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "test-not-settled",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf(
				"The configured test command did not report an exit status of its own, so it "+
					"established neither a pass nor a failure. Approving carries the run past a check "+
					"that answered nothing; skipping records the stage as skipped; cancelling ends the "+
					"run.\n\ncommand: %s\ncommit: %s\n%s\n\n%s",
				command, commit, testNotSettledReason(result), testOutputSection(result, record)),
		})

	case result.code == 0:
		report.Summary = fmt.Sprintf(
			"The configured test command passed against %s: %q exited zero in the run's isolated copy.",
			commit, command)

	default:
		report.Summary = fmt.Sprintf(
			"The configured test command failed against %s: %q exited %d in the run's isolated copy.",
			commit, command, result.code)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "test-failed",
			Severity: findings.SeverityError,
			Action:   findings.ActionFix,
			Description: fmt.Sprintf(
				"The configured test command failed, so this change does not pass the check this "+
					"repository holds it to.\n\ncommand: %s\nexit status: %d\ncommit: %s\n\n%s",
				command, result.code, commit, testOutputSection(result, record)),
		})
	}
	if !record.recorded() {
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "test-evidence-unrecorded",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionNote,
			Description: fmt.Sprintf(
				"The command's full output could not be recorded, so this report names no evidence "+
					"file: a path offered as the full output has to hold it. What the check answered "+
					"is unaffected and is reported above; what is lost is whatever the command printed "+
					"beyond the bounded extract in this report.\n\nwhat went wrong: %v", record.err),
		})
	}
	return redactReport(r, report)
}

// redactReport returns the report with every field this stage composes from
// the command line, the command's output, or an error around them passed
// through the redactor: the summary, the entries of Tested, each evidence
// artifact's label and path, and each finding's description. It runs once,
// where the report leaves the stage, which is what makes it a boundary rather
// than a convention each composition site above it has to remember; a field
// this stage starts composing later has to be added here, and that is the
// residual gap this shape carries.
//
// The projection inside a description arrives already redacted, upstream of
// the bound that could have cut a credential's shape apart, so this pass adds
// the command line and the error texts rather than covering the output twice
// over. The fields left alone - the revision and each finding's identifier,
// action, and severity - are this build's own vocabulary and carry nothing the
// command wrote.
func redactReport(r redact.Redactor, report findings.Report) findings.Report {
	report.Summary = r.Redact(report.Summary)
	for i, tested := range report.Tested {
		report.Tested[i] = r.Redact(tested)
	}
	for i, artifact := range report.Evidence {
		report.Evidence[i].Label = r.Redact(artifact.Label)
		report.Evidence[i].Path = r.Redact(artifact.Path)
	}
	for i, finding := range report.Findings {
		report.Findings[i].Description = r.Redact(finding.Description)
	}
	return report
}

// testNotSettledReason renders what os/exec reported about a command that never
// gave a status, and says so plainly when it reported nothing.
func testNotSettledReason(result commandResult) string {
	if result.err != nil {
		return "what happened instead: " + result.err.Error()
	}
	return "what happened instead: it was ended by something other than its own exit"
}

// testOutputSection renders the command's output for a finding: the bounded tail,
// preceded by a marker naming what was left out and where the whole of it is,
// and followed by the evidence path.
//
// The marker is PRD section 8's requirement that a bounded projection say what
// was omitted and how to read the rest. Where there is no record to read the
// rest from, it says that instead of naming a file: a pointer to output
// nothing wrote is worse than no pointer.
//
// A command that printed nothing at all is said so rather than left as a blank
// space, and that is only said where nothing was omitted either: a tail empty
// because the projection left everything out is already explained by its
// marker, and saying nothing was written over it would contradict the line
// above.
func testOutputSection(result commandResult, record *testEvidence) string {
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

// noTestCommandConfigured is the report for a run whose configuration names no test
// command.
//
// PRD section 10 makes commands.test empty by default, so this is the ordinary
// state of a repository that has not configured one rather than a malfunction.
// It is still a stage that established nothing, and the finding is an ask
// rather than a note for that reason: a note would let a run advance past a
// stage that never checked anything, which is the one thing this stage may not
// do.
//
// It reads no state and touches no filesystem, so a run with no command
// configured holds here whatever else is wrong with the machine. That is
// deliberate: the answer does not depend on the isolated copy, so failing to
// open one would replace a hold a person can act on with an error they cannot.
func noTestCommandConfigured() findings.Report {
	return findings.Report{
		Summary: "No test command is configured, so this stage ran no check and established nothing " +
			"about this change.",
		Findings: []findings.Finding{{
			ID:       "test-no-command",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: "No test command is configured, so the test stage had nothing targeted to " +
				"run and could not gather evidence that this change does what it set out to do. " +
				"Setting commands.test in this machine's own configuration file is what gives this " +
				"stage something to run in this build: the trusted repository layer is not read " +
				"yet, so committing the key to the default branch does not reach here. The key " +
				"executes shell, so the branch under validation may name it only where " +
				"allow_pushed_commands is set, and that opt-out is refused from a pushed branch, " +
				"so a branch cannot turn it on for itself. Approving carries the run past a stage " +
				"that checked nothing; skipping records it as skipped; cancelling ends the run.",
		}},
	}
}
