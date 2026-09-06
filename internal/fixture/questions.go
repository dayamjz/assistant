package fixture

// OpenQuestions are the decisions this package ran into and did not make.
// Deciding any of them here would make the fixture the owner of a fact that
// belongs somewhere else, and a fixture that decides how a harness drives a
// condition has stopped being a fixture.
//
// They are carried in the manifest so that a harness reading it sees them
// rather than rediscovering them.
func OpenQuestions() []OpenQuestion {
	return []OpenQuestion{
		{
			ID:       "question-config-document-name",
			Question: "What is the repository configuration document called, and who owns that name?",
			Owner:    "internal/config, or whichever package resolves a repository layer from git",
			Provisional: "PRD section 10 places the document at the repository root and does not name it, " +
				"and no package in this product owns the name. This package plants it at the path in " +
				"ConfigPath and carries that path in the manifest, so a harness reads where it was planted " +
				"rather than assuming. When a package owns the name, ConfigPath follows it and nothing " +
				"else here changes.",
		},
		{
			ID: "question-agent-response-delivery",
			Question: "How is a fake agent pointed at one of the canned responses, and how does a run " +
				"select which response a given stage gets?",
			Owner: "assistant-fake-agent and assistant-journey-harness",
			Provisional: "The responses are written as the exact bytes an agent prints, one file per " +
				"condition, and their paths are in Scenario.AgentResponses. Nothing here decides how they " +
				"are served.",
		},
		{
			ID: "question-p3-through-the-review-path",
			Question: "How is P3 exercised through the review path, where a report has to carry the " +
				"revision it read and the set of paths it read before any finding is reached, and does " +
				"that want a condition of its own?",
			Owner: "whoever plants this package's conditions, which is a change of its own rather than " +
				"the one that recorded this",
			Provisional: "The three conditions this package plants for P3 target findings.ParseReport, " +
				"and each says so. A review stage's output goes through findings.ParseReviewReport, " +
				"which refuses a report stating no revision with ErrWrongRevision before any finding is " +
				"reached, so the bytes planted here reach the action on the one path and not on the " +
				"other. Nothing about them became false and none of them was changed.\n\n" +
				"What a review-path condition's bytes would be is the open part, and one constraint on " +
				"them is already known: the revision has to be the commit the run asked about, which is " +
				"not the head this build records, because the run rebases and may add fix commits. That " +
				"is the same substitution question-provider-response-delivery records for the checks " +
				"answer, so whoever plants it inherits that problem rather than meeting a new one.",
		},
		{
			ID: "question-provider-response-delivery",
			Question: "How is the code host's provider command replaced by something that prints the " +
				"canned answer?",
			Owner: "assistant-journey-harness",
			Provisional: "The answer is written as the exact bytes the provider command prints, and its " +
				"path is in Scenario.ProviderResponses. internal/forge invokes a provider command whose " +
				"path a caller supplies, so a harness has a seam; choosing it is not this package's.\n\n" +
				"Whatever the seam is, it has to substitute the head. The checks answer carries " +
				"Commits[\"branch-head\"] as the build left it, and the run rebases and may add fix " +
				"commits, so the commit it pushed is not that one. An answer naming a commit the run did " +
				"not push is a stale check list, which internal/forge distinguishes from an empty one, so " +
				"serving these bytes unchanged reports something other than the planted condition.",
		},
		{
			ID:       "question-deferred-plant-timing",
			Question: "At which point in a run are AdvanceRemoteOutOfBand and CopyGatedWorkingCopy called?",
			Owner:    "assistant-journey-harness",
			Provisional: "Each deferred condition states the constraint its plant puts on the timing and " +
				"why an earlier plant exercises nothing. The point in the run that satisfies the " +
				"constraint is the harness's to choose.",
		},
		{
			ID: "question-checks-timeout-observation",
			Question: "How does the harness observe that a run waiting on VerdictNoChecks waited rather " +
				"than concluded, without waiting out the whole checks_timeout?",
			Owner: "assistant-journey-harness",
			Provisional: "None. checks_timeout is global-only, so it is set in the operator's own file " +
				"rather than in anything this package plants, and shortening it is a harness decision.",
		},
		{
			ID: "question-project-instructions-suppression",
			Question: "Should the hostile harness installation's AGENTS.md and CLAUDE.md be exercised with " +
				"suppress_project_instructions set, unset, or both?",
			Owner: "assistant-journey-harness",
			Provisional: "The trusted document leaves the key at its default, which is unset, so the " +
				"installation's project instructions are ordinary prose an agent may read. The condition " +
				"as recorded is that nothing in that prose becomes an executed command or a selected " +
				"agent. Exercising the suppressed case as well needs a second trusted document, which is " +
				"a scenario this package will add once the harness says it wants one.",
		},
		{
			ID: "question-trusted-and-pushed-composition",
			Question: "Which component reads the trusted document from the default branch, resolves it, and " +
				"combines that resolution with the pushed one, so a run has the trusted commands and agent " +
				"together with the keys a pushed branch may set?",
			Owner: "the gate, most likely: PRD section 10 puts reading the trusted document at a freshly " +
				"fetched commit there, and internal/config/doc.go says enforcing where the bytes came from " +
				"needs git and belongs to the gate",
			Provisional: "Two facts about internal/config decide the shape of the answer, and both are " +
				"checkable there. config.Resolve takes one global layer and one repository layer, so a " +
				"single call is handed either the trusted document or the pushed one, never both. And the " +
				"allow_pushed_commands opt-out is only honored from a repository layer whose origin is " +
				"trusted, while it only changes an outcome for a repository layer whose origin is pushed, " +
				"so the two sides cannot be one call by construction. None of that is a shortfall in " +
				"internal/config, which is complete for the merge it defines; the composition above it is " +
				"simply not owned by any package yet.\n\n" +
				"What refusal-pushed-commands-and-agent records is therefore scoped to the one call it " +
				"names: the three rejections and the pushed ignore_patterns winning. The other half of " +
				"that condition belongs to whoever answers this question, and is the values the trusted " +
				"document carries: commands.test \"go test ./...\", commands.lint \"go vet ./...\", and " +
				"agent [\"fixture-trusted-agent\"]. A run whose resolved commands or agent are the " +
				"branch's has taken the pushed document as trusted, whatever composed it.",
		},
		{
			ID: "question-hookspath-redirect-observation",
			Question: "Which process is given the configuration file that redirects core.hooksPath, and " +
				"is a push driven through the gate under it?",
			Owner: "assistant-journey-harness",
			Provisional: "The file and the directory it redirects to are in the scenario's paths and " +
				"nothing here exports either. Initialization succeeding is enough to record the gap, and " +
				"the two redirected hooks are tripwires, so a harness that does drive a push under the " +
				"file sees which hook ran rather than inferring it. Whether observing that is worth " +
				"driving a push belongs to whoever reports the gap.",
		},
	}
}
