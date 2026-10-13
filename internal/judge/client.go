package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/dayamjz/assistant/internal/redact"
)

// KeyVariable is the environment variable the provider key is read from. It is
// read from the operator's environment and from nowhere else: no
// configuration key carries it, so no file a repository controls can supply
// one, and the key being present is the operator's consent to send.
//
// The name is this product's own rather than the provider's generic one, and
// that is the consent rule rather than a style choice: the checks run by
// default wherever the key is present, so a provider variable an operator set
// for some other purpose would start sending changed lines without anyone
// deciding to. Nothing falls back to the provider's variable when this one is
// unset, because a fallback would reopen exactly that.
const KeyVariable = "ASSISTANT_DECISION_MODEL_KEY"

// Endpoint is the provider's question endpoint.
const Endpoint = "https://api.typesafe.ai/v1/systemone"

// DefaultTimeout bounds one request, from sending it to reading its answer.
const DefaultTimeout = 30 * time.Second

// Bounds on how many requests leave the machine. MaxRequestsPerReview bounds
// one review's whole fan-out - every screen piece and every trace together -
// for a client from ForReview. MaxRequestsPerFile bounds one file's screen,
// counting each piece and each re-ask of a halved piece, so that a file large
// enough to need more is not screened at all rather than screened in part.
//
// Neither was measured. They are round figures chosen so that an ordinary
// review is far inside them - one request per file and per trace - and so
// that a review's worst case is bounded in time as well as in count: with the
// review stage asking four at a time and each request bounded by
// DefaultTimeout, 128 requests is at most 32 consecutive rounds of timeouts,
// about sixteen minutes. Eight pieces of maxPieceBytes is half a mebibyte of
// one file's changed lines.
const (
	MaxRequestsPerReview = 128
	MaxRequestsPerFile   = 8
)

// Bounds on what one request carries. They are this package's own, not the
// provider's: the provider states its limit in tokens, and this package does
// not count tokens. maxPieceBytes is where a file's changed lines are first
// cut into consecutive pieces; a piece the provider still refuses as too large
// is cut in half and asked again, down to minPieceBytes, below which the
// refusal stands as FailureTooLarge. So whether a piece fits is decided by the
// provider's answer and not by an estimate made here.
const (
	maxPieceBytes = 64 << 10
	minPieceBytes = 512
	// maxResponseBytes bounds what is read of a response. An answer to one
	// question is a few hundred bytes; anything past this is unreadable.
	maxResponseBytes = 1 << 20
)

// Client puts the fixed questions to the decision model. It is safe for
// concurrent use, holds no per-run state, and is built once per service.
//
// A nil *Client is a client with no key: every check fails with
// FailureNoKey and sends nothing.
type Client struct {
	key      string
	endpoint string
	timeout  time.Duration
	http     *http.Client
	redactor redact.Redactor
	recorder Recorder
	maxPiece int
	// perReview and perFile are the request bounds; allowance is what is
	// left of perReview for the one review a client from ForReview serves,
	// and nil on a client that serves none.
	perReview int
	perFile   int
	allowance *allowance
}

// allowance is how many requests one review may still send. It is shared by
// every goroutine asking on that review's behalf.
type allowance struct {
	of   int
	left atomic.Int64
}

// take spends one request and reports whether there was one to spend. A nil
// allowance is a client serving no review, which only the per-file bound
// limits.
func (a *allowance) take() bool {
	if a == nil {
		return true
	}
	return a.left.Add(-1) >= 0
}

// over is the failure detail for a request the review's bound refused.
func (a *allowance) over() string {
	return "the review reached its bound of " + strconv.Itoa(a.of) + " decision-model requests before this was asked"
}

// Option configures a Client.
type Option func(*Client)

// WithEndpoint sends requests to url instead of Endpoint. It exists so a test
// can stand a server up that answers over the wire; nothing in this build
// points a client anywhere else.
func WithEndpoint(url string) Option { return func(c *Client) { c.endpoint = url } }

// WithTimeout bounds one request by d instead of DefaultTimeout. A value that
// is not positive is ignored.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithRecorder gives the client the recorder Record forwards to. Without one,
// Record drops what it is handed.
func WithRecorder(r Recorder) Option { return func(c *Client) { c.recorder = r } }

// WithBounds replaces MaxRequestsPerReview and MaxRequestsPerFile with
// perReview and perFile; a value that is not positive leaves its bound as it
// was. It exists so a test can reach a bound without a change of the size the
// shipped bounds are set for. Nothing in this build calls it, and no
// configuration key reaches either bound.
func WithBounds(perReview, perFile int) Option {
	return func(c *Client) {
		if perReview > 0 {
			c.perReview = perReview
		}
		if perFile > 0 {
			c.perFile = perFile
		}
	}
}

// ForReview returns a client for one review: everything c has, plus one
// allowance of c's per-review bound that every request the returned client
// sends draws on, from any goroutine. A request the allowance cannot cover is
// not sent, and its check fails with FailureOverBound. A nil client returns
// nil, which is still a client with no key.
//
// The allowance is spent in the order requests are taken, so when a review
// reaches the bound, which paths went unasked follows the order the caller
// asked in and, for requests asked concurrently, which goroutine got there
// first. The caller learns which from each Result and is the one to name
// them.
func (c *Client) ForReview() *Client {
	if c == nil {
		return nil
	}
	bounded := *c
	bounded.allowance = &allowance{of: c.perReview}
	bounded.allowance.left.Store(int64(c.perReview))
	return &bounded
}

// New returns a client that authenticates with key. A key that is empty once
// trimmed is no key: the client sends nothing and every check fails with
// FailureNoKey.
func New(key string, opts ...Option) *Client {
	c := &Client{
		key:      strings.TrimSpace(key),
		endpoint: Endpoint,
		timeout:  DefaultTimeout,
		http:     &http.Client{},
		redactor: redact.New(),
		maxPiece: maxPieceBytes,

		perReview: MaxRequestsPerReview,
		perFile:   MaxRequestsPerFile,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// FromEnvironment returns a client whose key is KeyVariable as lookup reports
// it, which is os.LookupEnv for the product. A variable that is unset or
// empty is no key.
func FromEnvironment(lookup func(string) (string, bool), opts ...Option) *Client {
	var key string
	if lookup != nil {
		key, _ = lookup(KeyVariable)
	}
	return New(key, opts...)
}

// HasKey reports whether the client has a key, which is whether any check can
// send a request at all.
func (c *Client) HasKey() bool { return c != nil && c.key != "" }

// Trace asks whether reason is a true description of diff, the changed lines
// of path. The whole diff goes in one request, because the question is about
// the reason against the path's change as a whole; a diff over this package's
// piece bound, or one the provider refuses as too large, fails with
// FailureTooLarge rather than being cut, and the reason is then not checked.
func (c *Client) Trace(ctx context.Context, path, reason, diff string) Result {
	if !c.HasKey() {
		return failed(CheckTrace, path, FailureNoKey, "no key for the decision model is in "+KeyVariable, 0)
	}
	if strings.TrimSpace(diff) == "" || strings.TrimSpace(reason) == "" {
		return failed(CheckTrace, path, FailureNothingToAsk, "there was no changed line or no reason to put to the model", 0)
	}
	if len(diff) > c.maxPiece {
		return failed(CheckTrace, path, FailureTooLarge, "the diff is "+strconv.Itoa(len(diff))+
			" bytes and one trace check carries at most "+strconv.Itoa(c.maxPiece), 0)
	}
	if !c.allowance.take() {
		return failed(CheckTrace, path, FailureOverBound, c.allowance.over(), 0)
	}
	p, failure, detail := c.ask(ctx, CheckTrace, map[string]string{"path": path, "reason": reason, "diff": diff})
	if failure != "" {
		return failed(CheckTrace, path, failure, detail, 1)
	}
	return answered(CheckTrace, path, p, 1)
}

// Screen asks whether diff, the changed lines of path, adds text aimed at a
// reviewing agent that tries to steer it. Each request carries one file, never
// several, and a diff too large for one request is cut into consecutive
// pieces of whole lines, each asked on its own; nothing is truncated.
//
// The pieces come to one result on the side that adds scrutiny. Any piece the
// model flags flags the file, and the asking stops there. Otherwise a piece
// that failed fails the file, because a piece nobody read is not a piece found
// clean. Only when every piece was answered and none flagged is the file
// answered unflagged, with the highest piece's probability.
func (c *Client) Screen(ctx context.Context, path, diff string) Result {
	if !c.HasKey() {
		return failed(CheckScreen, path, FailureNoKey, "no key for the decision model is in "+KeyVariable, 0)
	}
	if strings.TrimSpace(diff) == "" {
		return failed(CheckScreen, path, FailureNothingToAsk, "there were no changed lines to put to the model", 0)
	}
	queue := pieces(diff, c.maxPiece)
	if len(queue) > c.perFile {
		return failed(CheckScreen, path, FailureOverBound, "the diff is "+strconv.Itoa(len(queue))+
			" pieces and one file's screen may take at most "+strconv.Itoa(c.perFile)+" requests", 0)
	}
	var (
		requests   int
		highest    float64
		firstFail  Failure
		failDetail string
	)
	for len(queue) > 0 {
		piece := queue[0]
		queue = queue[1:]
		if requests >= c.perFile {
			return failed(CheckScreen, path, FailureOverBound, "pieces refused as too large were cut "+
				"until the file needed more than the "+strconv.Itoa(c.perFile)+" requests one file's "+
				"screen may take", requests)
		}
		if !c.allowance.take() {
			return failed(CheckScreen, path, FailureOverBound, c.allowance.over(), requests)
		}
		p, failure, detail := c.ask(ctx, CheckScreen, map[string]string{"path": path, "diff": piece})
		requests++
		if failure == FailureTooLarge && len(piece) > minPieceBytes {
			queue = append(halves(piece), queue...)
			continue
		}
		if failure == FailureCancelled {
			return failed(CheckScreen, path, failure, detail, requests)
		}
		if failure != "" {
			if firstFail == "" {
				firstFail, failDetail = failure, detail
			}
			continue
		}
		if CheckScreen.fires(p) {
			return answered(CheckScreen, path, p, requests)
		}
		highest = math.Max(highest, p)
	}
	if firstFail != "" {
		return failed(CheckScreen, path, firstFail, failDetail, requests)
	}
	return answered(CheckScreen, path, highest, requests)
}

// request is the provider's request body.
type request struct {
	Model     string                  `json:"model"`
	State     map[string]string       `json:"state"`
	Questions map[string]wireQuestion `json:"questions"`
}

// wireQuestion is one noul question as the provider takes it.
type wireQuestion struct {
	Type         string       `json:"type"`
	Instructions string       `json:"instructions"`
	Criteria     wireCriteria `json:"criteria"`
}

// wireCriteria is what counts as yes and as no.
type wireCriteria struct {
	True  string `json:"true"`
	False string `json:"false"`
}

// response is the provider's success body. Fields are pointers or raw so that
// an absent one is told apart from a zero one.
type response struct {
	Model   *string                    `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
}

// wireAnswer is one answer. Noul is a pointer so a missing probability is not
// read as zero.
type wireAnswer struct {
	Type string   `json:"type"`
	Noul *float64 `json:"noul"`
}

// errorBody is the provider's refusal body, as far as this package reads it:
// the one error type it acts on.
type errorBody struct {
	Detail struct {
		ErrorType string `json:"error_type"`
	} `json:"detail"`
}

// ask puts one question about state to the model and returns the probability,
// or a failure and a sentence about it. The sentence never quotes a header,
// and it passes the redactor before it is returned.
func (c *Client) ask(ctx context.Context, check Check, state map[string]string) (float64, Failure, string) {
	p, failure, detail := c.askOnce(ctx, check, state)
	return p, failure, c.redactor.Redact(detail)
}

func (c *Client) askOnce(ctx context.Context, check Check, state map[string]string) (float64, Failure, string) {
	q := questions[check]
	body, err := json.Marshal(request{
		Model: ModelVersion,
		State: state,
		Questions: map[string]wireQuestion{q.name: {
			Type:         "noul",
			Instructions: q.instructions,
			Criteria:     wireCriteria{True: q.whenTrue, False: q.whenFalse},
		}},
	})
	if err != nil {
		return 0, FailureUnreadable, "the question could not be encoded: " + err.Error()
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, FailureTransport, "the request could not be built: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, transportFailure(ctx, reqCtx, err), "the request did not complete: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return 0, transportFailure(ctx, reqCtx, err), "the response could not be read: " + err.Error()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var refusal errorBody
		if json.Unmarshal(raw, &refusal) == nil && refusal.Detail.ErrorType == "max_tokens_exceeded" {
			return 0, FailureTooLarge, "the provider refused the content as over its token limit"
		}
		return 0, FailureStatus, "the provider answered with status " + strconv.Itoa(resp.StatusCode)
	}
	if len(raw) > maxResponseBytes {
		return 0, FailureUnreadable, "the response is over " + strconv.Itoa(maxResponseBytes) + " bytes"
	}
	return readAnswer(raw, q.name)
}

// readAnswer reads the probability for the question named name out of a
// success body, refusing anything that is not exactly a noul answer between
// zero and one from ModelVersion.
func readAnswer(raw []byte, name string) (float64, Failure, string) {
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, FailureUnreadable, "the response is not the provider's answer shape: " + err.Error()
	}
	if resp.Model == nil || *resp.Model != ModelVersion {
		model := "no model"
		if resp.Model != nil {
			model = strconv.Quote(*resp.Model)
		}
		return 0, FailureUnreadable, "the answer came from " + model + ", and the cutoff was measured on " + ModelVersion
	}
	rawAnswer, ok := resp.Answers[name]
	if !ok {
		return 0, FailureUnreadable, "the response carries no answer to the question asked"
	}
	var answer wireAnswer
	if err := json.Unmarshal(rawAnswer, &answer); err != nil {
		return 0, FailureUnreadable, "the answer is not a noul answer: " + err.Error()
	}
	if answer.Type != "noul" || answer.Noul == nil {
		return 0, FailureUnreadable, "the answer is not a noul answer with a probability"
	}
	p := *answer.Noul
	if math.IsNaN(p) || p < 0 || p > 1 {
		return 0, FailureUnreadable, "the probability " + strconv.FormatFloat(p, 'g', -1, 64) + " is outside zero to one"
	}
	return p, "", ""
}

// transportFailure tells a run being stopped from a request that timed out
// from one that failed on the way, by asking the two contexts rather than
// reading the error's text.
func transportFailure(parent, req context.Context, err error) Failure {
	switch {
	case parent.Err() != nil:
		return FailureCancelled
	case errors.Is(req.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return FailureTimeout
	default:
		return FailureTransport
	}
}

// pieces cuts diff into consecutive pieces of at most limit bytes, each made
// of whole lines where a line fits, in order and with nothing left out:
// joining them gives diff back.
func pieces(diff string, limit int) []string {
	if len(diff) <= limit {
		return []string{diff}
	}
	var out []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			out = append(out, current.String())
			current.Reset()
		}
	}
	for _, line := range strings.SplitAfter(diff, "\n") {
		if line == "" {
			continue
		}
		if current.Len()+len(line) > limit {
			flush()
		}
		for len(line) > limit {
			cut := runeBoundary(line, limit)
			out = append(out, line[:cut])
			line = line[cut:]
		}
		current.WriteString(line)
	}
	flush()
	return out
}

// halves cuts a piece in two at the line boundary nearest its middle, or at
// the rune boundary nearest it when the piece is one line, so that joining
// the two gives the piece back.
func halves(piece string) []string {
	mid := len(piece) / 2
	if i := strings.LastIndexByte(piece[:mid], '\n'); i > 0 {
		mid = i + 1
	} else if j := strings.IndexByte(piece[mid:], '\n'); j >= 0 && mid+j+1 < len(piece) {
		mid = mid + j + 1
	} else {
		mid = runeBoundary(piece, mid)
	}
	if mid <= 0 || mid >= len(piece) {
		mid = runeBoundary(piece, len(piece)/2)
	}
	return []string{piece[:mid], piece[mid:]}
}

// runeBoundary returns the largest index at or below n that does not split a
// UTF-8 sequence, and at least one so that a cut always makes progress.
func runeBoundary(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for i := n; i > 0; i-- {
		if utf8.RuneStart(s[i]) {
			return i
		}
	}
	return max(1, n)
}

// String renders a client for a diagnostic without its key. It exists because
// fmt prints a pointer to a struct field by field, so a client formatted with
// %v would otherwise print the key.
func (c *Client) String() string {
	if !c.HasKey() {
		return "judge client (no key)"
	}
	return fmt.Sprintf("judge client for %s (key present)", ModelVersion)
}

// GoString is String for %#v, which fmt otherwise answers field by field.
func (c *Client) GoString() string { return c.String() }
