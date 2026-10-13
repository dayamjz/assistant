package judge

// Record is one answer as PRD section 5 has it kept: which run and stage
// asked, which check, about which path, of which model version, what came
// back, the cutoff it was compared against, and the finding it produced.
//
// A failed call is recorded too, with no probability and its failure, so the
// record says how often a check could not run as well as what it said when it
// did.
type Record struct {
	// Run is the run the check was asked for.
	Run string
	// Stage is the stage that asked.
	Stage string
	// Check is the question asked.
	Check Check
	// Path is the repository-relative path the question was about.
	Path string
	// Model is the model version the question was put to.
	Model string
	// Probability is the answer, meaningful only when Answered is true.
	Probability float64
	// Answered is whether the check produced an answer. It is false for a
	// failed call, whose Probability is then zero and means nothing.
	Answered bool
	// Cutoff is the cutoff the answer was compared against.
	Cutoff float64
	// Failure is why the check produced no answer, empty when it produced
	// one.
	Failure Failure
	// Finding is the action of the finding the answer produced, empty when it
	// produced none. It is the caller's to state, because the caller builds
	// the finding: this package answers and does not report.
	Finding string
}

// Recorder keeps answers. It returns nothing, so recording is best-effort by
// the shape of the seam: an implementation that cannot keep a row drops it,
// and the record understates rather than holding a run up.
type Recorder interface {
	RecordAnswer(Record)
}

// RecordOf is the record of r as asked for run at stage, with finding the
// action of the finding the caller built from it, empty for none.
func RecordOf(run, stage string, r Result, finding string) Record {
	p, ok := r.Probability()
	rec := Record{
		Run: run, Stage: stage, Check: r.Check(), Path: r.Path(), Model: r.Model(),
		Probability: p, Answered: ok, Cutoff: r.Cutoff(), Finding: finding,
	}
	if failure, _, isFailed := r.Failed(); isFailed {
		rec.Failure = failure
	}
	return rec
}

// Record hands rec to the client's recorder, and drops it when the client was
// built without one. A nil client drops it too.
func (c *Client) Record(rec Record) {
	if c == nil || c.recorder == nil {
		return
	}
	c.recorder.RecordAnswer(rec)
}
