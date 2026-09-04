package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dayamjz/assistant/internal/graph"
)

// stageNode builds the graph node that runs one stage. The node's own reads
// and writes are the implementation's declarations plus the keys this adapter
// needs: it reads what decides whether the stage runs at all, and it writes
// the stage's report and outcome, which no implementation may write.
func stageNode(stage Stage, impl Implementation) graph.Node {
	reads := union(impl.Reads, KeySkip, KeyDiffEmpty)
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
				// Normalizing here is where P3 lands for every stage at once:
				// a finding with a missing, empty, or unrecognized action
				// becomes ask, and classify then holds the stage for it.
				report := out.Report.Normalize()
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
func holdNode(stage Stage) graph.Node {
	return graph.Node{
		Name:   stage.HoldNode(),
		Reads:  []string{string(stage.AnswerKey())},
		Writes: []string{string(stage.OutcomeKey())},
		Halt: &graph.Halt{
			Question: fmt.Sprintf("The %s stage is holding for your decision.", stage),
			Options:  holdAnswers(),
			Into:     string(stage.AnswerKey()),
		},
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
	writes := union(fixer.Writes, stage.FixKey())
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
				if err := w.Set(string(stage.FixKey()), graph.TextValue(out.Summary)); err != nil {
					return err
				}
				return applyWrites(w, permitted, "the fixer for "+stage.String(), out.Writes)
			}
		},
	}
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
