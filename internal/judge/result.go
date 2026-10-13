package judge

// Failure is why a check did not produce an answer. A Result carrying one has
// no probability: Probability reports false and Fired reports false, so no
// path reads a failed call as an answer in either direction.
type Failure string

const (
	// FailureNoKey is a client with no provider key. Nothing was sent.
	FailureNoKey Failure = "no-key"
	// FailureNothingToAsk is a question with no changed lines to put to the
	// model. Nothing was sent, because an answer about nothing is not one.
	FailureNothingToAsk Failure = "nothing-to-ask"
	// FailureTooLarge is content the model would not take in one request:
	// the provider refused it as over its token limit, or for a check that
	// is never split it was over this package's own bound before anything
	// was sent.
	FailureTooLarge Failure = "too-large"
	// FailureTransport is a request that did not complete: the connection
	// failed or was cut off.
	FailureTransport Failure = "transport"
	// FailureTimeout is a request that did not answer within the client's
	// timeout.
	FailureTimeout Failure = "timeout"
	// FailureCancelled is a request abandoned because the caller's context
	// ended, which is the run being stopped rather than the provider failing.
	FailureCancelled Failure = "cancelled"
	// FailureStatus is a response whose status was not a success.
	FailureStatus Failure = "status"
	// FailureUnreadable is a successful response the program cannot read as
	// an answer to the question asked: malformed, missing the answer, of
	// another type, out of the range zero to one, or answered by a model
	// other than ModelVersion.
	FailureUnreadable Failure = "unreadable"
)

// Result is what one check came to for one path: a probability, or a failure
// with its reason, and never both.
//
// Its fields are unexported so that the two cannot be confused: a Result is
// built only by this package, and the accessors below are the only way to
// read it. The zero Result is a failure of no kind and reports nothing as an
// answer.
type Result struct {
	check       Check
	path        string
	probability float64
	answered    bool
	failure     Failure
	detail      string
	requests    int
}

// Check is the check this result answers.
func (r Result) Check() Check { return r.check }

// Path is the repository-relative path the check was asked about.
func (r Result) Path() string { return r.path }

// Probability returns the model's probability of yes and true, or false when
// the check produced no answer. For a check split across several requests it
// is the answer that decided the result: the highest of the pieces' answers
// for the screen, which fires on a high answer.
func (r Result) Probability() (float64, bool) { return r.probability, r.answered }

// Failed returns why the check produced no answer and a sentence saying more,
// or false when it produced one. The sentence has passed the credential
// redactor and names no key.
func (r Result) Failed() (Failure, string, bool) {
	if r.answered {
		return "", "", false
	}
	failure := r.failure
	if failure == "" {
		failure = FailureUnreadable
	}
	return failure, r.detail, true
}

// Fired reports whether this result is the check's finding: a trace reason the
// model did not confirm, or a file the screen flagged. It is false for a
// failed check, because a failed call is never read as an answer either way.
func (r Result) Fired() bool { return r.answered && r.check.fires(r.probability) }

// Cutoff is the cutoff the answer was compared against.
func (r Result) Cutoff() float64 { return r.check.Cutoff() }

// Model is the model version the question was put to, which is ModelVersion
// whether or not the call was answered.
func (r Result) Model() string { return ModelVersion }

// Requests is how many requests left the machine for this result. It is zero
// when nothing was sent, which is how a caller can show that a missing key or
// an empty question sent nothing.
func (r Result) Requests() int { return r.requests }

// failed builds a failure result.
func failed(c Check, path string, f Failure, detail string, requests int) Result {
	return Result{check: c, path: path, failure: f, detail: detail, requests: requests}
}

// answered builds an answer result.
func answered(c Check, path string, p float64, requests int) Result {
	return Result{check: c, path: path, probability: p, answered: true, requests: requests}
}
