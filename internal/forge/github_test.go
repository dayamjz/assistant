package forge_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The JSON below is what gh puts on standard output for the field sets this
// adapter asks for. It carries the fields the adapter ignores as well as the
// ones it reads, and it keeps gh's own key order and vocabulary, so a test
// cannot pass over an answer the real command could not produce.
const (
	openPullRequestJSON = `{"baseRefName":"main","headRefName":"fm/work",` +
		`"headRefOid":"6f1a3c2b0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a","isDraft":false,` +
		`"mergeable":"MERGEABLE","number":12,"state":"OPEN","title":"feat: a change",` +
		`"url":"https://github.com/owner/name/pull/12"}`

	conflictedPullRequestJSON = `{"baseRefName":"main","headRefName":"fm/work",` +
		`"headRefOid":"6f1a3c2b0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a","isDraft":false,` +
		`"mergeable":"CONFLICTING","number":12,"state":"OPEN","title":"feat: a change",` +
		`"url":"https://github.com/owner/name/pull/12"}`

	// gh answers a list read with an array, and with an empty array when the
	// branch has no open pull request.
	oneListedJSON  = `[{"number":12}]`
	twoListedJSON  = `[{"number":12},{"number":13}]`
	noneListedJSON = `[]`

	// The create call's answer is not parsed, so its shape decides nothing
	// here; this is what gh prints.
	createdURL = "https://github.com/owner/name/pull/12\n"

	headCommit = "6f1a3c2b0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a"
)

// rollup renders a check read the way gh does: the head commit alongside the
// rollup entries.
func rollup(nodes ...string) string {
	return `{"headRefOid":"` + headCommit + `","statusCheckRollup":[` + strings.Join(nodes, ",") + `]}`
}

// checkRunNode is one CheckRun entry of the rollup, with every field gh emits.
func checkRunNode(name, status, conclusion string) string {
	return `{"__typename":"CheckRun","completedAt":"2026-04-01T10:03:11Z","conclusion":"` + conclusion +
		`","detailsUrl":"https://github.com/owner/name/actions/runs/42/job/99","name":"` + name +
		`","startedAt":"2026-04-01T10:01:00Z","status":"` + status + `","workflowName":"CI"}`
}

// statusContextNode is one StatusContext entry of the rollup, with every field
// gh emits. It reports its state in a different field and a different
// vocabulary than a CheckRun does, which is the difference the adapter reads.
func statusContextNode(context, state string) string {
	return `{"__typename":"StatusContext","context":"` + context +
		`","createdAt":"2026-04-01T10:02:00Z","description":"the external check","state":"` + state +
		`","targetUrl":"https://ci.example.test/build/7"}`
}

func TestFindReportsNoOpenPullRequestAsAnAnswer(t *testing.T) {
	h := newHarness(t, ghScript{"list": {{Stdout: noneListedJSON}}})
	pr, found, err := h.gh.Find(context.Background(), "fm/work")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if found {
		t.Errorf("Find reported a pull request: %+v", pr)
	}
	list := h.callsFor("list")
	if len(list) != 1 {
		t.Fatalf("Find made %d list calls, want 1", len(list))
	}
	for _, want := range []string{"--head=fm/work", "--state=open"} {
		if !list[0].hasArg(want) {
			t.Errorf("the list call is missing %q: %v", want, list[0].Args)
		}
	}
}

func TestFindReadsTheOpenPullRequest(t *testing.T) {
	h := newHarness(t, ghScript{
		"list": {{Stdout: oneListedJSON}},
		"view": {{Stdout: openPullRequestJSON}},
	})
	pr, found, err := h.gh.Find(context.Background(), "fm/work")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !found {
		t.Fatalf("Find reported no pull request")
	}
	want := forge.PullRequest{
		Number:       12,
		URL:          "https://github.com/owner/name/pull/12",
		Title:        "feat: a change",
		State:        forge.PullRequestStateOpen,
		Mergeability: forge.MergeabilityMergeable,
		Head:         "fm/work",
		HeadCommit:   headCommit,
		Base:         "main",
	}
	if pr != want {
		t.Errorf("Find returned %+v, want %+v", pr, want)
	}
}

func TestFindRefusesABranchWithMoreThanOneOpenPullRequest(t *testing.T) {
	h := newHarness(t, ghScript{"list": {{Stdout: twoListedJSON}}})
	_, found, err := h.gh.Find(context.Background(), "fm/work")
	if found {
		t.Errorf("Find reported a pull request while refusing")
	}
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Find returned %v, want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonAmbiguous {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonAmbiguous)
	}
	if !errors.Is(err, forge.ErrRefused) {
		t.Errorf("the refusal does not match ErrRefused")
	}
	if n := len(h.callsFor("view")); n != 0 {
		t.Errorf("the adapter read a pull request it had refused to choose: %d view calls", n)
	}
}

func TestMergeabilityAndChecksAreSeparateAnswers(t *testing.T) {
	h := newHarness(t, ghScript{
		"view":   {{Stdout: conflictedPullRequestJSON}},
		"checks": {{Stdout: rollup(checkRunNode("build", "COMPLETED", "SUCCESS"))}},
	})
	pr, err := h.gh.Get(context.Background(), 12)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pr.Mergeability != forge.MergeabilityConflicted {
		t.Errorf("mergeability is %v, want conflicted", pr.Mergeability)
	}
	report, err := h.gh.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	result := report.Evaluate(forge.NoCIDeclaration{})
	if !result.Green() {
		t.Errorf("checks over a passing check are %v, want green", result)
	}
	// The two answers disagree, and that is the point: a caller can tell
	// "green but not mergeable" from "still running".
	if pr.State != forge.PullRequestStateOpen {
		t.Errorf("state is %v, want open", pr.State)
	}
}

func TestChecksOverAnEmptyRollupIsNotGreen(t *testing.T) {
	h := newHarness(t, ghScript{"checks": {{Stdout: rollup()}}})
	report, err := h.gh.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if len(report.Runs) != 0 {
		t.Fatalf("Checks returned %d runs over an empty rollup", len(report.Runs))
	}
	if report.HeadCommit != headCommit {
		t.Errorf("head commit is %q, want %q", report.HeadCommit, headCommit)
	}
	if got := report.Evaluate(forge.NoCIDeclaration{}); got.Green() {
		t.Errorf("an empty rollup was reported green: %v", got)
	}
	if got := report.Evaluate(declared(t)); !got.Green() {
		t.Errorf("an empty rollup under the declaration is %v, want green", got)
	}
}

// TestChecksMapsTheRollupOntoStates reads one rollup carrying every entry
// shape the provider produces, so the mapping is proved over an answer the
// real command could return and the order the provider reported is proved to
// survive.
func TestChecksMapsTheRollupOntoStates(t *testing.T) {
	cases := []struct {
		node     string
		state    forge.CheckState
		reported string
	}{
		{checkRunNode("completed-success", "COMPLETED", "SUCCESS"), forge.CheckStateSucceeded, ""},
		{checkRunNode("completed-failure", "COMPLETED", "FAILURE"), forge.CheckStateFailed, ""},
		{checkRunNode("completed-timeout", "COMPLETED", "TIMED_OUT"), forge.CheckStateFailed, ""},
		{checkRunNode("completed-startup-failure", "COMPLETED", "STARTUP_FAILURE"), forge.CheckStateFailed, ""},
		{checkRunNode("completed-action-required", "COMPLETED", "ACTION_REQUIRED"), forge.CheckStateFailed, ""},
		{checkRunNode("completed-stale", "COMPLETED", "STALE"), forge.CheckStateFailed, ""},
		{checkRunNode("completed-cancelled", "COMPLETED", "CANCELLED"), forge.CheckStateCancelled, ""},
		{checkRunNode("completed-skipped", "COMPLETED", "SKIPPED"), forge.CheckStateSkipped, ""},
		{checkRunNode("completed-neutral", "COMPLETED", "NEUTRAL"), forge.CheckStateNeutral, ""},
		{checkRunNode("queued", "QUEUED", ""), forge.CheckStatePending, ""},
		{checkRunNode("in-progress", "IN_PROGRESS", ""), forge.CheckStatePending, ""},
		{checkRunNode("waiting", "WAITING", ""), forge.CheckStatePending, ""},
		{checkRunNode("requested", "REQUESTED", ""), forge.CheckStatePending, ""},
		{checkRunNode("unknown-status", "MOONWALKING", ""), forge.CheckStateUnrecognized, "MOONWALKING"},
		{checkRunNode("unknown-conclusion", "COMPLETED", "REDECORATED"),
			forge.CheckStateUnrecognized, "COMPLETED/REDECORATED"},
		{checkRunNode("completed-with-no-conclusion", "COMPLETED", ""),
			forge.CheckStateUnrecognized, "COMPLETED/"},
		{statusContextNode("status-success", "SUCCESS"), forge.CheckStateSucceeded, ""},
		{statusContextNode("status-failure", "FAILURE"), forge.CheckStateFailed, ""},
		{statusContextNode("status-error", "ERROR"), forge.CheckStateFailed, ""},
		{statusContextNode("status-pending", "PENDING"), forge.CheckStatePending, ""},
		{statusContextNode("status-expected", "EXPECTED"), forge.CheckStatePending, ""},
		{statusContextNode("status-unknown", "MOONWALKING"), forge.CheckStateUnrecognized, "MOONWALKING"},
	}
	nodes := make([]string, 0, len(cases))
	for _, c := range cases {
		nodes = append(nodes, c.node)
	}
	h := newHarness(t, ghScript{"checks": {{Stdout: rollup(nodes...)}}})
	report, err := h.gh.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if len(report.Runs) != len(cases) {
		t.Fatalf("Checks returned %d runs, want %d", len(report.Runs), len(cases))
	}
	for i, c := range cases {
		got := report.Runs[i]
		if got.State != c.state {
			t.Errorf("run %d (%s): state is %v, want %v", i, got.Name, got.State, c.state)
		}
		if got.Reported != c.reported {
			t.Errorf("run %d (%s): reported is %q, want %q", i, got.Name, got.Reported, c.reported)
		}
		if got.Name == "" {
			t.Errorf("run %d carries no name", i)
		}
		if got.URL == "" {
			t.Errorf("run %d (%s) carries no URL for a person to look at", i, got.Name)
		}
	}
}

func TestARollupEntryOfAnUnknownKindKeepsTheCallerWaiting(t *testing.T) {
	node := `{"__typename":"SomethingElse","name":"build","state":"SUCCESS"}`
	h := newHarness(t, ghScript{"checks": {{Stdout: rollup(node)}}})
	report, err := h.gh.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if len(report.Runs) != 1 || report.Runs[0].State != forge.CheckStateUnrecognized {
		t.Fatalf("Checks returned %v, want one unrecognized run", report.Runs)
	}
	if got := report.Evaluate(forge.NoCIDeclaration{}); got.Verdict != forge.VerdictRunning {
		t.Errorf("verdict is %v, want running", got.Verdict)
	}
}

func TestACancelledCheckDoesNotLeaveTheRunWaiting(t *testing.T) {
	h := newHarness(t, ghScript{"checks": {{Stdout: rollup(
		checkRunNode("build", "COMPLETED", "CANCELLED"),
		checkRunNode("lint", "COMPLETED", "SUCCESS"),
	)}}})
	report, err := h.gh.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	got := report.Evaluate(forge.NoCIDeclaration{})
	if got.Verdict != forge.VerdictFailed {
		t.Fatalf("verdict is %v, want failed", got.Verdict)
	}
	if w := got.Waiting(); len(w) != 0 {
		t.Errorf("the run is waiting on %v", w)
	}
}

// The guard that refuses a check answer naming no commit has no test, and
// deliberately: gh emits every field a --json read asks for and headRefOid is
// not nullable, so there is no answer the real command could return that
// reaches it. Stating one here would be a test passing over a shape the
// mechanism cannot produce. Why the guard stays anyway is written where it is.

func TestGetRefusesAnAnswerAboutADifferentPullRequest(t *testing.T) {
	h := newHarness(t, ghScript{"view": {{Stdout: openPullRequestJSON}}})
	_, err := h.gh.Get(context.Background(), 13)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Get returned %v, want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonMalformed {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonMalformed)
	}
}

func TestAnUnrunnableProviderRefusesRatherThanReportingNothing(t *testing.T) {
	gh, err := forge.NewGitHub(testRedactor,
		forge.WithBinary(t.TempDir()+"/not-installed"),
		forge.WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	report, err := gh.Checks(context.Background(), 12)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Checks returned %v, want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonUnavailable {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonUnavailable)
	}
	if got := report.Evaluate(forge.NoCIDeclaration{}); got.Green() {
		t.Errorf("a refused read produced a green verdict: %v", got)
	}
}

func TestAnAuthenticationFailureIsNamed(t *testing.T) {
	h := newHarness(t, ghScript{"checks": {{
		Exit:   4,
		Stderr: "gh: To get started with GitHub CLI, please run:  gh auth login\n",
	}}})
	_, err := h.gh.Checks(context.Background(), 12)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Checks returned %v, want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonUnauthenticated {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonUnauthenticated)
	}
}

func TestAnOrdinaryFailureIsRejectedAndCarriesTheProviderMessage(t *testing.T) {
	h := newHarness(t, ghScript{"checks": {{Exit: 1, Stderr: "no pull requests found for branch\n"}}})
	_, err := h.gh.Checks(context.Background(), 12)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Checks returned %v, want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonRejected {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonRejected)
	}
	if !strings.Contains(refusal.Detail, "no pull requests found") {
		t.Errorf("the refusal dropped the provider's message: %q", refusal.Detail)
	}
}

// TestNoErrorPathCarriesACredential drives every route by which text this
// package did not write can reach an error: what the provider printed, what a
// decoder said about what the provider printed, and what a caller passed in.
func TestNoErrorPathCarriesACredential(t *testing.T) {
	cases := []struct {
		name string
		call func(*testing.T) error
	}{
		{
			name: "the provider's own message on a failure",
			call: func(t *testing.T) error {
				h := newHarness(t, ghScript{"checks": {{
					Exit:   1,
					Stderr: "fatal: could not read from " + secretURL + "\n",
				}}})
				_, err := h.gh.Checks(context.Background(), 12)
				return err
			},
		},
		{
			name: "the provider's own message on an authentication failure",
			call: func(t *testing.T) error {
				h := newHarness(t, ghScript{"view": {{
					Exit:   4,
					Stderr: "gh: bad credentials for " + secretURL + "\n",
				}}})
				_, err := h.gh.Get(context.Background(), 12)
				return err
			},
		},
		{
			name: "a head branch a caller passed",
			call: func(t *testing.T) error {
				h := newHarness(t, ghScript{})
				// The space is what the branch is refused for; the credential
				// is what must not come back in the refusal.
				_, _, err := h.gh.Find(context.Background(), secretURL+" and more")
				return err
			},
		},
		{
			name: "a title a caller passed",
			call: func(t *testing.T) error {
				h := newHarness(t, ghScript{})
				_, err := h.gh.Open(context.Background(), forge.OpenSpec{
					Head: "fm/work", Base: "main", Title: "clone of " + secretURL + "\nsecond line",
				})
				return err
			},
		},
		{
			name: "a repository specifier a caller passed",
			call: func(t *testing.T) error {
				_, err := forge.NewGitHub(testRedactor, forge.WithRepository(secretURL))
				return err
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.call(t)
			if err == nil {
				t.Fatalf("the call succeeded, so there is no error path to check")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error carries the credential: %v", err)
			}
		})
	}
}

// TestATruncatedProviderMessageDropsTheLineTheCutLandedIn drives a provider
// that says more than one invocation collects, with a credentialed URL placed
// so the bound falls inside its userinfo.
//
// A Redactor recognizes a userinfo by the "@" that ends it, so half of one is
// the shape it cannot match, and the token would reach the refusal if the
// fragment were kept. What keeps it out is that the line the cut landed in is
// dropped whole.
func TestATruncatedProviderMessageDropsTheLineTheCutLandedIn(t *testing.T) {
	// The bound on collected provider text is not exported, so this test says
	// where it believes the cut falls and then checks that it fell there. A
	// bound that moved fails that check rather than leaving a test that passes
	// without ever reaching the boundary.
	const collectedBound = 8 << 10
	// The credentialed URL begins this far before the bound, so the cut lands
	// inside the token rather than before or after it: the userinfo of
	// secretURL runs from its byte 8 to its byte 54.
	const intoTheURL = 40
	const opening = "gh: the provider began with this line\n"

	head := opening + strings.Repeat("x", collectedBound-intoTheURL-len(opening)-1) + "\n"
	written := head + secretURL + " could not be reached\n" +
		strings.Repeat("gh: and it went on talking after that\n", 20)

	h := newHarness(t, ghScript{"checks": {{Exit: 1, Stderr: written}}})
	_, err := h.gh.Checks(context.Background(), 12)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Checks returned %v, want a *Refusal", err)
	}

	if strings.Contains(refusal.Detail, secret[:8]) {
		t.Errorf("the refusal carries part of the credential: %q", tail(refusal.Detail))
	}
	if !strings.Contains(refusal.Detail, strings.TrimSuffix(opening, "\n")) {
		t.Errorf("the refusal dropped what the provider said before the cut: %q", tail(refusal.Detail))
	}
	// Everything before the last line of the detail is what was collected; the
	// last line is the marker saying it stopped short. Where that collected
	// text ends is what says the cut fell inside the credential rather than
	// somewhere the assertion above would pass without meaning anything.
	cut := strings.LastIndexByte(refusal.Detail, '\n')
	if cut < 0 {
		t.Fatalf("the detail carries no truncation marker, so nothing was cut: %q", tail(refusal.Detail))
	}
	if kept := refusal.Detail[:cut]; !strings.HasSuffix(kept, strings.TrimSuffix(head, "\n")) {
		t.Errorf("the cut did not land where this test places it, so the boundary was not exercised; %d bytes were kept, ending %q",
			len(kept), tail(kept))
	}
}

// tail returns the end of a message, for a failure report that should not
// print kilobytes.
func tail(s string) string {
	const n = 120
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// TestADecoderMessageIsRedacted proves the filter on the decoding path is not
// a filter over text that can never carry anything. A decoder quotes a numeric
// literal it could not fit into the field it was decoding into, so a provider
// answer carrying one puts provider bytes into the decoder's message, and the
// Redactor supplied at construction is what removes them.
func TestADecoderMessageIsRedacted(t *testing.T) {
	const literal = "99999999999999999999999999999999"
	redactor := vcs.RedactorFunc(func(s string) string {
		return strings.ReplaceAll(s, literal, "REMOVED")
	})
	dir := t.TempDir()
	writeScript(t, dir, ghScript{"view": {{Stdout: `{"number":` + literal + `,"state":"OPEN"}`}}})
	gh, err := forge.NewGitHub(redactor,
		forge.WithBinary(os.Args[0]),
		forge.WithBaseEnvironment([]string{fakeGHDir + "=" + dir}),
		forge.WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	_, err = gh.Get(context.Background(), 12)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Get returned %v, want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonMalformed {
		t.Fatalf("reason is %q, want %q", refusal.Reason, forge.ReasonMalformed)
	}
	if !strings.Contains(refusal.Detail, "REMOVED") {
		t.Fatalf("the decoder did not quote the provider's bytes, so this test proves nothing: %q", refusal.Detail)
	}
	if strings.Contains(err.Error(), literal) {
		t.Errorf("the decoder's message reached the caller unfiltered: %v", err)
	}
}

func TestSubmitOpensOnceAndThenUpdatesTheBody(t *testing.T) {
	h := newHarness(t, ghScript{
		// The first list read is the one Submit makes before deciding, and it
		// finds nothing. Every later one is the read that follows the create.
		"list":   {{Stdout: noneListedJSON}, {Stdout: oneListedJSON}},
		"create": {{Stdout: createdURL}},
		"view":   {{Stdout: openPullRequestJSON}},
		"edit":   {{}},
	})
	spec := forge.OpenSpec{
		Head:  "fm/work",
		Base:  "main",
		Title: "feat: a change",
		Body:  "what changed, what was checked, what the risks are",
	}
	pr, err := forge.Submit(context.Background(), h.gh, spec)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if pr.Number != 12 {
		t.Errorf("Submit returned pull request %d, want 12", pr.Number)
	}
	created := h.callsFor("create")
	if len(created) != 1 {
		t.Fatalf("Submit made %d create calls, want 1", len(created))
	}
	if created[0].Stdin != spec.Body {
		t.Errorf("the body reached the provider as %q, want %q", created[0].Stdin, spec.Body)
	}
	if !created[0].hasArg("--body-file=-") {
		t.Errorf("the body did not travel on standard input: %v", created[0].Args)
	}

	second := forge.OpenSpec{Head: "fm/work", Base: "main", Title: "feat: a change", Body: "a second round"}
	if _, err := forge.Submit(context.Background(), h.gh, second); err != nil {
		t.Fatalf("Submit again: %v", err)
	}
	if n := len(h.callsFor("create")); n != 1 {
		t.Errorf("Submit opened a second pull request: %d create calls", n)
	}
	edits := h.callsFor("edit")
	if len(edits) != 1 {
		t.Fatalf("Submit made %d edit calls, want 1", len(edits))
	}
	if edits[0].Stdin != second.Body {
		t.Errorf("the new body reached the provider as %q, want %q", edits[0].Stdin, second.Body)
	}
}

func TestOpenRefusesWhenTheProviderThenReportsNoPullRequest(t *testing.T) {
	h := newHarness(t, ghScript{
		"create": {{Stdout: createdURL}},
		"list":   {{Stdout: noneListedJSON}},
	})
	_, err := h.gh.Open(context.Background(), forge.OpenSpec{
		Head: "fm/work", Base: "main", Title: "feat: a change",
	})
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Open returned %v, want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonMalformed {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonMalformed)
	}
}

func TestArgumentsAreRefusedBeforeTheProviderIsInvoked(t *testing.T) {
	cases := []struct {
		name string
		call func(*harness) error
	}{
		{"an empty head branch", func(h *harness) error {
			_, _, err := h.gh.Find(context.Background(), "")
			return err
		}},
		{"a head branch that would be read as an option", func(h *harness) error {
			_, _, err := h.gh.Find(context.Background(), "--repo=elsewhere")
			return err
		}},
		{"a head branch carrying a newline", func(h *harness) error {
			_, _, err := h.gh.Find(context.Background(), "fm/work\nmore")
			return err
		}},
		{"a pull request number of zero", func(h *harness) error {
			_, err := h.gh.Get(context.Background(), 0)
			return err
		}},
		{"a negative pull request number", func(h *harness) error {
			_, err := h.gh.Checks(context.Background(), -1)
			return err
		}},
		{"a title carrying a newline", func(h *harness) error {
			_, err := h.gh.Open(context.Background(), forge.OpenSpec{
				Head: "fm/work", Base: "main", Title: "feat: one\nfeat: two",
			})
			return err
		}},
		{"an empty title", func(h *harness) error {
			_, err := h.gh.Open(context.Background(), forge.OpenSpec{Head: "fm/work", Base: "main"})
			return err
		}},
		{"a body carrying a null byte", func(h *harness) error {
			return errBody(h)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, ghScript{})
			err := c.call(h)
			if !errors.Is(err, forge.ErrInvalidArgument) {
				t.Fatalf("the call returned %v, want ErrInvalidArgument", err)
			}
			if n := len(h.calls()); n != 0 {
				t.Errorf("the provider was invoked %d times for a refused argument", n)
			}
		})
	}
}

func errBody(h *harness) error {
	_, err := h.gh.UpdateBody(context.Background(), 12, "a body\x00with a null")
	return err
}

func TestNewGitHubRefusesARepositoryItCannotAddressSafely(t *testing.T) {
	cases := []string{
		secretURL,
		"https://github.com/owner/name",
		"owner",
		"owner/name/extra",
		"-owner/name",
		"owner/-name",
		"owner/",
		"/name",
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			if _, err := forge.NewGitHub(testRedactor, forge.WithRepository(spec)); !errors.Is(err, forge.ErrInvalidArgument) {
				t.Errorf("NewGitHub accepted %q, returning %v", spec, err)
			}
		})
	}
	if _, err := forge.NewGitHub(testRedactor, forge.WithRepository("owner/name")); err != nil {
		t.Errorf("NewGitHub refused owner/name: %v", err)
	}
}

func TestNewGitHubNeedsSomethingToAddress(t *testing.T) {
	if _, err := forge.NewGitHub(testRedactor); !errors.Is(err, forge.ErrInvalidArgument) {
		t.Errorf("NewGitHub accepted an adapter with nothing to address, returning %v", err)
	}
}

func TestNewGitHubRequiresARedactor(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Errorf("NewGitHub accepted a nil Redactor")
		}
	}()
	_, _ = forge.NewGitHub(nil, forge.WithRepository("owner/name"))
}

func TestTheRepositoryIsNamedOnEveryCall(t *testing.T) {
	h := newHarness(t, ghScript{"checks": {{Stdout: rollup()}}}, forge.WithRepository("owner/name"))
	if _, err := h.gh.Checks(context.Background(), 12); err != nil {
		t.Fatalf("Checks: %v", err)
	}
	calls := h.calls()
	if len(calls) != 1 {
		t.Fatalf("the adapter made %d calls, want 1", len(calls))
	}
	if !calls[0].hasArg("--repo=owner/name") {
		t.Errorf("the call does not name the repository: %v", calls[0].Args)
	}
}

func TestACallTheContextEndsIsARefusal(t *testing.T) {
	h := newHarness(t, ghScript{"checks": {{Stdout: rollup(), SleepMs: 30_000}}})
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	_, err := h.gh.Checks(ctx, 12)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Checks returned %v, want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonUnavailable {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonUnavailable)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the refusal does not match the context's error: %v", err)
	}
}

// A provider that exits while a descendant still holds its output pipes must
// not hold the call. Killing the provider does not close a pipe a descendant
// inherited, and nothing here pursues that descendant, so what releases the
// call is the grace deadline. What it collected under that deadline is refused
// rather than reported, because it may be short of what the provider wrote.
func TestAProviderWhoseOutputIsHeldOpenIsReleasedRatherThanWaitedOn(t *testing.T) {
	answer := rollup(checkRunNode("build", "COMPLETED", "SUCCESS"))
	h := newHarness(t,
		ghScript{"checks": {{Stdout: answer, HoldPipes: true}}},
		forge.WithProviderGrace(100*time.Millisecond))

	start := time.Now()
	_, err := h.gh.Checks(context.Background(), 12)
	elapsed := time.Since(start)

	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Checks returned %v, want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonUnavailable {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonUnavailable)
	}
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Errorf("the refusal does not name the wait that was abandoned: %v", err)
	}
	// The descendant holds the pipes for fakeGHHoldFor, and without the
	// deadline this call would wait all of it and then report the output as a
	// whole answer.
	if elapsed >= fakeGHHoldFor {
		t.Errorf("Checks waited %v, as long as the descendant held the pipes", elapsed)
	}

	// The accepting path through the same stand-in and the same grace period:
	// an invocation with nothing holding its pipes answers.
	plain := newHarness(t,
		ghScript{"checks": {{Stdout: answer}}},
		forge.WithProviderGrace(100*time.Millisecond))
	if _, err := plain.gh.Checks(context.Background(), 12); err != nil {
		t.Errorf("Checks against a provider that held nothing open: %v", err)
	}
}

// TestOutputPastTheBoundIsDiscardedRatherThanRead also proves the answer is
// not silently reduced to an unreadable one: an over-bound answer has its own
// reason, so a caller can tell "the provider said too much" from "the provider
// said something I cannot read" and knows that reading again at the same bound
// will not help.
func TestOutputPastTheBoundIsDiscardedRatherThanRead(t *testing.T) {
	h := newHarness(t,
		ghScript{"checks": {{Stdout: rollup(checkRunNode("build", "COMPLETED", "SUCCESS"))}}},
		forge.WithMaxOutput(32))
	_, err := h.gh.Checks(context.Background(), 12)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Checks returned %v, want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonOversizeAnswer {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonOversizeAnswer)
	}
}

func TestTheAdapterSatisfiesTheProviderInterface(t *testing.T) {
	h := newHarness(t, ghScript{"checks": {{Stdout: rollup()}}})
	var p forge.Provider = h.gh
	if _, err := p.Checks(context.Background(), 12); err != nil {
		t.Fatalf("Checks through the interface: %v", err)
	}
}

// TestTheRedactorIsTheOneSuppliedAtConstruction proves the seam is used rather
// than a redactor of this package's own: a Redactor that removes a word this
// package could not know about must remove it from a refusal.
func TestTheRedactorIsTheOneSuppliedAtConstruction(t *testing.T) {
	redactor := vcs.RedactorFunc(func(s string) string {
		return strings.ReplaceAll(s, "hunter2", "REMOVED")
	})
	dir := t.TempDir()
	writeScript(t, dir, ghScript{"checks": {{Exit: 1, Stderr: "the password is hunter2\n"}}})
	gh, err := forge.NewGitHub(redactor,
		forge.WithBinary(os.Args[0]),
		forge.WithBaseEnvironment([]string{fakeGHDir + "=" + dir}),
		forge.WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	_, err = gh.Checks(context.Background(), 12)
	if err == nil {
		t.Fatalf("Checks succeeded over a failing provider")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the supplied Redactor was not applied: %v", err)
	}
	if !strings.Contains(err.Error(), "REMOVED") {
		t.Errorf("the refusal does not carry the redacted message: %v", err)
	}
}
