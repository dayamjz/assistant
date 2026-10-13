package stages

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The judge rule: PRD section 5's "A check does not judge its own edits".
//
// A check is only as independent as the files that decide its verdict. The
// test command comes from the trusted copy, but the tests it runs, the lint
// configuration it reads, and the CI workflow definitions come from the change
// under validation, so whoever edits those can make a check pass without
// making the code right. config.Config.JudgePaths is the patterns standing for
// those files, read only from the trusted copy; a file counts only when one of
// them matches it, and the default covers the conventions its own
// documentation lists and no others. This file is the one place in this build that
// applies them. It has two halves, and both are program-built: the paths come
// from git through internal/vcs and are matched by config.PatternSet, and no
// agent's account of what it did enters either.
//
// # The pipeline may not edit the judge on its own authority
//
// A commit the pipeline authors after the review stage has read the change is
// compared against the judge paths using the paths that commit actually
// touched, and every judge path it added, changed, deleted, or renamed holds
// the stage with an ask naming the path, what happened to it, and the commit.
// In this build such a commit is a fix round of the test, lint, or checks
// stage, which Fix makes and raises the holds for through
// pipeline.FixOutput.Holds; internal/pipeline then adds them to the stage's
// report on the execution verifying that round, so no stage body can leave one
// out. Review's own fix round is not in it, because the review stage re-reads
// that round's work, and neither is rebase's, which precedes review.
//
// PRD section 5 also names the document stage's in-pass edits. The document
// stage in this build commits nothing and writes no head: it runs the
// configured command in the run's copy and reports what the command said. So
// no commit of its own exists to compare. What its command leaves in the copy
// is uncommitted, so it reaches a commit only when a later fix round's
// vcs.Repository.CommitAll takes every change in the copy, and that commit is
// then judged here like any other, under the stage whose round made it. A
// document command whose edits no later round commits does not reach the push
// stage, which pushes the run's head commit.
//
// # The author's edits are reported in review
//
// The review stage lists every judge path the change touched, with what
// happened to it, and asks the reviewer whether each edit makes a check weaker.
// Independent of that answer, each is reported: an edit or an addition as a
// note, and a judge file deleted, or renamed to a path no judge pattern
// matches, as an ask, because removing a check is the plainest way to make a
// run pass. This is part of review and adds no stage, on the same terms as
// internal/scope's lens.
//
// The review lens reads the change unfiltered by config.IgnorePatterns. That
// key is one a pushed branch may set, so a lens that honoured it would let a
// branch exclude its own tests and then delete them unseen. The reviewer is
// asked only about the judge paths it is reviewing, and the excluded ones are
// reported to the person all the same.
//
// # The residual gaps
//
//   - The judge paths are patterns. A check that reads a file outside them, such
//     as a script the test command calls, a build file, or a package manifest
//     holding test configuration, can be changed without a hold.
//   - A weakened assertion inside a judge file the change edited is reported as
//     an edit, a note, and not detected as a weakening. That judgment is the
//     reviewer's, in a finding of its own.
//   - The rule stops the pipeline changing the judge after review, not changing
//     the code: a fix round's edits outside the judge paths still reach the
//     push stage without a review, which PRD section 14 lists as open.
//   - The fixer is told the judge paths are not its to change. Nothing depends
//     on it listening, and nothing here prevents the edit: the hold is what
//     follows from it.

// judgeEdit is one judge path a commit or a change touched: the change as git
// reported it, and the judge pattern that made it one.
type judgeEdit struct {
	change  vcs.FileChange
	pattern config.Pattern
	// removed says the change takes a judge file away: a deletion, or a
	// rename to a path no judge pattern matches.
	removed bool
}

// judgeEdits returns the changes that touch a judge path, in the order git
// reported them. A change counts when its path matches, or, for a rename, when
// the path it moved from does. A copy counts only by its new path, because its
// source is left as it was. An empty set of patterns matches nothing, which is
// what turning the rule off means.
func judgeEdits(judge config.PatternSet, changed []vcs.FileChange) []judgeEdit {
	var out []judgeEdit
	for _, c := range changed {
		matched, ok := judge.Match(c.Path)
		renamedFrom := false
		if !ok && c.Status == vcs.StatusRenamed {
			matched, ok = judge.Match(c.OldPath)
			renamedFrom = ok
		}
		if !ok {
			continue
		}
		out = append(out, judgeEdit{
			change:  c,
			pattern: matched,
			removed: c.Status == vcs.StatusDeleted || renamedFrom,
		})
	}
	return out
}

// what says what happened to the path, as a phrase that reads after the
// commit or the change that did it: "deleted a_test.go".
func (e judgeEdit) what() string {
	c := e.change
	switch c.Status {
	case vcs.StatusAdded:
		return "added " + c.Path
	case vcs.StatusModified:
		return "modified " + c.Path
	case vcs.StatusDeleted:
		return "deleted " + c.Path
	case vcs.StatusRenamed:
		return "renamed " + c.OldPath + " to " + c.Path
	case vcs.StatusCopied:
		return "added " + c.Path + " as a copy of " + c.OldPath
	case vcs.StatusTypeChanged:
		return "changed the file type of " + c.Path
	default:
		return "changed " + c.Path + " (" + string(c.Status) + ")"
	}
}

// where is the path a finding about this edit is located at: the path the
// change left, or the one it took away.
func (e judgeEdit) where() string {
	if e.change.Status == vcs.StatusRenamed && e.removed {
		return e.change.OldPath
	}
	return e.change.Path
}

// judgedAfterReview reports whether a fix round of this stage is a commit the
// pipeline authors after the review stage has read the change, which is the
// set the judge rule holds. Review's own rounds are re-reviewed and the
// rebase stage's precede review, so neither is. It is read off the pipeline's
// order rather than listed, so the answer follows the one owner of it.
func judgedAfterReview(stage pipeline.Stage) bool {
	reviewAt, stageAt := -1, -1
	for i, s := range pipeline.Order() {
		switch s {
		case pipeline.StageReview:
			reviewAt = i
		case stage:
			stageAt = i
		}
	}
	return reviewAt >= 0 && stageAt > reviewAt
}

// judgeRound is the fix half of the rule: the holds a fix round's commit
// raises. before is where the copy stood before the round and committed is
// the commit the round made on top of it, so the paths compared are exactly
// the ones that commit touched.
//
// A commit whose paths cannot be listed raises a hold saying so rather than
// none, because whether it touched a judge path is then unknown and a person
// is who resolves an unknown here. A cancelled run is returned as the error it
// is: nobody would answer the hold.
func judgeRound(ctx context.Context, repo *vcs.Repository, judge config.PatternSet, stage pipeline.Stage,
	before, committed string) ([]findings.Finding, error) {
	if len(judge) == 0 || !judgedAfterReview(stage) {
		return nil, nil
	}
	changed, err := repo.ChangedFiles(ctx, before, committed)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, errors.Join(ctxErr, err)
		}
		return []findings.Finding{{
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("The %s stage's fix round committed %s, and the paths that "+
				"commit touched could not be listed (%v), so whether it changed a file that decides "+
				"whether a check passes is not established. Approving accepts the commit without "+
				"that being known.", stage, committed, err),
		}}, nil
	}
	edits := judgeEdits(judge, changed)
	holds := make([]findings.Finding, 0, len(edits))
	for _, edit := range edits {
		holds = append(holds, findings.Finding{
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Location: findings.Location{Path: edit.where()},
			Description: fmt.Sprintf("The %s stage's fix round committed %s, which %s. That is a "+
				"judge path (it matches %q): a file that decides whether a check passes, and the "+
				"pipeline may not change one on its own authority. This was decided from the paths "+
				"the commit touched, not from what the fixer said it did. Approving accepts the "+
				"edit; cancelling ends the run with the work recoverable.",
				stage, committed, edit.what(), edit.pattern.String()),
		})
	}
	return holds, nil
}

// judgeFixGuidance is the sentence the fixer is told about the judge paths. It
// is a courtesy: the hold judgeRound raises does not depend on it being read.
// It is empty where the rule does not apply to this round.
func judgeFixGuidance(judge config.PatternSet, stage pipeline.Stage) string {
	if len(judge) == 0 || !judgedAfterReview(stage) {
		return ""
	}
	return "The files matching these patterns decide whether a check passes, and they are not " +
		"yours to change: " + strings.Join(judge.Strings(), ", ") + ". Fix the code, not the " +
		"check. If a finding can only be resolved by editing one of them, leave it and say so; " +
		"a commit touching one of them holds the run for a person whatever you report."
}

// judgeReviewGuidance is the review half's question to the reviewer: every
// judge path the reviewable part of the change touched, with what happened to
// it, and the ask that the reviewer say whether each makes a check weaker. It
// is empty when the change touches no reviewable judge path, so the section
// is absent rather than asking about nothing.
//
// It says what is reported regardless of the answer, so a reviewer is not
// invited to report a path merely for appearing here.
func judgeReviewGuidance(edits []judgeEdit, excluded []string) string {
	skip := make(map[string]struct{}, len(excluded))
	for _, path := range excluded {
		skip[path] = struct{}{}
	}
	var asked []judgeEdit
	others := 0
	for _, edit := range edits {
		if _, out := skip[edit.where()]; out {
			others++
			continue
		}
		asked = append(asked, edit)
	}
	if len(asked) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Judge paths: this change touches " + strconv.Itoa(len(asked)) + " file(s) " +
		"matched by the repository's judge paths, the patterns naming files that decide whether " +
		"a check passes. What the change did to each:\n")
	for _, edit := range asked {
		b.WriteString("\n  - " + edit.what())
	}
	b.WriteString("\n\nFor each, say whether the edit makes a check weaker: an assertion loosened, " +
		"a case removed, a test skipped, a rule switched off, a job dropped from CI. Report a " +
		"weakening as a finding of your own, with the action you judge it deserves. Each of these " +
		"paths is already reported to the person - an edit or an addition as a note, and a " +
		"deletion or a move out of the judge paths as an ask - so do not report a path only for " +
		"being on this list.")
	if others > 0 {
		b.WriteString(" " + strconv.Itoa(others) + " further judge path(s) are among the paths " +
			"excluded from this review, and are reported to the person directly.")
	}
	return b.String()
}

// judgeReviewFindings is the review half's report: one finding per judge path
// the change touched, built here rather than parsed from the reviewer, so a
// reviewer that says nothing about a path does not make it disappear. An edit
// or an addition is a note; a removal is an ask, because whether a check was
// meant to go is the author's call and approving is how it is made.
func judgeReviewFindings(edits []judgeEdit) []findings.Finding {
	out := make([]findings.Finding, 0, len(edits))
	for _, edit := range edits {
		if edit.removed {
			out = append(out, findings.Finding{
				Severity: findings.SeverityWarning,
				Action:   findings.ActionAsk,
				Location: findings.Location{Path: edit.where()},
				Description: fmt.Sprintf("This change %s, taking away a judge path (it matched %q): "+
					"a file that decides whether a check passes. Removing a check is the plainest "+
					"way to make a run pass, so whether it was meant to go is yours to decide. "+
					"Approving accepts the removal.", edit.what(), edit.pattern.String()),
			})
			continue
		}
		out = append(out, findings.Finding{
			Severity: findings.SeverityInfo,
			Action:   findings.ActionNote,
			Location: findings.Location{Path: edit.where()},
			Description: fmt.Sprintf("This change %s, a judge path (it matches %q): a file that "+
				"decides whether a check passes. This is reported so it is seen and blocks "+
				"nothing; whether the edit makes a check weaker is the reviewer's to say in a "+
				"finding of its own.", edit.what(), edit.pattern.String()),
		})
	}
	return out
}
