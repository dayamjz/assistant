package judge_test

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/judge/judgetest"
)

// labels returns what the stand-in provider needs to answer as if it knew
// the right answer: each question's label, keyed by what the request carries.
func labels(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, check := range []judge.Check{judge.CheckTrace, judge.CheckScreen} {
		c, err := loadCorpus(corpusDir, check)
		if err != nil {
			t.Fatal(err)
		}
		for _, lc := range c.cases {
			out[questionKey(lc.path, lc.reason, lc.diff)] = lc.label
		}
	}
	return out
}

func questionKey(path, reason, diff string) string { return path + "\x00" + reason + "\x00" + diff }

// oracle answers every request with high for a label-true question and low
// for a label-false one, and fails the test on a question the corpus does not
// hold, which is the harness sending something other than the corpus.
func oracle(t *testing.T, known map[string]bool, high, low float64) func(judgetest.Request) judgetest.Reply {
	return func(r judgetest.Request) judgetest.Reply {
		label, ok := known[questionKey(r.Path, r.Reason, r.Diff)]
		if !ok {
			t.Errorf("the measurement asked a question the corpus does not hold: %s", r.Path)
			return judgetest.Status(500)
		}
		if label {
			return judgetest.Answer(high)
		}
		return judgetest.Answer(low)
	}
}

func bothChecks(opts measureOptions) measureOptions {
	opts.dir = corpusDir
	opts.checks = []judge.Check{judge.CheckTrace, judge.CheckScreen}
	if opts.workers == 0 {
		opts.workers = 8
	}
	return opts
}

// The measurement runs end to end over the real corpus through a real client
// and the stand-in provider. Answered by a provider that knows every label,
// it reports perfect separation on both checks, in every family and template
// and on both sides of the held-out split, and every case is sent exactly
// once; answered by one that inverts every label, it reports the opposite.
// That is the positive control: a harness that miscounted, sent another
// question, or read the cutoff from anywhere but the client would not report
// both.
func TestTheMeasurementRunsOverTheCorpusEndToEnd(t *testing.T) {
	t.Parallel()
	known := labels(t)
	provider := judgetest.New(t, oracle(t, known, 0.95, 0.05))
	client := judge.New("ts_test_key_0123456789abcdef", judge.WithEndpoint(provider.URL()))
	opts := bothChecks(measureOptions{train: familyBlunt, test: familySubtle})
	opts.checks = []judge.Check{judge.CheckScreen}
	var out strings.Builder
	reports, err := measure(t.Context(), client, opts, &out)
	if err != nil {
		t.Fatal(err)
	}
	opts = bothChecks(measureOptions{})
	opts.checks = []judge.Check{judge.CheckTrace}
	trace, err := measure(t.Context(), client, opts, &out)
	if err != nil {
		t.Fatal(err)
	}
	reports = append(trace, reports...)

	if got, want := provider.Count(), len(known); got != want {
		t.Fatalf("the measurement sent %d requests for %d cases", got, want)
	}
	tr, sc := reports[0], reports[1]
	for _, r := range reports {
		if r.answered != r.cases || len(r.failures) != 0 {
			t.Fatalf("%s: %d of %d answered, failures %v", r.check, r.answered, r.cases, r.failures)
		}
		if !r.aucOK || r.auc != 1 {
			t.Fatalf("%s: AUC %v (%v) under a provider that knows every label", r.check, r.auc, r.aucOK)
		}
	}
	// The trace check fires when a reason is not confirmed: no true reason
	// fires and every false one does.
	if tr.pos.fired != 0 || tr.neg.fired != tr.neg.answered || tr.pos.answered != tr.neg.answered {
		t.Fatalf("trace: true reasons fired %v, false reasons fired %v", tr.pos, tr.neg)
	}
	if sc.pos.fired != sc.pos.answered || sc.neg.fired != 0 {
		t.Fatalf("screen: injected fired %v, clean fired %v", sc.pos, sc.neg)
	}
	if len(sc.families) != len(families) {
		t.Fatalf("screen reports %d families, want %d", len(sc.families), len(families))
	}
	for _, g := range append(append([]group{}, sc.families...), sc.templates...) {
		if g.fired.answered == 0 || g.fired.fired != g.fired.answered || !g.aucOK || g.auc != 1 {
			t.Fatalf("screen group %s: fired %v, AUC %v (%v)", g.name, g.fired, g.auc, g.aucOK)
		}
	}
	h := sc.holdout
	if h == nil || !h.trainOK || !h.testOK || h.trainAUC != 1 || h.testAUC != 1 || !h.chosenOK {
		t.Fatalf("the held-out split did not report perfect separation: %+v", h)
	}
	if h.trainCases+h.testNum != sc.cases {
		t.Fatalf("the held-out split put %d+%d of %d cases on a side", h.trainCases, h.testNum, sc.cases)
	}
	if h.chosenTestPos.fired != h.chosenTestPos.answered || h.chosenTestNeg.fired != 0 {
		t.Fatalf("the cutoff chosen on train did not separate test: %v, %v", h.chosenTestPos, h.chosenTestNeg)
	}
	for _, want := range []string{"trace-reason on " + judge.ModelVersion, "reviewer-directed-text on " + judge.ModelVersion,
		"cutoff 0.80", "cutoff 0.30", "held out", "blunt/9", "subtle/7"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the report does not say %q:\n%s", want, out.String())
		}
	}

	inverted := judgetest.New(t, oracle(t, known, 0.05, 0.95))
	reports, err = measure(t.Context(), judge.New("ts_test_key_0123456789abcdef", judge.WithEndpoint(inverted.URL())),
		bothChecks(measureOptions{}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reports {
		if !r.aucOK || r.auc != 0 {
			t.Fatalf("%s: AUC %v under a provider that inverts every label", r.check, r.auc)
		}
	}
}

// A failed call is left out of every figure and counted as what it was, not
// read as an answer either way.
func TestTheMeasurementCountsFailedCallsApart(t *testing.T) {
	t.Parallel()
	known := labels(t)
	answer := oracle(t, known, 0.95, 0.05)
	var n atomic.Int64
	provider := judgetest.New(t, func(r judgetest.Request) judgetest.Reply {
		if n.Add(1)%5 == 0 {
			return judgetest.Status(503)
		}
		return answer(r)
	})
	reports, err := measure(t.Context(), judge.New("ts_test_key_0123456789abcdef", judge.WithEndpoint(provider.URL())),
		bothChecks(measureOptions{}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var failed int
	for _, r := range reports {
		failed += r.failures[judge.FailureStatus]
		if r.answered+r.failures[judge.FailureStatus] != r.cases {
			t.Fatalf("%s: %d answered and %v failed of %d", r.check, r.answered, r.failures, r.cases)
		}
		if r.pos.answered+r.neg.answered != r.answered || r.auc != 1 {
			t.Fatalf("%s: a failed call was counted as an answer: %v %v AUC %v", r.check, r.pos, r.neg, r.auc)
		}
	}
	if want := int(n.Load() / 5); failed != want {
		t.Fatalf("%d failures counted, the provider refused %d", failed, want)
	}
}

// Repeated calls on the same cases report how far the answers move: nothing
// for a provider that always gives one answer per question, and the gap it
// actually introduced for one that does not.
func TestTheMeasurementReportsCallToCallSpread(t *testing.T) {
	t.Parallel()
	known := labels(t)
	steady := judgetest.New(t, oracle(t, known, 0.95, 0.05))
	opts := bothChecks(measureOptions{subset: 12, repeat: 2})
	reports, err := measure(t.Context(), judge.New("ts_test_key_0123456789abcdef", judge.WithEndpoint(steady.URL())), opts, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reports {
		s := r.spread
		if s == nil || s.cases != 12 || s.repeats != 3 || s.steady != 12 || s.maxRange != 0 || s.flips != 0 || !s.aucOK || s.aucLow != 1 {
			t.Fatalf("%s: spread under a steady provider: %+v", r.check, s)
		}
	}

	// The wobbling provider moves every answer across the shipped cutoff of
	// both checks on alternate calls to the same question.
	var calls callCounter
	wobbly := judgetest.New(t, func(r judgetest.Request) judgetest.Reply {
		if calls.next(questionKey(r.Path, r.Reason, r.Diff))%2 == 0 {
			return judgetest.Answer(0.1)
		}
		return judgetest.Answer(0.9)
	})
	opts = bothChecks(measureOptions{subset: 12, repeat: 1})
	reports, err = measure(t.Context(), judge.New("ts_test_key_0123456789abcdef", judge.WithEndpoint(wobbly.URL())), opts, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reports {
		s := r.spread
		if s == nil || s.steady != 12 || s.flips != 12 || s.maxRange < 0.79 || s.meanRange < 0.79 {
			t.Fatalf("%s: spread under a provider that alternates its answers: %+v", r.check, s)
		}
	}
}

// callCounter counts the calls made with each question.
type callCounter struct {
	mu    sync.Mutex
	count map[string]int
}

func (c *callCounter) next(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count == nil {
		c.count = map[string]int{}
	}
	c.count[key]++
	return c.count[key]
}

// A measurement without a key refuses before it loads or sends anything, and
// a held-out split the corpus cannot give is refused rather than run on a
// side with nothing in it.
func TestTheMeasurementRefusesWhatItCannotMeasure(t *testing.T) {
	t.Parallel()
	provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.5)))
	if _, err := measure(t.Context(), judge.New("", judge.WithEndpoint(provider.URL())), bothChecks(measureOptions{}), io.Discard); !errors.Is(err, errNoKey) {
		t.Fatalf("a measurement with no key returned %v, want the refusal", err)
	}
	client := judge.New("ts_test_key_0123456789abcdef", judge.WithEndpoint(provider.URL()))
	for name, opts := range map[string]measureOptions{
		"a trace split":    bothChecks(measureOptions{train: familyBlunt, test: familySubtle}),
		"an unknown kind":  {dir: corpusDir, checks: []judge.Check{judge.CheckScreen}, train: familyBlunt, test: "clever"},
		"one family twice": {dir: corpusDir, checks: []judge.Check{judge.CheckScreen}, train: familyBlunt, test: familyBlunt},
		"half a split":     {dir: corpusDir, checks: []judge.Check{judge.CheckScreen}, train: familyBlunt},
	} {
		if _, err := measure(t.Context(), client, opts, io.Discard); err == nil {
			t.Errorf("%s was measured rather than refused", name)
		}
	}
	if n := provider.Count(); n != 0 {
		t.Fatalf("a refused measurement sent %d requests", n)
	}
}

// The checked-in corpus loads, and its shape is what the measurement relies
// on: every trace diff asked once with its own reason and once with its
// pair's, and every screen positive in a declared family and template.
func TestTheCorpusIsWellFormed(t *testing.T) {
	t.Parallel()
	tr, err := loadCorpus(corpusDir, judge.CheckTrace)
	if err != nil {
		t.Fatal(err)
	}
	var trueReasons int
	for _, lc := range tr.cases {
		if lc.label {
			trueReasons++
		}
	}
	if len(tr.cases) == 0 || 2*trueReasons != len(tr.cases) {
		t.Fatalf("the trace corpus has %d cases, %d of them true", len(tr.cases), trueReasons)
	}
	sc, err := loadCorpus(corpusDir, judge.CheckScreen)
	if err != nil {
		t.Fatal(err)
	}
	per := map[family]int{}
	for _, lc := range sc.cases {
		if lc.label {
			per[lc.family]++
			if lc.diff == "" || !strings.Contains(lc.diff, sc.templates[lc.family][lc.template]) {
				t.Fatalf("screen positive %s does not carry its template", lc.id)
			}
		}
	}
	for _, f := range families {
		if per[f] == 0 {
			t.Fatalf("the screen corpus has no %s positives", f)
		}
	}
}

// A malformed corpus is refused, not measured: each mutation below of the
// checked-in files is one a measurement would otherwise silently miscount.
func TestAMalformedCorpusIsRefused(t *testing.T) {
	t.Parallel()
	read := func(check judge.Check) map[string]any {
		data, err := os.ReadFile(filepath.Join(corpusDir, string(check)+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := func(v map[string]any) []any { return v["cases"].([]any) }
	item := func(v map[string]any, i int) map[string]any { return cases(v)[i].(map[string]any) }
	firstPositive := func(v map[string]any) map[string]any {
		for _, c := range cases(v) {
			if m := c.(map[string]any); m["label"] == true {
				return m
			}
		}
		t.Fatal("no positive in the screen corpus")
		return nil
	}

	for name, m := range map[string]struct {
		check  judge.Check
		mutate func(v map[string]any)
		want   string
	}{
		"a trace case with no label": {judge.CheckTrace, func(v map[string]any) { delete(item(v, 0), "label") }, "no label"},
		"a trace pair asked once": {judge.CheckTrace, func(v map[string]any) {
			v["cases"] = cases(v)[1:]
		}, "unbalanced"},
		"a trace label flipped": {judge.CheckTrace, func(v map[string]any) { item(v, 1)["label"] = true }, "labeled true"},
		"a reason from outside the pair": {judge.CheckTrace, func(v map[string]any) {
			item(v, 1)["reason_of"] = item(v, 4)["diff"]
		}, "not its pair"},
		"an odd number of diffs": {judge.CheckTrace, func(v map[string]any) {
			v["diffs"] = v["diffs"].([]any)[1:]
		}, "pairs"},
		"an unknown field": {judge.CheckTrace, func(v map[string]any) { item(v, 0)["lable"] = true }, "unknown field"},
		"a screen case with no label": {judge.CheckScreen, func(v map[string]any) {
			delete(item(v, 0), "label")
		}, "no label"},
		"an unknown payload family": {judge.CheckScreen, func(v map[string]any) {
			firstPositive(v)["family"] = "clever"
		}, "unknown payload family"},
		"an undeclared family's templates": {judge.CheckScreen, func(v map[string]any) {
			v["templates"].(map[string]any)["clever"] = []any{"x"}
		}, "unknown payload family"},
		"a template out of range": {judge.CheckScreen, func(v map[string]any) {
			firstPositive(v)["template"] = 99
		}, "template"},
		"a line not from its template": {judge.CheckScreen, func(v map[string]any) {
			firstPositive(v)["line"] = "+// an ordinary comment"
		}, "does not carry template"},
		"a positive with no base": {judge.CheckScreen, func(v map[string]any) {
			firstPositive(v)["base"] = "nowhere"
		}, "not a clean case"},
		"a clean case carrying a payload": {judge.CheckScreen, func(v map[string]any) {
			item(v, 0)["family"] = "blunt"
		}, "carries a payload"},
		"a template nothing uses": {judge.CheckScreen, func(v map[string]any) {
			ts := v["templates"].(map[string]any)
			ts["subtle"] = append(ts["subtle"].([]any), "an unused template")
		}, "has no case"},
	} {
		v := read(m.check)
		m.mutate(v)
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		_, err = parseCorpus(m.check, data)
		if err == nil || !strings.Contains(err.Error(), m.want) {
			t.Errorf("%s: parse returned %v, want a refusal saying %q", name, err, m.want)
		}
	}
}
