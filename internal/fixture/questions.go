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
			ID: "question-provider-response-delivery",
			Question: "How is the code host's provider command replaced by something that prints the " +
				"canned answer?",
			Owner: "assistant-journey-harness",
			Provisional: "The answer is written as the exact bytes the provider command prints, and its " +
				"path is in Scenario.ProviderResponses. internal/forge invokes a provider command whose " +
				"path a caller supplies, so a harness has a seam; choosing it is not this package's.",
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
