package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/store"
)

// decisionRecorder feeds the store's decision-model record, which is PRD
// section 5's record of every answer the checks got. It is the recorder the
// service's judge.Client is built with, so every answer a review stage records
// through that client lands one row.
//
// It holds the service rather than the store for the reason
// invocationRecorder does: the client is built before the store is open, and
// it records only once a run reviews something, by which time the store is
// the open one.
//
// A row the store refuses is logged, naming the answer and why, and returned,
// and the review stage says in a note on the run's report that the record
// lost answers, so the record understating is visible where the run is read
// and not only in the service log. It is not retried, and no decision a run
// makes reads the record.
type decisionRecorder struct {
	s *Service
}

// RecordAnswer stores one answer. It runs on the reviewing goroutine under the
// service's stop context, so a service that is closing does not block a
// review's last record.
func (r decisionRecorder) RecordAnswer(rec judge.Record) error {
	st := r.s.store
	if st == nil {
		return fmt.Errorf("service: the store is not open")
	}
	probability := store.Unknown[float64]()
	if rec.Answered {
		probability = store.Known(rec.Probability)
	}
	_, err := st.AppendDecisionAnswer(r.s.recordCtx(), store.DecisionAnswer{
		Run:         rec.Run,
		Stage:       rec.Stage,
		Check:       string(rec.Check),
		Path:        rec.Path,
		Model:       rec.Model,
		Probability: probability,
		Cutoff:      rec.Cutoff,
		Failure:     string(rec.Failure),
		Finding:     rec.Finding,
	})
	if err != nil && r.s.log != nil {
		r.s.log.Printf("dropping run %s's decision-model answer of %s about %s: %v",
			rec.Run, rec.Check, rec.Path, err)
	}
	return err
}

// proposalWindow is how many of a check's most recent firing runs the
// proposal to turn it off reads. It is deliberately conservative: a check
// that fired in fewer counted runs of the repository than this is never
// proposed off, and one is proposed off only when nothing followed any of its
// firings in every one of them, so a single firing a person or a later stage
// acted on anywhere in the window keeps the proposal back. It was not
// measured; it is a count large enough that its firings going unanswered run
// after run is a pattern rather than a quiet week.
const proposalWindow = 10

// proposals is PRD section 5's "Turning a check off is proposed, never
// applied", on the terms of "Recurring findings become a proposal, never a
// rule": for each check that answered in this run, a note proposing to turn
// the checks off when the check fired in each of the proposalWindow most
// recent counted runs of the repository in which it fired, and nothing
// followed any of those firings. The note names the window and the runs, holds
// nothing, and changes no mode.
//
// It is raised on the run's answer, read from the store when the answer is
// built, and never written into the review's report. That report is graph
// state the convergence bound fingerprints, and this note's text moves
// whenever another run of the repository finishes, so in the report it would
// keep a fix loop that changed nothing from converging. The cost is that the
// proposal reflects the record as it stands when the run is read, so the same
// run can carry it on one reading and not on a later one.
//
// A run counts only when it passed, which is a run that went through every
// stage, every hold, and every fix round where a follow-up could have been
// recorded. A run that failed or was terminated - cancelled, superseded, or
// stopped - is left out even when it got past its review, because the status
// cannot say whether it did; leaving such a run out only delays a proposal. A
// run still in progress is left out because what follows may not have
// happened yet, and so is the run being read.
//
// A record that cannot be read is said in a note rather than treated as a
// check that never fired, and the service log says why.
func (s *Service) proposals(ctx context.Context, record store.Run) []findings.Finding {
	answers, err := s.store.DecisionAnswers(ctx, record.ID)
	if err != nil {
		s.log.Printf("not reading run %s's decision-model record for a proposal: %v", record.ID, err)
		return []findings.Finding{unreadableRecord()}
	}
	var checks []string
	for _, a := range answers {
		if !containsString(checks, a.Check) {
			checks = append(checks, a.Check)
		}
	}
	var out []findings.Finding
	for _, check := range checks {
		rows, err := s.store.DecisionFirings(ctx, record.RepositoryID, check)
		if err != nil {
			s.log.Printf("not reading the decision-model record of check %s for a proposal: %v", check, err)
			out = append(out, unreadableRecord())
			continue
		}
		var counted []store.DecisionFiring
		for _, row := range rows {
			if row.Run != record.ID && row.Status == store.RunPassed {
				counted = append(counted, row)
			}
		}
		if proposal, ok := proposeOff(check, counted); ok {
			out = append(out, proposal)
		}
	}
	return out
}

// proposeOff is the rule: the check's proposalWindow most recent counted
// firing runs, and only when there are that many and nothing followed any
// firing in any of them, a note naming the window and the runs.
func proposeOff(check string, firings []store.DecisionFiring) (findings.Finding, bool) {
	if len(firings) < proposalWindow {
		return findings.Finding{}, false
	}
	window := firings[:proposalWindow]
	runIDs := make([]string, 0, len(window))
	fired := 0
	for _, f := range window {
		if f.Followed > 0 || f.Fired <= 0 {
			return findings.Finding{}, false
		}
		fired += f.Fired
		runIDs = append(runIDs, f.Run)
	}
	return findings.Finding{
		Severity: findings.SeverityInfo,
		Action:   findings.ActionNote,
		Description: "Proposal: the decision model's " + check + " check fired " + strconv.Itoa(fired) +
			" time(s) across the " + strconv.Itoa(proposalWindow) + " most recent passed runs of this " +
			"repository in which it fired, and nothing followed any of those firings - no later finding " +
			"at the path, no fix round touching it, and no person's answer at a hold over it. Runs that " +
			"failed or were terminated are not counted. Those runs are the window: " +
			strings.Join(runIDs, ", ") + ". Check them rather than taking the count on faith. If the " +
			"check is not earning its place here, the operator can turn the checks off by setting " +
			"decision_model to off in the global configuration, which is one setting for both checks. " +
			"Nothing here changes it: the mode changes only when the operator changes it. This is " +
			"informational and holds nothing.",
	}, true
}

// unreadableRecord is the note for a record the proposal could not read.
func unreadableRecord() findings.Finding {
	return findings.Finding{
		Severity: findings.SeverityInfo,
		Action:   findings.ActionNote,
		Description: "Whether the decision-model record shows a check firing with nothing following " +
			"could not be read, so no proposal to turn it off was considered; the service log says " +
			"why. This is informational and holds nothing.",
	}
}

// containsString reports whether values holds v.
func containsString(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

// recordCtx is the context the records this service keeps beside a run are
// written under: the service's stop context, so a service that is closing
// does not block a stage's last record, and the background otherwise.
func (s *Service) recordCtx() context.Context {
	if s.stopCtx == nil {
		return context.Background()
	}
	return s.stopCtx
}
