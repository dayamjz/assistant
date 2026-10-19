package service

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The records kept beside a run's execution, and why they are kept here.
//
// Two facts about a run are not the run's graph state and not any stage's to
// write: when each stage execution started and ended, and what the run did
// next with a path a decision-model answer was about. Both are history in the
// store, and both are written here, around the bodies, because this is the one
// place that holds the store and builds the topology the bodies run in. A body
// stays unaware of either, and neither ever enters graph state: a time there
// would differ on every execution and defeat the convergence bound, which
// reads that state as the run's fingerprint.
//
// What is recorded around each:
//
//   - A stage execution: its start and end, appended once the body returns,
//     whether it returned a report or an error. A stage the run skips runs no
//     body and has no row.
//   - A stage's findings: each path a finding is located at is a follow-up,
//     of kind finding, of every answer about that path the run recorded
//     before the execution began. Answers the execution itself produced are
//     after that mark, so the review's own flagged note is not read as
//     something that followed it.
//   - A fix round: each path its commit touched is a follow-up, of kind fix,
//     of every answer about that path recorded before the round began.
//   - Which stage an agent call served: each body and each fix round runs on
//     a context marked with its stage, which contain.go's wrapper stamps on
//     every invocation it launches, so the agent invocation record names it.
//   - A person's answer at a hold: respond records each path a held finding
//     is located at as a follow-up, of kind answer, once the answer is
//     accepted.
//   - The stage's log: when each body and each fix round began and ended,
//     with what it returned, written to PRD section 8's per-stage log, which
//     stagelog.go owns. It is evidence, never read back here, and never
//     state.
//
// The residual gaps:
//
//   - Recording is best-effort. A row the store refuses, and a fix commit
//     whose paths cannot be listed, are logged and dropped, and nothing on the
//     run's report says so: the answer rows are the record the review names a
//     loss of, and these are not. A dropped follow-up understates what
//     followed, which leans toward the proposal to turn a check off; that
//     proposal is a note and never changes the mode. A run that ended before
//     anything could follow would lean the same way, which is why the
//     proposal counts only passed runs and leaves failed and terminated ones
//     out; decision.go says what that costs.
//   - A later finding at the path counts whatever produced it, including the
//     decision-model checks of a later review re-flagging the same file - the
//     push stage's review of commits written after review is one, since it
//     runs the review stage's own read - so the record can overstate what
//     followed an answer. That leans away from the proposal, which is the
//     direction a conservative rule wants.
//   - A finding names a path only through its location; one that names the
//     path only in its text is not a follow-up of it.
//   - Only what a body returns is read. The holds internal/pipeline adds to a
//     stage's report after its body returns - a fix round's - are not, though
//     the round that raised them is recorded as a fix follow-up of every path
//     its commit touched.
//   - An execution a hard kill interrupted has no row, so the record
//     understates how often a stage ran rather than holding an end nobody
//     observed.
//   - An answer standing consent gives at a hold is not recorded as a
//     follow-up of kind answer, because that kind is a person's answer and
//     consent's is the program's. A round it asks for is still recorded as a
//     fix follow-up of every path the round's commit touched. This stays a
//     stated gap, by the owner's decision on #253, until this record keeps
//     follow-ups for any program-side answer.

// observeStages wraps every one of the nine stages' bodies so their
// executions are recorded, walking the stage table rather than naming fields
// so a stage cannot be missed.
func (s *Service) observeStages(st pipeline.Stages) pipeline.Stages {
	for _, stage := range pipeline.Order() {
		impl := st.At(stage)
		*impl = s.observeStage(*impl)
	}
	return st
}

// observeStage wraps one implementation. It adds pipeline.KeyRun to the
// declared reads, because the record is the run's; that also lets the body
// read the run's identifier, a run input any body may declare anyway, and
// widens the declaration by nothing else.
func (s *Service) observeStage(impl pipeline.Implementation) pipeline.Implementation {
	if impl.NewBody == nil {
		return impl
	}
	inner := impl.NewBody
	impl.Reads = withKeys(impl.Reads, pipeline.KeyRun)
	impl.NewBody = func() pipeline.Body {
		body := inner()
		return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
			run := readText(in.State, pipeline.KeyRun)
			mark := s.answerMark(run)
			started := time.Now()
			s.logBodyBegan(run, in.Stage, started)
			// A usage ceiling refusal is a hold for a person rather than a
			// failed step; ceiling.go says why.
			out, err := heldAtCeiling(body(withServing(ctx, in.Stage.String()), in))
			// The end is the start plus the monotonic time elapsed, so a wall
			// clock stepped back mid-execution cannot record an end before
			// its start.
			ended := started.Add(time.Since(started))
			s.recordExecution(run, in.Stage, started, ended)
			s.logBodyEnded(run, in.Stage, started, ended, out.Report, err)
			if err == nil {
				s.recordFindings(run, in.Stage, out.Report, mark)
			}
			return out, err
		}
	}
	return impl
}

// observeFixer wraps the fix body so each round's commit is recorded as a
// follow-up of the answers about the paths it touched. open is how the run's
// isolated copy is reached, which is where the commit lives.
func (s *Service) observeFixer(fixer pipeline.Fixer,
	open func(ctx context.Context, repositoryID, runID string) (*vcs.Repository, error)) pipeline.Fixer {
	if fixer.NewBody == nil {
		return fixer
	}
	inner := fixer.NewBody
	fixer.Reads = withKeys(fixer.Reads, pipeline.KeyRun, pipeline.KeyRepository, pipeline.KeyHead)
	fixer.NewBody = func() pipeline.FixBody {
		body := inner()
		return func(ctx context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
			run := readText(in.State, pipeline.KeyRun)
			before := readText(in.State, pipeline.KeyHead)
			mark := s.answerMark(run)
			started := time.Now()
			s.logRoundBegan(run, in, started)
			out, err := body(withServing(ctx, in.Stage.String()), in)
			out, err = roundHeldAtCeiling(in.Stage, out, err)
			s.logRoundEnded(run, in.Stage, started, started.Add(time.Since(started)), out, err)
			if err != nil {
				return out, err
			}
			after, _ := out.Writes[pipeline.KeyHead].Text()
			if after == "" || after == before || before == "" || mark == 0 {
				return out, nil
			}
			repo, openErr := open(s.recordCtx(), readText(in.State, pipeline.KeyRepository), run)
			if openErr != nil {
				s.log.Printf("not recording what run %s's %s fix round touched: %v", run, in.Stage, openErr)
				return out, nil
			}
			changed, listErr := repo.ChangedFiles(s.recordCtx(), before, after)
			if listErr != nil {
				s.log.Printf("not recording what run %s's %s fix round touched: %v", run, in.Stage, listErr)
				return out, nil
			}
			var paths []string
			for _, c := range changed {
				paths = append(paths, c.Path)
				if c.OldPath != "" {
					paths = append(paths, c.OldPath)
				}
			}
			for _, path := range dedupe(paths) {
				s.followUp(run, path, store.FollowUpFix, in.Stage.String(), after, mark)
			}
			return out, nil
		}
	}
	return fixer
}

// answerMark is the latest answer recorded for run, taken before the work
// whose follow-ups are about to be recorded. A mark that cannot be read is
// zero, which follows nothing: the record understates rather than attaching
// work to answers that work produced.
func (s *Service) answerMark(run string) int64 {
	if run == "" || s.store == nil {
		return 0
	}
	mark, err := s.store.LastDecisionAnswer(s.recordCtx(), run)
	if err != nil {
		s.log.Printf("not recording decision-model follow-ups for run %s: %v", run, err)
		return 0
	}
	return mark
}

// recordExecution appends one stage execution, logging a row the store
// refuses.
func (s *Service) recordExecution(run string, stage pipeline.Stage, started, ended time.Time) {
	if run == "" || s.store == nil {
		return
	}
	if _, err := s.store.AppendStageExecution(s.recordCtx(), store.StageExecution{
		Run: run, Stage: stage.String(), Started: started, Ended: ended,
	}); err != nil {
		s.log.Printf("dropping the record of run %s's %s stage execution: %v", run, stage, err)
	}
}

// recordFindings records each path a finding of report is located at as a
// follow-up of the answers before mark, with the actions of the findings
// there as the detail. The report is read as the pipeline will normalize it,
// so an action P3 turns into ask is recorded as ask.
func (s *Service) recordFindings(run string, stage pipeline.Stage, report findings.Report, mark int64) {
	if mark == 0 {
		return
	}
	actions := map[string][]string{}
	for _, f := range report.Normalize().Findings {
		path := strings.TrimSpace(f.Location.Path)
		if path == "" {
			continue
		}
		if !slices.Contains(actions[path], string(f.Action)) {
			actions[path] = append(actions[path], string(f.Action))
		}
	}
	for _, path := range sortedKeys(actions) {
		sort.Strings(actions[path])
		s.followUp(run, path, store.FollowUpFinding, stage.String(), strings.Join(actions[path], ","), mark)
	}
}

// recordAnswered records a person's answer at a hold over findings located at
// paths, as a follow-up of the answers before mark.
func (s *Service) recordAnswered(run, stage, answer string, paths []string, mark int64) {
	if mark == 0 {
		return
	}
	for _, path := range dedupe(paths) {
		s.followUp(run, path, store.FollowUpAnswer, stage, answer, mark)
	}
}

// heldPaths returns the stage holding a run and the paths its held findings
// are located at, read from the run's latest checkpoint, with mark the latest
// answer recorded before the person answered. A run with no readable hold has
// none.
func (s *Service) heldPaths(ctx context.Context, runID string) (string, []string, int64) {
	latest, err := s.checkpoints.Latest(ctx, runID)
	if err != nil || latest.Decision == nil {
		return "", nil, 0
	}
	view := decisionView(*latest.Decision, latest.State)
	var paths []string
	for _, f := range view.Findings {
		if path := strings.TrimSpace(f.Location.Path); path != "" {
			paths = append(paths, path)
		}
	}
	if view.Stage == "" || len(paths) == 0 {
		return "", nil, 0
	}
	return view.Stage, paths, s.answerMark(runID)
}

// followUp appends one follow-up, logging a row the store refuses.
func (s *Service) followUp(run, path string, kind store.FollowUpKind, stage, detail string, mark int64) {
	if _, err := s.store.AppendDecisionFollowUp(s.recordCtx(), run, path, kind, stage, detail, mark); err != nil {
		s.log.Printf("dropping a decision-model follow-up of run %s at %s: %v", run, path, err)
	}
}

// readText reads a text key the wrapper added to the declaration, empty when
// it is unset.
func readText(state pipeline.Reader, key pipeline.Key) string {
	value, err := state.Get(key)
	if err != nil {
		return ""
	}
	text, _ := value.Text()
	return strings.TrimSpace(text)
}

// withKeys adds keys to a declaration that does not already hold them.
func withKeys(declared []pipeline.Key, keys ...pipeline.Key) []pipeline.Key {
	out := slices.Clone(declared)
	for _, key := range keys {
		if !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	return out
}

// dedupe returns the distinct non-empty values, sorted.
func dedupe(values []string) []string {
	var out []string
	for _, v := range values {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// sortedKeys returns a map's keys in order.
func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stageTimes is what the store holds about when a run's stages executed,
// grouped by stage.
func stageTimes(executions []store.StageExecution) map[string][]store.StageExecution {
	out := map[string][]store.StageExecution{}
	for _, e := range executions {
		out[e.Stage] = append(out[e.Stage], e)
	}
	return out
}
