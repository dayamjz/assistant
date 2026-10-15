package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// Standing consent: PRD section 9's "unless standing consent to resolve
// unattended was given".
//
// # Who may give it, and how
//
// Two things together, and neither alone. The repository allows it with
// config.KeyAllowStandingConsent, read from the trusted copy at a fresh fetch
// of the default branch; the key is trusted-only, so a pushed branch cannot
// allow it for itself (P7). And the per-run flag on the bare command asks for
// it for the run that call starts or attaches to. A flag the repository does
// not allow is refused with ErrStandingConsentNotAllowed, as an invalid
// request, before any run is recorded or touched, and the refusal names the
// remedy. A trusted copy that cannot be fetched, read or parsed fails the
// request with the error trustedLayerAt gives a run for the same thing,
// rather than reading the key as false or true.
//
// Once given, it is the run record's: store.Run.StandingConsent, written once
// and never withdrawn, so a run whose decisions the program answered keeps
// saying so. The key is read again for every segment that would answer, from
// the run's own resolution, so a repository that withdraws the key stops the
// consent from answering anything further in its runs, and the hold waits for
// a person.
//
// # What it answers
//
// The way no-mistakes' --yes does, and only at a hold of a stage whose hold
// offers a requested round, which pipeline.Stage.Requestable owns: intent
// through lint. A hold whose recorded report carries any finding that holds or
// is fix-eligible, by findings.Finding.Holds and FixEligible, is answered with
// a fix round selecting every such finding, ask included, and leaving notes
// out. The one ask it does not send to a round is the fix review: the hold a
// requested round leaves so its result is seen, which
// pipeline.FixReviewFinding tells from any other ask. A hold carrying nothing
// but notes and that review is approved, which is accepting the fix review
// and approving a hold of notes alike. Nothing else is answered: not skip, not
// cancel, and not a hold whose report reads as neither.
//
// # What it never answers
//
// The push, pull request and checks holds, because those stages are not
// requestable and this answers nothing that is not. That line is also where
// this build's P6 and P7 refusals land, so it is what keeps consent from
// answering past one: P6's refusals are the push stage's (an anchor missing or
// for another target, a head no review approved, a target that moved) and
// hold there; P7's are refusals to run at all, before any stage launches, so
// there is no hold to answer. A pre-push stage that later grows a P6 or P7
// refusal of its own would be answered with a fix round here, and nothing in
// this file would notice; that stage would need to raise its refusal where
// this does not reach.
//
// # Bounds
//
// Consent adds no bound and lifts none. A fix answer travels the requested
// round's own edges, so pipeline.RequestedFixRounds and the run-wide step
// budget bound it, and an answer internal/graph refuses for either is taken
// as the consent stopping: the hold stays open for a person and the refusal is
// logged. A park ends the run as it always does; consent answers holds and a
// park is not one.
//
// # How each answer is recorded
//
// Every answer is a hold row registered and resolved together by
// store.RecordHoldResolution, keyed by the run and the checkpoint the decision
// stood at, resolved as store.ResolvedByStandingConsent: the program's answer,
// never a person's and never a caller's. It is written once the run has moved
// past that checkpoint, which is what makes it an answer that was applied
// rather than one that was offered. Request.HoldResolver is not consulted,
// because nothing on the socket answered this: the program did.
//
// # Residual gaps
//
//   - The record is written after the answer is applied, so a service that
//     dies between the two leaves an applied answer with no hold row. The run's
//     record still says it was under standing consent, so the history
//     understates which decisions consent answered rather than claiming one
//     a person made.
//   - A report the review stage recorded no longer says whether an ask was
//     stated by the reviewer or is P3's default for an action nobody could
//     read, so both are sent to the requested round alike. P3 is held by the
//     finding stopping at a hold rather than entering the automatic loop; the
//     consent then answers that hold as the operator consented it would.
//   - What PRD section 5 has the decision-model record keep about a person's
//     answer at a hold is not written for a consent answer, because it is not
//     a person's: the follow-up kind that record keeps is a person's answer.
//   - The requested round's cycle is exempt from the convergence bound on the
//     ground that a person asked for each round. Under consent the program
//     asked, so RequestedFixRounds and the budget are what stop a round that
//     changes nothing.

// ErrStandingConsentNotAllowed reports a request for standing consent on a
// run of a repository whose trusted configuration does not allow it. It
// travels as ipc.ErrInvalidRequest, because the remedy is the caller's: a key
// on the default branch, or the request without the flag.
var ErrStandingConsentNotAllowed = errors.New("service: standing consent was asked for and this repository's trusted configuration does not allow it")

// standingConsentRefused is the refusal for a repository that does not allow
// standing consent, naming the key, the document and the branch that would.
func standingConsentRefused(repository store.Repository) error {
	return fmt.Errorf("%w: %w: set %q to true in %s on %s, the branch the trusted copy is read from, "+
		"or ask without standing consent; a pushed branch cannot set it, so the branch under validation "+
		"carrying it does not count", ipc.ErrInvalidRequest, ErrStandingConsentNotAllowed,
		string(config.KeyAllowStandingConsent), config.RepositoryDocument, repository.DefaultBranch)
}

// consentTrustedRef is the reference the default branch's tip is fetched
// under for one check of whether a repository allows standing consent. It is
// named for the check rather than for a run because the check precedes the
// run, and it is removed as soon as the check has read it.
func consentTrustedRef(check string) string { return "refs/assistant/trusted/consent/" + check }

// standingConsentAllowed reads whether repository allows standing consent:
// its trusted copy at a fresh fetch of the default branch, resolved with the
// operator's layer and no pushed layer, on projectPosture's terms. A trusted
// copy that cannot be read is an error wrapping ErrTrustedConfig, never an
// answer either way.
func (s *Service) standingConsentAllowed(ctx context.Context, repository store.Repository) (bool, error) {
	check, err := newRunID()
	if err != nil {
		return false, err
	}
	forWhat := "standing consent for " + repository.ID
	bare, err := s.gateRepositoryOf(ctx, repository, forWhat)
	if err != nil {
		return false, err
	}
	ref := consentTrustedRef(check)
	defer func() { _ = bare.DeleteRef(context.WithoutCancel(ctx), ref) }()
	trusted, _, _, err := s.trustedLayerAt(ctx, bare, repository, ref, forWhat)
	if err != nil {
		return false, err
	}
	resolution, err := config.ResolveRun(s.global, trusted, config.Absent(config.OriginPushed))
	if err != nil {
		return false, fmt.Errorf("service: resolving whether %s allows standing consent: %w", repository.ID, err)
	}
	return resolution.Config.AllowStandingConsent, nil
}

// giveStandingConsent puts a run under standing consent once its repository
// is known to allow it, and returns the record as it then stands. A run that
// has finished is returned as it stands and given nothing, because there is
// nothing left for consent to answer and a record saying otherwise would be
// a claim about a run that was over.
func (s *Service) giveStandingConsent(ctx context.Context, record store.Run) (store.Run, error) {
	if !unfinished(record.Status) {
		return record, nil
	}
	given, err := s.store.GiveRunStandingConsent(ctx, record.ID)
	if err != nil {
		return store.Run{}, err
	}
	s.log.Printf("run %s was put under standing consent", record.ID)
	return given, nil
}

// consentAnswer is what standing consent answers one decision with.
type consentAnswer struct {
	// stage is the stage whose hold is answered.
	stage pipeline.Stage
	// option is the answer, one of the decision's options.
	option pipeline.Outcome
	// detail is the encoded fix request a fix answer carries, empty for an
	// approval.
	detail string
	// selected is the identifiers a fix answer selects.
	selected []string
}

// resolution is the answer as the hold row records it.
func (a consentAnswer) resolution() string {
	if len(a.selected) == 0 {
		return string(a.option)
	}
	return string(a.option) + ": " + strings.Join(a.selected, ", ")
}

// consentAnswerAt decides what standing consent answers the decision a result
// stands at, on this file's terms, and false for a decision it does not
// answer. It reads the result alone: whether the run is under consent and
// whether the repository still allows it are the caller's to have asked.
func consentAnswerAt(result graph.Result) (consentAnswer, bool) {
	if result.Status != graph.StatusHalted || result.Decision == nil {
		return consentAnswer{}, false
	}
	stage, ok := heldStage(result.Position)
	if !ok || !stage.Requestable() {
		return consentAnswer{}, false
	}
	report, err := pipeline.StageReport(result.State, stage)
	if err != nil {
		return consentAnswer{}, false
	}
	// The hold a requested round leaves so its result is seen is the fix
	// review, which consent accepts rather than sends to another round;
	// internal/pipeline is what tells it from an ask anything else raised.
	review, reviewed, err := pipeline.FixReviewFinding(result.State, stage)
	if err != nil {
		return consentAnswer{}, false
	}
	answer := consentAnswer{stage: stage}
	actionable := false
	for _, f := range report.Findings {
		if reviewed && f.ID == review {
			continue
		}
		if f.Holds() || f.FixEligible() {
			answer.selected = append(answer.selected, f.ID)
			continue
		}
		if f.Action != findings.ActionNote {
			actionable = true
		}
	}
	switch {
	case actionable:
		// A finding that neither holds, is fix-eligible, nor is a note is
		// one a normalized report cannot carry; nothing here answers for it.
		return consentAnswer{}, false
	case len(answer.selected) > 0:
		request := pipeline.FixRequest{Selected: answer.selected}
		if _, err := request.Select(report); err != nil {
			return consentAnswer{}, false
		}
		detail, err := request.Encode()
		if err != nil {
			return consentAnswer{}, false
		}
		answer.option, answer.detail = pipeline.OutcomeFixRequested, detail
	default:
		answer.option = pipeline.OutcomeApproved
	}
	if !slices.Contains(result.Decision.Options, string(answer.option)) {
		return consentAnswer{}, false
	}
	return answer, true
}

// underStandingConsent answers, under standing consent, the decision a
// segment stopped at, and the one after that, until it stops at one consent
// does not answer or stops for any other reason. It runs inside advance, in
// the segment and the slot that segment holds, so the run is never recorded
// held at a decision the program is about to answer and nothing else can
// advance it in between.
//
// A result is returned as it came for a run not under consent, a repository
// whose resolution no longer allows it, and a decision consent does not
// answer. An answer internal/graph refuses before moving the run, such as one
// past RequestedFixRounds or the budget, ends the answering with the decision
// still open; one it accepted and whose segment then failed is returned as
// that failure, after its record is written.
func (s *Service) underStandingConsent(ctx context.Context, runID string, topo *topology, result graph.Result) (graph.Result, error) {
	for {
		answer, ok := consentAnswerAt(result)
		if !ok {
			return result, nil
		}
		record, err := s.store.Run(ctx, runID)
		if err != nil {
			s.log.Printf("run %s holds for a person: whether it is under standing consent could not be read: %v", runID, err)
			return result, nil
		}
		if !record.StandingConsent.IsKnown() {
			return result, nil
		}
		if !topo.allowsConsent {
			s.log.Printf("run %s holds for a person: it is under standing consent and its repository's trusted "+
				"configuration no longer sets %s", runID, config.KeyAllowStandingConsent)
			return result, nil
		}
		next, err := topo.executor.AnswerWith(ctx, runID, string(answer.option), answer.detail)
		applied := err == nil || s.movedPast(ctx, runID, result.Checkpoint.Seq)
		if applied {
			s.recordConsentAnswer(ctx, runID, result, answer)
		}
		if err != nil {
			if applied {
				return graph.Result{}, err
			}
			s.log.Printf("run %s holds for a person: standing consent's %s at the %s stage was refused: %v",
				runID, answer.option, answer.stage, err)
			return result, nil
		}
		result = next
	}
}

// movedPast reports whether the run's latest checkpoint is later than seq,
// which is what says an answer to the decision at seq was written. A history
// that cannot be read reports false, so an answer whose fate is unknown is
// not recorded as given.
func (s *Service) movedPast(ctx context.Context, runID string, seq int) bool {
	latest, err := s.checkpoints.Latest(context.WithoutCancel(ctx), runID)
	if err != nil {
		s.log.Printf("run %s: could not read whether standing consent's answer was applied: %v", runID, err)
		return false
	}
	return latest.Seq > seq
}

// StandingConsentHoldKey is the key a decision standing consent answered is
// recorded under: the run and the checkpoint the decision stood at, so one
// decision is one record however often it is looked at, and two decisions at
// the same stage are two.
//
// It is exported for a reader of the record, such as a test or a report, on
// ChecksHoldKey's terms: the key names a hold and resolving one is this
// package's.
func StandingConsentHoldKey(runID string, seq int) string {
	return "run/" + runID + "/standing-consent/" + strconv.Itoa(seq)
}

// recordConsentAnswer writes the hold row for one answer standing consent
// gave, registered and resolved together as the program's. A row the store
// refuses is logged rather than failing the run: the answer was applied, and
// the run's own record still says it ran under consent.
func (s *Service) recordConsentAnswer(ctx context.Context, runID string, at graph.Result, answer consentAnswer) {
	key := StandingConsentHoldKey(runID, at.Checkpoint.Seq)
	_, err := s.store.RecordHoldResolution(context.WithoutCancel(ctx), store.HoldClosure{
		Hold: store.Hold{
			Key:     key,
			RunID:   store.Known(runID),
			Subject: "what to do about the findings this change stopped on before it was sent upstream",
			Detail: fmt.Sprintf("run %s held at the %s stage's hold, checkpoint %d; answered under standing consent",
				runID, answer.stage, at.Checkpoint.Seq),
		},
		Resolution: answer.resolution(),
		By:         store.ResolvedByStandingConsent(),
	})
	if err != nil {
		s.log.Printf("run %s: standing consent answered %s at the %s stage and the record of it was refused: %v",
			runID, answer.resolution(), answer.stage, err)
		return
	}
	s.log.Printf("run %s: standing consent answered %s at the %s stage", runID, answer.resolution(), answer.stage)
	s.publishHoldByKey(context.WithoutCancel(ctx), key)
}

// answerHeldUnderConsent is attaching to a held run that is under standing
// consent: the decision it holds on is answered when consent answers it, and
// the run is reported as it stands when consent does not. It is how consent
// given to a run already holding reaches that run's open decision, and how a
// held run under consent is carried on by the bare command.
//
// What is answered is read again inside the segment's slot rather than taken
// from the read here, so an answer a person gave in between is the one that
// stands and consent answers whatever the run holds on after it, if anything.
func (s *Service) answerHeldUnderConsent(ctx context.Context, record store.Run) (machine.Run, error) {
	latest, err := s.checkpoints.Latest(ctx, record.ID)
	if err != nil {
		return s.view(ctx, record.ID)
	}
	if _, ok := consentAnswerAt(latest.Result()); !ok {
		return s.view(ctx, record.ID)
	}
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.Run{}, err
	}
	// The answer resumes the run, so its configuration is resolved again on
	// the terms attach and respond state, which is also where whether the
	// repository still allows consent is read.
	resolved, err := s.resolveRunConfig(ctx, record)
	if err != nil {
		return machine.Run{}, err
	}
	topo, err := s.topologyFor(built, resolved)
	if err != nil {
		return machine.Run{}, err
	}
	if !topo.allowsConsent {
		return s.view(ctx, record.ID)
	}
	if _, err := built.runs.Release(ctx, record.ID); err != nil {
		var wrong *store.RunStatusError
		if errors.As(err, &wrong) {
			// Answered or ended by someone else since the read above: the
			// record is authoritative and is reported as it stands.
			return s.view(ctx, record.ID)
		}
		return machine.Run{}, err
	}
	return s.advance(ctx, record.ID, topo, func(ctx context.Context) (graph.Result, error) {
		at, err := s.checkpoints.Latest(ctx, record.ID)
		if err != nil {
			return graph.Result{}, err
		}
		return at.Result(), nil
	})
}
