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
	// against what is actually runnable at run start. A role that names no
	// agent uses this list. The default fixer names cursor and does not.
	KeyAgent Key = "agent"
	// KeyRoles is "roles": which agent, model provider, model type, model
	// version, effort, and context window each role runs. The default names fix as cursor
	// and names no other role, so review and worker use KeyAgent. A document
	// that sets this key and does not name fix keeps that fixer. A fix role
	// that names no agent uses KeyAgent. review, fix, worker and
	// housekeeping are the four the program assigns, housekeeping resolving
	// to the fix row unless the table names it; any other name is a worker
	// role a task may name. It is the same trust class as KeyAgent. A provider, type, or
	// version outside the model table, a partial triple, an effort other
	// than low, medium, or high, and a context outside ContextWindows or on
	// a role with no triple fail at parse time and name the role. Each role
	// may name one explicit fallback, with its own agent, model, effort, and
	// context.
	KeyRoles Key = "roles"
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
	// KeyStallHoldCount is "stall_hold_count": how many surfaces of the same
	// unchanged screen past stall_inspection_count demand a deeper
	// inspection before the next one also holds the task for a person, PRD
	// section 6's "a stalled worker holds for you". Counting past the
	// inspection keeps the coordinator's inspection ahead of the person's
	// decision at every value. It is global-only on
	// KeyStallInspectionCount's terms. It ends nothing: the hold it raises
	// waits on a person, so it is not a loop bound, parks nothing, and is not
	// relaunch_attempts. The default and both limits are chosen, not
	// measured; PRD section 10's keys table has its row and the limits table
	// its floor and ceiling.
	KeyStallHoldCount Key = "stall_hold_count"
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
	// nothing and holds every such task. Only internal/service reads it:
	// reconcile.go and relaunch.go for this relaunch, taskholds.go and
	// workerfallback.go where a stop's retry, or a worker fallback's switch
	// or retry, would begin an attempt past it, and subordinates.go and
	// delegations.go for a subordinate coordinator's relaunch, which it
	// bounds on the same terms. taskholds.go owns a task's hold and its
	// answers.
	// PRD section 10's keys table has its row and the limits table its
	// ceiling; the floor is zero.
	KeyRelaunchAttempts Key = "relaunch_attempts"
	// KeyStopGrace is "stop_grace": how long a stop or SIGTERM waits for
	// the service's shutdown, and with it the process groups the service
	// registered, before it ends those groups by force and exits, PRD
	// section 8's "stopping is bounded" and section 10's row. It is
	// global-only for KeySupervisionBeaconInterval's reason: the stop is the
	// service's act on the operator's machine. It is not a loop bound and
	// stops no run on its own; it bounds how long giving up the home may
	// take. internal/service's stopDeadline is the one reader: Shutdown
	// forces the stop at it and Close abandons the supervisor loop at it,
	// and before it Close waits for the loop to end. The default is
	// the PRD's; the floor and ceiling are chosen, not measured, and PRD
	// section 10's limits table has no row for them yet.
	KeyStopGrace Key = "stop_grace"
	// KeyCommandTimeout is "command_timeout": how long one configured stage
	// command (commands.test, commands.lint, commands.document) may run
	// before internal/stages ends it with its process group and the stage
	// holds on a check that answered nothing, PRD P15's deadline for a check
	// command. It is global-only because the deadline protects the
	// operator's machine: a repository that could raise it could hold the
	// host as long as it liked. An operator sets it when a repository's
	// targeted check legitimately runs longer than the default, which is the
	// one remedy a fixed bound would leave them without. internal/stages is
	// the one reader. The default, floor and ceiling are chosen, not
	// measured, and PRD section 10's schema does not yet have the row.
	KeyCommandTimeout Key = "command_timeout"
	// KeyGitDeadline is "git_deadline": how long one invocation of git the
	// service makes may run, when its caller set no deadline of its own,
	// before internal/vcs ends it with its process group, PRD P15's deadline
	// for git and section 8's process model. It is global-only for
	// KeyCommandTimeout's reason: it bounds a program on the operator's
	// machine, so a repository that could raise it could hold the host as
	// long as it liked. internal/service is the one reader: it builds every
	// vcs handle it and the packages it drives open with this value through
	// vcs.WithInvocationDeadline, and internal/vcs reads no configuration.
	// The command line does not read it; its own git runs under internal/vcs's
	// fallback, which DefaultGitDeadline equals.
	KeyGitDeadline Key = "git_deadline"
	// KeyGHDeadline is "gh_deadline": KeyGitDeadline for gh, the GitHub
	// command line internal/forge runs, on the same terms and with the same
	// one reader, through forge.WithInvocationDeadline. The GitLab adapter
	// starts no program, so this does not bound it.
	KeyGHDeadline Key = "gh_deadline"
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
	// section 6's autonomy flag, the repositories - each named as
	// internal/forge names it, owner/name on GitHub or
	// gitlab.com/group/project on GitLab - where the program may merge a run
	// that ended checks-passed, after the live read section 6 gives. It is
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
	// is TrustGlobalLoosens, the owner's decision on #304: only the
	// operator's own file may choose a lighter posture than the default, so
	// no commit, on the default branch or on a pushed branch, turns a lighter
	// path on without the operator's consent (P7). A repository file may name
	// the posture beneath it or a stricter one, which takes effect, and
	// naming a lighter one refuses the resolution with a *LoosenError naming
	// the layer and both postures. The order compared is Delivery.Lighter.
	// The operator's posture applies to every repository of the home that
	// does not tighten it, because the global file has no per-repository
	// section. Its default is DeliveryGated. Its values are Delivery's closed
	// set; "direct" is section 12's deferred mode and is refused at parse
	// time rather than accepted and ignored. It decides only what a task's
	// own push does, and only through the posture recorded on the task at
	// intake: it never changes what a person's push to the gate does. What each
	// value requires, publishes and leaves to whom is its Policy, posture.go.
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

	// KeyUsageCeilingWindow is "usage_ceiling.window": how far back from
	// each comparison the invocation record is summed for the usage
	// ceiling, a duration between MinUsageCeilingWindow and
	// MaxUsageCeilingWindow, refused rather than clamped outside them. The
	// four count rows below are the ceilings themselves, one per token count
	// the record keeps, each a whole number where zero, the default, is no
	// ceiling on that count.
	//
	// All five are global-only, and that is a P7 decision. A ceiling decides
	// whether the service starts another agent call on the operator's
	// machine, on the operator's account, so a repository has no standing to
	// raise it or to lift it, in either copy; and the comparison is the
	// home's, over every run's calls, so no one repository's copy is the
	// document it could be read from. A repository file naming one is
	// refused at parse time like every global-only key.
	//
	// It is not one of internal/graph's three loop bounds: it is compared
	// against the home's record and not against anything a run's checkpoint
	// carries, it counts every run's calls together, and it parks nothing.
	// internal/service's containRunner asks it before every agent call it
	// starts, and a refusal there holds the stage that needed the call for a
	// person; internal/service's ceiling.go owns that and what it leaves
	// open. Nor is it the routing by quota or price PRD section 12 defers: it
	// asks no provider what remains of a quota and carries no price.
	//
	// PRD section 10's schema has one row per key and its limits table the
	// window's floor and ceiling; section 14's decision 17 says what the
	// ceiling bounds.
	KeyUsageCeilingWindow Key = "usage_ceiling.window"
	// KeyUsageCeilingInputTokens is "usage_ceiling.input_tokens", the
	// ceiling on recorded input tokens; KeyUsageCeilingWindow owns the rest.
	KeyUsageCeilingInputTokens Key = "usage_ceiling.input_tokens"
	// KeyUsageCeilingOutputTokens is "usage_ceiling.output_tokens", the
	// ceiling on recorded output tokens.
	KeyUsageCeilingOutputTokens Key = "usage_ceiling.output_tokens"
	// KeyUsageCeilingCacheReadTokens is "usage_ceiling.cache_read_tokens",
	// the ceiling on recorded cache read tokens.
	KeyUsageCeilingCacheReadTokens Key = "usage_ceiling.cache_read_tokens"
	// KeyUsageCeilingCacheCreationTokens is
	// "usage_ceiling.cache_creation_tokens", the ceiling on recorded cache
	// creation tokens.
	KeyUsageCeilingCacheCreationTokens Key = "usage_ceiling.cache_creation_tokens"

	// KeyNotifyCommand is "notify.command": the operator's private channel,
	// a command line the service runs once for each wake the supervisor
	// loop appends, with the wake's row on standard input. It is a line of
	// shell on the commands.* keys' terms and runs through the runner they
	// run through, in the home's root, with the service's environment.
	// Empty, the default, is no channel: nothing is run and nothing is
	// recorded.
	//
	// It is global-only, and that is a P7 decision. The command executes on
	// the operator's machine with the operator's credentials, and where a
	// wake is sent is the operator's to choose for the whole home, so no
	// repository's copy, trusted or pushed, is the document it could be read
	// from. A repository file naming it is refused at parse time like every
	// global-only key. The command decides nothing the program does: what it
	// is sent is a copy of a row the coordinator is also offered, and how it
	// exits is recorded and never read back as an answer. internal/service
	// reads it once per service, at open, and its notify.go owns what is
	// sent, the record, and what is not covered.
	KeyNotifyCommand Key = "notify.command"

	// KeyRunUsageBudgetOutputTokens is "run_usage_budget.output_tokens": the
	// per-run usage budget, a whole number of output tokens one run's own
	// agent calls may report generating before the run parks, where zero, the
	// default, is no budget. A negative one is refused.
	//
	// It is one count and not four, and the count is output tokens. A prompt
	// is reported across three counts, input and the two cache counts, which
	// each adapter reads from fields of its own agent's report, and this
	// build establishes nothing about how those fields divide a prompt
	// between them, so a budget on input tokens alone could bound different
	// work under different agents and a budget on a sum could count one token
	// twice. Output tokens are the one count that names what a call
	// generated, and that is what grows with every round a run spends.
	//
	// It is global-only, a P7 decision on the usage ceiling's grounds: the
	// budget decides how much of the operator's account one run may spend,
	// so a repository has no standing to raise or lift it in either copy. A
	// repository file naming it is refused at parse time.
	//
	// It is not the usage ceiling. It is a run-level bound internal/graph
	// owns beside the run-wide step budget: a run records it when it starts,
	// carries it in its checkpoint's counters, is compared against it before
	// every node, and parks when it is reached, where the ceiling refuses the
	// next call across the home and holds for a person. The comparison is
	// against the run's own rows of the agent invocation record, read at each
	// comparison; internal/service's runbudget.go owns that reading and what
	// it leaves open. PRD section 10's keys table has its row.
	KeyRunUsageBudgetOutputTokens Key = "run_usage_budget.output_tokens"

	// KeyRemoteHosts is "remote_hosts": the other machines workers may run
	// on, PRD section 8's "Workers on another machine" and section 10's row.
	// Each entry is a RemoteHost: the host as the operator's SSH
	// configuration names it, the absolute remote root on it, the absolute
	// path of the binary its bridge runs, and optionally the three placement
	// limits a dispatch there needs. Empty, the default, runs every worker on
	// this machine. The list holds at most MaxRemoteHosts entries, the first
	// increment's one host.
	//
	// It is global-only, a P7 decision: each field chooses where, and as
	// what, a process starts with the operator's credentials, so no
	// repository copy, trusted or pushed, is the document it could be read
	// from, and a repository file naming it is refused at parse time.
	//
	// A remote host requires KeySessionProvider to be herdr, and a document
	// that names a host under any other provider, the default included, is
	// refused at parse time naming this key. Both keys are global-only, so
	// the operator's document is the one that decides both, and that check
	// is complete there.
	//
	// What parsing does not check: that the host is reachable, that it runs
	// macOS (the first increment's other limit, PRD section 12), that the
	// root is empty or absent at first use, or that the binary is there.
	// Those need the host, and a bridge reads them there. Nothing in this
	// build reads the key: internal/service refuses a home whose file sets
	// it, an empty list included, until it can run a worker there (its
	// unhonored.go).
	KeyRemoteHosts Key = "remote_hosts"
	// KeyPlacementLocal is "placement.local": the placement limits for this
	// machine, on the terms a remote host's are, PlacementLimits. Unset
	// limits nothing, which is the default, so a home with no remote host
	// places exactly as it did before the key existed. It is global-only on
	// KeyRemoteHosts' terms: the limits are the operator's about their own
	// machines. Nothing in this build reads it, and internal/service refuses
	// a home whose file sets it until a dispatch applies it (its
	// unhonored.go).
	KeyPlacementLocal Key = "placement.local"
	// KeyPlacementArch is "placement.arch": the architectures a repository's
	// tasks may run on, each spelled as Architectures lists it. Empty, the
	// default, allows every one. It can only narrow which machines a task
	// may go to and never adds one.
	//
	// It is trusted-only (P7): a pushed copy setting it is dropped at the
	// merge and reported as a rejection, so a branch cannot choose where its
	// own work runs. The class also admits the operator's own file, as it
	// does for every trusted-only key, so an operator who sets it there
	// narrows every repository of the home whose trusted copy does not set
	// it; a trusted copy that sets it replaces the operator's list, as a
	// repository layer replaces any list.
	//
	// Nothing in this build places by it, so internal/service refuses it
	// wherever it reads a document that sets it, the home's own file and a
	// repository's trusted copy, naming the key (its unhonored.go, which also
	// owns what that refusal leaves uncovered), until a dispatch places by it
	// (PRD section 8's "Which machine a task runs on").
	KeyPlacementArch Key = "placement.arch"
	// KeyRemoteDisconnectGrace is "remote_disconnect_grace": how long a
	// bridge whose transport is gone keeps the workers it started before it
	// ends them and then itself, PRD section 8's ownership row. It is
	// global-only on KeyRemoteHosts' terms. A duration between
	// MinRemoteDisconnectGrace and MaxRemoteDisconnectGrace, refused rather
	// than clamped outside them; the default and both limits are the PRD's,
	// chosen, not measured. It is not a loop bound: it ends no task and
	// parks nothing, and it bounds nothing once the bridge itself is gone.
	// Nothing in this build reads it, and internal/service refuses a home
	// whose file sets it (its unhonored.go).
	KeyRemoteDisconnectGrace Key = "remote_disconnect_grace"
	// KeyHousekeepingPaths is "housekeeping.paths": the path patterns naming
	// a repository's agent context, the files a housekeeping pass keeps true
	// and the only ones it may change, PRD section 6's housekeeping. It is
	// trusted-only, because a branch that could widen the list could let a
	// pass edit anything. A repository's value replaces the default list, and
	// an empty list turns housekeeping off for that repository.
	KeyHousekeepingPaths Key = "housekeeping.paths"
	// KeyHousekeepingInterval is "housekeeping.interval": how long after one
	// housekeeping pass of a repository was created the next may fall due,
	// on the terms PRD section 6's housekeeping gives. Zero, the default,
	// makes no pass due anywhere, and does not stop the coordinator creating
	// a pass when asked for one. It is global-only, because it decides when
	// the program asks for agent work to start with the operator's
	// credentials, so a repository file naming it is refused at parse time
	// (P7). A value that is not zero lies between MinHousekeepingInterval and
	// MaxHousekeepingInterval, refused rather than clamped outside them. It
	// is not a loop bound: it stops nothing and parks nothing. The service
	// refuses an operator's file whose value is not zero for as long as it
	// runs no due check (its unhonored.go).
	KeyHousekeepingInterval Key = "housekeeping.interval"
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
// value it takes when nobody sets it, how a JSON value becomes that value,
// and where the value lands in a Config. A TrustGlobalLoosens key's order is
// its value type's, through ordered, so the row names it by its decoder.
// Every fact about a key lives in exactly one row, per P14.
type spec struct {
	key    Key
	trust  Trust
	def    any
	decode func(k Key, v any) (any, error)
	set    func(c *Config, v any)
}

// ordered is what a TrustGlobalLoosens key's value type implements: the order
// its values are compared in, which the type owns rather than the row, so the
// order has one owner. Delivery's is the policy table's, through Lighter. A
// test holds every row of that class to a default that implements it, and
// ResolveRun refuses any compared value that does not implement it.
type ordered interface {
	// looserThan reports whether the value is looser than beneath, a value
	// of the same type.
	looserThan(beneath any) bool
}

// specs is the schema. Order is the order keys are applied and the order
// Keys reports them in; it does not affect the resolved value, because each
// key lands in a distinct field.
var specs = []spec{
	{KeyAgent, TrustCommands, []string{DefaultAgent}, decodeAgent,
		func(c *Config, v any) { c.Agent = cloneSlice(v.([]string)) }},
	{KeyRoles, TrustCommands, defaultRoles(), decodeRoles,
		func(c *Config, v any) { c.Roles = resolvedRoles(v) }},
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
	{KeyStallHoldCount, TrustGlobal, DefaultStallHoldCount, decodeStallHoldCount,
		func(c *Config, v any) { c.StallHoldCount = v.(int) }},
	{KeyRelaunchAttempts, TrustGlobal, DefaultRelaunchAttempts, decodeRelaunchAttempts,
		func(c *Config, v any) { c.RelaunchAttempts = v.(int) }},
	{KeyStopGrace, TrustGlobal, DefaultStopGrace, decodeStopGrace,
		func(c *Config, v any) { c.StopGrace = v.(time.Duration) }},
	{KeyCommandTimeout, TrustGlobal, DefaultCommandTimeout, decodeCommandTimeout,
		func(c *Config, v any) { c.CommandTimeout = v.(time.Duration) }},
	{KeyGitDeadline, TrustGlobal, DefaultGitDeadline, decodeGitDeadline,
		func(c *Config, v any) { c.GitDeadline = v.(time.Duration) }},
	{KeyGHDeadline, TrustGlobal, DefaultGHDeadline, decodeGHDeadline,
		func(c *Config, v any) { c.GHDeadline = v.(time.Duration) }},
	{KeyCommitFixMessage, TrustPushed, DefaultCommitFixMessage, decodeFixMessage,
		func(c *Config, v any) { c.CommitFixMessage = v.(string) }},
	{KeyAllowPushedCommands, TrustTrusted, false, decodeBool,
		func(c *Config, v any) { c.AllowPushedCommands = v.(bool) }},
	{KeyAutonomyMerge, TrustGlobal, []string(nil), decodeAutonomyMerge,
		func(c *Config, v any) { c.AutonomyMerge = cloneSlice(v.([]string)) }},
	{KeyDelivery, TrustGlobalLoosens, DefaultDelivery, decodeDelivery,
		func(c *Config, v any) { c.Delivery = v.(Delivery) }},
	{KeyAllowStandingConsent, TrustTrusted, false, decodeBool,
		func(c *Config, v any) { c.AllowStandingConsent = v.(bool) }},
	{KeySessionProvider, TrustGlobal, DefaultSessionProvider, decodeSessionProvider,
		func(c *Config, v any) { c.SessionProvider = v.(SessionProvider) }},
	{KeyUsageCeilingWindow, TrustGlobal, DefaultUsageCeilingWindow, decodeUsageCeilingWindow,
		func(c *Config, v any) { c.UsageCeiling.Window = v.(time.Duration) }},
	{KeyUsageCeilingInputTokens, TrustGlobal, int64(0), decodeTokenCeiling,
		func(c *Config, v any) { c.UsageCeiling.InputTokens = v.(int64) }},
	{KeyUsageCeilingOutputTokens, TrustGlobal, int64(0), decodeTokenCeiling,
		func(c *Config, v any) { c.UsageCeiling.OutputTokens = v.(int64) }},
	{KeyUsageCeilingCacheReadTokens, TrustGlobal, int64(0), decodeTokenCeiling,
		func(c *Config, v any) { c.UsageCeiling.CacheReadTokens = v.(int64) }},
	{KeyUsageCeilingCacheCreationTokens, TrustGlobal, int64(0), decodeTokenCeiling,
		func(c *Config, v any) { c.UsageCeiling.CacheCreationTokens = v.(int64) }},
	{KeyNotifyCommand, TrustGlobal, "", decodeCommand,
		func(c *Config, v any) { c.NotifyCommand = v.(string) }},
	{KeyRunUsageBudgetOutputTokens, TrustGlobal, int64(0), decodeRunUsageBudget,
		func(c *Config, v any) { c.RunUsageBudget = v.(int64) }},
	{KeyRemoteHosts, TrustGlobal, []RemoteHost(nil), decodeRemoteHosts,
		func(c *Config, v any) { c.RemoteHosts = cloneSlice(v.([]RemoteHost)) }},
	{KeyPlacementLocal, TrustGlobal, PlacementLimits{}, decodePlacementLocal,
		func(c *Config, v any) { c.PlacementLocal = v.(PlacementLimits) }},
	{KeyPlacementArch, TrustTrusted, []string(nil), decodePlacementArch,
		func(c *Config, v any) { c.PlacementArch = cloneSlice(v.([]string)) }},
	{KeyRemoteDisconnectGrace, TrustGlobal, DefaultRemoteDisconnectGrace, decodeRemoteDisconnectGrace,
		func(c *Config, v any) { c.RemoteDisconnectGrace = v.(time.Duration) }},
	{KeyHousekeepingPaths, TrustTrusted, defaultHousekeepingPaths, decodeHousekeepingPaths,
		func(c *Config, v any) { c.HousekeepingPaths = cloneSlice(v.(PatternSet)) }},
	{KeyHousekeepingInterval, TrustGlobal, DefaultHousekeepingInterval, decodeHousekeepingInterval,
		func(c *Config, v any) { c.HousekeepingInterval = v.(time.Duration) }},
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
