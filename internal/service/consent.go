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
// Two kinds of ask leave the whole hold for a person, and the service log
// says which finding and why. An ask whose action was P3's default rather
// than the stage's, findings.Finding.ActionDefaulted, because P3's point is
// that an action nobody could read waits for a human. And an ask whose remedy
// lies outside the change, findings.Finding.RemedyOutsideChange, because a
// fix round edits the change and so cannot resolve it: a command stage with
// no command configured, a stage with no body, and the review stage's two
// findings that say no review happened, review-change-too-large, whose answer
// is splitting the change, and review-not-established, whose answer is a
// review a fix round does not perform (#320 item 5b). Both facts are the
// finding's own, set where the report is normalized or built, so nothing here
// matches an identifier, and a finding is added to either kind by marking it
// where it is built.
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
// refusal of its own would be answered with a fix round here unless it marks
// its ask RemedyOutsideChange, and nothing in this file would notice; that
// stage would need to raise its refusal where this does not reach.
//
// # Bounds
//
// Consent adds no bound and lifts none. A fix answer travels the requested
// round's own edges, so pipeline.RequestedFixRounds and the run-wide step
// budget bound it, and an answer internal/graph refuses for either is taken
// as the consent stopping: the hold stays open for a person and the refusal is
// recorded. It is given through graph.Executor.AnswerUnattended, because
// nobody attended it, so the requested cycle's edges are not exempt from the
// convergence check for a round consent asked for: an answer that would send
// the run into a round from exactly the state the previous answer at that
// hold sent it in from parks the run converged before that round runs, so a
// loop of rounds that change nothing spends two and not the whole of
// pipeline.RequestedFixRounds. internal/pipeline's doc.go says why two is the
// fewest. A park ends the run as it always does; consent answers holds and a
// park is not one.
//
// # How each answer is recorded
//
// Every answer is a hold row keyed by the run and the checkpoint the decision
// stood at, resolved as store.ResolvedByStandingConsent: the program's answer,
// never a person's and never a caller's. The row is written open, saying what
// consent is about to answer, before the answer is given, and a row the store
// refuses means the answer is not given. It is resolved with the answer once
// internal/graph reports the checkpoint carrying it written, and with a
// resolution beginning "not applied: " when the answer was refused. A service
// that stops in between leaves the row open, and finishConsentRows finishes
// it when the service next opens, from the run's history and without giving
// the answer. A row already resolved for a decision still open is a decision
// consent took and could not apply, so it is not taken a second time.
// Request.HoldResolver is not consulted, because nothing on the socket
// answered this: the program did.
//
// # Residual gaps
//
//   - The open row is visible to a reader of the open holds, catching up
//     among them, between being written and being resolved: the moment it
//     takes internal/graph to check and write the answer, or, after a service
//     stopped there, until the next open finishes it. A history recovery
//     cannot read leaves it open longer, saying so in the log.
//   - A report recorded before findings.Finding.ActionDefaulted existed says
//     false for every finding, so an ask P3 supplied there reads as stated
//     and is sent to a round. The same holds for a finding a stage built by
//     hand and never normalized; every stage report is normalized by the
//     stage node before it is recorded. A review report recorded before
//     review-change-too-large and review-not-established were marked
//     RemedyOutsideChange says false for them too, so consent given to a run
//     already held on one sends it to a round.
//   - What PRD section 5 has the decision-model record keep about a person's
//     answer at a hold is not written for a consent answer, because it is not
//     a person's: the follow-up kind that record keeps is a person's answer.
//     The owner's decision on #253 keeps this a stated gap until that record
//     keeps follow-ups for any program-side answer.
//   - A loop of rounds that change nothing still spends two before it parks.
//     The first round's effect includes the hold it leaves in the stage's
//     re-run report, which the report it read did not carry, so over the
//     whole state it changed something; the second is the first that can be
//     seen to change nothing, and the answer that would start a third is
//     where that is seen. That third answer is therefore recorded as given,
//     on the checkpoint that claims the run, and the run parks on it.
//   - "Changes nothing" is over the whole state, so a stage whose report
//     differs between two such rounds, or a fixer whose summary does,
//     defeats it, and the loop is bounded by pipeline.RequestedFixRounds and
//     the run-wide budget instead.

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
// answer either way, classified for the wire by refusedConfig as a run's
// resolution is.
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
		return false, refusedConfig(err)
	}
	resolution, err := config.ResolveRun(s.global, trusted, config.Absent(config.OriginPushed))
	if err != nil {
		return false, refusedConfig(fmt.Errorf("%w: resolving whether %s allows standing consent: %w",
			resolutionRefusal(err), repository.ID, err))
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

// consentNotApplied begins the resolution of a hold row whose answer was
// written down and never applied, so a reader of the record can tell an
// answer the run took from one it did not without reading the run's history.
const consentNotApplied = "not applied: "

// consentAnswerAt decides what standing consent answers the decision a result
// stands at, on this file's terms, and false for a decision it does not
// answer. It reads the result alone: whether the run is under consent and
// whether the repository still allows it are the caller's to have asked.
//
// A decision consent would otherwise answer and leaves for a person because
// of a finding it carries comes back false with why set to a sentence saying
// which finding and why, so the caller can record it; every other false
// carries an empty why, because it is not a decision consent answers at all.
func consentAnswerAt(result graph.Result) (answer consentAnswer, ok bool, why string) {
	if result.Status != graph.StatusHalted || result.Decision == nil {
		return consentAnswer{}, false, ""
	}
	stage, found := heldStage(result.Position)
	if !found || !stage.Requestable() {
		return consentAnswer{}, false, ""
	}
	report, err := pipeline.StageReport(result.State, stage)
	if err != nil {
		return consentAnswer{}, false, ""
	}
	// The hold a requested round leaves so its result is seen is the fix
	// review, which consent accepts rather than sends to another round;
	// internal/pipeline is what tells it from an ask anything else raised.
	review, reviewed, err := pipeline.FixReviewFinding(result.State, stage)
	if err != nil {
		return consentAnswer{}, false, ""
	}
	answer = consentAnswer{stage: stage}
	actionable := false
	for _, f := range report.Findings {
		if reviewed && f.ID == review {
			continue
		}
		switch {
		case f.Holds() && f.ActionDefaulted:
			// P3: an action nobody could read waits for a person, and
			// consent is not one.
			return consentAnswer{}, false, fmt.Sprintf("finding %s at the %s stage stated no action this "+
				"build could read, and P3 leaves such a finding for a person", f.ID, stage)
		case f.Holds() && f.RemedyOutsideChange:
			// A round edits the change, and what resolves this lies
			// outside it, so a round asked for it is spent on nothing.
			return consentAnswer{}, false, fmt.Sprintf("finding %s at the %s stage is resolved outside "+
				"the change, so no fix round can resolve it", f.ID, stage)
		case f.Holds() || f.FixEligible():
			answer.selected = append(answer.selected, f.ID)
		case f.Action != findings.ActionNote:
			actionable = true
		}
	}
	switch {
	case actionable:
		// A finding that neither holds, is fix-eligible, nor is a note is
		// one a normalized report cannot carry; nothing here answers for it.
		return consentAnswer{}, false, ""
	case len(answer.selected) > 0:
		request := pipeline.FixRequest{Selected: answer.selected}
		if _, err := request.Select(report); err != nil {
			return consentAnswer{}, false, ""
		}
		detail, err := request.Encode()
		if err != nil {
			return consentAnswer{}, false, ""
		}
		answer.option, answer.detail = pipeline.OutcomeFixRequested, detail
	default:
		answer.option = pipeline.OutcomeApproved
	}
	if !slices.Contains(result.Decision.Options, string(answer.option)) {
		return consentAnswer{}, false, ""
	}
	return answer, true, ""
}

// underStandingConsent answers, under standing consent, the decision a
// segment stopped at, and the one after that, until it stops at one consent
// does not answer or stops for any other reason. It runs inside advance, in
// the segment and the slot that segment holds, so the run is never recorded
// held at a decision the program is about to answer and nothing else can
// advance it in between.
//
// A result is returned as it came for a run not under consent, a repository
// whose resolution no longer allows it, a decision consent does not answer,
// and a decision whose hold row cannot be written or was written before. An
// answer internal/graph refuses before moving the run, such as one past
// RequestedFixRounds or the budget, ends the answering with the decision
// still open; one it accepted and whose segment then failed is returned as
// that failure.
func (s *Service) underStandingConsent(ctx context.Context, runID string, topo *topology, result graph.Result) (graph.Result, error) {
	for {
		answer, ok, why := consentAnswerAt(result)
		if !ok && why == "" {
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
		if !ok {
			s.log.Printf("run %s holds for a person: it is under standing consent and %s", runID, why)
			return result, nil
		}
		if !topo.allowsConsent {
			s.log.Printf("run %s holds for a person: it is under standing consent and its repository's trusted "+
				"configuration no longer sets %s", runID, config.KeyAllowStandingConsent)
			return result, nil
		}
		key := StandingConsentHoldKey(runID, result.Checkpoint.Seq)
		if !s.writeConsentAnswer(ctx, runID, key, result, answer) {
			return result, nil
		}
		applied := false
		next, err := topo.executor.AnswerUnattended(ctx, runID, string(answer.option), answer.detail,
			func(graph.CheckpointID) {
				applied = true
				s.finishConsentAnswer(ctx, runID, key, answer.resolution())
			})
		if err != nil {
			if applied {
				return graph.Result{}, err
			}
			s.finishConsentAnswer(ctx, runID, key, consentNotApplied+"refused: "+err.Error())
			s.log.Printf("run %s holds for a person: standing consent's %s at the %s stage was refused: %v",
				runID, answer.option, answer.stage, err)
			return result, nil
		}
		result = next
	}
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
	return consentKeyPrefix(runID) + strconv.Itoa(seq)
}

// consentKeyPrefix is what every key StandingConsentHoldKey gives for runID
// begins with.
func consentKeyPrefix(runID string) string { return "run/" + runID + "/standing-consent/" }

// consentKeySeq is StandingConsentHoldKey read back: the checkpoint a key
// written for runID names, and false for a key that function did not write
// for that run. It is the one reader of that key's shape, so the two cannot
// disagree.
func consentKeySeq(runID, key string) (int, bool) {
	rest, found := strings.CutPrefix(key, consentKeyPrefix(runID))
	if !found {
		return 0, false
	}
	seq, err := strconv.Atoi(rest)
	if err != nil || seq < 1 || StandingConsentHoldKey(runID, seq) != key {
		return 0, false
	}
	return seq, true
}

// writeConsentAnswer writes the hold row for one answer standing consent is
// about to give, open, before the answer is applied, and reports whether the
// answer may go ahead. That order is P6's refuse-and-report stance applied to
// the record: an answer is never applied that has no record, a row the store
// refuses means the answer is not given, and a row whose answer was then not
// applied is finished as such, here or by finishConsentRows after a restart.
//
// A row already resolved under that key is a decision consent has already
// taken at this checkpoint, so it is not taken again: the decision is left
// for a person. An open one is this decision's own row from an attempt that
// did not finish it, and the answer goes ahead under it.
func (s *Service) writeConsentAnswer(ctx context.Context, runID, key string, at graph.Result, answer consentAnswer) bool {
	h, err := s.store.RegisterHold(context.WithoutCancel(ctx), store.Hold{
		Key:     key,
		RunID:   store.Known(runID),
		Subject: "what to do about the findings this change stopped on before it was sent upstream",
		Detail: fmt.Sprintf("run %s held at the %s stage's hold, checkpoint %d; standing consent answers it with %s",
			runID, answer.stage, at.Checkpoint.Seq, answer.resolution()),
	})
	if err != nil {
		s.log.Printf("run %s holds for a person: standing consent would answer %s at the %s stage and the record "+
			"of it was refused, so the answer was not given: %v", runID, answer.resolution(), answer.stage, err)
		return false
	}
	if resolution, done := h.Resolution.Get(); done {
		s.log.Printf("run %s holds for a person: standing consent already decided the %s stage's hold at checkpoint "+
			"%d (%s), and does not decide it twice", runID, answer.stage, at.Checkpoint.Seq, resolution)
		return false
	}
	return true
}

// finishConsentAnswer resolves the hold row writeConsentAnswer wrote, as the
// program's under standing consent, with what became of the answer: the
// answer itself once it was applied, or why it was not. A resolution the
// store refuses is logged and leaves the row open, and finishConsentRows
// finishes it when the service next opens.
func (s *Service) finishConsentAnswer(ctx context.Context, runID, key, resolution string) {
	if _, err := s.store.ResolveHold(context.WithoutCancel(ctx), key, resolution, store.ResolvedByStandingConsent()); err != nil {
		s.log.Printf("run %s: standing consent's record %s could not be finished with %q: %v", runID, key, resolution, err)
		return
	}
	s.log.Printf("run %s: standing consent's record %s: %s", runID, key, resolution)
	s.publishHoldByKey(context.WithoutCancel(ctx), key)
}

// finishConsentRows is recovery's half of the hold-row-first order: every
// standing consent row left open by a service that stopped between writing it
// and finishing it is finished from the run's checkpoint history, which is
// the record of whether its answer was applied.
//
// The decision the row was written for is the checkpoint its key names, and
// the answer is what consentAnswerAt derives there again, since the answer is
// a function of that checkpoint. The checkpoint after it is the claim an
// applied answer wrote. A claim carrying that answer finishes the row with
// it; no claim, or a claim carrying another answer, finishes it as not
// applied, and the decision is the run's to hold for a person as it already
// does. Nothing is answered here: an answer the service did not live to
// apply is reported, never given after the fact.
//
// A row whose run history cannot be read, or whose answer can no longer be
// derived where a claim exists to compare it with, is left open and logged,
// because finishing it would claim a fact nothing verified; it stays among
// the open holds a person is shown until a later open can read it.
func (s *Service) finishConsentRows(ctx context.Context) {
	open, err := s.store.OpenHolds(ctx)
	if err != nil {
		s.log.Printf("recovery could not list open holds to finish standing consent's records: %v", err)
		return
	}
	for _, h := range open {
		runID, ofRun := h.RunID.Get()
		if !ofRun {
			continue
		}
		seq, ours := consentKeySeq(runID, h.Key)
		if !ours {
			continue
		}
		resolution, verified := s.consentRowOutcome(ctx, runID, seq)
		if !verified {
			continue
		}
		s.finishConsentAnswer(ctx, runID, h.Key, resolution)
	}
}

// consentRowOutcome is what became of the answer standing consent wrote a row
// for at the run's checkpoint seq, as the row's resolution, and false where
// that cannot be verified; finishConsentRows says how it is decided.
func (s *Service) consentRowOutcome(ctx context.Context, runID string, seq int) (string, bool) {
	history, err := s.checkpoints.History(ctx, runID)
	if err != nil {
		s.log.Printf("recovery left standing consent's record for run %s at checkpoint %d open: the run's history "+
			"could not be read: %v", runID, seq, err)
		return "", false
	}
	var decision, claim *graph.Checkpoint
	for i := range history {
		switch history[i].Seq {
		case seq:
			decision = &history[i]
		case seq + 1:
			claim = &history[i]
		}
	}
	if decision == nil {
		s.log.Printf("recovery left standing consent's record for run %s at checkpoint %d open: the run's history "+
			"holds no such checkpoint", runID, seq)
		return "", false
	}
	if claim == nil {
		return consentNotApplied + "the service stopped before the answer was applied, and the decision is left " +
			"for a person", true
	}
	answer, ok, _ := consentAnswerAt(decision.Result())
	if !ok {
		s.log.Printf("recovery left standing consent's record for run %s at checkpoint %d open: the answer it was "+
			"written for can no longer be derived to compare with what the run took", runID, seq)
		return "", false
	}
	given, detail := claimedAnswer(claim.State, answer.stage)
	if given != string(answer.option) || detail != answer.detail {
		return fmt.Sprintf("%sthe decision at checkpoint %d was answered %q, not with this record's answer",
			consentNotApplied, seq, given), true
	}
	return answer.resolution(), true
}

// claimedAnswer is the answer and the detail a claim checkpoint carries at
// stage's hold, empty where it carries none.
func claimedAnswer(state graph.State, stage pipeline.Stage) (answer, detail string) {
	if v, ok := state.Get(string(stage.AnswerKey())); ok {
		answer, _ = v.Text()
	}
	if v, ok := state.Get(string(stage.RequestKey())); ok {
		detail, _ = v.Text()
	}
	return answer, detail
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
	if _, ok, _ := consentAnswerAt(latest.Result()); !ok {
		return s.view(ctx, record.ID)
	}
	if h, err := s.store.Hold(ctx, StandingConsentHoldKey(record.ID, latest.Seq)); err == nil && !h.Open() {
		// Consent already decided this decision and it is still open, so its
		// answer was not applied; it is the person's now.
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
