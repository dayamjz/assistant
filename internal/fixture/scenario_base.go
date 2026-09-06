package fixture

import (
	"path/filepath"
	"strings"
)

// buildBase builds the scenario carrying every condition a run can meet while
// still reaching the end of the pipeline: the four stage plants, the two
// trust-boundary plants on the branch, the agent output P3 is about, and the
// empty check list the code host answers with.
func buildBase(b *builder) (*Scenario, []Condition, error) {
	s, err := b.newScenario(ScenarioBase,
		"Every condition a run can meet without the run being stopped before the stage that meets it.")
	if err != nil {
		return nil, nil, err
	}
	if err := b.initSubject(s); err != nil {
		return nil, nil, err
	}
	if err := b.startBranch(s); err != nil {
		return nil, nil, err
	}

	var conditions []Condition
	for _, plant := range []func(*builder, *Scenario) ([]Condition, error){
		plantLogicBugAndFailingTest,
		plantStaleDocumentation,
		plantLintViolation,
		plantPushedCommandsAndAgent,
		plantHarnessInstallation,
		plantFindingsWithoutAction,
		plantEmptyCheckList,
	} {
		planted, err := plant(b, s)
		if err != nil {
			return nil, nil, err
		}
		conditions = append(conditions, planted...)
	}
	if err := b.pushBranch(s); err != nil {
		return nil, nil, err
	}
	// The installer's other half is applied after the build's own push. Set
	// before it, the planted pre-push hook fires on that push, and the
	// tripwire file then reports the build rather than the run it was planted
	// to watch. What it produces belongs to the harness-installation condition,
	// which already claims the two .githooks scripts; it is not a second
	// condition and it is not the gap internal/gate/doc.go names, which is
	// about a configuration file redirecting the gate's own hooks and is
	// planted in the hostile-template scenario.
	if _, err := b.git.run(s.WorkingCopy, "config", "core.hooksPath", ".githooks"); err != nil {
		return nil, nil, err
	}
	return s, conditions, nil
}

// plantLogicBugAndFailingTest moves the loop bound in Total by one. One edit
// produces two conditions, and they are recorded separately because they are
// answered by different stages: review reads the code against the doc comment
// above it, and the test stage reads the failure the toolchain reports.
func plantLogicBugAndFailingTest(b *builder, s *Scenario) ([]Condition, error) {
	if err := writeFile(s.WorkingCopy, "total.go", 0o644, subjectTotalGoBuggy); err != nil {
		return nil, err
	}
	commit, err := b.git.commitAll(s.WorkingCopy, "narrow the Total loop bound")
	if err != nil {
		return nil, err
	}
	s.Commits["logic-bug"] = commit

	return []Condition{
		{
			ID:       "stage-logic-bug",
			Scenario: s.Name,
			Kind:     KindStage,
			Planted: "total.go on the branch loops to len(xs)-1 while its own doc comment above it still says " +
				"every element, so the defect is visible from the diff alone and does not need the test to " +
				"be read.",
			Mechanism: "the review stage, over the diff between the default branch and the branch",
			Expect: Outcome{
				Summary: "Review reports one finding against total.go naming the loop bound, with action fix: " +
					"the code contradicts the contract stated beside it, which is a mechanical fix and not a " +
					"question of intent.",
				Value:           "findings.ActionFix",
				MessageContains: []string{"total.go"},
			},
		},
		{
			ID:       "stage-failing-test",
			Scenario: s.Name,
			Kind:     KindStage,
			Planted: "The same edit breaks TestTotalSumsEveryElement, which was passing on the default branch. " +
				"It is the only failing test in the scenario, so a stage reporting more than one has found " +
				"something this fixture did not plant.",
			Mechanism: "the test stage, running the trusted commands.test",
			Expect: Outcome{
				Summary: "The trusted test command exits non-zero and the stage reports the failure naming " +
					"the test.",
				MessageContains: []string{"TestTotalSumsEveryElement", "Total([1 2 3]) = 3, want 6"},
			},
		},
	}, nil
}

// plantStaleDocumentation changes the default unit deliberately and leaves the
// documentation saying what it used to be. It is a change of intent rather
// than a defect, so the correct answer is to update the document; the test
// that asserted the old default is updated in the same commit, which is what
// keeps the failing-test condition unambiguous.
func plantStaleDocumentation(b *builder, s *Scenario) ([]Condition, error) {
	if err := writeFile(s.WorkingCopy, "total.go", 0o644, subjectTotalGoBuggyShortUnit); err != nil {
		return nil, err
	}
	if err := writeFile(s.WorkingCopy, "total_test.go", 0o644, subjectTotalTestGoShortUnit); err != nil {
		return nil, err
	}
	commit, err := b.git.commitAll(s.WorkingCopy, "render the default unit as B")
	if err != nil {
		return nil, err
	}
	s.Commits["stale-documentation"] = commit

	return []Condition{{
		ID:       "stage-stale-documentation",
		Scenario: s.Name,
		Kind:     KindStage,
		Planted: "Format's default unit changes from \"bytes\" to \"B\" on the branch. docs/behavior.md still " +
			"says bytes and is not touched by the commit, so the document is made stale by a change rather " +
			"than being wrong when it was written.",
		Mechanism: "the document stage, over the diff and the documents the change made stale",
		Expect: Outcome{
			Summary: "The document stage reports docs/behavior.md as made stale by the change and the fix is " +
				"to the document, not to the code: the change of default was deliberate.",
			Value:           "findings.ActionFix",
			MessageContains: []string{"docs/behavior.md"},
		},
	}}, nil
}

// plantLintViolation adds a call to context.WithCancel whose cancel function
// is discarded, which is the lostcancel check. `go vet` names the call and the
// function that goes unused, so the expected message is the toolchain's rather
// than this package's invention.
//
// The check is chosen for what `go test` does not run. A violation inside the
// vet subset `go test` runs for itself fails the build of the test binary, and
// the failing-test condition planted in the same scenario is then never
// observed, so a printf verb that does not match its argument would hide the
// condition beside it rather than sit alongside it.
func plantLintViolation(b *builder, s *Scenario) ([]Condition, error) {
	if err := writeFile(s.WorkingCopy, "report.go", 0o644, subjectReportGo); err != nil {
		return nil, err
	}
	commit, err := b.git.commitAll(s.WorkingCopy, "add Report")
	if err != nil {
		return nil, err
	}
	s.Commits["lint-violation"] = commit

	return []Condition{{
		ID:       "stage-lint-violation",
		Scenario: s.Name,
		Kind:     KindStage,
		Planted: "report.go discards the cancel function context.WithCancel returns, which the trusted " +
			"commands.lint reports. The check is one `go test` does not run for itself, so this condition " +
			"and the failing-test condition can both be observed in one run rather than the first hiding " +
			"the second.",
		Mechanism: "the lint stage, running the trusted commands.lint",
		Expect: Outcome{
			Summary: "The trusted lint command exits non-zero and the stage reports the violation naming " +
				"report.go.",
			MessageContains: []string{"report.go", "the cancel function returned by context.WithCancel"},
		},
	}}, nil
}

// plantPushedCommandsAndAgent rewrites the repository configuration document
// on the branch so that it sets the two keys a pushed branch may not set. The
// document is committed and pushed, which is the route P7 is about: a document
// written into a checkout would never reach the branch a run reads.
//
// The values it sets are tripwires rather than plausible commands. A condition
// whose failure produces a plausible test run is a condition whose failure is
// invisible.
func plantPushedCommandsAndAgent(b *builder, s *Scenario) ([]Condition, error) {
	const scriptPath = ".fixture/pushed-test-command.sh"
	if err := b.git.writeExecutable(s.WorkingCopy, scriptPath,
		tripwireScript("pushed-commands-test", s.Tripwire,
			"The commands.test a pushed branch asked for.")); err != nil {
		return nil, err
	}
	pushed := `{
  "commands": {
    "test": "sh ` + scriptPath + `",
    "lint": "sh ` + scriptPath + `"
  },
  "agent": "fixture-pushed-agent",
  "ignore_patterns": ["vendor/**"]
}
`
	if err := writeFile(s.WorkingCopy, ConfigPath, 0o644, pushed); err != nil {
		return nil, err
	}
	commit, err := b.git.commitAll(s.WorkingCopy, "point the commands and the agent at the branch's own script")
	if err != nil {
		return nil, err
	}
	s.Commits["pushed-commands"] = commit

	return []Condition{{
		ID:        "refusal-pushed-commands-and-agent",
		Scenario:  s.Name,
		Kind:      KindRefusal,
		Principle: "P7",
		Planted: "The branch commits a configuration document setting commands.test, commands.lint, and " +
			"agent, and pushes it. The default branch's document sets all three to something else, so " +
			"which copy was read is observable rather than inferred. ignore_patterns is set alongside them " +
			"and is a key a pushed branch may set, so a run that dropped the whole document rather than the " +
			"three keys is distinguishable from one that applied the trust classes.",
		Mechanism: "config.Resolve over the operator's global layer and the pushed layer",
		Expect: Outcome{
			Summary: "Each of the three keys is dropped and reported as a config.Rejection rather than " +
				"silently ignored, and the resolved ignore_patterns are the branch's, because that is a " +
				"key a pushed branch may set. Nothing the branch named is executed.\n\n" +
				"What the resolved commands and agent then are is not this call's answer. Resolve takes " +
				"one repository layer, so a call handed the pushed document has not read the trusted one, " +
				"and the trusted values the default branch carries are recorded against whoever composes " +
				"the two; see question-trusted-and-pushed-composition.",
			MessageContains: []string{
				"commands.test is trusted-unless-opted-out and was set from the pushed layer",
				"commands.lint is trusted-unless-opted-out and was set from the pushed layer",
				"agent is trusted-unless-opted-out and was set from the pushed layer",
			},
			TripwiresQuiet: []string{"pushed-commands-test"},
		},
	}}, nil
}

// findingsLocationPath is the path the P3 findings locate themselves in, and
// the whole of the evidence set the review-path variants declare. It is a path
// this scenario's change touches, so a run's own demand names it too, which is
// what keeps the evidence binding from deciding anything about these
// conditions.
const findingsLocationPath = "total.go"

// plantFindingsWithoutAction writes the agent output P3 is about. It is the
// bytes an agent prints, not a constructed report: the path P3 lives on runs
// from the agent's text through a parser in findings, and a fixture that
// handed a caller an assembled Finding would skip every step of it.
//
// Three shapes are planted because P3 has three ways in, and the one most
// likely to be handled and the two most likely to be forgotten are not the
// same shape. Each is planted twice, because the two entry points a report can
// arrive through are different rules and P3 has to hold under both:
//
//   - findings.ParseReport, the entry point that holds a report to no evidence
//     rule. Those bytes state no revision and no read set, which is what a
//     stage that is not a review prints.
//   - findings.ParseReviewReport, which a review stage's output goes through.
//     It applies PRD section 5's evidence binding first, and the binding is
//     what decides whether the action is reached at all: a report stating a
//     revision the run did not ask about is refused whole with
//     ErrWrongRevision, and a finding naming a path the declared evidence set
//     does not hold is refused for want of evidence and becomes a note. The
//     review-path bytes therefore carry a revision and a read set chosen so
//     that neither refusal fires and the action is the only thing left to
//     decide the outcome.
//
// The review path is where P3 earns its keep, because it is the path that
// carries findings in a real run. The findings.ParseReport variants are kept
// because a stage that is not the review reports through the entry point that
// holds a report to no evidence rule, and a report it prints states no
// revision by design.
//
// The review-path bytes name a revision, and the one they name is
// Commits["branch-head"] as the build left it, registered here and written by
// pushBranch from that value for the same reason the empty check list's answer
// is. It is a placeholder on the same terms: the run rebases the branch and
// may add fix commits, so by the review stage the commit the run asks about is
// not the one recorded here, and a harness has to substitute it before serving
// the bytes. Serving them unchanged reaches ErrWrongRevision, which is the
// review path's own rule firing and not the condition planted here. How the
// substitution is made is the harness's; see question-agent-response-delivery.
func plantFindingsWithoutAction(b *builder, s *Scenario) ([]Condition, error) {
	responses := []struct {
		id      ID
		file    string
		missing string
		body    string
		planted string
	}{
		{
			id:      "refusal-finding-action-missing",
			file:    "review-action-missing",
			missing: "no action field at all",
			body:    `{"id": "total-loop-bound", "severity": "error", "location": {"path": "` + findingsLocationPath + `", "line": 10}, "description": "The loop stops one element short."}`,
			planted: "a finding object carrying no action field",
		},
		{
			id:      "refusal-finding-action-empty",
			file:    "review-action-empty",
			missing: "an empty action",
			body:    `{"id": "total-loop-bound", "severity": "error", "action": "", "location": {"path": "` + findingsLocationPath + `", "line": 10}, "description": "The loop stops one element short."}`,
			planted: "a finding whose action is the empty string",
		},
		{
			id:      "refusal-finding-action-unrecognized",
			file:    "review-action-unrecognized",
			missing: "an unrecognized action",
			body:    `{"id": "total-loop-bound", "severity": "error", "action": "autofix", "location": {"path": "` + findingsLocationPath + `", "line": 10}, "description": "The loop stops one element short."}`,
			planted: "a finding whose action is a word this product does not define",
		},
	}

	expect := func(missing string) Outcome {
		return Outcome{
			Summary: "The report parses, the finding survives, and its action is ask. It is not " +
				"fix-eligible and never enters the automatic fix loop; it holds for a person. " +
				"A run that reported " + missing + " as an error, or dropped the finding, has the " +
				"wrong answer: this is defined behavior, not an error path.",
			Value: "findings.ActionAsk",
		}
	}

	// expectOnTheReviewPath is the same answer reached through the entry point
	// that binds evidence, which produces one finding the other does not. The
	// review path appends an informational note to every report it binds, so a
	// harness holding these bytes to an expectation naming only the reviewer's
	// finding would read the correct answer as a mismatch. The note is what the
	// review path reports about the evidence set rather than a judgement of it,
	// and it is recorded here because this package records what a condition
	// must produce, not only the part of it the condition is about.
	//
	// The recorded substrings are held to what the planted bytes settle for
	// every demand a run here can put to them, which is that the review
	// declared reading and that everything it declared is touched by the
	// change. What the note says about paths left undeclared turns on the
	// demand rather than on these bytes, so recording it would be a substring
	// that fails on a correct run, and the answer states the dependency instead
	// of pinning it.
	expectOnTheReviewPath := func(missing string) Outcome {
		out := expect(missing)
		out.Summary += " The bound report carries one finding more than the reviewer wrote: " +
			"the review path appends an informational note to every report it binds, stating " +
			"what the review declared reading against what the change touched. Here it reports " +
			"a read set holding nothing beyond the change and paths the change touched that the " +
			"review did not declare, both of which the binding permits and neither of which is " +
			"the condition under test. Two findings is the answer, not a mismatch. The recorded " +
			"substrings are carried by that appended note and not by the reviewer's finding, " +
			"which is the one this answer's value is about; whether the note also reports paths " +
			"left undeclared is decided by the demand the run makes and not by these bytes, so " +
			"it is not recorded as a substring. This holds only if the report names the revision " +
			"the run asked the review stage about; a harness that served the build-time revision " +
			"has met findings.ErrWrongRevision and has not reached this condition."
		out.MessageContains = []string{
			"Evidence: the review declared reading",
			"all of them touched by this change",
		}
		return out
	}

	var conditions []Condition
	for _, r := range responses {
		file := r.file + ".txt"
		if err := writeFile(s.Root, "agent-responses/"+file, 0o644, findingsReport(r.body, "", nil)); err != nil {
			return nil, err
		}
		s.AgentResponses[string(r.id)] = filepath.Join(s.Root, "agent-responses", file)
		conditions = append(conditions, Condition{
			ID:        r.id,
			Scenario:  s.Name,
			Kind:      KindRefusal,
			Principle: "P3",
			Planted: "The exact bytes an agent prints, carrying " + r.planted + ". The report is " +
				"otherwise well formed and the finding is otherwise complete, so nothing but the action " +
				"decides the outcome. The route is findings.ParseReport, the entry point that holds a " +
				"report to no evidence rule, which is what the stages that are not a review report " +
				"through. These bytes state no revision and no read set, so they are not the review " +
				"path's condition and cannot be served to it: findings.ParseReviewReport refuses a " +
				"report stating no revision with ErrWrongRevision before any finding is reached. The " +
				"same shape for that path is " + string(r.id) + "-review-path.",
			Mechanism: "findings.ParseReport, then Report.Normalize",
			Expect:    expect(r.missing),
		})

		reviewID := r.id + "-review-path"
		reviewFile := r.file + "-review-path.txt"
		body := r.body
		b.afterBranchHead(s, "the review-path bytes for "+string(reviewID), func(head string) error {
			return writeFile(s.Root, "agent-responses/"+reviewFile, 0o644,
				findingsReport(body, head, []string{findingsLocationPath}))
		})
		s.AgentResponses[string(reviewID)] = filepath.Join(s.Root, "agent-responses", reviewFile)
		conditions = append(conditions, Condition{
			ID:        reviewID,
			Scenario:  s.Name,
			Kind:      KindRefusal,
			Principle: "P3",
			Planted: "The exact bytes a reviewing agent prints, carrying " + r.planted + ", on the " +
				"route a review stage's output actually takes. The report states a revision and " +
				"declares reading " + findingsLocationPath + ", which is the one path the finding " +
				"locates itself in and a path this scenario's change touches, so PRD section 5's " +
				"evidence binding refuses nothing: the revision matches what the run asked about, the " +
				"finding names a path the evidence set holds, and the binding's demotion to a note " +
				"does not reach it on either half of its test, since that demotion asks whether a " +
				"finding named no path and whether the reviewer stated an action, and this finding " +
				"named one and stated none. Nothing but the action is " +
				"left to decide the outcome, which is the same condition as " + string(r.id) + " with " +
				"the review path's rule satisfied rather than avoided.\n\n" +
				"The revision is a placeholder. It is Commits[\"branch-head\"] as the build left it, " +
				"and a harness has to replace it with the commit the run asked the review stage " +
				"about: the run rebases the branch and, with fix_rounds.review set in the trusted " +
				"document, may add fix commits, so by the review stage the head has moved. Served " +
				"unchanged, the report names a commit the run did not ask about and " +
				"findings.ParseReviewReport refuses it whole with ErrWrongRevision, which is that " +
				"path's own rule firing and not this condition. How the substitution is made is the " +
				"harness's; see question-agent-response-delivery.",
			Mechanism: "findings.ParseReviewReport, then Report.Normalize",
			Expect:    expectOnTheReviewPath(r.missing),
		})
	}
	return conditions, nil
}

// findingsReport renders the exact bytes an agent prints around one finding:
// the prose an agent writes before the report and the fenced JSON object the
// parser scans for. revision and read are omitted when empty, which is what a
// report answering to no evidence rule states.
func findingsReport(finding, revision string, read []string) string {
	var b strings.Builder
	b.WriteString("I read the diff against the intent and found one thing.\n\n")
	b.WriteString("```json\n{\n")
	b.WriteString(`  "summary": "One finding on the change to Total.",` + "\n")
	b.WriteString(`  "risk": "medium",` + "\n")
	if revision != "" {
		b.WriteString(`  "revision": "` + revision + `",` + "\n")
	}
	if len(read) > 0 {
		b.WriteString(`  "read": ["` + strings.Join(read, `", "`) + `"],` + "\n")
	}
	b.WriteString(`  "findings": [` + "\n")
	b.WriteString("    " + finding + "\n")
	b.WriteString("  ]\n}\n```\n")
	return b.String()
}

// plantEmptyCheckList writes the bytes the code host's provider command prints
// for a head with no check registered, and leaves the configuration without a
// no-CI declaration. The empty list is the answer; what it means is the
// condition.
//
// The answer names a head, and the head it names is the one pushBranch records
// as Commits["branch-head"], because the write is registered here and made
// there from that value. Reading the head here instead would agree only while
// nothing committed between this plant and the push, and the catalog tells a
// harness to look for the recorded head in these bytes.
func plantEmptyCheckList(b *builder, s *Scenario) ([]Condition, error) {
	const file = "checks-empty.json"
	b.afterBranchHead(s, "the empty check list's answer", func(head string) error {
		return writeFile(s.Root, "provider-responses/"+file, 0o644,
			`{"headRefOid":"`+head+`","statusCheckRollup":[]}`+"\n")
	})
	s.ProviderResponses["checks-empty"] = filepath.Join(s.Root, "provider-responses", file)

	return []Condition{{
		ID:       "refusal-no-registered-checks",
		Scenario: s.Name,
		Kind:     KindRefusal,
		Planted: "The provider answers the checks read with an empty rollup on a named head, which is what a " +
			"repository with nothing registered looks like on the wire. Neither the trusted document nor " +
			"the branch's sets no_ci, so nothing declares that this repository has no checks.\n\n" +
			"The head in the answer is a placeholder. It is Commits[\"branch-head\"] as the build left it, " +
			"and the harness has to replace it with the commit the run actually pushed before serving the " +
			"answer: the run rebases the branch and, with fix_rounds.review set in the trusted document, " +
			"may add fix commits, so by the checks stage the head has moved. Served unchanged, the answer " +
			"names a commit that is not the one under test, and forge.PullRequest.HeadCommit exists so a " +
			"caller can tell exactly that apart from an empty list, so the run would be reading a stale " +
			"check list rather than the condition planted here. How the substitution is made is the " +
			"harness's; see question-provider-response-delivery.",
		Mechanism: "forge.ChecksReport.Evaluate with forge.DeclaredNoCI over the resolved configuration",
		Expect: Outcome{
			Summary: "Evaluate returns VerdictNoChecks, which is not green and not a failure. An empty " +
				"list means unregistered, and only the no_ci declaration turns it into a pass. What the " +
				"run then does with that verdict is the caller's, and internal/forge says what it owes: " +
				"the caller waits, bounded by checks_timeout, and never reports the checks as passed. " +
				"This holds only if the answer names the head the run pushed; a harness that served the " +
				"build-time head has observed a stale check list and has not reached this condition.",
			Value:           "forge.VerdictNoChecks",
			MessageContains: []string{"no-checks"},
		},
	}}, nil
}
