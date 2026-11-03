package judge

import (
	"encoding/json"
	"time"
)

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
	// Measured is whether the client measured a call for this answer. It is
	// false for a path whose diff could not be read (Unasked): nothing was
	// sent or timed, so Requests, Usage, and Elapsed are zero and mean nothing.
	Measured bool
	// Requests is how many requests left the machine for this answer, failed
	// ones included; Result.Requests is its source.
	Requests int
	// Usage is those requests' size; Usage says which field is a reported
	// token count and which a size in bytes.
	Usage Usage
	// Elapsed is from the check's start to its parsed answer or its
	// failure, across every request it took.
	Elapsed time.Duration
	// Rate is the rate the call was priced at, kept so the row outlives a
	// change of rate, and nil when its price is not known: the answer is not
	// Measured, rates has no valid entry for Model, or a request for it
	// reported no tokens.
	Rate *Rate
}

// Price is the call's price in US dollars at Rate, and false when the call
// was not priced.
func (r Record) Price() (float64, bool) {
	if r.Rate == nil {
		return 0, false
	}
	return float64(r.Usage.ReportedInputTokens)*r.Rate.InputUSDPerMillionTokens/1e6 +
		float64(r.Usage.ReportedOutputTokens)*r.Rate.OutputUSDPerMillionTokens/1e6, true
}

// Usage is how large one check's requests were. A success reply may carry a
// usage field with input_tokens and output_tokens, the provider's own counts,
// which the Reported fields sum. A reply read without them adds its request's
// and its own body sizes in bytes to the Unreported fields, for a later
// estimate at a stated ratio; no field holds an estimate. A request that got
// no reply is in neither; Record.Requests counts it.
type Usage struct {
	// ReportedRequests is how many replies carried a usage field.
	ReportedRequests int
	// ReportedInputTokens and ReportedOutputTokens are those replies' counts,
	// summed.
	ReportedInputTokens, ReportedOutputTokens int64
	// UnreportedRequests is how many replies were read without a usage field.
	UnreportedRequests int
	// UnreportedSentBytes and UnreportedReceivedBytes are the body sizes of
	// those requests and of their replies, summed.
	UnreportedSentBytes, UnreportedReceivedBytes int64
}

// add counts one reply read to the request body sent: its usage field when
// a success carries a whole one, and the two bodies' sizes otherwise.
func (u *Usage) add(status int, sent, raw []byte) {
	var reply struct {
		Usage struct {
			Input  *int64 `json:"input_tokens"`
			Output *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if status >= 200 && status <= 299 && json.Unmarshal(raw, &reply) == nil &&
		reply.Usage.Input != nil && reply.Usage.Output != nil && *reply.Usage.Input >= 0 && *reply.Usage.Output >= 0 {
		u.ReportedRequests++
		u.ReportedInputTokens += *reply.Usage.Input
		u.ReportedOutputTokens += *reply.Usage.Output
		return
	}
	u.UnreportedRequests++
	u.UnreportedSentBytes += int64(len(sent))
	u.UnreportedReceivedBytes += int64(len(raw))
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
	return recordAt(run, stage, r, finding, rates)
}

// recordAt is RecordOf pricing from table, which is rates outside a test.
func recordAt(run, stage string, r Result, finding string, table map[string]Rate) Record {
	p, ok := r.Probability()
	rec := Record{
		Run: run, Stage: stage, Check: r.Check(), Path: r.Path(), Model: r.Model(),
		Probability: p, Answered: ok, Cutoff: r.Cutoff(), Finding: finding,
		Measured: r.measured, Requests: r.requests, Usage: r.usage, Elapsed: r.elapsed,
	}
	if failure, _, isFailed := r.Failed(); isFailed {
		rec.Failure = failure
	}
	if rate, ok := table[rec.Model]; ok && rate.valid() == nil && rec.Measured &&
		rec.Usage.ReportedRequests == rec.Requests {
		rec.Rate = &rate
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
