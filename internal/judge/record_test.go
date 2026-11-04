package judge_test

import (
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/judge/judgetest"
	"github.com/dayamjz/assistant/internal/store"
)

// Every call's record says how long it took and how large it was, failed
// calls included, and keeps a reported count apart from the sizes an estimate
// would start from. A reply carrying the provider's usage field is recorded
// as that count with no sizes; a reply without one is recorded as the bodies'
// sizes and the request count with no reported count; a call that got no
// reply records its failure, no tokens and no sizes, and still its elapsed
// time. Each row asks both checks. No row is priced, because the shipped rate
// table is empty.
func TestEveryCallRecordsItsTimeAndSize(t *testing.T) {
	t.Parallel()
	const answers = `"answers":{"trace_reason":{"type":"noul","noul":0.9},"reviewer_directed_text":{"type":"noul","noul":0.1}}`
	withUsage := `{"model":"` + judge.ModelVersion + `",` + answers + `,"usage":{"input_tokens":123,"output_tokens":4}}`
	withoutUsage := `{"model":"` + judge.ModelVersion + `",` + answers + `}`
	reported := func(in, out int64) judge.Usage {
		return judge.Usage{ReportedRequests: 1, ReportedInputTokens: in, ReportedOutputTokens: out}
	}
	cases := []struct {
		name    string
		reply   judgetest.Reply
		timeout time.Duration
		failure judge.Failure
		// usage is the record's Usage; sent is checked apart, because the
		// request body's size is the client's to choose.
		usage judge.Usage
	}{
		{name: "a stubbed answer records its elapsed and its reported size", reply: judgetest.Answer(0.5),
			usage: reported(int64(len(diff)/4+1), 1)},
		{name: "a reply with a usage field records the reported count and no sizes",
			reply: judgetest.Raw(http.StatusOK, withUsage), usage: reported(123, 4)},
		{name: "a reply without a usage field records sizes and no reported count",
			reply: judgetest.Raw(http.StatusOK, withoutUsage),
			usage: judge.Usage{UnreportedRequests: 1, UnreportedReceivedBytes: int64(len(withoutUsage))}},
		{name: "a refusal records its failure and sizes and no tokens",
			reply: judgetest.Raw(http.StatusServiceUnavailable, withUsage), failure: judge.FailureStatus,
			usage: judge.Usage{UnreportedRequests: 1, UnreportedReceivedBytes: int64(len(withUsage))}},
		{name: "a call with no reply records its failure, no tokens and its elapsed",
			reply: judgetest.Hang(), timeout: 100 * time.Millisecond, failure: judge.FailureTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := judgetest.New(t, judgetest.Always(tc.reply))
			c := client(provider, judge.WithTimeout(tc.timeout))
			for _, r := range []judge.Result{
				c.Trace(t.Context(), "notes.md", "adds a line", diff),
				c.Screen(t.Context(), "notes.md", diff),
			} {
				rec := judge.RecordOf("run-1", "review", "batch-1", r, "")
				if rec.Failure != tc.failure || rec.Answered != (tc.failure == "") || rec.Requests != 1 {
					t.Fatalf("%s recorded failure %q, answered %v and %d request(s), want %q and 1",
						rec.Check, rec.Failure, rec.Answered, rec.Requests, tc.failure)
				}
				if rec.Elapsed <= tc.timeout {
					t.Fatalf("%s recorded an elapsed time of %v, want more than %v", rec.Check, rec.Elapsed, tc.timeout)
				}
				got := rec.Usage
				if tc.usage.UnreportedRequests > 0 {
					if got.UnreportedSentBytes <= int64(len(diff)) {
						t.Fatalf("%s recorded %d bytes sent, and the diff alone is %d", rec.Check,
							got.UnreportedSentBytes, len(diff))
					}
					got.UnreportedSentBytes = 0
				}
				if got != tc.usage {
					t.Fatalf("%s recorded usage %+v, want %+v", rec.Check, rec.Usage, tc.usage)
				}
				if !rec.Measured || rec.Rate != nil {
					t.Fatalf("%s recorded measured %v and rate %+v, want measured and no rate: the shipped table is empty",
						rec.Check, rec.Measured, rec.Rate)
				}
			}
		})
	}
}

// A screen cut into pieces records every piece's reported count, summed, and
// as many requests as the provider received.
func TestASplitScreenRecordsEveryPiece(t *testing.T) {
	t.Parallel()
	provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.01)))
	c := client(provider, judge.WithMaxPiece(64))
	large := strings.Repeat("+a line of the change, long enough to need cutting\n", 6)
	rec := judge.RecordOf("run-1", "review", "batch-1", c.Screen(t.Context(), "notes.md", large), "")

	requests := provider.Requests()
	want := judge.Usage{ReportedRequests: len(requests), ReportedOutputTokens: int64(len(requests))}
	for _, r := range requests {
		want.ReportedInputTokens += int64(len(r.Diff)/4 + 1)
	}
	if len(requests) < 2 || rec.Requests != len(requests) || rec.Usage != want {
		t.Fatalf("a screen of %d requests recorded %d and usage %+v, want %+v",
			len(requests), rec.Requests, rec.Usage, want)
	}
}

// A call is priced only at a rate that names its source, and only when every
// request it sent reported its tokens; the price is the input and output
// tokens each at its own figure. Otherwise the record has no rate and no
// price, never a price of zero. Every row runs a real call against the stand-in
// provider and prices it from a table of its own, because the shipped table is
// empty until the owner sets a rate.
func TestACallIsPricedOnlyAtASourcedRateWithEveryTokenReported(t *testing.T) {
	t.Parallel()
	sourced := judge.Rate{InputUSDPerMillionTokens: 2, OutputUSDPerMillionTokens: 8, Source: "a test's own figures"}
	withoutUsage := `{"model":"` + judge.ModelVersion + `","answers":{"reviewer_directed_text":{"type":"noul","noul":0.1}}}`
	cases := []struct {
		name    string
		reply   judgetest.Reply
		timeout time.Duration
		table   map[string]judge.Rate
		priced  bool
	}{
		{"a reported call at a sourced rate is priced", judgetest.Answer(0.1), 0,
			map[string]judge.Rate{judge.ModelVersion: sourced}, true},
		{"no rate for the model prices nothing", judgetest.Answer(0.1), 0, map[string]judge.Rate{}, false},
		{"a rate for another model prices nothing", judgetest.Answer(0.1), 0,
			map[string]judge.Rate{"jev-latest": sourced}, false},
		{"a rate naming no source prices nothing", judgetest.Answer(0.1), 0,
			map[string]judge.Rate{judge.ModelVersion: {InputUSDPerMillionTokens: 2, OutputUSDPerMillionTokens: 8}}, false},
		{"a negative figure prices nothing", judgetest.Answer(0.1), 0,
			map[string]judge.Rate{judge.ModelVersion: {InputUSDPerMillionTokens: -2, OutputUSDPerMillionTokens: 8, Source: "x"}}, false},
		{"a reply without token counts is not priced", judgetest.Raw(http.StatusOK, withoutUsage), 0,
			map[string]judge.Rate{judge.ModelVersion: sourced}, false},
		{"a call with no reply is not priced", judgetest.Hang(), 50 * time.Millisecond,
			map[string]judge.Rate{judge.ModelVersion: sourced}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := judgetest.New(t, judgetest.Always(tc.reply))
			r := client(provider, judge.WithTimeout(tc.timeout)).Screen(t.Context(), "notes.md", diff)
			rec := judge.RecordWithRates("run-1", "review", "batch-1", r, "", tc.table)
			price, ok := rec.Price()
			if !tc.priced {
				if rec.Rate != nil || ok || price != 0 {
					t.Fatalf("recorded rate %+v and price (%v, %v), want neither", rec.Rate, price, ok)
				}
				return
			}
			want := float64(rec.Usage.ReportedInputTokens)*2/1e6 + float64(rec.Usage.ReportedOutputTokens)*8/1e6
			if rec.Rate == nil || *rec.Rate != sourced || !ok || price != want || rec.Usage.ReportedOutputTokens == 0 {
				t.Fatalf("recorded rate %+v and price (%v, %v) for %+v, want %+v and %v", rec.Rate, price, ok,
					rec.Usage, sourced, want)
			}
		})
	}
}

// Every shipped rate prices a fully reported call by store.PricedAt, the one
// owner of that rule. The rule is asked of rates it must refuse too, so an
// empty table passing here is not the check looking at nothing.
func TestEveryShippedRateNamesItsSource(t *testing.T) {
	t.Parallel()
	reported := store.DecisionMeasures{Requests: 1, ReportedRequests: 1}
	for model, rate := range judge.ShippedRates() {
		if err := store.PricedAt(reported, rate); err != nil {
			t.Errorf("the shipped rate for %s prices nothing: %v", model, err)
		}
	}
	if err := store.PricedAt(reported, judge.Rate{InputUSDPerMillionTokens: 1, OutputUSDPerMillionTokens: 3, Source: "x"}); err != nil {
		t.Fatalf("a rate with both figures and a source was refused: %v", err)
	}
	for name, bad := range map[string]judge.Rate{
		"no source":         {InputUSDPerMillionTokens: 1, OutputUSDPerMillionTokens: 3, Source: " "},
		"a negative figure": {InputUSDPerMillionTokens: 1, OutputUSDPerMillionTokens: -3, Source: "x"},
		"not a number":      {InputUSDPerMillionTokens: math.NaN(), OutputUSDPerMillionTokens: 3, Source: "x"},
		"infinite":          {InputUSDPerMillionTokens: math.Inf(1), OutputUSDPerMillionTokens: 3, Source: "x"},
	} {
		if store.PricedAt(reported, bad) == nil {
			t.Errorf("a rate with %s was accepted", name)
		}
	}
}

// A path whose diff could not be read was never a call, so its record is not
// measured, where a check that failed before sending, for want of a key, was
// one and is.
func TestAnUnaskedPathIsNotRecordedAsMeasured(t *testing.T) {
	t.Parallel()
	unasked := judge.RecordOf("run-1", "review", "batch-1", judge.Unasked(judge.CheckScreen, "a.go", "unreadable"), "")
	if unasked.Measured || unasked.Elapsed != 0 || unasked.Rate != nil {
		t.Fatalf("an unasked path was recorded as %+v, want unmeasured", unasked)
	}
	keyless := judge.RecordOf("run-1", "review", "batch-1", judge.New("").Screen(t.Context(), "a.go", diff), "")
	if !keyless.Measured || keyless.Requests != 0 {
		t.Fatalf("a check with no key was recorded as %+v, want measured with no requests", keyless)
	}
}
