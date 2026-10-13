package stages

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
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
// failed call, an unreadable answer, a diff that could not be read - leaves
// the review exactly as it would be with the checks off and says so in a note,
// which holds nothing.
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
// file, a note in evaluate and an ask in enforce, and one note naming every
// file the screen could not answer for. It records every answer, with the
// action of the finding it produced.
func (d decisionRun) screenFindings(outcomes []checked) []findings.Finding {
	var out []findings.Finding
	var notRun []string
	for _, c := range outcomes {
		if why, isFailed := c.failure(); isFailed {
			notRun = append(notRun, c.path+" ("+why+")")
			d.record(c.result, "")
			continue
		}
		if !c.result.Fired() {
			d.record(c.result, "")
			continue
		}
		flagged := flaggedFinding(d.mode, c.result)
		d.record(c.result, string(flagged.Action))
		out = append(out, flagged)
	}
	if len(notRun) > 0 {
		out = append(out, notRunNote("The decision model's screen for text aimed at the reviewer did not "+
			"answer for "+strconv.Itoa(len(notRun))+" file(s), so nothing here says whether they add "+
			"any: ", notRun))
	}
	return out
}

// traceFindings turns the trace check's outcomes into the traces the model did
// not confirm, which the scope lens counts as accounting for nothing, and a
// note naming every trace the check could not answer for. Those count on the
// reviewer's word, as they would with the check off. It records every answer;
// an unconfirmed trace's finding is the scope lens's note.
func (d decisionRun) traceFindings(outcomes []checked) ([]findings.Trace, []findings.Finding) {
	var unconfirmed []findings.Trace
	var notRun []string
	for _, c := range outcomes {
		if why, isFailed := c.failure(); isFailed {
			notRun = append(notRun, c.path+" ("+why+")")
			d.record(c.result, "")
			continue
		}
		if c.result.Fired() {
			unconfirmed = append(unconfirmed, findings.Trace{Path: c.path, Reason: c.reason})
			d.record(c.result, string(findings.ActionNote))
			continue
		}
		d.record(c.result, "")
	}
	var out []findings.Finding
	if len(notRun) > 0 {
		out = append(out, notRunNote("The decision model did not check the review's reason for "+
			strconv.Itoa(len(notRun))+" traced path(s), so those traces count on the reviewer's word, "+
			"as they would with the check off: ", notRun))
	}
	return unconfirmed, out
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

// record hands one answer to the client's recorder.
func (d decisionRun) record(r judge.Result, finding string) {
	d.client.Record(judge.RecordOf(d.run, pipeline.StageReview.String(), r, finding))
}
