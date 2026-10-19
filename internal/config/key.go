package config

import (
	"encoding/json"
	"strings"
	"time"
)

// Key is a configuration key, written in the dotted form the PRD section 10
// schema uses. The constants below are the whole schema: a key that is not one
// of them is refused at parse time rather than ignored.
type Key string

// The configuration schema. Each key's default, trust class, and meaning are
// stated once, in the table this file declares, and everything else in the
// package reads that table.
const (
	// KeyAgent is "agent": one agent, or an ordered fallback list, resolved
	// against what is actually runnable at run start.
	KeyAgent Key = "agent"
	// KeyCommandsTest is "commands.test": a targeted check for the change.
	KeyCommandsTest Key = "commands.test"
	// KeyCommandsLint is "commands.lint": static analysis.
	KeyCommandsLint Key = "commands.lint"
	// KeyCommandsFormat is "commands.format": run immediately before pushing.
	KeyCommandsFormat Key = "commands.format"
	// KeyCommandsDocument is "commands.document": updates documentation the
	// change made stale.
	KeyCommandsDocument Key = "commands.document"
	// KeyFixRoundsReview is "fix_rounds.review": automatic review fix rounds,
	// over fix findings only.
	KeyFixRoundsReview Key = "fix_rounds.review"
	// KeyFixRoundsRebase is "fix_rounds.rebase": the rebase stage's limit.
	KeyFixRoundsRebase Key = "fix_rounds.rebase"
	// KeyFixRoundsTest is "fix_rounds.test": the test stage's limit.
	KeyFixRoundsTest Key = "fix_rounds.test"
	// KeyFixRoundsLint is "fix_rounds.lint": the lint stage's limit.
	KeyFixRoundsLint Key = "fix_rounds.lint"
	// KeyFixRoundsChecks is "fix_rounds.checks": the checks stage's limit.
	KeyFixRoundsChecks Key = "fix_rounds.checks"
	// KeyRunBudget is "run_budget": total node executions per run.
	KeyRunBudget Key = "run_budget"
	// KeyIgnorePatterns is "ignore_patterns": paths excluded from review and
	// documentation checks.
	KeyIgnorePatterns Key = "ignore_patterns"
	// KeyReviewPathRules is "review.path_rules": review guidance scoped to
	// paths, applied in declared order.
	KeyReviewPathRules Key = "review.path_rules"
	// KeyJudgePaths is "judge_paths": the patterns naming the files that
	// decide whether a check passes. It is trusted-only, because a branch that
	// could shrink the list could exempt its own edits to those files.
	KeyJudgePaths Key = "judge_paths"
	// KeyDocumentOwnership is "document.ownership": which document owns which
	// subject.
	KeyDocumentOwnership Key = "document.ownership"
	// KeySuppressProjectInstructions is "suppress_project_instructions".
	KeySuppressProjectInstructions Key = "suppress_project_instructions"
	// KeyNoCI is "no_ci": a positive declaration that there are no checks.
	KeyNoCI Key = "no_ci"
	// KeyChecksTimeout is "checks_timeout": the idle timeout for checks. It is
	// global-only, so only the operator's own file may set it.
	KeyChecksTimeout Key = "checks_timeout"
	// KeySessionReuse is "session_reuse": one durable fixer session per run. It
	// is global-only, so only the operator's own file may set it.
	KeySessionReuse Key = "session_reuse"
	// KeyDecisionModel is "decision_model": the mode of the decision-model
	// checks PRD section 5 describes. It is global-only, so a repository can
	// turn the checks neither on, off, nor into enforce, in either copy.
	KeyDecisionModel Key = "decision_model"
	// KeySupervisionBeaconInterval is "supervision_beacon_interval": how
	// often the supervisor loop rewrites its liveness beacon. It is
	// global-only, because the loop is the service's and a repository has no
	// standing over how the operator's machine supervises its fleet.
	//
	// It is the one row the two supervision durations derive from, which is
	// the reading of PRD section 10's "every count or duration is a row"
	// taken here: a beacon is stale at internal/supervisor's
	// StalenessMultiple times this interval, and arm verification waits
	// FirstBeaconWaitMultiple times it for the first beacon. Both are
	// derived rather than chosen, so neither is a second row that could be
	// set apart from the interval it is a multiple of. The default and both
	// limits are chosen, not measured. It is not a loop bound: it stops
	// nothing and parks nothing. PRD section 10's keys table has its row and
	// the limits table its floor and ceiling.
	KeySupervisionBeaconInterval Key = "supervision_beacon_interval"
	// KeyEscalationWindow is "escalation_window": how long a terminal may
	// stay unchanged, with positive evidence of progress, before the
	// supervisor surfaces it anyway, PRD section 10's row. It is global-only
	// for KeySupervisionBeaconInterval's reason: the loop is the service's.
	// The default and both limits are chosen, not measured, and refused
	// rather than clamped. It is not a loop bound: it stops nothing and parks
	// nothing, it only decides when an absorbed stall surfaces.
	KeyEscalationWindow Key = "escalation_window"
	// KeyRecheckWindow is "recheck_window": how long a declared external wait
	// may sit idle before it surfaces, so a forgotten wait cannot stay
	// invisible, PRD section 10's row. Global-only, bounded and refused on
	// the same terms as KeyEscalationWindow, and not a loop bound either.
	KeyRecheckWindow Key = "recheck_window"
	// KeyStallInspectionCount is "stall_inspection_count": how many times the
	// same unchanged screen may surface before the next surface demands a
	// deeper inspection, PRD section 11's "after enough repetitions". It is
	// global-only on the same terms as the two windows, and it is a level on
	// a verdict rather than a bound: internal/supervisor's Stall says it stops
	// nothing. The default and both limits are chosen, not measured. PRD
	// section 10's keys table has its row and the limits table its floor and
	// ceiling, and section 11's repeated-stall row names it.
	KeyStallInspectionCount Key = "stall_inspection_count"
	// KeyRelaunchAttempts is "relaunch_attempts": how many times
	// reconciliation at service start relaunches a task that resolves
	// Failed with its terminal gone, counted from the task's attempt rows,
	// before the task holds for a person instead. It is global-only for the
	// beacon interval's reason: the relaunch is the service's act on the
	// operator's machine, and a repository has no standing over how many
	// workers are started on its behalf.
	//
	// It is a task-level bound and not one of internal/graph's three loop
	// bounds: it stops no run and parks nothing. A loop bound limits how
	// far one run's execution may go and survives a resume with the thing
	// it is compared against; this row limits how many attempts of one
	// task the program begins on its own, and the thing it is compared
	// against is the attempt history, which is durable in its own right.
	//
	// Past it the task holds, as PRD section 10's row says: internal/service
	// registers a hold naming the task and the attempt that reached the
	// bound, and a person decides whether it is retried by answering that
	// hold with --answer, given the task. A retry relaunches once more
	// through the same anchored relaunch and is not counted against this row
	// for that one attempt; the next failure holds again. Zero relaunches
	// nothing and holds every such task. internal/service's reconcile.go is
	// the one reader, and its taskholds.go owns the hold and its answers.
	// PRD section 10's keys table has its row and the limits table its
	// ceiling; the floor is zero.
	KeyRelaunchAttempts Key = "relaunch_attempts"
	// KeyCommitFixMessage is "commit.fix_message": the fix commit subject
	// template. It shapes the subject line and nothing else: a fix commit
	// also carries a body, the fixer's whole account of the round, which
	// internal/stages writes below the rendered subject and no template here
	// decides. What FixMessagePlaceholder is rendered with is said there.
	KeyCommitFixMessage Key = "commit.fix_message"
	// KeyAllowPushedCommands is "allow_pushed_commands": the opt-out that lets
	// a pushed branch set the command and agent keys.
	//
	// PRD section 10 describes this opt-out in prose but does not give it a
	// row in the schema table, so the name was chosen here. It is a settled
	// decision, not an open question: the name says exactly what it permits,
	// and what the PRD still owes is a schema row, not a rename.
	KeyAllowPushedCommands Key = "allow_pushed_commands"
	// KeyAutonomyMerge is "autonomy.merge": PRD section 10's merge half of
	// section 6's autonomy flag, the repositories - each named as the code
	// host names it, owner/name - where the program may merge a run that
	// ended checks-passed, after the live read section 6 gives. It is
	// global-only, so a repository can never grant itself merge authority,
	// and empty by default, so the program merges nothing until the operator
	// lists a repository. What the live read requires is internal/service's;
	// whether a merge is allowed is the code host's.
	KeyAutonomyMerge Key = "autonomy.merge"
	// KeyAllowStandingConsent is "allow_standing_consent": whether a run of
	// this repository may be put under the standing consent PRD section 9
	// describes, answering the decisions its stages before push hold on
	// without a person. It allows and does not turn on: a run is put under
	// it only by the per-run flag PRD section 9 gives the bare command, and
	// that flag is refused where this resolved false.
	//
	// It is trusted-only, so a pushed branch can never allow standing consent
	// for itself (P7): a pushed copy setting it is dropped at the merge and
	// reported as a rejection like any other trusted-only key. The class also
	// admits the operator's own file, as it does for every trusted-only key,
	// so an operator who sets it there allows it in every repository of the
	// home. It is false by default. What the consent answers, and what it
	// never answers, is internal/service's consent.go.
	//
	// PRD section 9 names the flag and this key; section 10's schema table
	// does not yet carry a row for it, which is the same position
	// KeyAllowPushedCommands was chosen in.
	KeyAllowStandingConsent Key = "allow_standing_consent"

	// KeyDelivery is "delivery": PRD section 10's delivery posture, the path
	// a repository's delivery tasks ship on, section 6's delivery modes. It
	// is trusted-only, so a branch cannot choose a lighter path for itself
	// (P7), and its default is DeliveryGated. Its values are Delivery's closed
	// set; "direct" is section 12's deferred mode and is refused at parse
	// time rather than accepted and ignored. It decides only what a task's
	// own push does, and only through the posture recorded on the task at
	// intake: it never changes what a person's push to the gate does.
	KeyDelivery Key = "delivery"

	// KeySessionProvider is "session_provider": which terminal session
	// provider the service starts a task's worker through, PRD section 8's
	// session module. Its values are SessionProvider's closed set and its
	// default is SessionProviderPipe, the reference provider.
	//
	// It is global-only, and that is a P7 decision rather than a convenience.
	// The provider decides which program the service runs to start every
	// worker - "tmux" has the service run the tmux binary on the operator's
	// PATH and have its server start the agent - so it chooses which process
	// starts with the operator's credentials, which is what TrustCommands
	// guards; and it is one choice for the whole service, made when the
	// service first builds its agent, so no repository's copy, trusted or
	// pushed, is the document it could be read from. A repository file
	// naming it is refused at parse time like every global-only key.
	// internal/service reads it once per service, in driverFor.
	KeySessionProvider Key = "session_provider"

	// KeyUsageCeiling is "usage_ceiling": the ceiling on recorded input
	// tokens that may start another agent invocation. It is one object, a
	// window in the syntax internal/machine's usage view reads and an
	// input-token count. Zero tokens is no ceiling: a start is not refused
	// for usage, which is the default, so a home that never sets the key
	// starts what it started before.
	//
	// It is global-only, and that is a P7 decision. The ceiling changes
	// which invocations run, so a repository file must not be able to raise
	// it for itself, in either copy. A repository file naming it is refused
	// at parse time like every global-only key. internal/service reads the
	// operator's resolved value before it starts an invocation and refuses
	// the start when the invocation history in the window is over the count,
	// or when that history does not establish the count. It does not cancel
	// a start that already passed the check.
	//
	// PRD section 10's schema table does not yet carry a row for it, which
	// is the same position KeySessionProvider was chosen in. Section 12
	// defers routing by model, effort, or quota; this key is not that
	// routing. It does not ask a provider what remains of a quota.
	KeyUsageCeiling Key = "usage_ceiling"
)

// cloneSlice copies a slice-valued configuration value. A default and a parsed
// layer are each held once and applied to every Config resolved from them, so
// handing out the stored slice would let one caller's edit reach every later
// resolution. A Pattern's own segments are unexported and never written after
// parsing, so copying the outer slice is enough.
func cloneSlice[T any](s []T) []T {
	if s == nil {
		return nil
	}
	out := make([]T, len(s))
	copy(out, s)
	return out
}

// clonePathRules copies the rule list and each rule's pattern list, which is
// the one nested slice a caller can reach and write through.
func clonePathRules(rs []PathRule) []PathRule {
	out := cloneSlice(rs)
	for i := range out {
		out[i].Paths = cloneSlice(out[i].Paths)
	}
	return out
}

// spec is one row of the schema: a key, the origins allowed to set it, the
// value it takes when nobody sets it, how a JSON value becomes that value, and
// where the value lands in a Config. Every fact about a key lives in exactly
// one row, per P14.
type spec struct {
	key    Key
	trust  Trust
	def    any
	decode func(k Key, v any) (any, error)
	set    func(c *Config, v any)
}

// specs is the schema. Order is the order keys are applied and the order
// Keys reports them in; it does not affect the resolved value, because each
// key lands in a distinct field.
var specs = []spec{
	{KeyAgent, TrustCommands, []string{DefaultAgent}, decodeAgent,
		func(c *Config, v any) { c.Agent = cloneSlice(v.([]string)) }},
	{KeyCommandsTest, TrustCommands, "", decodeCommand,
		func(c *Config, v any) { c.Commands.Test = v.(string) }},
	{KeyCommandsLint, TrustCommands, "", decodeCommand,
		func(c *Config, v any) { c.Commands.Lint = v.(string) }},
	{KeyCommandsFormat, TrustCommands, "", decodeCommand,
		func(c *Config, v any) { c.Commands.Format = v.(string) }},
	{KeyCommandsDocument, TrustCommands, "", decodeCommand,
		func(c *Config, v any) { c.Commands.Document = v.(string) }},
	{KeyFixRoundsReview, TrustPushed, DefaultFixRoundsReview, decodeFixRounds,
		func(c *Config, v any) { c.FixRounds.Review = v.(int) }},
	{KeyFixRoundsRebase, TrustPushed, DefaultFixRoundsStage, decodeFixRounds,
		func(c *Config, v any) { c.FixRounds.Rebase = v.(int) }},
	{KeyFixRoundsTest, TrustPushed, DefaultFixRoundsStage, decodeFixRounds,
		func(c *Config, v any) { c.FixRounds.Test = v.(int) }},
	{KeyFixRoundsLint, TrustPushed, DefaultFixRoundsStage, decodeFixRounds,
		func(c *Config, v any) { c.FixRounds.Lint = v.(int) }},
	{KeyFixRoundsChecks, TrustPushed, DefaultFixRoundsStage, decodeFixRounds,
		func(c *Config, v any) { c.FixRounds.Checks = v.(int) }},
	{KeyRunBudget, TrustTrusted, DefaultRunBudget, decodeRunBudget,
		func(c *Config, v any) { c.RunBudget = v.(int) }},
	{KeyIgnorePatterns, TrustPushed, PatternSet(nil), decodeIgnorePatterns,
		func(c *Config, v any) { c.IgnorePatterns = cloneSlice(v.(PatternSet)) }},
	{KeyReviewPathRules, TrustTrusted, []PathRule(nil), decodePathRules,
		func(c *Config, v any) { c.ReviewPathRules = clonePathRules(v.([]PathRule)) }},
	{KeyJudgePaths, TrustTrusted, defaultJudgePaths, decodeJudgePaths,
		func(c *Config, v any) { c.JudgePaths = cloneSlice(v.(PatternSet)) }},
	{KeyDocumentOwnership, TrustTrusted, []Ownership(nil), decodeOwnership,
		func(c *Config, v any) { c.DocumentOwnership = cloneSlice(v.([]Ownership)) }},
	{KeySuppressProjectInstructions, TrustTrusted, false, decodeBool,
		func(c *Config, v any) { c.SuppressProjectInstructions = v.(bool) }},
	{KeyNoCI, TrustTrusted, false, decodeBool,
		func(c *Config, v any) { c.NoCI = v.(bool) }},
	{KeyChecksTimeout, TrustGlobal, DefaultChecksTimeout, decodeChecksTimeout,
		func(c *Config, v any) { c.ChecksTimeout = v.(time.Duration) }},
	{KeySessionReuse, TrustGlobal, DefaultSessionReuse, decodeBool,
		func(c *Config, v any) { c.SessionReuse = v.(bool) }},
	{KeyDecisionModel, TrustGlobal, DefaultDecisionModel, decodeDecisionModel,
		func(c *Config, v any) { c.DecisionModel = v.(DecisionModel) }},
	{KeySupervisionBeaconInterval, TrustGlobal, DefaultSupervisionBeaconInterval, decodeSupervisionBeaconInterval,
		func(c *Config, v any) { c.SupervisionBeaconInterval = v.(time.Duration) }},
	{KeyEscalationWindow, TrustGlobal, DefaultEscalationWindow, decodeEscalationWindow,
		func(c *Config, v any) { c.EscalationWindow = v.(time.Duration) }},
	{KeyRecheckWindow, TrustGlobal, DefaultRecheckWindow, decodeRecheckWindow,
		func(c *Config, v any) { c.RecheckWindow = v.(time.Duration) }},
	{KeyStallInspectionCount, TrustGlobal, DefaultStallInspectionCount, decodeStallInspectionCount,
		func(c *Config, v any) { c.StallInspectionCount = v.(int) }},
	{KeyRelaunchAttempts, TrustGlobal, DefaultRelaunchAttempts, decodeRelaunchAttempts,
		func(c *Config, v any) { c.RelaunchAttempts = v.(int) }},
	{KeyCommitFixMessage, TrustPushed, DefaultCommitFixMessage, decodeFixMessage,
		func(c *Config, v any) { c.CommitFixMessage = v.(string) }},
	{KeyAllowPushedCommands, TrustTrusted, false, decodeBool,
		func(c *Config, v any) { c.AllowPushedCommands = v.(bool) }},
	{KeyAutonomyMerge, TrustGlobal, []string(nil), decodeAutonomyMerge,
		func(c *Config, v any) { c.AutonomyMerge = cloneSlice(v.([]string)) }},
	{KeyDelivery, TrustTrusted, DefaultDelivery, decodeDelivery,
		func(c *Config, v any) { c.Delivery = v.(Delivery) }},
	{KeyAllowStandingConsent, TrustTrusted, false, decodeBool,
		func(c *Config, v any) { c.AllowStandingConsent = v.(bool) }},
	{KeySessionProvider, TrustGlobal, DefaultSessionProvider, decodeSessionProvider,
		func(c *Config, v any) { c.SessionProvider = v.(SessionProvider) }},
	{KeyUsageCeiling, TrustGlobal, defaultUsageCeiling, decodeUsageCeiling,
		func(c *Config, v any) { c.UsageCeiling = v.(UsageCeiling) }},
}

// specByKey indexes the schema, and sections holds every dotted prefix a key
// uses, which is what tells the parser an object is a section to descend into
// rather than a value of the wrong type.
var (
	specByKey = func() map[Key]spec {
		m := make(map[Key]spec, len(specs))
		for _, s := range specs {
			m[s.key] = s
		}
		return m
	}()
	sections = func() map[string]bool {
		m := make(map[string]bool)
		for _, s := range specs {
			if prefix, _, found := strings.Cut(string(s.key), "."); found {
				m[prefix] = true
			}
		}
		return m
	}()
)

// Keys returns every key in the schema, in the order the table declares them.
func Keys() []Key {
	out := make([]Key, len(specs))
	for i, s := range specs {
		out[i] = s.key
	}
	return out
}

// TrustOf returns the trust class of a key, and reports whether the key is
// part of the schema at all.
func TrustOf(k Key) (Trust, bool) {
	s, ok := specByKey[k]
	if !ok {
		return 0, false
	}
	return s.trust, true
}

// jsonText renders a value the way it appeared in the document, for naming it
// in a refusal. A value that cannot be rendered is described rather than
// dropped, so a refusal never loses the thing it is about.
func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "<unrenderable value>"
	}
	return string(b)
}

func keyErr(k Key, v any, detail string) error {
	return &KeyError{Key: k, Value: jsonText(v), Detail: detail}
}
