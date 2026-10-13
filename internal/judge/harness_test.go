package judge_test

// This file is the offline measurement behind the cutoffs in check.go. It
// puts every labeled case in testdata/corpus through a real judge.Client, so
// what it measures is the shipped question, ModelVersion, and cutoff and never
// a second copy of them: whether a case fired is Result.Fired, and the cutoff
// it reports is Check.Cutoff. measure_test.go runs it in make check against
// the stand-in provider; measure_live_test.go runs it against the provider,
// is built only with the judgelive tag, and refuses without a key. How to run
// it is in testdata/corpus/README.md.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/dayamjz/assistant/internal/judge"
)

// measureOptions is what one measurement asks.
type measureOptions struct {
	// dir is the corpus directory.
	dir string
	// checks are the checks to measure, in order.
	checks []judge.Check
	// train and test, when both set, are the held-out split by kind of
	// failure: a cutoff is chosen on the train family's positives and the
	// clean cases they were built from, and reported on the test family's
	// positives and every other clean case, so no diff is on both sides.
	// Only the screen has more than one kind of failure.
	train, test family
	// subset and repeat, when both positive, ask subset cases (spread evenly
	// over the corpus) repeat more times each, so the variation between
	// repeated calls is visible next to any difference being claimed.
	subset, repeat int
	// workers bounds the requests in flight.
	workers int
}

// errNoKey is the refusal of a measurement with no key: it would send
// nothing and report every case failed, which reads as a measurement and is
// not one.
var errNoKey = errors.New("the measurement needs a provider key in " + judge.KeyVariable + " and refuses to run without one")

// answer is one case's result.
type answer struct {
	c labeledCase
	r judge.Result
}

// rates is how often a set of answered cases fired at the shipped cutoff.
type rates struct {
	fired, answered int
}

func (r rates) String() string {
	if r.answered == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d (%.1f%%)", r.fired, r.answered, 100*float64(r.fired)/float64(r.answered))
}

// group is a slice of the positives: a family, or a family's template.
type group struct {
	name   string
	fired  rates
	auc    float64
	aucOK  bool
	lowest float64
}

// holdoutReport is the held-out split's result.
type holdoutReport struct {
	train, test         family
	trainAUC, testAUC   float64
	trainOK, testOK     bool
	shippedTrainPos     rates
	shippedTrainNeg     rates
	shippedTestPos      rates
	shippedTestNeg      rates
	chosen              float64
	chosenOK            bool
	chosenTestPos       rates
	chosenTestNeg       rates
	trainCases, testNum int
}

// spreadReport is the variation between repeated calls on the same cases.
type spreadReport struct {
	cases, repeats int
	// meanRange and maxRange are over cases answered every time: the mean
	// and the largest gap between a case's highest and lowest answer.
	meanRange, maxRange float64
	// flips is how many of those cases fired on some calls and not others.
	flips int
	// steady is how many cases were answered on every call.
	steady int
	// aucLow and aucHigh are the subset's AUC across repetitions.
	aucLow, aucHigh float64
	aucOK           bool
}

// checkReport is one check's measurement.
type checkReport struct {
	check    judge.Check
	cases    int
	answered int
	failures map[judge.Failure]int
	auc      float64
	aucOK    bool
	// pos and neg are how often label-true and label-false cases fired.
	pos, neg  rates
	families  []group
	templates []group
	holdout   *holdoutReport
	spread    *spreadReport
}

// measure runs opts against client and writes a report to out. It refuses a
// client with no key before loading anything, so nothing is sent and nothing
// is reported.
func measure(ctx context.Context, client *judge.Client, opts measureOptions, out io.Writer) ([]checkReport, error) {
	if !client.HasKey() {
		return nil, errNoKey
	}
	if (opts.train == "") != (opts.test == "") {
		return nil, fmt.Errorf("a held-out split needs both a train and a test family")
	}
	if opts.train != "" && (!knownFamily(opts.train) || !knownFamily(opts.test) || opts.train == opts.test) {
		return nil, fmt.Errorf("the held-out split %q:%q is not two different known families", opts.train, opts.test)
	}
	if opts.workers < 1 {
		opts.workers = 1
	}
	var reports []checkReport
	for _, check := range opts.checks {
		if opts.train != "" && check != judge.CheckScreen {
			return nil, fmt.Errorf("%s has one kind of failure, so it has no held-out split by kind", check)
		}
		c, err := loadCorpus(opts.dir, check)
		if err != nil {
			return nil, err
		}
		rep, err := measureCheck(ctx, client, c, opts)
		if err != nil {
			return nil, err
		}
		writeReport(out, rep)
		reports = append(reports, rep)
	}
	return reports, nil
}

func measureCheck(ctx context.Context, client *judge.Client, c corpus, opts measureOptions) (checkReport, error) {
	answers := ask(ctx, client, c.cases, opts.workers)
	if err := ctx.Err(); err != nil {
		return checkReport{}, err
	}
	rep := checkReport{check: c.check, cases: len(c.cases), failures: map[judge.Failure]int{}}
	for _, a := range answers {
		if f, _, failed := a.r.Failed(); failed {
			rep.failures[f]++
			continue
		}
		rep.answered++
	}
	rep.auc, rep.aucOK = aucOf(answers)
	rep.pos, rep.neg = firedRates(answers)

	if c.check == judge.CheckScreen {
		var negatives []answer
		for _, a := range answers {
			if !a.c.label {
				negatives = append(negatives, a)
			}
		}
		for _, fam := range families {
			rep.families = append(rep.families, groupOf(string(fam), negatives, answers, func(lc labeledCase) bool {
				return lc.label && lc.family == fam
			}))
			for i := range c.templates[fam] {
				rep.templates = append(rep.templates, groupOf(fmt.Sprintf("%s/%d", fam, i), negatives, answers, func(lc labeledCase) bool {
					return lc.label && lc.family == fam && lc.template == i
				}))
			}
		}
		if opts.train != "" {
			rep.holdout = holdout(answers, opts.train, opts.test)
		}
	}
	if opts.subset > 0 && opts.repeat > 0 {
		s, err := spread(ctx, client, c.cases, answers, opts)
		if err != nil {
			return checkReport{}, err
		}
		rep.spread = s
	}
	return rep, nil
}

// ask puts every case to the client with at most workers requests in flight,
// and returns the answers in case order.
func ask(ctx context.Context, client *judge.Client, cases []labeledCase, workers int) []answer {
	out := make([]answer, len(cases))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range jobs {
				lc := cases[i]
				var r judge.Result
				if lc.reason != "" {
					r = client.Trace(ctx, lc.path, lc.reason, lc.diff)
				} else {
					r = client.Screen(ctx, lc.path, lc.diff)
				}
				out[i] = answer{c: lc, r: r}
			}
		})
	}
	for i := range cases {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return out
}

// aucOf is the area under the ROC curve of the answered cases, with label
// true as the positive class and the model's probability as the score: the
// chance a label-true case is answered higher than a label-false one, ties
// counting half. It is false when either class has no answered case.
func aucOf(answers []answer) (float64, bool) {
	var pos, neg []float64
	for _, a := range answers {
		p, ok := a.r.Probability()
		if !ok {
			continue
		}
		if a.c.label {
			pos = append(pos, p)
		} else {
			neg = append(neg, p)
		}
	}
	if len(pos) == 0 || len(neg) == 0 {
		return 0, false
	}
	var wins float64
	for _, p := range pos {
		for _, n := range neg {
			switch {
			case p > n:
				wins++
			case p == n:
				wins += 0.5
			}
		}
	}
	return wins / float64(len(pos)*len(neg)), true
}

// firedRates is how often the answered label-true and label-false cases
// fired at the shipped cutoff, as Result.Fired decides it.
func firedRates(answers []answer) (pos, neg rates) {
	for _, a := range answers {
		if _, ok := a.r.Probability(); !ok {
			continue
		}
		r := &neg
		if a.c.label {
			r = &pos
		}
		r.answered++
		if a.r.Fired() {
			r.fired++
		}
	}
	return pos, neg
}

// groupOf reports the positives in answers that in selects, each against
// every clean case.
func groupOf(name string, negatives, answers []answer, in func(labeledCase) bool) group {
	var members []answer
	g := group{name: name, lowest: math.Inf(1)}
	for _, a := range answers {
		if !in(a.c) {
			continue
		}
		members = append(members, a)
		if p, ok := a.r.Probability(); ok {
			g.lowest = math.Min(g.lowest, p)
		}
	}
	g.fired, _ = firedRates(members)
	g.auc, g.aucOK = aucOf(append(members, negatives...))
	return g
}

// holdout splits the screen's answers by kind of failure. The train side is
// the train family's positives and the clean cases they were built from; the
// test side is the test family's positives and every other clean case. The
// candidate cutoff is the one that best separates the train side, as the
// largest gap between how often its positives and its clean cases would be
// flagged at or above it; it is reported on the test side beside the shipped
// cutoff, which is what a change to the cutoff would have to beat there.
func holdout(answers []answer, train, test family) *holdoutReport {
	bases := map[string]bool{}
	for _, a := range answers {
		if a.c.label && a.c.family == train {
			bases[a.c.base] = true
		}
	}
	var trainSide, testSide []answer
	for _, a := range answers {
		switch {
		case a.c.label && a.c.family == train, !a.c.label && bases[a.c.id]:
			trainSide = append(trainSide, a)
		case a.c.label && a.c.family == test, !a.c.label:
			testSide = append(testSide, a)
		}
	}
	h := &holdoutReport{train: train, test: test, trainCases: len(trainSide), testNum: len(testSide)}
	h.trainAUC, h.trainOK = aucOf(trainSide)
	h.testAUC, h.testOK = aucOf(testSide)
	h.shippedTrainPos, h.shippedTrainNeg = firedRates(trainSide)
	h.shippedTestPos, h.shippedTestNeg = firedRates(testSide)

	h.chosen, h.chosenOK = bestCutoff(trainSide)
	if h.chosenOK {
		h.chosenTestPos, h.chosenTestNeg = flaggedAt(testSide, h.chosen)
	}
	return h
}

// bestCutoff is the answered probability c maximizing the share of
// positives at or above c less the share of clean cases at or above it, the
// lowest such c on a tie. It is the measurement's candidate and never the
// shipped cutoff.
func bestCutoff(side []answer) (float64, bool) {
	var scores []float64
	for _, a := range side {
		if p, ok := a.r.Probability(); ok {
			scores = append(scores, p)
		}
	}
	sort.Float64s(scores)
	best, bestGap, found := 0.0, math.Inf(-1), false
	for _, c := range scores {
		pos, neg := flaggedAt(side, c)
		if pos.answered == 0 || neg.answered == 0 {
			return 0, false
		}
		gap := float64(pos.fired)/float64(pos.answered) - float64(neg.fired)/float64(neg.answered)
		if gap > bestGap {
			best, bestGap, found = c, gap, true
		}
	}
	return best, found
}

// flaggedAt is how often the answered positives and clean cases in side are
// at or above c, the screen's side of a candidate cutoff.
func flaggedAt(side []answer, c float64) (pos, neg rates) {
	for _, a := range side {
		p, ok := a.r.Probability()
		if !ok {
			continue
		}
		r := &neg
		if a.c.label {
			r = &pos
		}
		r.answered++
		if p >= c {
			r.fired++
		}
	}
	return pos, neg
}

// pickSubset picks up to n cases, half of them label true and half label
// false where the corpus has enough of each, each half spread evenly over the
// corpus's order, so the subset's AUC is defined and the pick is the same on
// every run.
func pickSubset(cases []labeledCase, n int) []int {
	var byLabel [2][]int
	for i, lc := range cases {
		if lc.label {
			byLabel[1] = append(byLabel[1], i)
		} else {
			byLabel[0] = append(byLabel[0], i)
		}
	}
	want := [2]int{n / 2, n - n/2}
	want[0] = min(want[0], len(byLabel[0]))
	want[1] = min(n-want[0], len(byLabel[1]))
	want[0] = min(n-want[1], len(byLabel[0]))
	var picked []int
	for l, idx := range byLabel {
		for i := range want[l] {
			picked = append(picked, idx[i*len(idx)/want[l]])
		}
	}
	sort.Ints(picked)
	return picked
}

// spread asks opts.subset cases, half of each label, opts.repeat more times
// each, and reports how far their answers move between calls. The first
// pass's answers count as one repetition.
func spread(ctx context.Context, client *judge.Client, cases []labeledCase, first []answer, opts measureOptions) (*spreadReport, error) {
	picked := pickSubset(cases, opts.subset)
	n := len(picked)
	sub := make([]labeledCase, n)
	reps := [][]answer{make([]answer, n)}
	for i, idx := range picked {
		sub[i] = cases[idx]
		reps[0][i] = first[idx]
	}
	for range opts.repeat {
		reps = append(reps, ask(ctx, client, sub, opts.workers))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	s := &spreadReport{cases: n, repeats: len(reps), aucLow: math.Inf(1), aucHigh: math.Inf(-1)}
	var total float64
	for i := range sub {
		lo, hi := math.Inf(1), math.Inf(-1)
		fired, all := 0, true
		for _, rep := range reps {
			p, ok := rep[i].r.Probability()
			if !ok {
				all = false
				break
			}
			lo, hi = math.Min(lo, p), math.Max(hi, p)
			if rep[i].r.Fired() {
				fired++
			}
		}
		if !all {
			continue
		}
		s.steady++
		total += hi - lo
		s.maxRange = math.Max(s.maxRange, hi-lo)
		if fired != 0 && fired != len(reps) {
			s.flips++
		}
	}
	if s.steady > 0 {
		s.meanRange = total / float64(s.steady)
	}
	for _, rep := range reps {
		if auc, ok := aucOf(rep); ok {
			s.aucOK = true
			s.aucLow, s.aucHigh = math.Min(s.aucLow, auc), math.Max(s.aucHigh, auc)
		}
	}
	return s, nil
}

// writeReport prints one check's measurement for a person.
func writeReport(out io.Writer, r checkReport) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	defer func() { _ = w.Flush() }()
	fmt.Fprintf(w, "== %s on %s, cutoff %.2f\n", r.check, judge.ModelVersion, r.check.Cutoff())
	fmt.Fprintf(w, "cases\t%d\tanswered %d\n", r.cases, r.answered)
	if len(r.failures) > 0 {
		var kinds []string
		for f, n := range r.failures {
			kinds = append(kinds, fmt.Sprintf("%s=%d", f, n))
		}
		sort.Strings(kinds)
		fmt.Fprintf(w, "failed\t%s\t(left out of every figure below)\n", strings.Join(kinds, " "))
	}
	fmt.Fprintf(w, "AUC\t%s\n", fmtAUC(r.auc, r.aucOK))
	if r.check == judge.CheckTrace {
		fmt.Fprintf(w, "true reasons passing\t%s\n", passing(r.pos))
		fmt.Fprintf(w, "false reasons passing\t%s\n", passing(r.neg))
	} else {
		fmt.Fprintf(w, "injected files flagged\t%s\n", r.pos)
		fmt.Fprintf(w, "clean files flagged\t%s\n", r.neg)
	}
	if len(r.families) > 0 {
		fmt.Fprintf(w, "by payload\tflagged\tAUC vs clean\tlowest answer\n")
		for _, g := range append(append([]group{}, r.families...), r.templates...) {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", g.name, g.fired, fmtAUC(g.auc, g.aucOK), fmtLowest(g.lowest))
		}
	}
	if h := r.holdout; h != nil {
		fmt.Fprintf(w, "held out\ttrain %s (%d cases), test %s (%d cases)\n", h.train, h.trainCases, h.test, h.testNum)
		fmt.Fprintf(w, "  train AUC\t%s\n", fmtAUC(h.trainAUC, h.trainOK))
		fmt.Fprintf(w, "  test AUC\t%s\n", fmtAUC(h.testAUC, h.testOK))
		fmt.Fprintf(w, "  shipped %.2f on train\tinjected %s\tclean %s\n", r.check.Cutoff(), h.shippedTrainPos, h.shippedTrainNeg)
		fmt.Fprintf(w, "  shipped %.2f on test\tinjected %s\tclean %s\n", r.check.Cutoff(), h.shippedTestPos, h.shippedTestNeg)
		if h.chosenOK {
			fmt.Fprintf(w, "  chosen on train %.3f, on test\tinjected %s\tclean %s\n", h.chosen, h.chosenTestPos, h.chosenTestNeg)
		}
	}
	if s := r.spread; s != nil {
		fmt.Fprintf(w, "call-to-call\t%d cases x %d calls, %d answered every time\n", s.cases, s.repeats, s.steady)
		fmt.Fprintf(w, "  answer range\tmean %.3f, max %.3f\n", s.meanRange, s.maxRange)
		fmt.Fprintf(w, "  fired on some calls only\t%d\n", s.flips)
		if s.aucOK {
			fmt.Fprintf(w, "  subset AUC\t%.3f to %.3f\n", s.aucLow, s.aucHigh)
		}
	}
	fmt.Fprintln(w)
}

func passing(r rates) rates { return rates{fired: r.answered - r.fired, answered: r.answered} }

func fmtAUC(v float64, ok bool) string {
	if !ok {
		return "n/a"
	}
	return fmt.Sprintf("%.3f", v)
}

func fmtLowest(v float64) string {
	if math.IsInf(v, 1) {
		return "n/a"
	}
	return fmt.Sprintf("%.3f", v)
}
