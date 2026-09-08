package stages_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// host is a stand-in code host. It models a host rather than scripting
// answers: it holds the open pull requests it has been asked to open, keyed by
// head branch, and every method answers out of that. A canned answer per call
// would let this state a sequence the real thing cannot produce, and the one
// property the stage rests on - that a head which already has a pull request
// is updated rather than opened again - is a relationship between calls that
// only a modelled host can get wrong.
//
// It refuses nothing on its own. The two shapes a test needs that this model
// does not reach on its own are set explicitly, and each is commented where it
// is set with what the GitHub adapter would do with it.
type host struct {
	mu sync.Mutex
	// open is the pull requests this host has open, by head branch.
	open map[string]forge.PullRequest
	// next is the number the next opened pull request gets.
	next int
	// refuse is returned by every method when it is set, which is what a
	// provider that could not answer does.
	refuse error
	// numbering overrides the number an opened pull request is given.
	numbering func(int) int
	// calls records every method call, in order, as "method head-or-number".
	calls []string
	// bodies records every body this host was handed, in order.
	bodies []string
}

func newHost() *host {
	return &host{open: map[string]forge.PullRequest{}, next: 1}
}

// retarget changes the base of the pull request open on a head, which is what
// a person retargeting one leaves behind. It is a change made to a pull
// request that already exists, because that is the only way a code host comes
// to hold one whose base is not the base it was opened with.
func (h *host) retarget(head, base string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pr := h.open[head]
	pr.Base = base
	h.open[head] = pr
}

func (h *host) record(call string) { h.calls = append(h.calls, call) }

func (h *host) Find(_ context.Context, head string) (forge.PullRequest, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record("find " + head)
	if h.refuse != nil {
		return forge.PullRequest{}, false, h.refuse
	}
	pr, ok := h.open[head]
	return pr, ok, nil
}

func (h *host) Open(_ context.Context, spec forge.OpenSpec) (forge.PullRequest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record("open " + spec.Head)
	h.bodies = append(h.bodies, spec.Body)
	if h.refuse != nil {
		return forge.PullRequest{}, h.refuse
	}
	number := h.next
	h.next++
	if h.numbering != nil {
		number = h.numbering(number)
	}
	pr := forge.PullRequest{
		Number:       number,
		URL:          "https://example.invalid/pull/" + strconv.Itoa(number),
		Title:        spec.Title,
		State:        forge.PullRequestStateOpen,
		Mergeability: forge.MergeabilityUnknown,
		Head:         spec.Head,
		HeadCommit:   "0123456789abcdef0123456789abcdef01234567",
		Base:         spec.Base,
		Draft:        spec.Draft,
	}
	h.open[spec.Head] = pr
	return pr, nil
}

func (h *host) UpdateBody(_ context.Context, number int, body string) (forge.PullRequest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record("update-body " + strconv.Itoa(number))
	h.bodies = append(h.bodies, body)
	if h.refuse != nil {
		return forge.PullRequest{}, h.refuse
	}
	for head, pr := range h.open {
		if pr.Number == number {
			return h.open[head], nil
		}
	}
	return forge.PullRequest{}, &forge.Refusal{
		Reason: forge.ReasonRejected,
		Op:     "update-body",
		Detail: "no such pull request",
	}
}

func (h *host) Get(_ context.Context, number int) (forge.PullRequest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record("get " + strconv.Itoa(number))
	if h.refuse != nil {
		return forge.PullRequest{}, h.refuse
	}
	for _, pr := range h.open {
		if pr.Number == number {
			return pr, nil
		}
	}
	return forge.PullRequest{}, &forge.Refusal{
		Reason: forge.ReasonRejected, Op: "get", Detail: "no such pull request"}
}

func (h *host) Checks(context.Context, int) (forge.ChecksReport, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record("checks")
	return forge.ChecksReport{}, h.refuse
}

var _ forge.Provider = (*host)(nil)

// runState is a run's state as the pull request stage reads it, built from a
// few facts and from what each stage recorded. Anything not named here holds
// the zero value of its kind, which is what a declared key a run has not
// written holds.
type runState struct {
	run       string
	branch    string
	base      string
	intent    string
	supplied  bool
	submitted string
	head      string
	pushed    string
	recorded  map[pipeline.Stage]recordedStage
}

// recordedStage is what one stage of the run left in state.
type recordedStage struct {
	outcome pipeline.Outcome
	report  findings.Report
	fix     string
}

// state renders the run as the state map the stage reads.
func (r runState) state(t *testing.T) map[pipeline.Key]graph.Value {
	t.Helper()
	out := map[pipeline.Key]graph.Value{
		pipeline.KeyRun:            graph.TextValue(r.run),
		pipeline.KeyBranch:         graph.TextValue(r.branch),
		pipeline.KeyBase:           graph.TextValue(r.base),
		pipeline.KeyIntent:         graph.TextValue(r.intent),
		pipeline.KeyIntentSupplied: graph.BoolValue(r.supplied),
		pipeline.KeySubmitted:      graph.TextValue(r.submitted),
		pipeline.KeyHead:           graph.TextValue(r.head),
		pipeline.KeyPushed:         graph.TextValue(r.pushed),
	}
	for stage, recorded := range r.recorded {
		out[stage.OutcomeKey()] = graph.TextValue(string(recorded.outcome))
		// A stage the run passed over records an outcome and no report, so a
		// recordedStage with no summary leaves the report key at its zero
		// value here too. The pipeline refuses a report without a summary, so
		// there is no recorded report this could be mistaken for.
		if recorded.report.Summary != "" {
			// The report is encoded the way the stage node encodes it, so a
			// body that read it some other way would not decode this.
			encoded, err := json.Marshal(recorded.report.Normalize())
			if err != nil {
				t.Fatalf("encoding the %s stage's report: %v", stage, err)
			}
			out[stage.ReportKey()] = graph.TextValue(string(encoded))
		}
		if recorded.fix != "" {
			out[stage.FixKey()] = graph.TextValue(recorded.fix)
		}
	}
	return out
}

// aRun is a run that reached the pull request stage with something recorded at
// every stage before it, so that a test which does not care about the
// narration still exercises the whole of it.
func aRun() runState {
	recorded := map[pipeline.Stage]recordedStage{}
	for _, stage := range pipeline.Order() {
		if stage == pipeline.StagePR || stage == pipeline.StageCI {
			continue
		}
		recorded[stage] = recordedStage{
			outcome: pipeline.OutcomePassed,
			report: findings.Report{
				Summary:  "the " + stage.String() + " stage had nothing to report",
				Findings: []findings.Finding{},
			},
		}
	}
	return runState{
		run:       "run-1",
		branch:    "feature/greeting",
		base:      "main",
		intent:    "Add a greeting to the entry point",
		supplied:  true,
		submitted: "1111111111111111111111111111111111111111",
		head:      "2222222222222222222222222222222222222222",
		pushed:    "2222222222222222222222222222222222222222",
		recorded:  recorded,
	}
}

// runPR runs the pull request stage's body over a run, through the same
// restriction on reads the stage node applies.
func runPR(t *testing.T, provider forge.Provider, run runState) (pipeline.Output, error) {
	t.Helper()
	impl := stages.PullRequest(stages.StageDeps{Forge: provider})
	allowed := make(map[pipeline.Key]bool, len(impl.Reads))
	for _, key := range impl.Reads {
		allowed[key] = true
	}
	return impl.NewBody()(t.Context(), pipeline.Input{
		Stage: pipeline.StagePR,
		State: declaredReader{allowed: allowed, state: run.state(t)},
	})
}

// mustRunPR runs the stage and fails when it could not.
func mustRunPR(t *testing.T, provider forge.Provider, run runState) pipeline.Output {
	t.Helper()
	out, err := runPR(t, provider, run)
	if err != nil {
		t.Fatalf("running the pull request stage: %v", err)
	}
	return out
}

// A build with no code host cannot open a pull request, and the stage says so
// rather than reporting a stage that established nothing as one that passed.
//
// The positive control is the same body against a host: without it this would
// pass just as well if the stage failed on everything.
func TestThePullRequestStageFailsWithNoCodeHost(t *testing.T) {
	t.Parallel()
	out, err := runPR(t, nil, aRun())
	if err == nil {
		t.Fatalf("the stage reported %+v with no code host, rather than failing", out.Report)
	}
	if !strings.Contains(err.Error(), "code host") {
		t.Fatalf("the failure %q does not say what is missing", err)
	}
	if len(out.Writes) != 0 {
		t.Fatalf("the stage asked to write %v after failing", out.Writes)
	}

	if _, err := runPR(t, newHost(), aRun()); err != nil {
		t.Fatalf("the stage failed against a code host too, so the assertion above "+
			"does not distinguish a missing host from anything else: %v", err)
	}
}

// PRD principle P1: pushing to the gate authorizes that run to open a pull
// request, and nothing else implies that consent. What this stage owes P1 is
// that the pull request it acts on is the one for the branch the run names,
// and that a run does not accumulate pull requests by being validated twice.
//
// Both are checked against the calls the host received rather than against
// what the stage reported, because what the stage said it did is not evidence
// about what it did.
func TestThePullRequestStageActsOnlyOnTheRunsOwnBranch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)

	h := newHost()
	run := aRun()
	first := mustRunPR(t, h, run)
	second := mustRunPR(t, h, run)

	for _, call := range h.calls {
		method, target, _ := strings.Cut(call, " ")
		if method != "find" && method != "open" {
			continue // Addressed by number, which is the pull request found for this branch.
		}
		if target != run.branch {
			t.Fatalf("the stage called %q, and the run's branch is %s", call, run.branch)
		}
	}
	if opened := countPrefix(h.calls, "open "); opened != 1 {
		t.Fatalf("two runs of the stage opened %d pull requests, want 1: %v", opened, h.calls)
	}
	if countPrefix(h.calls, "update-body ") != 1 {
		t.Fatalf("the second run did not update the pull request the first opened: %v", h.calls)
	}
	firstNumber, _ := first.Writes[pipeline.KeyPullRequest].Text()
	secondNumber, _ := second.Writes[pipeline.KeyPullRequest].Text()
	if firstNumber != secondNumber {
		t.Fatalf("two runs of the stage recorded different pull requests: %q then %q",
			firstNumber, secondNumber)
	}
}

// The run's record of its pull request is the number every later call
// addresses it by, so that is what the stage writes.
func TestThePullRequestStageRecordsTheNumberTheHostAnswered(t *testing.T) {
	t.Parallel()
	h := newHost()
	h.next = 41
	out := mustRunPR(t, h, aRun())
	written, ok := out.Writes[pipeline.KeyPullRequest]
	if !ok {
		t.Fatalf("the stage wrote %v, and none of it is the pull request", out.Writes)
	}
	if text, _ := written.Text(); text != "41" {
		t.Fatalf("the stage recorded pull request %q, and the host answered 41", text)
	}
	if len(out.Writes) != 1 {
		t.Fatalf("the stage wrote %v, which is more than the pull request", out.Writes)
	}
}

// A number that identifies no pull request is refused rather than recorded,
// because a run whose record points at nothing is one the checks stage cannot
// address.
//
// The shape is one forge.Provider permits and the GitHub adapter in this
// module does not produce: that adapter refuses such an answer with
// ReasonMalformed before it reaches a caller. So what this checks is the
// stage's own write against the interface it is written to, and against that
// adapter it catches nothing.
func TestThePullRequestStageRefusesANumberThatIdentifiesNoPullRequest(t *testing.T) {
	t.Parallel()
	h := newHost()
	h.numbering = func(int) int { return 0 }
	out, err := runPR(t, h, aRun())
	if err == nil {
		t.Fatalf("the stage accepted a pull request with no number and reported %+v", out.Report)
	}
	if len(out.Writes) != 0 {
		t.Fatalf("the stage asked to write %v for a pull request with no number", out.Writes)
	}
}

// A host that did not answer is a refusal, and the stage returns it rather
// than reporting a finding: a stage that could not reach the code host did not
// run. The refusal survives the wrapping, so a caller can still read why.
func TestThePullRequestStageReturnsTheHostsRefusal(t *testing.T) {
	t.Parallel()
	h := newHost()
	h.refuse = &forge.Refusal{
		Reason: forge.ReasonUnauthenticated,
		Op:     "find",
		Detail: "the provider reported this caller is not authenticated",
	}
	out, err := runPR(t, h, aRun())
	if err == nil {
		t.Fatalf("the stage reported %+v over a host that refused", out.Report)
	}
	if !errors.Is(err, forge.ErrRefused) {
		t.Fatalf("the stage's failure %q does not carry the refusal", err)
	}
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != forge.ReasonUnauthenticated {
		t.Fatalf("the stage's failure %q does not carry why the host refused", err)
	}
	if len(out.Writes) != 0 {
		t.Fatalf("the stage asked to write %v after the host refused", out.Writes)
	}
}

// PRD section 5 asks for a body written for a reviewer who was not present:
// what changed, what was checked, what the risks are, what the pipeline had to
// fix. Each of the four is checked by something only that section can carry,
// so a body that dropped a section fails here even though its heading would
// still be present.
func TestThePullRequestBodyAnswersTheFourQuestions(t *testing.T) {
	t.Parallel()
	run := aRun()
	run.recorded[pipeline.StageReview] = recordedStage{
		outcome: pipeline.OutcomeApproved,
		fix:     "rewrote the greeting to use the configured locale",
		report: findings.Report{
			Summary:       "the change is coherent with the intent",
			Risk:          findings.RiskMedium,
			RiskRationale: "it changes a path every request takes",
			Tested:        []string{"read the entry point and its one caller"},
			Findings: []findings.Finding{{
				ID:          "hardcoded-locale",
				Severity:    findings.SeverityWarning,
				Action:      findings.ActionAsk,
				Location:    findings.Location{Path: "main.go", Line: 12},
				Description: "the greeting is in one language and the intent does not say which",
			}},
		},
	}
	h := newHost()
	mustRunPR(t, h, run)
	body := onlyBody(t, h)

	for _, want := range []struct {
		section string
		text    string
	}{
		{"what changed", "feature/greeting"},
		{"what changed", run.intent},
		{"what changed", run.submitted},
		{"what was checked", "read the entry point and its one caller"},
		{"what was checked", "the greeting is in one language"},
		{"what was checked", "main.go:12"},
		{"what the risks are", "medium"},
		{"what the risks are", "it changes a path every request takes"},
		{"what the risks are", "hardcoded-locale"},
		{"what the pipeline had to fix", "rewrote the greeting to use the configured locale"},
	} {
		if !strings.Contains(body, want.text) {
			t.Errorf("the body's %s section does not carry %q:\n\n%s", want.section, want.text, body)
		}
	}
	for _, heading := range []string{
		"## What changed", "## What was checked",
		"## What the risks are", "## What the pipeline had to fix",
	} {
		if !strings.Contains(body, heading) {
			t.Errorf("the body has no %q section:\n\n%s", heading, body)
		}
	}
}

// The body accounts for every stage of the run. A stage missing from it is a
// stage a reviewer is not told about, and reading the set from
// pipeline.Order is what keeps this from having to say how many there are.
func TestThePullRequestBodyAccountsForEveryStage(t *testing.T) {
	t.Parallel()
	h := newHost()
	mustRunPR(t, h, aRun())
	body := onlyBody(t, h)
	for _, stage := range pipeline.Order() {
		if !strings.Contains(body, "### "+stage.String()) {
			t.Errorf("the body has no section for the %s stage:\n\n%s", stage, body)
		}
	}
}

// A stage that recorded nothing is not a stage that found nothing, and the
// body says which. The two cases are different facts: a stage the run passed
// over was decided against, and a stage with no outcome at all had not been
// reached when the body was written, which is what this stage's own section
// and every section after it says.
func TestThePullRequestBodyDoesNotReportAStageThatDidNotRun(t *testing.T) {
	t.Parallel()
	run := aRun()
	skipped := pipeline.StageLint
	run.recorded[skipped] = recordedStage{outcome: pipeline.OutcomeSkipped}
	h := newHost()
	mustRunPR(t, h, run)
	sections := bodySections(t, onlyBody(t, h))

	for stage, want := range map[pipeline.Stage]string{
		skipped:          "did not run",
		pipeline.StagePR: "had not run",
		pipeline.StageCI: "had not run",
	} {
		section, ok := sections[stage.String()]
		if !ok {
			t.Fatalf("the body has no section for the %s stage", stage)
		}
		if !strings.Contains(section, want) {
			t.Errorf("the %s stage's section does not say it %q:\n\n%s", stage, want, section)
		}
		if strings.Contains(section, "reported no findings") {
			t.Errorf("the %s stage's section reports it as having found nothing:\n\n%s", stage, section)
		}
	}
	// The positive control: a stage that did run is reported as having run, so
	// the assertions above are not passing over a body that says "did not run"
	// everywhere.
	if section := sections[pipeline.StageIntent.String()]; !strings.Contains(section, "This stage ran") {
		t.Fatalf("the intent stage ran and its section says otherwise:\n\n%s", section)
	}
}

// A pull request that already exists keeps the base a person gave it, so a run
// can validate against one branch while the pull request merges into another.
// The stage reports that rather than passing over it, because nothing the run
// established is evidence about the other merge.
func TestThePullRequestStageReportsABaseTheRunDidNotValidateAgainst(t *testing.T) {
	t.Parallel()
	run := aRun()
	h := newHost()
	// The pull request is opened by a run first, and a person then retargets
	// it. That is the path a base the run did not validate against is reached
	// by: a host opens what it is asked for, so nothing else produces one.
	mustRunPR(t, h, run)
	h.retarget(run.branch, "release/2")

	out := mustRunPR(t, h, run)
	if err := out.Report.Normalize().Validate(); err != nil {
		t.Fatalf("the stage produced a report the pipeline refuses: %v", err)
	}
	if !mentions(out.Report, "release/2") || !mentions(out.Report, run.base) {
		t.Fatalf("the report does not name both bases: %+v", out.Report)
	}

	// The same run against a host that kept the run's base reports only that
	// the pull request exists, so the assertion above reads the mismatch
	// rather than something the stage always says.
	matching := mustRunPR(t, newHost(), run)
	if mentions(matching.Report, "release/2") {
		t.Fatalf("the report names a base nothing in the run carries: %+v", matching.Report)
	}
	if len(matching.Report.Findings) >= len(out.Report.Findings) {
		t.Fatalf("a matching base reported %d findings and a mismatched one %d, so the "+
			"mismatch is not what the extra finding reports",
			len(matching.Report.Findings), len(out.Report.Findings))
	}
}

// The stage reports information and nothing that needs anything done about it:
// it either reached the code host, and what it did is a fact, or it did not,
// and it failed instead. A finding that held here would stop a run at the last
// stage that can still be answered, over something no person can act on.
func TestThePullRequestStageReportsOnlyNotes(t *testing.T) {
	t.Parallel()
	run := aRun()
	h := newHost()
	mustRunPR(t, h, run)
	h.retarget(run.branch, "release/2")
	for _, out := range []pipeline.Output{mustRunPR(t, newHost(), run), mustRunPR(t, h, run)} {
		report := out.Report.Normalize()
		if err := report.Validate(); err != nil {
			t.Fatalf("the stage produced a report the pipeline refuses: %v", err)
		}
		if !report.AllNotes() {
			t.Fatalf("the stage reported something that is not a note: %+v", report)
		}
	}
}

// The title a new pull request is opened with comes from the run, because the
// run is all this stage knows that names the change.
func TestThePullRequestTitleComesFromTheRun(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a very wordy intent ", 10)
	for _, c := range []struct {
		name   string
		intent string
		want   func(string) bool
		says   string
	}{
		{
			name:   "the first line of the intent",
			intent: "Add a greeting\n\nand the reasoning behind it",
			want:   func(got string) bool { return got == "Add a greeting" },
			says:   "the first line of the intent",
		},
		{
			name:   "the branch when the run carries no intent",
			intent: "",
			want:   func(got string) bool { return got == "feature/greeting" },
			says:   "the branch name",
		},
		{
			name:   "cut short when the intent is prose",
			intent: long,
			want: func(got string) bool {
				return len(got) < len(long) && strings.HasSuffix(got, "...") &&
					!strings.ContainsAny(got, "\r\n")
			},
			says: "the intent cut short",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			run := aRun()
			run.intent = c.intent
			h := newHost()
			mustRunPR(t, h, run)
			h.mu.Lock()
			defer h.mu.Unlock()
			pr, ok := h.open[run.branch]
			if !ok {
				t.Fatalf("the stage opened nothing for %s", run.branch)
			}
			if !c.want(pr.Title) {
				t.Fatalf("the pull request was opened as %q, and the title should be %s", pr.Title, c.says)
			}
			if strings.ContainsAny(pr.Title, "\r\n") {
				t.Fatalf("the title %q carries a line break, which internal/forge refuses", pr.Title)
			}
		})
	}
}

// The stage declares every key it reads. A read it did not declare is refused
// by the reader it is handed and fails the step, so this drives the body over
// a run with something at every stage and checks it came back with a body
// rather than a refusal.
//
// The positive control is a reader that allows nothing: without it, a body
// that read nothing at all would pass this just as well.
func TestThePullRequestStageDeclaresEveryKeyItReads(t *testing.T) {
	t.Parallel()
	mustRunPR(t, newHost(), aRun())

	impl := stages.PullRequest(stages.StageDeps{Forge: newHost()})
	_, err := impl.NewBody()(t.Context(), pipeline.Input{
		Stage: pipeline.StagePR,
		State: declaredReader{allowed: map[pipeline.Key]bool{}, state: aRun().state(t)},
	})
	if err == nil {
		t.Fatal("the stage read nothing at all, so the run above proves no declaration")
	}
}

// onlyBody returns the one body the host was handed, failing when it was
// handed another number of them.
func onlyBody(t *testing.T, h *host) string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.bodies) != 1 {
		t.Fatalf("the host was handed %d bodies, want 1", len(h.bodies))
	}
	return h.bodies[0]
}

// bodySections splits a rendered body into its per-stage sections, keyed by
// the stage name each heading carries.
func bodySections(t *testing.T, body string) map[string]string {
	t.Helper()
	sections := map[string]string{}
	var name string
	var current strings.Builder
	flush := func() {
		if name != "" {
			sections[name] = current.String()
		}
		current.Reset()
	}
	for _, line := range strings.Split(body, "\n") {
		if heading, ok := strings.CutPrefix(line, "### "); ok {
			flush()
			name = strings.TrimSpace(heading)
			continue
		}
		if strings.HasPrefix(line, "## ") {
			flush()
			name = ""
			continue
		}
		current.WriteString(line + "\n")
	}
	flush()
	return sections
}

// mentions reports whether a report says text anywhere a person would read it.
func mentions(report findings.Report, text string) bool {
	if strings.Contains(report.Summary, text) {
		return true
	}
	for _, finding := range report.Findings {
		if strings.Contains(finding.Description, text) {
			return true
		}
	}
	return false
}

// countPrefix counts the calls that start with prefix.
func countPrefix(calls []string, prefix string) int {
	var n int
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}
