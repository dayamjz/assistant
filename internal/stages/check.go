package stages

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// checkProjectionBytes bounds the command output that travels in a stage's
// report. The whole output goes to the run's evidence file, and what a person
// or an agent is shown is a bounded projection of it carrying an explicit
// marker for what was left out.
//
// That evidence file is not offered as the authoritative full output. PRD
// section 8 gives that role to logs/<run>/<stage>.log, which nothing in this
// build writes, and calls evidence/<run> the run's evidence instead. Naming
// the record here as the authority would give one fact two owners and would
// settle a convention for all nine stages from inside whichever body landed
// next, so this file describes what it writes and claims no more for it.
const checkProjectionBytes = 8 << 10

// configuredCheck is what one run of one configured command in the run's
// isolated copy produced: the commit it ran against, the record of its output,
// and what the command answered.
//
// It reports and classifies nothing. Which exit status means what is the
// stage's own judgement - a status the test stage calls a failure to fix is
// one the lint stage may call a tool that could not run - so every field here
// is a fact and the verdict is built from them by the stage.
type configuredCheck struct {
	// commit is the commit in the isolated copy when the command started.
	commit string
	// record is where the whole output went, and what went wrong if it could
	// not be written there.
	record *checkEvidence
	// result is what the command answered.
	result commandResult
}

// runConfiguredCheck runs one stage's configured command line in the run's
// isolated copy and records everything it wrote.
//
// It is the one owner of that sequence, which the test and lint stages both
// need and which differs between them only in the command and in what its
// answer is taken to mean. A second copy of it would be a second set of
// decisions about where evidence goes and which commit a report names.
//
// # Where the command must have come from
//
// The command is the caller's, and every caller reads it from StageDeps.Config
// and from nowhere else. A configured command executes shell, so P7 puts it on
// the trusted side: it belongs to the default branch and never to the branch
// under validation, and nothing here opens a configuration file in the
// isolated copy.
//
// That is the half of P7 a stage owns. The other half is that what it was
// handed was itself resolved from a trusted commit, and that half belongs to
// whoever resolves the configuration; StageDeps.Config says where this build's
// value comes from and what of PRD section 10 it is short of. Refusing to be a
// second way in is what reading only what was given amounts to, and it is all
// this function claims.
//
// # Where the line between an error and a finding falls
//
// This returns an error only where no check could have run: the run's own
// state, the isolated copy, and the commit in it are the build's machinery, so
// a failure in any of them is not the change's fault and there is nothing to
// report about the change. A cancelled run is an error for the same reason and
// is returned as the context's own, so a caller can recognize it.
//
// The command's own failure is the other side of that line and is not an error
// here. It ran, or it could not be started, and either way there is something
// to report, so the result carries it back and the stage decides what it means.
//
// # Evidence
//
// PRD section 8 puts a stage's evidence at evidence/<run>, deliberately
// outside the isolated copy, so an artifact of checking never becomes part of
// the change being validated. The record is written there and never into the
// copy, and it is written while the command runs, so it survives a command
// this build gives up waiting on.
//
// Filing that record is never allowed to decide the verdict, which is why a
// failure to write it is carried on the record rather than returned. A stage
// reports it as a note and reports what the check said; the alternative is a
// gate that stops validating when a disk fills.
func runConfiguredCheck(ctx context.Context, deps StageDeps, in pipeline.Input, command string) (configuredCheck, error) {
	repositoryID, runID, err := readRunIdentity(in.State)
	if err != nil {
		return configuredCheck{}, err
	}
	copied, err := deps.Copy(ctx, repositoryID, runID)
	if err != nil {
		return configuredCheck{}, err
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
		return configuredCheck{}, fmt.Errorf(
			"stages: reading the commit the %s stage would check in %s: %w", in.Stage, copied.Path(), err)
	}

	// deps.Copy refused a nil home above, so the evidence path can be composed
	// here without asking again. Both the file and the section header are
	// named after the stage rather than stated per caller, so a stage's
	// evidence is where its name says and no second row has to agree.
	label := in.Stage.String() + " stage"
	record := openCheckEvidence(filepath.Join(deps.Home.Evidence(runID), in.Stage.String()+".log"), label)
	record.header(command, commit, copied.Path())
	result := runCommand(ctx, commandSpec{
		command:    command,
		dir:        copied.Path(),
		record:     record,
		projection: checkProjectionBytes,
	})
	record.footer(result)
	record.close()
	if err := ctx.Err(); err != nil {
		return configuredCheck{}, err
	}
	return configuredCheck{commit: commit, record: record, result: result}, nil
}

// readRunIdentity reads the two facts locating a run's working area: which
// repository it validates and which run it is. They are what the isolated copy
// and the evidence directory are derived from, and neither is derivable from
// the other.
func readRunIdentity(state pipeline.Reader) (repositoryID, runID string, err error) {
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

// checkEvidence is a stage's evidence file for one run, and what went wrong if
// it could not be written.
//
// Its Write never reports an error, which is the whole reason it exists. The
// command's output goes to this and to the report's bounded tail through one
// io.MultiWriter, and a MultiWriter stops at the first writer that fails, so a
// file that could not be written would otherwise cut the command's output
// short and end its run with an error - turning a full disk into a verdict.
// The first failure is kept here instead and reported as a note.
type checkEvidence struct {
	// path is where the record was to be written, and is what the report
	// names when there is a record to name.
	path string
	// label names the stage in each attempt's header, so a reader of a file
	// can tell what wrote it.
	label string
	// file is the open file, nil when it could not be opened.
	file *os.File
	// err is the first thing that went wrong: opening it, writing to it, or
	// closing it. It is nil when the record is whole.
	err error
}

// openCheckEvidence opens a stage's evidence file for appending, creating the
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
func openCheckEvidence(path, label string) *checkEvidence {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &checkEvidence{path: path, label: label,
			err: fmt.Errorf("making the evidence directory %s: %w", dir, err)}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return &checkEvidence{path: path, label: label, err: fmt.Errorf("opening %s: %w", path, err)}
	}
	return &checkEvidence{path: path, label: label, file: file}
}

// Write implements io.Writer and never fails. It reports every byte consumed
// whatever became of them, so the writer it is combined with sees the whole
// output.
func (e *checkEvidence) Write(p []byte) (int, error) {
	if e.file == nil || e.err != nil {
		return len(p), nil
	}
	if _, err := e.file.Write(p); err != nil {
		e.err = fmt.Errorf("writing %s: %w", e.path, err)
	}
	return len(p), nil
}

// header opens one attempt's section, so a reader of a file holding several
// attempts can tell which command ran against which commit in which directory.
//
// It discards what Write answered, here and in footer, because Write never
// fails: a failure is kept on the record itself and read off recorded, so
// there is nothing at this call to handle.
func (e *checkEvidence) header(command, commit, dir string) {
	_, _ = fmt.Fprintf(e, "=== %s\n=== command: %s\n=== commit:  %s\n=== copy:    %s\n",
		e.label, command, commit, dir)
}

// footer closes one attempt's section with what the command answered.
func (e *checkEvidence) footer(result commandResult) {
	switch {
	case result.exited:
		_, _ = fmt.Fprintf(e, "\n=== exit status: %d\n\n", result.code)
	case result.err != nil:
		_, _ = fmt.Fprintf(e, "\n=== no exit status: %v\n\n", result.err)
	default:
		_, _ = fmt.Fprint(e, "\n=== no exit status\n\n")
	}
}

// close finishes the record, keeping a failure to flush on the same terms as a
// failure to write.
func (e *checkEvidence) close() {
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
func (e *checkEvidence) recorded() bool { return e.err == nil }

// unrecordedNote is the finding a stage adds when the command's output could
// not be filed. It is a note, because the check itself answered and that
// answer stands; what is lost is the output beyond the bounded extract the
// report carries. A stage that made this a warning or an error would let a
// full disk decide whether a change is good.
//
// The identifier is built from the stage, so a stage's own findings are filed
// under its own name without a second table saying which name that is.
func (e *checkEvidence) unrecordedNote(stage pipeline.Stage) findings.Finding {
	return findings.Finding{
		ID:       stage.String() + "-evidence-unrecorded",
		Severity: findings.SeverityWarning,
		Action:   findings.ActionNote,
		Description: fmt.Sprintf(
			"The command's full output could not be recorded, so this run has no evidence file "+
				"and the report names none. What the check answered is unaffected and is reported "+
				"above; what is lost is everything the command printed beyond the bounded extract "+
				"in this report.\n\nwhat went wrong: %v", e.err),
	}
}

// notSettledReason renders what os/exec reported about a command that never
// gave a status, and says so plainly when it reported nothing.
func notSettledReason(result commandResult) string {
	if result.err != nil {
		return "what happened instead: " + result.err.Error()
	}
	return "what happened instead: it was ended by something other than its own exit"
}

// checkOutputSection renders the command's output for a finding: the bounded
// tail, preceded by a marker naming what was left out and where the whole of
// it is, and followed by the evidence path.
//
// The marker is the discipline PRD section 8 asks of a bounded projection -
// name what was omitted and how to read the rest - applied to the record this
// package does write. It does not make that record the authoritative log
// section 8 names; checkProjectionBytes says where that stands. Where there is
// no record to read the rest from, the marker says that instead of naming a
// file: a pointer to output nothing wrote is worse than no pointer.
//
// A command that printed nothing at all is said so rather than left as a blank
// space, and that is only said where nothing was omitted either: a tail empty
// because the projection left everything out is already explained by its
// marker, and saying nothing was written over it would contradict the line
// above.
func checkOutputSection(result commandResult, record *checkEvidence) string {
	var b strings.Builder
	if result.omitted > 0 {
		if record.recorded() {
			fmt.Fprintf(&b, "[%d bytes of earlier output omitted; the whole of it is at %s]\n",
				result.omitted, record.path)
		} else {
			fmt.Fprintf(&b, "[%d bytes of earlier output omitted, and the record that would hold "+
				"them could not be written, so they are gone]\n", result.omitted)
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
