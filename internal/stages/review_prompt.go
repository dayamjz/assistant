package stages

import (
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
)

// reviewPrompt assembles what the reviewer is asked. Every section is derived
// from the run or from the resolved configuration, and the two sections whose
// text is a rule the answer is checked against - the evidence demand and the
// scope lens - are written by the packages that do the checking rather than
// restated here, so a reviewer is never held to a rule nobody stated.
//
// lens is empty when the scope lens does not apply to this run, which is what
// scope.Guidance refuses for a run carrying no intent. The section is then
// absent rather than replaced by a weaker one, and observeScope reports why in
// the stage's own report.
//
// It is one string rather than a template so that what reaches the agent is
// what a reader of this function sees. The order is deliberate: what the
// reviewer is, what it is looking at, what the change was for, the two rules
// its answer is checked against, the repository's own extra rules, what a
// previous fix round claimed, the shape of the answer, and last the diff,
// which is the only unbounded part.
func reviewPrompt(cfg config.Config, f reviewFacts, change reviewChangeSet, evidence, lens string) string {
	var b strings.Builder
	section := func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimRight(text, "\n"))
	}

	section(reviewRole())
	section(reviewSubject(f, change))
	section(reviewIntent(f))
	section(lens)
	section(evidence)
	section(reviewRules(cfg, change.touched))
	section(reviewPriorRound(f))
	section(reviewAnswerShape(lens != ""))
	section("The change, as a unified diff from " + change.from + " to " + f.head + ":\n\n" + change.diff)
	return b.String()
}

// reviewRole says what the reviewer is for. It is the one section that is the
// same on every run.
//
// It tells the reviewer not to change anything, because this stage runs the
// agent with no fix role and a repaired working copy would be a change nobody
// reviewed. It does not claim the agent is prevented from editing: nothing
// here prevents it, the isolated copy is the invocation's working directory,
// and PRD section 5's separation of roles is arranged by what the pipeline
// does with the answer rather than by what the agent can reach.
func reviewRole() string {
	return "You are reviewing a change to this repository, independently and adversarially. " +
		"You are the stage that decides whether this change is good, and the stages after you " +
		"act on what you report.\n\n" +
		"Read the change and report what you find. Do not edit anything: this is a review, and " +
		"a repair made here is a change nobody reviewed. Report what is wrong instead, and let " +
		"the fix round that follows make the edit."
}

// reviewSubject says what is being reviewed: the branch, the commit, where the
// change is measured from, and which of its paths are outside review.
func reviewSubject(f reviewFacts, change reviewChangeSet) string {
	var b strings.Builder
	b.WriteString("The change under review is commit " + f.head + " on branch " + f.branch +
		", measured against " + change.from + ", which is where this branch left " + f.base + ".\n")
	b.WriteString("It touches " + strconv.Itoa(len(change.touched)) + " path(s) that are in scope for review.")
	if len(change.excluded) > 0 {
		b.WriteString("\n\nThe repository's configuration excludes " + strconv.Itoa(len(change.excluded)) +
			" further path(s) from review: " + strings.Join(change.excluded, ", ") + ". " +
			"The diff below is the whole change and still shows them. Do not report findings " +
			"about them; they are not part of what this review covers.")
	}
	return b.String()
}

// reviewIntent frames what the change set out to do, in the three states a run
// carries it in. The framing is PRD section 5's: supplied intent is acceptance
// criteria the change answers to, and an intent that was not supplied is a
// low-confidence hint the change is not held to, so departing from it is not
// by itself a defect.
//
// A run with no intent is told so plainly. The alternative is a reviewer that
// invents criteria and then reports the change for missing them, which is the
// failure that makes a hint read as a requirement.
func reviewIntent(f reviewFacts) string {
	switch {
	case f.intent != "" && f.supplied:
		return "Intent, supplied by a person as the acceptance criteria this change answers to:\n\n" +
			f.intent + "\n\nHold the change to this. A change that does not meet it is a finding."
	case f.intent != "":
		return "Intent, offered as a hint rather than stated as acceptance criteria:\n\n" +
			f.intent + "\n\nThe change is not held to this. Departing from it is not by itself a " +
			"defect, so do not report a departure as one; judge the change on what it does."
	default:
		return "No intent was recorded for this run, so there are no acceptance criteria to " +
			"measure this change against. Judge it on what it does. Do not invent an intent " +
			"and then report the change for not meeting it."
	}
}

// reviewRules is the repository's own path-scoped review guidance, and only
// the rules that match a path this review covers.
//
// Each rule is labelled with the patterns it is scoped to, because
// config.PathRule keeps a scoped rule from being presented as a
// repository-wide one: a reviewer told a rule without its scope applies it to
// files nobody scoped it to.
func reviewRules(cfg config.Config, touched []string) string {
	var b strings.Builder
	for _, rule := range cfg.ReviewPathRules {
		matched := make([]string, 0, len(touched))
		for _, path := range touched {
			if rule.Matches(path) {
				matched = append(matched, path)
			}
		}
		if len(matched) == 0 {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("This repository adds review rules of its own. Each applies only to the " +
				"paths listed with it and to nothing else.\n")
		}
		b.WriteString("\nScoped to " + rule.Scope() + ", which this change touches at " +
			strings.Join(matched, ", ") + ":\n" + rule.Guidance + "\n")
	}
	return b.String()
}

// reviewPriorRound is PRD principle P5 as text the reviewer is held to: a
// change the pipeline authored is author code, and the previous findings and
// the fix summary are claims to check rather than evidence that anything was
// resolved.
//
// It is present only for an execution that is verifying a round, and it says
// what it is quoting and where it came from. What it must not do is read as a
// record of resolved findings, so every sentence here is about checking the
// claim rather than about what was fixed.
//
// This reviewer has no memory of the round. That is not this text's doing:
// agents.StageAgent runs with no session and internal/pipeline builds a new
// body for every execution, so the only thing that crosses is what is written
// here.
func reviewPriorRound(f reviewFacts) string {
	if !f.rereview() {
		return ""
	}
	var b strings.Builder
	b.WriteString("An automatic fix round has already run over an earlier review of this change. " +
		"Treat anything it wrote as author code: it gets the same reading as everything else " +
		"here, and it is the least-reviewed code in this change precisely because it looks like " +
		"housekeeping.\n\n")
	b.WriteString("You have no memory of that round, and what follows is not evidence that " +
		"anything was resolved. It is a set of claims, and your job includes checking them: a " +
		"finding reported as fixed may not be fixed, may be fixed in a way that breaks something " +
		"else, or may have been the wrong reading of the code in the first place.\n")
	if f.fixSummary != "" {
		b.WriteString("\nWhat the fix round says it changed, in its own words:\n\n" + f.fixSummary + "\n")
	}
	if len(f.previous.Findings) > 0 {
		b.WriteString("\nWhat the earlier review reported. The round was sent the findings marked " +
			"fix and nothing else, so the rest are here as context rather than as work it was " +
			"asked to do:\n")
		for _, found := range f.previous.Findings {
			b.WriteString("\n  - [" + string(found.Action) + "] ")
			if location := found.Location.String(); location != "" {
				b.WriteString(location + ": ")
			}
			b.WriteString(strings.TrimSpace(found.Description) + "\n")
		}
	}
	return b.String()
}

// reviewAnswerShape says what to print. It names every field the report is
// read out of and says what each is for, and it states P3 where the reviewer
// meets it: an action this system cannot read goes to a person, so an
// unclassified finding is not a cheap way to raise something.
//
// The two fields another package's guidance already owns, "revision" and
// "read", are named here as part of the shape and their rules are not
// restated: findings.Demand.Guidance is the one owner of what they must hold,
// and a second telling of it here would be the copy that drifts.
//
// lensed says whether the scope section is in this prompt at all, and it is
// the caller's answer rather than a second reading of the run, so the field
// asking for the lens's answer is described exactly when the question was
// asked. Asking for an answer to a question nobody put would get one invented.
func reviewAnswerShape(lensed bool) string {
	var b strings.Builder
	b.WriteString("Answer with one JSON object and nothing after it. Its fields:\n\n")
	b.WriteString(`  - "summary": what you read and what you concluded. Required.` + "\n")
	b.WriteString(`  - "revision": the commit you reviewed, as the evidence section above ` +
		"requires.\n")
	b.WriteString(`  - "read": every repository-relative path you actually opened and read, ` +
		"as the evidence section above requires.\n")
	if lensed {
		b.WriteString(`  - "traced": your answer to the scope section above, as a list of ` +
			`{"path": ..., "reason": ...}. The path is one of the paths this change touches, ` +
			"repeated exactly as it was listed; the reason is what part of the intent it " +
			"follows from, in your own words. A path you cannot account for is left out.\n")
	}
	b.WriteString(`  - "findings": what you found, as a list. Each finding is ` +
		`{"id": ..., "severity": ..., "action": ..., "location": {"path": ..., "line": ...}, ` +
		`"cites": [...], "description": ...}. Give every finding an identifier of your own, ` +
		"unique within this report. The description is what a person acts on, so write it for " +
		"someone who has not read the diff.\n")
	b.WriteString(`  - "risk" and "risk_rationale": optional. Risk is one of "low", "medium", ` +
		`or "high", and any other word makes the whole report unreadable, so leave it out ` +
		"rather than guessing.\n\n")
	b.WriteString("Severity is one of \"error\", \"warning\", or \"info\", and it orders the list " +
		"rather than deciding anything.\n\n")
	b.WriteString("The action decides who resolves the finding, and it is the most important " +
		"field you write:\n\n")
	b.WriteString(`  - "fix": objectively wrong and mechanically fixable. It goes to an ` +
		"automatic fix round. Correctness, reliability and security problems belong here even " +
		"when the smallest correct repair restores a little previously deleted logic.\n")
	b.WriteString(`  - "ask": it touches the person's intent or judgment. It holds the run for ` +
		"their decision and never enters a fix round. Questioning a deliberate product choice, " +
		"arguing that an intentional removal should be undone, and reporting that you could not " +
		"gather enough evidence all belong here.\n")
	b.WriteString(`  - "note": informational. It blocks nothing. A report whose findings are ` +
		"all notes approves the change as it stands.\n\n")
	b.WriteString("A finding whose action is missing, empty, or a word this system does not " +
		`recognize becomes "ask" and holds the run for a person. That is defined behaviour and ` +
		"not an error, so an unclassified finding is not a lighter way to raise something: it " +
		"is the heaviest.")
	return b.String()
}
