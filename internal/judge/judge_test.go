package judge_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/judge/judgetest"
)

// key is the provider key every client here is built with. It is long enough
// to be a bearer token by internal/redact's rule, so a test that finds it in a
// sentence has found a leak and not a coincidence.
const key = "ts_test_key_0123456789abcdef"

const diff = "--- a/notes.md\n+++ b/notes.md\n@@ -1 +1,2 @@\n line\n+another line\n"

func client(p *judgetest.Provider, opts ...judge.Option) *judge.Client {
	return judge.New(key, append([]judge.Option{judge.WithEndpoint(p.URL())}, opts...)...)
}

// Every answer from certain no to certain yes is read as exactly that answer,
// and each check fires on its own side of its own cutoff. The request is what
// the provider's schema asks for: the pinned model, one noul question, the
// state the check names, and the key as a bearer token.
func TestEveryAnswerFromZeroToOneIsReadAsGiven(t *testing.T) {
	t.Parallel()
	for _, p := range []float64{0, 0.05, 0.29, 0.3, 0.31, 0.5, 0.79, 0.8, 0.81, 0.99, 1} {
		provider := judgetest.New(t, judgetest.Always(judgetest.Answer(p)))
		c := client(provider)

		trace := c.Trace(t.Context(), "notes.md", "adds a line", diff)
		screen := c.Screen(t.Context(), "notes.md", diff)
		for _, r := range []judge.Result{trace, screen} {
			got, ok := r.Probability()
			if !ok || got != p {
				t.Fatalf("%s answered %v read as (%v, %v)", r.Check(), p, got, ok)
			}
			if _, _, isFailed := r.Failed(); isFailed {
				t.Fatalf("%s answered %v reads as failed", r.Check(), p)
			}
		}
		if want := p < 0.8; trace.Fired() != want {
			t.Errorf("the trace check at %v fired %v, want %v: it fires below 0.8", p, trace.Fired(), want)
		}
		if want := p >= 0.3; screen.Fired() != want {
			t.Errorf("the screen at %v fired %v, want %v: it fires at or above 0.3", p, screen.Fired(), want)
		}

		requests := provider.Requests()
		if len(requests) != 2 {
			t.Fatalf("two checks sent %d requests, want 2", len(requests))
		}
		for _, r := range requests {
			if r.Model != judge.ModelVersion || r.Authorization != "Bearer "+key || r.Path != "notes.md" || r.Diff != diff {
				t.Fatalf("a request does not carry the pinned model, the key, and the state: %+v", r)
			}
		}
		if requests[0].Reason != "adds a line" || requests[1].Reason != "" {
			t.Fatalf("the trace check carries the reason and the screen does not: %+v", requests)
		}
	}
}

// A failed call is never an answer. Each way a call can fail produces a
// failure of its own kind, no probability, nothing fired, and a sentence that
// does not carry the key.
func TestAFailedCallIsNeverAnAnswer(t *testing.T) {
	t.Parallel()
	answeredUnder := func(name string) judgetest.Reply {
		return judgetest.Raw(http.StatusOK, fmt.Sprintf(
			`{"model":%q,"answers":{%q:{"type":"noul","noul":0.9}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			judge.ModelVersion, name))
	}
	cases := []struct {
		name  string
		reply judgetest.Reply
		want  judge.Failure
	}{
		{"a server error", judgetest.Status(http.StatusInternalServerError), judge.FailureStatus},
		{"an authentication refusal", judgetest.Status(http.StatusUnauthorized), judge.FailureStatus},
		{"a refusal over the token limit", judgetest.TooLarge(), judge.FailureTooLarge},
		{"a body that is not JSON", judgetest.Raw(http.StatusOK, "<html>ok</html>"), judge.FailureUnreadable},
		{"an empty body", judgetest.Raw(http.StatusOK, ""), judge.FailureUnreadable},
		{"an answer under another name", answeredUnder("something_else"), judge.FailureUnreadable},
		{"an answer of another type", judgetest.Raw(http.StatusOK, `{"model":"`+judge.ModelVersion+`","answers":{"trace_reason":{"type":"score","score":3},"reviewer_directed_text":{"type":"score","score":3}},"usage":{"input_tokens":1,"output_tokens":1}}`), judge.FailureUnreadable},
		{"an answer with no probability", judgetest.Raw(http.StatusOK, `{"model":"`+judge.ModelVersion+`","answers":{"trace_reason":{"type":"noul"},"reviewer_directed_text":{"type":"noul"}},"usage":{"input_tokens":1,"output_tokens":1}}`), judge.FailureUnreadable},
		{"a probability as text", judgetest.Raw(http.StatusOK, `{"model":"`+judge.ModelVersion+`","answers":{"trace_reason":{"type":"noul","noul":"0.9"},"reviewer_directed_text":{"type":"noul","noul":"0.9"}},"usage":{"input_tokens":1,"output_tokens":1}}`), judge.FailureUnreadable},
		{"a probability above one", judgetest.Answer(1.5), judge.FailureUnreadable},
		{"a probability below zero", judgetest.Answer(-0.1), judge.FailureUnreadable},
		{"an answer from another model", judgetest.AnswerFrom("jev-latest", 0.9), judge.FailureUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := judgetest.New(t, judgetest.Always(tc.reply))
			c := client(provider)
			assertFailed(t, c.Trace(t.Context(), "notes.md", "adds a line", diff), tc.want)
			assertFailed(t, c.Screen(t.Context(), "notes.md", diff), tc.want)
		})
	}
}

// A client's request survives another provider in the process stopping. That
// was #470: httptest.Server.Close closes the idle connections of
// http.DefaultTransport, and a reply with no body puts its connection back
// among the idle ones before the client has its response, so a sibling
// subtest's provider stopping at that moment turned "an empty body" into a
// transport failure. The trace hook PutIdleConn runs at that moment, so the
// test stops a sibling there on every request rather than waiting for chance
// to line the two up.
func TestAnotherProviderStoppingDoesNotReachAClient(t *testing.T) {
	t.Parallel()
	provider := judgetest.New(t, judgetest.Always(judgetest.Raw(http.StatusOK, "")))
	c := client(provider)
	for range 200 {
		sibling := httptest.NewServer(http.NotFoundHandler())
		var once sync.Once
		ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{
			PutIdleConn: func(err error) {
				if err == nil {
					once.Do(sibling.Close)
				}
			},
		})
		assertFailed(t, c.Screen(ctx, "notes.md", diff), judge.FailureUnreadable)
		// The hook firing is what makes this test test anything: without it
		// no sibling stopped while the reply was in flight.
		stopped := true
		once.Do(func() { stopped = false })
		if !stopped {
			t.Fatal("no connection went idle during the request, so no sibling stopped while it was in flight")
		}
		sibling.Close()
	}
}

// A provider that never answers is a timeout, and the client gives up within
// its own bound rather than the run's.
func TestAProviderThatNeverAnswersIsATimeout(t *testing.T) {
	t.Parallel()
	provider := judgetest.New(t, judgetest.Always(judgetest.Hang()))
	c := client(provider, judge.WithTimeout(50*time.Millisecond))
	start := time.Now()
	assertFailed(t, c.Trace(t.Context(), "notes.md", "adds a line", diff), judge.FailureTimeout)
	assertFailed(t, c.Screen(t.Context(), "notes.md", diff), judge.FailureTimeout)
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("two timed-out checks took %v", elapsed)
	}
}

// A run being stopped is told apart from the provider failing.
func TestACancelledRunIsNotAProviderFailure(t *testing.T) {
	t.Parallel()
	provider := judgetest.New(t, judgetest.Always(judgetest.Hang()))
	c := client(provider)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	assertFailed(t, c.Screen(ctx, "notes.md", diff), judge.FailureCancelled)
}

// A provider nobody is listening at is a transport failure. The endpoint here
// carries the key as a URL's userinfo, which Go's own error text keeps, so the
// sentence coming back without it is the redactor's doing.
func TestAnUnreachableProviderIsATransportFailure(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the port: %v", err)
	}
	c := judge.New(key, judge.WithEndpoint("http://"+key+"@"+address+"/v1/systemone"),
		judge.WithTimeout(5*time.Second))
	assertFailed(t, c.Trace(t.Context(), "notes.md", "adds a line", diff), judge.FailureTransport)
}

// No key sends nothing, whatever is asked.
func TestNoKeySendsNothing(t *testing.T) {
	t.Parallel()
	provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.9)))
	for _, c := range []*judge.Client{
		judge.New("", judge.WithEndpoint(provider.URL())),
		judge.New("   ", judge.WithEndpoint(provider.URL())),
		judge.FromEnvironment(func(string) (string, bool) { return "", false }, judge.WithEndpoint(provider.URL())),
		judge.FromEnvironment(nil, judge.WithEndpoint(provider.URL())),
		nil,
	} {
		if c.HasKey() {
			t.Fatalf("%v reports a key", c)
		}
		for _, r := range []judge.Result{
			c.Trace(t.Context(), "notes.md", "adds a line", diff),
			c.Screen(t.Context(), "notes.md", diff),
		} {
			assertFailed(t, r, judge.FailureNoKey)
			if r.Requests() != 0 {
				t.Fatalf("a check with no key reports %d requests", r.Requests())
			}
		}
	}
	if n := provider.Count(); n != 0 {
		t.Fatalf("clients with no key sent %d requests", n)
	}
	withKey := judge.FromEnvironment(func(name string) (string, bool) {
		if name == judge.KeyVariable {
			return key, true
		}
		return "", false
	}, judge.WithEndpoint(provider.URL()))
	if r := withKey.Screen(t.Context(), "notes.md", diff); !r.Fired() || provider.Count() != 1 {
		t.Fatalf("the positive control did not send: %d requests, fired %v", provider.Count(), r.Fired())
	}
}

// The provider's own generic variable is not consent. With it set and the
// product's variable unset, nothing is sent, through the process environment
// itself and not a lookup this test wrote.
func TestTheProvidersOwnVariableIsNotConsent(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", key)
	t.Setenv(judge.KeyVariable, "")
	provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.9)))
	c := judge.FromEnvironment(os.LookupEnv, judge.WithEndpoint(provider.URL()))
	if c.HasKey() {
		t.Fatal("a client built with only the provider's variable set has a key")
	}
	assertFailed(t, c.Screen(t.Context(), "notes.md", diff), judge.FailureNoKey)
	if provider.Count() != 0 {
		t.Fatalf("%d requests left the machine on the provider's variable alone", provider.Count())
	}
	t.Setenv(judge.KeyVariable, key)
	if !judge.FromEnvironment(os.LookupEnv).HasKey() {
		t.Fatal("the positive control: the product's own variable is not read")
	}
}

// The screen sends one file at a time and never truncates: a diff over the
// piece bound is cut into consecutive pieces that put back together are the
// diff, and a piece refused as too large is halved and asked again.
func TestTheScreenSplitsALargeFileAndLeavesNothingOut(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&b, "+line %03d of a long added file\n", i)
	}
	long := b.String()
	provider := judgetest.New(t, func(r judgetest.Request) judgetest.Reply {
		if len(r.Diff) > 1500 {
			return judgetest.TooLarge()
		}
		return judgetest.Answer(0.1)
	})
	r := client(provider, judge.WithMaxPiece(4096), judge.WithBounds(0, 64)).Screen(t.Context(), "big.txt", long)
	if p, ok := r.Probability(); !ok || p != 0.1 || r.Fired() {
		t.Fatalf("a large clean file came to (%v, %v), fired %v", p, ok, r.Fired())
	}
	var answered strings.Builder
	for _, req := range provider.Requests() {
		if req.Path != "big.txt" {
			t.Fatalf("a piece was sent under %q", req.Path)
		}
		if len(req.Diff) <= 1500 {
			answered.WriteString(req.Diff)
		}
	}
	if answered.String() != long {
		t.Fatalf("the answered pieces do not put back together as the diff: %d of %d bytes",
			answered.Len(), len(long))
	}
	if r.Requests() != provider.Count() || r.Requests() < 4 {
		t.Fatalf("the result reports %d requests and the provider saw %d", r.Requests(), provider.Count())
	}
}

// One flagged piece flags the file, and a piece that could not be read fails
// the file rather than counting as clean.
func TestThePiecesComeToTheStricterResult(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := range 100 {
		fmt.Fprintf(&b, "+line %03d\n", i)
	}
	long := b.String()
	flagLast := judgetest.New(t, func(r judgetest.Request) judgetest.Reply {
		if strings.Contains(r.Diff, "line 099") {
			return judgetest.Answer(0.95)
		}
		return judgetest.Answer(0.01)
	})
	if r := client(flagLast, judge.WithMaxPiece(200)).Screen(t.Context(), "f.txt", long); !r.Fired() {
		t.Fatalf("a file whose last piece was flagged came to unflagged")
	}
	failOne := judgetest.New(t, func(r judgetest.Request) judgetest.Reply {
		if strings.Contains(r.Diff, "line 050") {
			return judgetest.Status(http.StatusBadGateway)
		}
		return judgetest.Answer(0.01)
	})
	assertFailed(t, client(failOne, judge.WithMaxPiece(200)).Screen(t.Context(), "f.txt", long), judge.FailureStatus)
}

// The trace check is never cut: a diff over the bound fails it before
// anything is sent, and a provider refusal over the token limit fails it too.
func TestATraceTooLargeForOneRequestIsNotChecked(t *testing.T) {
	t.Parallel()
	provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.1)))
	r := client(provider, judge.WithMaxPiece(16)).Trace(t.Context(), "notes.md", "adds a line", diff)
	assertFailed(t, r, judge.FailureTooLarge)
	if provider.Count() != 0 {
		t.Fatalf("a trace over the bound sent %d requests", provider.Count())
	}
}

// The cutoffs are the measured ones, and nothing outside the table answers
// for a check that is not one of the two.
func TestTheCutoffsAreTheMeasuredOnes(t *testing.T) {
	t.Parallel()
	if judge.CheckTrace.Cutoff() != 0.8 || judge.CheckScreen.Cutoff() != 0.3 {
		t.Fatalf("cutoffs are %v and %v, want 0.8 and 0.3", judge.CheckTrace.Cutoff(), judge.CheckScreen.Cutoff())
	}
	if judge.Check("other").Cutoff() != 0 {
		t.Fatal("a check outside the table has a cutoff")
	}
	if judge.ModelVersion != "jev-1.13.0" {
		t.Fatalf("the pinned model is %q", judge.ModelVersion)
	}
}

// A client formatted for a diagnostic does not print its key.
func TestAClientDoesNotFormatItsKey(t *testing.T) {
	t.Parallel()
	c := judge.New(key)
	for _, text := range []string{fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprint(c)} {
		if strings.Contains(text, key) {
			t.Fatalf("a formatted client carries its key: %s", text)
		}
	}
}

// A file whose screen would take more requests than one file may is not
// screened at all, and sends nothing, rather than being screened in part; a
// file that fits the same bound is screened in full, which is the control
// that the bound is what refused the first.
func TestAFileOverTheRequestBoundIsNotScreenedInPart(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := range 30 {
		fmt.Fprintf(&b, "+line %03d\n", i)
	}
	long := b.String()
	provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.01)))
	over := client(provider, judge.WithMaxPiece(100), judge.WithBounds(0, 2)).Screen(t.Context(), "f.txt", long)
	assertFailed(t, over, judge.FailureOverBound)
	if provider.Count() != 0 || over.Requests() != 0 {
		t.Fatalf("a file over the bound sent %d request(s)", provider.Count())
	}
	fits := client(provider, judge.WithMaxPiece(100), judge.WithBounds(0, 8)).Screen(t.Context(), "f.txt", long)
	if _, ok := fits.Probability(); !ok || fits.Requests() != provider.Count() || provider.Count() < 3 {
		t.Fatalf("a file inside the bound came to %+v after %d request(s)", fits, provider.Count())
	}

	// Halving a piece the provider refused counts against the same bound.
	var large strings.Builder
	for i := range 400 {
		fmt.Fprintf(&large, "+line %03d\n", i)
	}
	halving := judgetest.New(t, judgetest.Always(judgetest.TooLarge()))
	cut := client(halving, judge.WithBounds(0, 3)).Screen(t.Context(), "f.txt", large.String())
	assertFailed(t, cut, judge.FailureOverBound)
	if halving.Count() != 3 || cut.Requests() != 3 {
		t.Fatalf("a file whose pieces kept being cut sent %d request(s), want the bound of 3", halving.Count())
	}
}

// A client for one review sends no more requests than the review's bound,
// across every check and every goroutine, and each check the bound refused
// fails as over the bound with nothing sent. The same files asked through a
// client serving no review all go out, which is the control.
func TestAReviewSendsNoMoreThanItsBound(t *testing.T) {
	t.Parallel()
	provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.01)))
	review := client(provider, judge.WithBounds(3, 0)).ForReview()
	results := make([]judge.Result, 6)
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = review.Screen(t.Context(), fmt.Sprintf("f%d.go", i), diff)
		}()
	}
	wg.Wait()
	results[5] = review.Trace(t.Context(), "f0.go", "adds a line", diff)
	if provider.Count() != 3 {
		t.Fatalf("a review bounded at 3 sent %d requests", provider.Count())
	}
	over := 0
	for _, r := range results {
		if failure, _, isFailed := r.Failed(); isFailed {
			assertFailed(t, r, judge.FailureOverBound)
			if failure == judge.FailureOverBound && r.Requests() == 0 {
				over++
			}
		}
	}
	if over != 3 {
		t.Fatalf("%d checks were refused by the bound, want 3: %+v", over, results)
	}

	control := judgetest.New(t, judgetest.Always(judgetest.Answer(0.01)))
	unbounded := client(control, judge.WithBounds(3, 0))
	for i := range 5 {
		unbounded.Screen(t.Context(), fmt.Sprintf("f%d.go", i), diff)
	}
	if control.Count() != 5 {
		t.Fatalf("a client serving no review sent %d of 5 requests", control.Count())
	}
}

func assertFailed(t *testing.T, r judge.Result, want judge.Failure) {
	t.Helper()
	failure, detail, isFailed := r.Failed()
	if !isFailed || failure != want {
		t.Fatalf("%s came to (%q, %q, failed=%v), want %q", r.Check(), failure, detail, isFailed, want)
	}
	if p, ok := r.Probability(); ok {
		t.Fatalf("a failed %s carries the probability %v", r.Check(), p)
	}
	if r.Fired() {
		t.Fatalf("a failed %s fired", r.Check())
	}
	if strings.Contains(detail, key) {
		t.Fatalf("a failure's sentence carries the key: %q", detail)
	}
	if detail == "" {
		t.Fatalf("a failed %s says nothing about why", r.Check())
	}
}
