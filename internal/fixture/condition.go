package fixture

// Kind separates the two reasons a condition is planted. Both are needed, and
// they are not weighed the same: a run that only exercises the stages proves
// the refusals were never reached.
type Kind string

const (
	// KindStage is a condition planted so that one of the nine stages has
	// something to find and the correct finding is known in advance.
	KindStage Kind = "stage"
	// KindRefusal is a condition planted for what the product declines to do.
	// Most of this product's value is in these, and the kind covers the whole
	// family rather than only the cases that end in an error: the negative
	// cases that give a refusal its meaning, such as a channel that is closed
	// so nothing arrives to refuse, and the places where the refusal does not
	// exist yet and Outcome.Gap says so, are planted for the same reason and
	// are useless apart from each other. A refusal nobody ever saw not fire is
	// a refusal nobody has shown is about anything.
	KindRefusal Kind = "refusal"
)

// ID names one planted condition. It is the key the harness reports results
// against, so it is stable and does not describe the plant, which changes.
type ID string

// Condition is one planted condition together with the outcome it is supposed
// to produce. Each is returned by the function that plants it, so the
// expectation is written beside the plant and the two cannot drift apart.
type Condition struct {
	// ID names the condition.
	ID ID `json:"id"`
	// Scenario is the scenario the condition is planted in. A condition that
	// aborts a run cannot share a scenario with one that needs the run to
	// finish, which is why there is more than one.
	Scenario ScenarioName `json:"scenario"`
	// Kind says whether this exists for a stage or for a refusal.
	Kind Kind `json:"kind"`
	// Principle names the PRD section 4 principle this condition exercises,
	// such as "P6", and is empty for a condition that exercises none.
	Principle string `json:"principle,omitempty"`
	// Planted says what was planted and, where it matters, by which route it
	// was reached. The route is part of the plant: a state assembled directly
	// rather than by the path the product takes to it exercises nothing on
	// that path.
	Planted string `json:"planted"`
	// Mechanism names the package and symbol expected to produce the outcome,
	// such as "safety.Guard.Decide". It is where a harness reporting a miss
	// sends the reader.
	Mechanism string `json:"mechanism"`
	// Expect is what the mechanism has to answer.
	Expect Outcome `json:"expect"`
	// Deferred describes a plant that cannot be applied when the scenario is
	// built, and is nil for every condition that is fully planted on disk.
	Deferred *Deferred `json:"deferred,omitempty"`
}

// Outcome is the recorded correct answer for one condition. A harness compares
// against it; nothing here interprets it.
type Outcome struct {
	// Summary states the answer in one sentence.
	Summary string `json:"summary"`
	// Sentinel names the error a caller is expected to match with errors.Is,
	// such as "gate.ErrTemplateHooks". It is empty for a condition whose
	// answer is not an error.
	Sentinel string `json:"sentinel,omitempty"`
	// Value names the non-error result expected, such as
	// "forge.VerdictNoChecks" or "findings.ActionAsk", and is empty when the
	// answer is an error alone.
	Value string `json:"value,omitempty"`
	// MessageContains are substrings the message has to carry. They are
	// separate from Summary because a refusal is judged on what it tells the
	// operator, not on having refused.
	MessageContains []string `json:"message_contains,omitempty"`
	// NamesAction is the action a refusal has to name, for the refusals that
	// are a dead end without one. It is empty where the refusal owes the
	// reader nothing beyond the reason.
	NamesAction string `json:"names_action,omitempty"`
	// ActionSucceeds says why that action gets the reader out from the state
	// this condition puts them in. A refusal that names an action which is
	// itself refused is what makes somebody delete a directory by hand, so the
	// action being available is part of the expectation rather than a remark
	// about it.
	ActionSucceeds string `json:"action_succeeds,omitempty"`
	// TripwiresQuiet names the tripwire identifiers that must not appear in
	// the scenario's tripwire file. It is how "nothing was executed" is
	// checked as a fact rather than assumed from an absence nobody looked at.
	TripwiresQuiet []string `json:"tripwires_quiet,omitempty"`
	// Gap records that the product does not answer this today and says what is
	// missing. It is set only where the shortfall is already written down in
	// the package that owns the question, and it names where. A condition
	// carrying it is planted so the harness reports a known gap rather than a
	// pass.
	Gap string `json:"gap,omitempty"`
}

// Deferred describes a plant that cannot be applied while the scenario is
// being built, because the state it creates only exists partway through a run.
type Deferred struct {
	// AppliedBy names the exported function in this package that applies it.
	AppliedBy string `json:"applied_by"`
	// AppliedWhen says the moment it has to be applied at. Deciding that
	// moment is the harness's, and this states the constraint the condition
	// puts on it rather than the sequence the harness should use.
	AppliedWhen string `json:"applied_when"`
	// WhyNotAtBuild says what would be lost by planting it earlier.
	WhyNotAtBuild string `json:"why_not_at_build"`
}

// OpenQuestion is a decision this package ran into that belongs to somebody
// else. It is recorded rather than answered: a fixture that decides how the
// harness drives a condition has stopped being a fixture.
type OpenQuestion struct {
	// ID names the question.
	ID ID `json:"id"`
	// Question is the decision that has to be made.
	Question string `json:"question"`
	// Owner names who it belongs to, such as "assistant-journey-harness".
	Owner string `json:"owner"`
	// Provisional is what this package did in the meantime, and is empty where
	// it did nothing.
	Provisional string `json:"provisional,omitempty"`
}
