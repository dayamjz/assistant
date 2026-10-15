package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// stageNode builds the graph node that runs one stage. The node's own reads
// and writes are the implementation's declarations plus the keys this adapter
// needs: it reads what decides whether the stage runs at all, and it writes
// the stage's report and outcome, which no implementation may write.
//
// fixed says the stage has a fix node or a request node, so a round can run
// before it executes. Such a stage also reads the holds its last round raised
// and reports them with its own findings, which is what makes a hold a round
// raised independent of the stage's implementation: a body cannot leave one
// out, because it never handles it.
func stageNode(stage Stage, impl Implementation, fixed bool) graph.Node {
	adapterReads := []Key{KeySkip, KeyDiffEmpty}
	if fixed {
		adapterReads = append(adapterReads, stage.HeldKey())
	}
	reads := union(impl.Reads, adapterReads...)
	writes := union(impl.Writes, stage.ReportKey(), stage.OutcomeKey())
	declared := keySet(impl.Reads)
	permitted := keySet(impl.Writes)
	return graph.Node{
		Name:   stage.Node(),
		Reads:  reads,
		Writes: writes,
		NewBody: func() graph.Body {
			return func(ctx context.Context, r graph.Reader, w graph.Writer) error {
				skip, err := skipped(r, stage)
				if err != nil {
					return err
				}
				if skip {
					return w.Set(string(stage.OutcomeKey()), graph.TextValue(string(OutcomeSkipped)))
				}
				// Constructing here rather than in the enclosing closure is
				// what keeps a stage body out of the round after it: the graph
				// builds one graph.Body per node for a whole advance segment,
				// so a body built out there would be the same Go value in
				// every round of the fix loop. It narrows what a stage can
				// carry; it does not close the constructor's own closure.
				body := impl.NewBody()
				refused := &refusal{}
				out, err := body(ctx, Input{
					Stage: stage,
					State: reader{
						from:    r,
						allowed: declared,
						who:     "stage " + stage.String(),
						refused: refused,
					},
				})
				if err == nil {
					err = refused.err
				}
				if err != nil {
					return err
				}
				if fixed {
					held, err := readHolds(r, stage)
					if err != nil {
						return err
					}
					out.Report.Findings = append(out.Report.Findings, held...)
				}
				// This is the one write of a stage's report into state, so
				// it is where every report's text passes internal/redact on
				// its way to the checkpoint and to every answer read off it.
				// It runs first so that what Normalize derives and what
				// Validate quotes are both made from the redacted text.
				//
				// Normalizing here is where P3 lands for every stage at once:
				// a finding with a missing, empty, or unrecognized action
				// becomes ask, and classify then holds the stage for it.
				report := redactedReport(out.Report).Normalize()
				if err := report.Validate(); err != nil {
					return fmt.Errorf("%w: %s: %w", ErrUnusableReport, stage, err)
				}
				encoded, err := json.Marshal(report)
				if err != nil {
					return fmt.Errorf("pipeline: record the %s report: %w", stage, err)
				}
				if err := w.Set(string(stage.ReportKey()), graph.TextValue(string(encoded))); err != nil {
					return err
				}
				if err := w.Set(string(stage.OutcomeKey()), graph.TextValue(string(classify(report)))); err != nil {
					return err
				}
				return applyWrites(w, permitted, "stage "+stage.String(), out.Writes)
			}
		},
	}
}

// skipped reports whether this stage does not run in this run. Two things say
// so and both are read here rather than routed around the stage, so a stage
// that does not run still records that it was skipped rather than leaving the
// question to whoever reads the state later.
//
// The first is the run's own skip list, which is a run input: P2 lets a person
// skip a stage on purpose for one run and never lets a standing configuration
// do it for them. The second is the empty-diff short circuit: once the rebase
// stage reports that nothing remains to change, PRD section 5 ends the run
// successfully with the rest of the stages skipped rather than failed.
func skipped(r graph.Reader, stage Stage) (bool, error) {
	empty, err := r.Get(string(KeyDiffEmpty))
	if err != nil {
		return false, err
	}
	if nothingLeft, _ := empty.Bool(); nothingLeft {
		return true, nil
	}
	requested, err := r.Get(string(KeySkip))
	if err != nil {
		return false, err
	}
	list, _ := requested.List()
	for _, name := range list {
		if name == stage.String() {
			return true, nil
		}
	}
	return false, nil
}

// holdNode builds the halt point a stage holds at. The engine stops before the
// node runs, so the node does not start until the decision is answered and
// there is nothing to replay on resume.
//
// The node records the answer as the stage's outcome and translates nothing:
// the halt point's options are exactly the outcomes a person may give a held
// stage, and the graph refuses any answer outside them.
//
// requests says the pipeline can run a fix round a person asks for. The hold
// then offers OutcomeFixRequested and declares the stage's RequestKey as its
// detail, which is where the request the answer carries lands.
func holdNode(stage Stage, requests bool) graph.Node {
	halt := &graph.Halt{
		Question: fmt.Sprintf("The %s stage is holding for your decision.", stage),
		Options:  holdAnswers(requests),
		Into:     string(stage.AnswerKey()),
	}
	if requests {
		halt.Detail = string(stage.RequestKey())
		halt.Detailed = []string{string(OutcomeFixRequested)}
	}
	return graph.Node{
		Name:   stage.HoldNode(),
		Reads:  []string{string(stage.AnswerKey())},
		Writes: []string{string(stage.OutcomeKey())},
		Halt:   halt,
		NewBody: graph.Stateless(func(ctx context.Context, r graph.Reader, w graph.Writer) error {
			answer, err := r.Get(string(stage.AnswerKey()))
			if err != nil {
				return err
			}
			text, _ := answer.Text()
			return w.Set(string(stage.OutcomeKey()), graph.TextValue(text))
		}),
	}
}

// fixNode builds the node that applies one stage's fix-eligible findings. It
// reads the report the stage recorded and the summary the last round wrote,
// and it writes the summary this round wrote; everything else it touches is
// what the fixer declared.
func fixNode(stage Stage, fixer Fixer) graph.Node {
	reads := union(fixer.Reads, stage.ReportKey(), stage.FixKey())
	writes := union(fixer.Writes, stage.FixKey(), stage.HeldKey())
	declared := keySet(fixer.Reads)
	permitted := keySet(fixer.Writes)
	return graph.Node{
		Name:   stage.FixNode(),
		Reads:  reads,
		Writes: writes,
		NewBody: func() graph.Body {
			body := fixer.NewBody()
			return func(ctx context.Context, r graph.Reader, w graph.Writer) error {
				recorded, err := r.Get(string(stage.ReportKey()))
				if err != nil {
					return err
				}
				text, _ := recorded.Text()
				report, err := decodeReport(text, stage)
				if err != nil {
					return err
				}
				previous, err := r.Get(string(stage.FixKey()))
				if err != nil {
					return err
				}
				summary, _ := previous.Text()
				refused := &refusal{}
				out, err := body(ctx, FixInput{
					Stage:    stage,
					Findings: report.Fixable(),
					Previous: summary,
					State: reader{
						from:    r,
						allowed: declared,
						who:     "the fixer for " + stage.String(),
						refused: refused,
					},
				})
				if err == nil {
					err = refused.err
				}
				if err != nil {
					return err
				}
				held, err := encodeHolds(stage, out.Holds)
				if err != nil {
					return err
				}
				if err := w.Set(string(stage.FixKey()), graph.TextValue(redactedText(out.Summary))); err != nil {
					return err
				}
				if err := w.Set(string(stage.HeldKey()), graph.TextValue(held)); err != nil {
					return err
				}
				return applyWrites(w, permitted, "the fixer for "+stage.String(), out.Writes)
			}
		},
	}
}

// requestNode builds the node that runs a fix round a person asked for at the
// stage's hold, PRD section 5's Fix action. It is the stage's own fixer, the
// same Fixer the automatic round runs, handed the person's selection, their
// own findings, their notes, and what they left unselected.
//
// The round always leaves one hold behind, requestedHold, so the stage holds
// again after it executes whatever its own report says: PRD section 5 has the
// stage re-run and hold again so the person sees the result. That is true
// only because wire builds this node for a stage before push and for no other
// (Stage.Requestable): such a stage's re-run commits nothing outside the
// run's copy, and the push stage later forwards what the round committed. A
// round that changed no key its fixer writes says so in that hold; it returns
// to the hold rather than parking, because the edge back to the stage is
// graph.Edge.Answered, exempt from convergence and still bounded by
// RequestedFixRounds. The fixer's own
// holds travel with it, which is how the judge rule reaches this round on the
// same terms it reaches an automatic one: decided from the commit the round
// made, by whatever fix body the pipeline was given.
//
// A request that cannot be read or selects nothing the report carries fails
// the node rather than running a round on a guess. The surface answering the
// hold applies FixRequest.Select before it answers, so a request reaching
// here has already passed the same rule once.
func requestNode(stage Stage, fixer Fixer) graph.Node {
	reads := union(append(append([]Key(nil), fixer.Reads...), fixer.Writes...), stage.ReportKey(), stage.FixKey(), stage.RequestKey())
	writes := union(fixer.Writes, stage.FixKey(), stage.HeldKey())
	declared := keySet(fixer.Reads)
	permitted := keySet(fixer.Writes)
	return graph.Node{
		Name:   stage.RequestNode(),
		Reads:  reads,
		Writes: writes,
		NewBody: func() graph.Body {
			body := fixer.NewBody()
			return func(ctx context.Context, r graph.Reader, w graph.Writer) error {
				recorded, err := r.Get(string(stage.ReportKey()))
				if err != nil {
					return err
				}
				text, _ := recorded.Text()
				report, err := decodeReport(text, stage)
				if err != nil {
					return err
				}
				asked, err := r.Get(string(stage.RequestKey()))
				if err != nil {
					return err
				}
				askedText, _ := asked.Text()
				request, err := ParseFixRequest(askedText)
				if err != nil {
					return fmt.Errorf("pipeline: the fix round requested at the %s stage's hold: %w", stage, err)
				}
				selection, err := request.Select(report)
				if err != nil {
					return fmt.Errorf("pipeline: the fix round requested at the %s stage's hold: %w", stage, err)
				}
				previous, err := r.Get(string(stage.FixKey()))
				if err != nil {
					return err
				}
				summary, _ := previous.Text()
				refused := &refusal{}
				out, err := body(ctx, FixInput{
					Stage:    stage,
					Findings: selection.Resolve,
					Requested: &Requested{
						Notes:      selection.Notes,
						Unselected: selection.Unselected,
					},
					Previous: summary,
					State: reader{
						from:    r,
						allowed: declared,
						who:     "the requested fix round for " + stage.String(),
						refused: refused,
					},
				})
				if err == nil {
					err = refused.err
				}
				if err != nil {
					return err
				}
				changed, err := changesState(r, permitted, out.Writes)
				if err != nil {
					return err
				}
				// The hold quotes the summary as it is recorded, so
				// FixReviewFinding can render it again from the fix key.
				account := redactedText(out.Summary)
				held, err := encodeHolds(stage, append(out.Holds, requestedHold(stage, account, changed)))
				if err != nil {
					return err
				}
				if err := w.Set(string(stage.FixKey()), graph.TextValue(account)); err != nil {
					return err
				}
				if err := w.Set(string(stage.HeldKey()), graph.TextValue(held)); err != nil {
					return err
				}
				return applyWrites(w, permitted, "the requested fix round for "+stage.String(), out.Writes)
			}
		},
	}
}

// requestedHold is the ask a requested round leaves for the stage's next
// execution, which is what makes the stage hold again after it. It quotes what
// the round said it did as the round's own account rather than as a result.
//
// changed is whether the round wrote any key its fixer declares to a value
// other than the one it held, which is changesState's answer; a round that did
// not is said to have changed nothing in the run's state, which is all that
// comparison establishes. Nothing here has been pushed: the stage is before
// push, so the push stage forwards the round's commit only after this hold is
// answered.
func requestedHold(stage Stage, summary string, changed bool) findings.Finding {
	account := strings.TrimSpace(summary)
	if account == "" {
		account = "nothing"
	}
	effect := ""
	if !changed {
		effect = " The round changed nothing in the run's state: every key its fixer writes holds " +
			"what it held before."
	}
	return findings.Finding{
		Severity: findings.SeverityInfo,
		Action:   findings.ActionAsk,
		Description: fmt.Sprintf("You asked for a fix round at the %s stage's hold, and this is the "+
			"stage's report after it. It holds so you see the result before anything is pushed: "+
			"approve accepts it as it stands, and you can ask for another round, skip, or cancel.%s "+
			"What the round said it did, in its own words: %s", stage, effect, account),
	}
}

// FixReviewFinding returns the identifier, in the stage's recorded report, of
// the hold its last requested fix round left so the stage holds again for the
// round's result to be seen, and false when the report carries no such hold.
// It is the one way to tell that hold from an ask the stage or its fixer
// raised, which matters to a caller that would accept a round's result as it
// stands and would not accept anything else on the same terms.
//
// It is decided from what the request node wrote and not from the report's
// text alone: the last hold the stage's last round left, which a requested
// round always appends after its fixer's own, has to read as requestedHold
// renders it for the summary that round wrote, and exactly one ask in the
// report may carry that text. A report carrying two, which only a stage body
// or a fixer imitating the hold could produce, answers false, so the
// imitation is never taken for the round's result. Anything that cannot be
// read is an error rather than false.
func FixReviewFinding(s graph.State, stage Stage) (string, bool, error) {
	if !stage.Requestable() {
		return "", false, nil
	}
	heldValue, ok := s.Get(string(stage.HeldKey()))
	if !ok {
		return "", false, nil
	}
	heldText, _ := heldValue.Text()
	if heldText == "" {
		return "", false, nil
	}
	var held []findings.Finding
	if err := json.Unmarshal([]byte(heldText), &held); err != nil {
		return "", false, fmt.Errorf("%w: %s: the holds its last fix round raised: %w", ErrBadReport, stage, err)
	}
	summaryValue, ok := s.Get(string(stage.FixKey()))
	if !ok {
		return "", false, nil
	}
	summary, _ := summaryValue.Text()
	// encodeHolds recorded the hold through redactedText, so it is compared
	// as recorded rather than as rendered.
	last := strings.TrimSpace(held[len(held)-1].Description)
	if last != strings.TrimSpace(redactedText(requestedHold(stage, summary, true).Description)) &&
		last != strings.TrimSpace(redactedText(requestedHold(stage, summary, false).Description)) {
		return "", false, nil
	}
	report, err := StageReport(s, stage)
	if err != nil {
		return "", false, err
	}
	id, found := "", 0
	for _, f := range report.Findings {
		if f.Holds() && strings.TrimSpace(f.Description) == last {
			id, found = f.ID, found+1
		}
	}
	if found != 1 {
		return "", false, nil
	}
	return id, true, nil
}

// changesState reports whether any of a fixer's writes would change the value
// its key holds. A key the fixer may not write is not judged here: applyWrites
// refuses it, and that refusal is the answer that matters.
func changesState(r graph.Reader, permitted map[Key]struct{}, writes map[Key]graph.Value) (bool, error) {
	for key, value := range writes {
		if _, ok := permitted[key]; !ok {
			continue
		}
		current, err := r.Get(string(key))
		if err != nil {
			return false, err
		}
		if !current.Equal(value) {
			return true, nil
		}
	}
	return false, nil
}

// encodeHolds renders what a fix round raised for its stage's next execution.
// Every hold passes internal/redact first, through redactedFindings, because
// what this returns is recorded in state as it stands and not only once the
// stage's report takes it in. Every hold is then normalized, so P3 makes a
// missing or unrecognized action
// an ask, and anything that is still not an ask is refused: a fixer may hold
// its stage and may say nothing else about it. Identifiers are dropped, because
// the stage's report assigns them when it is normalized and a fixer's own
// could collide with one the stage reported. A round that raised nothing is
// the empty string, which is what clears the last round's holds.
func encodeHolds(stage Stage, holds []findings.Finding) (string, error) {
	if len(holds) == 0 {
		return "", nil
	}
	normalized := make([]findings.Finding, len(holds))
	for i, hold := range findings.NormalizeFindings(redactedFindings(holds)) {
		if !hold.Holds() {
			return "", fmt.Errorf("%w: the %s stage's fix round raised %q with action %q",
				ErrFixerFinding, stage, hold.Description, hold.Action)
		}
		if hold.Description == "" {
			// The stage's report would refuse it later with ErrUnusableReport,
			// blaming the stage for what its fixer raised. It is refused here,
			// where the round that raised it is the one named.
			return "", fmt.Errorf("%w: the %s stage's fix round raised a hold with no description",
				ErrFixerFinding, stage)
		}
		hold.ID = ""
		normalized[i] = hold
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("pipeline: record the holds the %s stage's fix round raised: %w", stage, err)
	}
	return string(encoded), nil
}

// readHolds reads what the last fix round of a stage raised. A value that
// cannot be read back is refused rather than read as no holds, because that
// would present a round that raised a hold as one that raised nothing.
func readHolds(r graph.Reader, stage Stage) ([]findings.Finding, error) {
	value, err := r.Get(string(stage.HeldKey()))
	if err != nil {
		return nil, err
	}
	text, _ := value.Text()
	if text == "" {
		return nil, nil
	}
	var held []findings.Finding
	if err := json.Unmarshal([]byte(text), &held); err != nil {
		return nil, fmt.Errorf("%w: %s: the holds its last fix round raised: %w", ErrBadReport, stage, err)
	}
	return held, nil
}

// union renders one node's declaration: the keys an implementation declared
// plus the keys this package's own adapter needs, with a key named by both
// appearing once. An implementation may declare a read the adapter also makes,
// and the graph refuses a node that lists one key twice.
func union(declared []Key, added ...Key) []string {
	seen := make(map[Key]struct{}, len(declared)+len(added))
	out := make([]string, 0, len(declared)+len(added))
	for _, key := range append(append([]Key(nil), declared...), added...) {
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, string(key))
	}
	return out
}

// NodeDone is the terminal node a run that reached the end of the pipeline
// completes at. It exists because the last stage declares outgoing edges, and
// a node that declares any must declare where a run goes when no guard
// matches; it does no work of its own.
const NodeDone = "done"

// NodeCancel is the terminal node a run a person cancelled completes at.
const NodeCancel = "cancel"

// doneNode is where a run that reached the end of the pipeline completes.
func doneNode() graph.Node {
	return graph.Node{
		Name: NodeDone,
		NewBody: graph.Stateless(func(context.Context, graph.Reader, graph.Writer) error {
			return nil
		}),
	}
}

// cancelNode is where a run a person cancelled completes. It records the
// cancellation, so a completed run says which of the two ends it reached.
func cancelNode() graph.Node {
	return graph.Node{
		Name:   NodeCancel,
		Writes: []string{string(KeyCancelled)},
		NewBody: graph.Stateless(func(ctx context.Context, r graph.Reader, w graph.Writer) error {
			return w.Set(string(KeyCancelled), graph.BoolValue(true))
		}),
	}
}
