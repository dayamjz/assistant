package judge

import "context"

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

// Recorder keeps answers. It returns why it could not keep one, so a caller
// can say the record lost a row rather than leaving that in a log: the review
// stage names every answer it could not record in a note. Recording is still
// best-effort - a row the recorder could not keep is not retried, and nothing
// is held up for it - and the record then understates, which that note is
// what makes visible.
type Recorder interface {
	RecordAnswer(Record) error
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

// Record hands rec to the client's recorder and returns why it could not keep
// it. A client built without a recorder, and a nil client, keep nothing and
// report nothing lost, because nothing was asked to keep it.
func (c *Client) Record(rec Record) error {
	if c == nil || c.recorder == nil {
		return nil
	}
	return c.recorder.RecordAnswer(rec)
}

// RunFirings is one earlier run in which a check fired: how many of its
// answers to the check produced a finding, and how many of those anything the
// run did next followed - a later finding at the path, a fix round touching
// it, or a person's answer at a hold over it.
type RunFirings struct {
	// Run is the run.
	Run string
	// Fired is how many answers produced a finding.
	Fired int
	// Followed is how many of those anything followed.
	Followed int
}

// History reads the record back for the one question the review stage asks
// of it: where did this check fire before, and did anything follow.
type History interface {
	// Firings returns the finished runs of the repository run validates,
	// other than run itself, in which check produced at least one finding,
	// newest first. A run still in progress is left out, because what
	// follows an answer in it may not have happened yet.
	Firings(ctx context.Context, run string, check Check) ([]RunFirings, error)
}

// Firings asks the client's history where check fired before run, and
// reports false when the client was built without one, which is a client that
// has no record to read rather than a record that is empty.
func (c *Client) Firings(ctx context.Context, run string, check Check) ([]RunFirings, bool, error) {
	if c == nil || c.history == nil {
		return nil, false, nil
	}
	firings, err := c.history.Firings(ctx, run, check)
	return firings, true, err
}
