package stages

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/vcs"
)

// decisionConcurrency is how many decision-model requests one review has in
// flight at once. It bounds this review's load on the provider and nothing
// else.
const decisionConcurrency = 4

// decisionRun is the decision-model checks as one review applies them: PRD
// section 5's "Checks a decision model may add", in the mode the run's
// configuration names, with the client the service built. It is the one place
// the review stage turns an answer into a finding, and every finding it builds
// is built here by the program rather than parsed from any agent's report.
//
// What it may do is only ever add. A flagged file is a note, or an ask in
// enforce; a trace the model did not confirm is handed to the scope lens as
// unconfirmed, which can only add a note. Nothing here removes a finding,
// lowers an action, or passes a stage. A check that cannot run - no key, a
// failed call, an unreadable answer, a diff that could not be read, a request
// bound the review reached - leaves the review exactly as it would be with
// the checks off and says so in a note, which holds nothing.
type decisionRun struct {
	mode   config.DecisionModel
	client *judge.Client
	repo   *vcs.Repository
	run    string
	from   string
	head   string
}

// active reports whether the checks are asked for at all. With the mode off,
// or any value Resolve cannot produce, nothing is sent and nothing is said.
func (d decisionRun) active() bool { return d.mode.RunsChecks() }

// sends reports whether a request may leave the machine: the checks are asked
// for and the operator's environment held a key.
func (d decisionRun) sends() bool { return d.active() && d.client.HasKey() }

// checked is one check's outcome for one path. A path whose diff could not be
// read carries judge.Unasked, so it is reported and recorded like any other
// failure rather than skipped.
type checked struct {
	path   string
	reason string
	result judge.Result
}

// failure returns why this check did not produce an answer, and false when it
// produced one.
func (c checked) failure() (string, bool) {
	failure, detail, isFailed := c.result.Failed()
	if !isFailed {
		return "", false
	}
	return string(failure) + ": " + detail, true
}

// screen puts every file the reviewer was shown to the model, one file per
// request. The reviewer is shown the whole diff, excluded paths included, so
// the excluded ones are screened too.
func (d decisionRun) screen(ctx context.Context, shown []string) []checked {
	return d.each(ctx, shown, judge.CheckScreen, func(ctx context.Context, path, diff string) judge.Result {
		return d.client.Screen(ctx, path, diff)
	})
}

// traces puts each trace naming a touched path, with a reason, to the model
// together with that path's diff.
func (d decisionRun) traces(ctx context.Context, touched []string, traced []findings.Trace) []checked {
	inChange := make(map[string]bool, len(touched))
	for _, p := range touched {
		inChange[p] = true
	}
	var asked []findings.Trace
	for _, t := range traced {
		t = findings.Trace{Path: strings.TrimSpace(t.Path), Reason: strings.TrimSpace(t.Reason)}
		if inChange[t.Path] && t.Reason != "" {
			asked = append(asked, t)
		}
	}
	out := make([]checked, len(asked))
	d.parallel(len(asked), func(i int) {
		t := asked[i]
		out[i] = d.ask(ctx, t.Path, judge.CheckTrace, func(ctx context.Context, path, diff string) judge.Result {
			return d.client.Trace(ctx, path, t.Reason, diff)
		})
		out[i].reason = t.Reason
	})
	return out
}

// each asks one question per path, in parallel up to decisionConcurrency, and
// returns the outcomes in the order of paths.
func (d decisionRun) each(ctx context.Context, paths []string, check judge.Check,
	ask func(context.Context, string, string) judge.Result) []checked {
	out := make([]checked, len(paths))
	d.parallel(len(paths), func(i int) { out[i] = d.ask(ctx, paths[i], check, ask) })
	return out
}

// ask reads one path's diff and puts it to the model as check. A diff that
// cannot be read sends nothing and yields judge.Unasked for check.
func (d decisionRun) ask(ctx context.Context, path string, check judge.Check,
	ask func(context.Context, string, string) judge.Result) checked {
	diff, err := d.repo.DiffPath(ctx, d.from, d.head, path)
	if err != nil {
		return checked{path: path, result: judge.Unasked(check, path, "the path's diff could not be read: "+err.Error())}
	}
	return checked{path: path, result: ask(ctx, path, diff)}
}

// parallel runs fn for every index below n, at most decisionConcurrency at a
// time, and returns when all have.
func (d decisionRun) parallel(n int, fn func(int)) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, decisionConcurrency)
	for i := range n {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			fn(i)
		}()
	}
	wg.Wait()
}

// screenFindings turns the screen's outcomes into findings: one per flagged
// file, a note in evaluate and an ask in enforce, one note naming every file
// the screen could not answer for, one naming every file a request bound kept
// it from asking about, and one naming every answer the record could not
// keep. It records every answer, with the action of the finding it produced.
func (d decisionRun) screenFindings(outcomes []checked) []findings.Finding {
	var out []findings.Finding
	var notRun, overBound, lost []string
	for _, c := range outcomes {
		if why, isFailed := c.failure(); isFailed {
			if bounded(c) {
				overBound = append(overBound, c.path+" ("+why+")")
			} else {
				notRun = append(notRun, c.path+" ("+why+")")
			}
			lost = d.record(lost, c.result, "")
			continue
		}
		if !c.result.Fired() {
			lost = d.record(lost, c.result, "")
			continue
		}
		flagged := flaggedFinding(d.mode, c.result)
		lost = d.record(lost, c.result, string(flagged.Action))
		out = append(out, flagged)
	}
	if len(notRun) > 0 {
		out = append(out, notRunNote("The decision model's screen for text aimed at the reviewer did not "+
			"answer for "+strconv.Itoa(len(notRun))+" file(s), so nothing here says whether they add "+
			"any: ", notRun))
	}
	if len(overBound) > 0 {
		out = append(out, notRunNote("The decision model's screen for text aimed at the reviewer did not "+
			"run for "+strconv.Itoa(len(overBound))+" file(s), because this review reached a bound on "+
			"how many requests the checks may send, so nothing here says whether they add any: ", overBound))
	}
	return append(out, lostNote(judge.CheckScreen, lost)...)
}

// traceFindings turns the trace check's outcomes into the traces the model did
// not confirm, which the scope lens counts as accounting for nothing, a note
// naming every trace the check could not answer for, one naming every trace a
// request bound kept it from asking about, and one naming every answer the
// record could not keep. The traces it did not check count on the reviewer's
// word, as they would with the check off. It records every answer; an
// unconfirmed trace's finding is the scope lens's note.
func (d decisionRun) traceFindings(outcomes []checked) ([]findings.Trace, []findings.Finding) {
	var unconfirmed []findings.Trace
	var notRun, overBound, lost []string
	for _, c := range outcomes {
		if why, isFailed := c.failure(); isFailed {
			if bounded(c) {
				overBound = append(overBound, c.path+" ("+why+")")
			} else {
				notRun = append(notRun, c.path+" ("+why+")")
			}
			lost = d.record(lost, c.result, "")
			continue
		}
		if c.result.Fired() {
			unconfirmed = append(unconfirmed, findings.Trace{Path: c.path, Reason: c.reason})
			lost = d.record(lost, c.result, string(findings.ActionNote))
			continue
		}
		lost = d.record(lost, c.result, "")
	}
	var out []findings.Finding
	if len(notRun) > 0 {
		out = append(out, notRunNote("The decision model did not check the review's reason for "+
			strconv.Itoa(len(notRun))+" traced path(s), so those traces count on the reviewer's word, "+
			"as they would with the check off: ", notRun))
	}
	if len(overBound) > 0 {
		out = append(out, notRunNote("The decision model's check of the review's reasons did not run for "+
			strconv.Itoa(len(overBound))+" traced path(s), because this review reached a bound on how "+
			"many requests the checks may send, so those traces count on the reviewer's word, as they "+
			"would with the check off: ", overBound))
	}
	return unconfirmed, append(out, lostNote(judge.CheckTrace, lost)...)
}

// bounded reports whether a check failed because a request bound refused it,
// which is reported apart from a provider that did not answer: the first is
// this build declining to send, the second is a call that went wrong.
func bounded(c checked) bool {
	failure, _, isFailed := c.result.Failed()
	return isFailed && failure == judge.FailureOverBound
}

// lostNote is the one note per check saying the record could not keep every
// answer to it, and nothing when it kept them all. The review's findings are
// what they would be either way; what the note says is that the evaluation
// record understates this review, which before it only the service log said.
//
// Its text is fixed on purpose. The note lands in the review's report, which
// is graph state the convergence bound fingerprints, so a count, a path list,
// or a store's error sentence here would let a refusal that varies between
// two fix rounds that changed nothing keep the loop from converging. Which
// answers were lost and why is the service log's, where the recorder writes
// each one.
func lostNote(check judge.Check, lost []string) []findings.Finding {
	if len(lost) == 0 {
		return nil
	}
	return []findings.Finding{{
		Severity: findings.SeverityInfo,
		Action:   findings.ActionNote,
		Description: "The decision-model record could not keep every answer of the " + checkName(check) +
			" from this review, so the record kept for deciding whether the checks earn their place " +
			"understates what they said here; the service log names each answer it lost and why. The " +
			"review's findings are as they would be had the record kept them. This is informational " +
			"and holds nothing.",
	}}
}

// checkName is how a note names a check.
func checkName(check judge.Check) string {
	switch check {
	case judge.CheckScreen:
		return "screen for text aimed at the reviewer"
	case judge.CheckTrace:
		return "check of the review's trace reasons"
	default:
		return string(check)
	}
}

// decisionChecks is the review's record of what the decision-model checks did
// over this change: PRD section 5's "The run records what ran", which the pull
// request stage writes into the body so that a pass from a machine with the
// checks on is distinguishable from one without. It is built by the program
// from the checks' own results, and the review body writes it on every path
// that read the change, so a re-review never leaves an earlier execution's
// record standing.
type decisionChecks struct {
	// Mode is the run's decision-model mode.
	Mode string `json:"mode"`
	// Model is the model version the checks asked, empty when nothing was
	// sent.
	Model string `json:"model,omitempty"`
	// Why says why neither check ran, empty when the checks were sent.
	Why string `json:"why,omitempty"`
	// Screen and Trace are each check's coverage, absent when it did not
	// run.
	Screen *checkCoverage `json:"screen,omitempty"`
	Trace  *checkCoverage `json:"trace,omitempty"`
	// TraceWhy says why the trace check did not run when the screen did.
	TraceWhy string `json:"trace_why,omitempty"`
}

// checkCoverage is how many paths or traces one check covered: put to it,
// answered by the model, and fired on.
type checkCoverage struct {
	Asked    int `json:"asked"`
	Answered int `json:"answered"`
	Fired    int `json:"fired"`
}

// coverageOf counts one check's outcomes.
func coverageOf(outcomes []checked) *checkCoverage {
	c := &checkCoverage{Asked: len(outcomes)}
	for _, o := range outcomes {
		if _, ok := o.result.Probability(); ok {
			c.Answered++
		}
		if o.result.Fired() {
			c.Fired++
		}
	}
	return c
}

// withDecisionChecks writes the record into the review's output.
func withDecisionChecks(out pipeline.Output, checks decisionChecks) pipeline.Output {
	encoded, err := json.Marshal(checks)
	if err != nil {
		// Strings and integers only: nothing here can fail to encode.
		panic("stages: encoding the decision-model record: " + err.Error())
	}
	if out.Writes == nil {
		out.Writes = map[pipeline.Key]graph.Value{}
	}
	out.Writes[pipeline.KeyDecisionChecks] = graph.TextValue(string(encoded))
	return out
}

// readDecisionChecks decodes the record, and reports false when there is
// none, which is a review that did not run or did not read the change.
func readDecisionChecks(v graph.Value) (decisionChecks, bool, error) {
	text, ok := v.Text()
	if !ok || text == "" {
		return decisionChecks{}, false, nil
	}
	var checks decisionChecks
	if err := json.Unmarshal([]byte(text), &checks); err != nil {
		return decisionChecks{}, false, err
	}
	return checks, true, nil
}

// noKeyNote is the one note a review whose checks are asked for gets when the
// operator's environment held no key: neither check ran and nothing was sent.
func noKeyNote() findings.Finding {
	return findings.Finding{
		Severity: findings.SeverityInfo,
		Action:   findings.ActionNote,
		Description: "The decision-model checks did not run over this change - neither the screen for " +
			"text aimed at the reviewer nor the check of the review's trace reasons - because no key " +
			"for the provider is in " + judge.KeyVariable + " in the operator's environment, so " +
			"nothing was sent. The review stands exactly as it would with the checks off. This is " +
			"informational and holds nothing.",
	}
}

// notRunNote is a note saying which check did not run where and why. It holds
// nothing, in every mode.
func notRunNote(lead string, items []string) findings.Finding {
	sort.Strings(items)
	return findings.Finding{
		Severity: findings.SeverityInfo,
		Action:   findings.ActionNote,
		Description: lead + strings.Join(items, "; ") + ". A check that cannot run leaves the review " +
			"as it would be with the checks off. This is informational and holds nothing.",
	}
}

// flaggedFinding is what a file the screen flagged becomes: a note in
// evaluate, an ask in enforce, naming the file so a person reading the
// report reads that file too.
func flaggedFinding(mode config.DecisionModel, r judge.Result) findings.Finding {
	p, _ := r.Probability()
	f := findings.Finding{
		Severity: findings.SeverityWarning,
		Action:   findings.ActionNote,
		Location: findings.Location{Path: r.Path()},
		Description: "A decision model flagged " + r.Path() + " as possibly adding text aimed at an AI " +
			"reviewer or coding agent that tries to steer its verdict, suppress its findings, or make it " +
			"act (probability " + strconv.FormatFloat(p, 'f', 3, 64) + " against a cutoff of " +
			strconv.FormatFloat(r.Cutoff(), 'f', 2, 64) + " on " + r.Model() + "). The reviewer has " +
			"already read this file, so read it yourself before relying on the review's verdict. The " +
			"model's answer is a judgment with no evidence behind it.",
	}
	if mode == config.DecisionEnforce {
		f.Severity = findings.SeverityError
		f.Action = findings.ActionAsk
		f.Description += " The decision model is in enforce mode, so this holds the review for a person."
		return f
	}
	f.Description += " The decision model is in evaluate mode, so this is informational and holds nothing."
	return f
}

// record hands one answer to the client's recorder, and adds it to lost when
// the recorder could not keep it.
func (d decisionRun) record(lost []string, r judge.Result, finding string) []string {
	if err := d.client.Record(judge.RecordOf(d.run, pipeline.StageReview.String(), r, finding)); err != nil {
		return append(lost, r.Path()+" ("+string(r.Check())+": "+err.Error()+")")
	}
	return lost
}
