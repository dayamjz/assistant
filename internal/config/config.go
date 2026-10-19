package config

import (
	"slices"
	"strings"
	"time"
	"unicode"
)

// Config is a fully resolved configuration. Every field holds a value: a key
// nobody set holds the default from the schema in PRD section 10. There is no
// unset state here, because resolution is the point at which absence becomes a
// documented default.
//
// Build one with Resolve. The zero Config is not a usable configuration and in
// particular is not the defaults; Defaults returns those.
type Config struct {
	// Agent is the agent to run, or an ordered fallback list. It is resolved
	// against what is actually runnable at run start by the caller, not here.
	Agent []string
	// Commands are the repository's own checks.
	Commands Commands
	// FixRounds is the per-stage automatic fix attempt limit.
	FixRounds FixRounds
	// RunBudget is the total node executions allowed per run.
	RunBudget int
	// IgnorePatterns are paths excluded from review and documentation checks.
	IgnorePatterns PatternSet
	// ReviewPathRules is extra review guidance scoped to paths, in declared
	// order.
	ReviewPathRules []PathRule
	// JudgePaths are the patterns naming the files the repository treats as
	// deciding whether a check passes. A file counts only if a pattern here
	// matches it; nothing else about it is consulted. PRD section 5's "A check does not judge its own edits" is the rule they
	// feed, and internal/stages applies it. An empty set turns that rule off.
	JudgePaths PatternSet
	// DocumentOwnership says which document owns which subject. Subjects are
	// unique across the list.
	DocumentOwnership []Ownership
	// SuppressProjectInstructions asks every agent to ignore the repository's
	// own instruction files. Whether the resolved agent has a mechanism for
	// that is not decided here: internal/pipeline refuses to build a run that
	// asks for it against an adapter that has not declared the capability,
	// which is before any agent is launched.
	SuppressProjectInstructions bool
	// NoCI is a positive declaration that the repository has no checks. Only
	// this makes an empty check list count as green.
	NoCI bool
	// ChecksTimeout is the idle timeout for checks, re-armed by the caller
	// whenever the base branch advances.
	ChecksTimeout time.Duration
	// SessionReuse keeps one durable fixer session per run. Review turns are
	// fresh regardless, per P4, which is the reviewing caller's rule and not
	// enforced by this field.
	SessionReuse bool
	// SupervisionBeaconInterval is how often the supervisor loop rewrites
	// its liveness beacon. The staleness window and the first-beacon wait
	// are internal/supervisor's multiples of it, never values of their own.
	SupervisionBeaconInterval time.Duration
	// EscalationWindow is how long a terminal may stay unchanged, with
	// positive evidence of progress, before the supervisor surfaces it
	// anyway. The loop measures it from the marker's window start.
	EscalationWindow time.Duration
	// RecheckWindow is how long a declared external wait may sit idle before
	// it surfaces, measured the same way.
	RecheckWindow time.Duration
	// StallInspectionCount is how many surfaces of the same unchanged screen
	// precede the one that demands a deeper inspection, read by the loop into
	// supervisor.Stall.InspectAt.
	StallInspectionCount int
	// RelaunchAttempts is how many times reconciliation at service start
	// relaunches a task that resolves Failed with its terminal gone, counted
	// from its attempt rows, before the task holds for a person instead.
	// KeyRelaunchAttempts says why it is a task-level bound and not a loop
	// bound, and who decides a retry past it.
	RelaunchAttempts int
	// DecisionModel is the mode of the decision-model checks PRD section 5
	// describes. Whether a request is sent at all also needs the provider's
	// key in the operator's environment, which this package never reads.
	DecisionModel DecisionModel
	// CommitFixMessage is the subject line template for fix commits. Render it
	// with RenderFixMessage. It decides the subject only; KeyCommitFixMessage
	// says what else a fix commit carries.
	CommitFixMessage string
	// AllowPushedCommands is the opt-out that lets a pushed branch set the
	// command and agent keys. It is itself never taken from a pushed branch.
	AllowPushedCommands bool
	// Delivery is the delivery posture of the repository's delivery tasks,
	// PRD section 6. A Config from Resolve holds a member of Delivery's
	// closed set. internal/tasks records it on a task at intake, and that
	// record, not a later read of this field, decides the task's path.
	Delivery Delivery
	// AutonomyMerge is the repositories, as the code host names them,
	// where the program may merge a run that ended checks-passed. Empty
	// means it merges nothing. Ask MergesIn rather than searching it.
	AutonomyMerge []string
	// AllowStandingConsent is whether a run of the repository may be put
	// under standing consent by the per-run flag. It is never taken from a
	// pushed branch; KeyAllowStandingConsent says what it allows and what it
	// does not.
	AllowStandingConsent bool
	// SessionProvider is the terminal session provider the service starts
	// workers through. A Config from Resolve holds a member of the closed
	// set; KeySessionProvider says why only the operator's file sets it.
	SessionProvider SessionProvider
	// UsageCeiling is the ceiling on recorded input tokens that may start
	// another agent invocation. InputTokens zero is no ceiling.
	// KeyUsageCeiling says who may set it and who reads it.
	UsageCeiling UsageCeiling
}

// UsageCeiling is KeyUsageCeiling's value: how far back recorded usage is
// summed, and the input-token count a later start is held to. Window is the
// text internal/machine's usage view reads, with the default filled in when
// a document set the key and named no window. InputTokens zero means no
// ceiling, including the zero value of this struct and the schema default.
type UsageCeiling struct {
	// Window is how far back the recorded input tokens are summed.
	Window string
	// InputTokens is the ceiling. Zero means the ceiling is off.
	InputTokens int64
}

// MergesIn reports whether repository, named as the code host names it, is
// one AutonomyMerge lists. The comparison ignores letter case, which is how
// internal/forge compares a specifier with the repository a provider
// resolves it to; an empty name is listed nowhere.
func (c Config) MergesIn(repository string) bool {
	if repository == "" {
		return false
	}
	for _, listed := range c.AutonomyMerge {
		if strings.EqualFold(listed, repository) {
			return true
		}
	}
	return false
}

// Commands are the repository's own checks. Each is a shell command line, and
// each is empty by default: an empty command means the stage has nothing of
// the repository's own to run, not that the stage is skipped.
type Commands struct {
	// Test is a targeted check for the change, not a full suite.
	Test string
	// Lint is static analysis. When empty, the document stage performs a
	// combined pass and routes its lint findings to the lint stage.
	Lint string
	// Format runs immediately before pushing.
	Format string
	// Document updates documentation the change made stale.
	Document string
}

// FixRounds is the per-stage limit on automatic fix attempts.
type FixRounds struct {
	// Review is the number of automatic fix rounds the review stage may take,
	// and it applies only to findings whose action is fix. An ask finding is
	// never eligible, so one holds the stage for a person whatever this value
	// is; that eligibility rule belongs to the review stage, not to this
	// package. Zero sends every review finding to the operator.
	Review int
	// Rebase is the rebase stage's attempt limit.
	Rebase int
	// Test is the test stage's attempt limit.
	Test int
	// Lint is the lint stage's attempt limit.
	Lint int
	// Checks is the checks stage's attempt limit.
	Checks int
}

// PathRule is review guidance scoped to a set of paths. Rules apply in
// declared order and each carries its own scope, so a scoped rule is never
// presented as repository-wide.
type PathRule struct {
	// Paths are the patterns this rule is scoped to. At least one is present.
	Paths PatternSet
	// Guidance is the extra instruction for files the rule matches.
	Guidance string
}

// Scope renders the rule's patterns as they were written, comma separated, for
// labelling the rule in a prompt or a report.
func (r PathRule) Scope() string { return strings.Join(r.Paths.Strings(), ", ") }

// Matches reports whether the rule applies to a repository-relative path.
func (r PathRule) Matches(filePath string) bool { return r.Paths.Matches(filePath) }

// Ownership names the one document that owns a subject. PRD principle P14
// gives every fact exactly one owner, so a subject may appear once across the
// whole list and a second claim on it is refused at parse time.
type Ownership struct {
	// Subject is what is owned, such as "the configuration schema".
	Subject string
	// Document is the path of the document that owns it.
	Document string
}

// The documented defaults, from the PRD section 10 schema. They are exported
// so a caller reporting "this run used the default" can name the value it
// means without restating it, which would be a second owner of the same fact.
const (
	// DefaultAgent is the single entry in the default agent list: resolve
	// whatever is runnable at run start.
	DefaultAgent = "auto"
	// DefaultFixRoundsReview is the review stage's automatic fix round limit.
	// It is one round over fix findings only, per decision 5 in PRD section
	// 14, which overrode a drafted default of zero.
	DefaultFixRoundsReview = 1
	// DefaultFixRoundsStage is the attempt limit for the rebase, test, lint,
	// and checks stages.
	DefaultFixRoundsStage = 3
	// DefaultRunBudget is the total node executions allowed per run.
	DefaultRunBudget = 40
	// DefaultChecksTimeout is the idle timeout for checks. On elapse the run
	// holds rather than ending.
	DefaultChecksTimeout = 168 * time.Hour
	// DefaultSessionReuse keeps one durable fixer session per run.
	DefaultSessionReuse = true
	// DefaultSupervisionBeaconInterval is how often the supervisor loop
	// rewrites its beacon when the operator's file says nothing. Chosen, not
	// measured: short enough that a stopped loop is noticed within a minute
	// at the staleness multiple, long enough that an idle service writes
	// almost nothing.
	DefaultSupervisionBeaconInterval = 10 * time.Second
	// DefaultEscalationWindow is PRD section 10's default for how long an
	// unchanged terminal with evidence of progress is absorbed. Chosen, not
	// measured.
	DefaultEscalationWindow = 30 * time.Minute
	// DefaultRecheckWindow is PRD section 10's default for how long a
	// declared external wait sits before it surfaces. Chosen, not measured.
	DefaultRecheckWindow = 2 * time.Hour
	// DefaultStallInspectionCount is how many surfaces of one unchanged
	// screen precede the one that demands a deeper inspection. Chosen, not
	// measured: the third surface of the same screen is a stall the first
	// two looks did not resolve.
	DefaultStallInspectionCount = 3
	// DefaultRelaunchAttempts is how many relaunches of a Failed task
	// reconciliation makes on its own when the operator's file says nothing.
	// Chosen, not measured: two restarts of a worker whose run failed is
	// enough to get past a transient and few enough that a worker which fails
	// the same way every time reaches a person within the hour.
	DefaultRelaunchAttempts = 2
	// DefaultDecisionModel is the decision-model mode a run gets when the
	// operator's file sets none: the checks run where the key is present and
	// what they find is a note.
	DefaultDecisionModel = DecisionEvaluate
	// DefaultCommitFixMessage is the subject line template for fix commits.
	// PRD section 10 records the default as "a template" without giving its
	// text; this is that text, and it is a conventional-commit subject.
	DefaultCommitFixMessage = "fix: " + FixMessagePlaceholder
	// DefaultDelivery is PRD section 10's default delivery posture: the gate.
	DefaultDelivery = DeliveryGated
	// DefaultSessionProvider is the reference provider, the one PRD section
	// 12's phase 1 names, so a home whose operator chose nothing runs its
	// workers exactly as it did before the key existed.
	DefaultSessionProvider = SessionProviderPipe
)

// SessionProvider names a terminal session provider. The set is closed:
// decoding refuses any other text, so a misspelled provider is refused where
// the operator wrote it rather than read as the default.
type SessionProvider string

const (
	// SessionProviderPipe is the reference provider: the worker is the
	// service's own child, read and written over pipes, and nothing can be
	// attached to.
	SessionProviderPipe SessionProvider = "pipe"
	// SessionProviderTmux hosts each worker in a tmux server of its own,
	// which a person can attach to, watch, and type into. It needs a tmux
	// binary on the service's PATH and an agent adapter that declares
	// terminal workers; internal/session's Tmux states both refusals.
	SessionProviderTmux SessionProvider = "tmux"
)

// sessionProviders is the closed set, in the order a refusal names them.
var sessionProviders = []SessionProvider{SessionProviderPipe, SessionProviderTmux}

// Recognized reports whether p is a member of the closed set.
func (p SessionProvider) Recognized() bool { return slices.Contains(sessionProviders, p) }

// DecisionModel is the mode of the decision-model checks. The three values
// below are the whole set: decoding refuses any other text, so a Config from
// Resolve holds one of them. A Config assembled by hand may hold the empty
// string, and RunsChecks reports false for it as for any value outside the
// set, so a mode nobody chose sends nothing.
type DecisionModel string

const (
	// DecisionEvaluate runs both checks and makes every result a note, so
	// nothing they say holds a run.
	DecisionEvaluate DecisionModel = "evaluate"
	// DecisionEnforce runs both checks as DecisionEvaluate does, except that a
	// file the screen flags becomes an ask.
	DecisionEnforce DecisionModel = "enforce"
	// DecisionOff runs neither check and sends nothing.
	DecisionOff DecisionModel = "off"
)

// RunsChecks reports whether this mode asks for the checks to run. It is true
// for DecisionEvaluate and DecisionEnforce and false for everything else,
// DecisionOff and the zero value included.
func (m DecisionModel) RunsChecks() bool {
	return m == DecisionEvaluate || m == DecisionEnforce
}

// Delivery is a delivery posture, PRD section 6's delivery modes as section
// 10's delivery key names them. The set is closed: decoding refuses any
// other text, and "direct", which section 12 defers, is refused by name.
//
// The set is ordered by how much of the gate a posture keeps, and the order
// is what Lighter answers: DeliveryGated keeps all of it and DeliveryLocal
// starts no run. That order is the whole of what "tighten, never loosen"
// compares at intake.
type Delivery string

const (
	// DeliveryGated is the gate: a delivery task's push starts the full
	// run, as every push does.
	DeliveryGated Delivery = "gated"
	// DeliveryLocal is the local posture: a delivery task's own push to the
	// gate starts no run, and the task holds until a person approves a
	// guarded fast-forward of their local default branch. Nothing leaves the
	// machine.
	DeliveryLocal Delivery = "local"
)

// deliveries is the closed set, strictest first: a posture's index is how
// much lighter it is than DeliveryGated.
var deliveries = []Delivery{DeliveryGated, DeliveryLocal}

// Deliveries returns the closed set, strictest first. The result is a copy.
func Deliveries() []Delivery { return slices.Clone(deliveries) }

// Recognized reports whether d is a member of the closed set.
func (d Delivery) Recognized() bool { return slices.Contains(deliveries, d) }

// Lighter reports whether d keeps less of the gate than than does. Both must
// be recognized; a posture outside the set is lighter than nothing and
// nothing is lighter than it, so a caller that compares one has already
// skipped the check that would have refused it.
func (d Delivery) Lighter(than Delivery) bool {
	a, b := slices.Index(deliveries, d), slices.Index(deliveries, than)
	return a >= 0 && b >= 0 && a > b
}

// ParseDelivery reads a posture from text, on the terms the delivery key is
// decoded on, so a posture stated anywhere but a configuration document - a
// coordinator's request at intake - is refused for the same reasons. A
// refusal is a *KeyError naming KeyDelivery.
func ParseDelivery(text string) (Delivery, error) {
	v, err := decodeDelivery(KeyDelivery, text)
	if err != nil {
		return "", err
	}
	return v.(Delivery), nil
}

// FixMessagePlaceholder is the one substitution a fix commit subject template
// may contain, and must contain. It is replaced by the agent's summary of the
// fix. internal/stages' fixSummary owns what that summary is, including that
// an agent's own conventional-commit prefix is removed rather than nested
// inside the template's, and its fixBody owns the commit body, which the
// template does not shape.
const FixMessagePlaceholder = "{summary}"

// Limits on what a document may declare. PRD section 10 requires bounded lists
// and prompt sections to have explicit limits but does not fix the numbers;
// these are this package's, chosen to be far above any legitimate use and
// still small enough that a generated or hostile file cannot turn into an
// unbounded prompt. Each is measured on the assembled value: rune counts on
// text, element counts on lists. Because a repository layer replaces a list
// rather than appending to it, a merged list is never longer than the longer
// of the two layers, and both were checked here.
const (
	// MaxAgents is the length of an agent fallback list.
	MaxAgents = 8
	// MaxCommandRunes is the length of one command line.
	MaxCommandRunes = 4096
	// MaxIgnorePatterns is the number of ignore patterns.
	MaxIgnorePatterns = 512
	// MaxPatternRunes is the length of one path pattern.
	MaxPatternRunes = 512
	// MaxPatternSegments is the number of "/"-separated segments one path
	// pattern may have. It bounds the matcher's work together with the length
	// of the path being tested.
	MaxPatternSegments = 64
	// MaxPathRules is the number of path-scoped review rules.
	MaxPathRules = 64
	// MaxPathRulePaths is the number of patterns one review rule may scope to.
	MaxPathRulePaths = 64
	// MaxGuidanceRunes is the length of one review rule's guidance text.
	MaxGuidanceRunes = 4096
	// MaxJudgePaths is the number of judge path patterns.
	MaxJudgePaths = 512
	// MaxOwnership is the number of document ownership entries.
	MaxOwnership = 512
	// MaxSubjectRunes is the length of an ownership subject or document path.
	MaxSubjectRunes = 512
	// MaxCommitSubjectRunes is the length of a fix commit subject line,
	// measured on the template and again on the rendered result.
	MaxCommitSubjectRunes = 72
	// MaxRunBudget is the largest total node execution budget a run may
	// declare. The budget exists to bound a run, so it is itself bounded.
	MaxRunBudget = 100000
	// MaxFixRounds is the largest per-stage automatic fix attempt limit.
	MaxFixRounds = 100
	// MaxChecksTimeout is the longest idle timeout for checks.
	MaxChecksTimeout = 8760 * time.Hour
	// MaxRelaunchAttempts is the most relaunches reconciliation may make of
	// one task on its own. The floor is zero, which is never relaunch a
	// Failed task and hold every one, and that is a value an operator may
	// choose. Above the ceiling a worker that fails the same way every time
	// is restarted for days before a person hears. Chosen, not measured.
	MaxRelaunchAttempts = 20
	// MinSupervisionBeaconInterval is the shortest beacon interval. Below it
	// the loop would spend its passes rewriting the beacon. Chosen, not
	// measured.
	MinSupervisionBeaconInterval = time.Second
	// MaxSupervisionBeaconInterval is the longest beacon interval. Above it a
	// stopped supervisor is invisible for most of an hour at the staleness
	// multiple. Chosen, not measured.
	MaxSupervisionBeaconInterval = 10 * time.Minute
	// MinEscalationWindow is PRD section 10's floor for escalation_window.
	// Chosen, not measured.
	MinEscalationWindow = time.Minute
	// MaxEscalationWindow is PRD section 10's ceiling for escalation_window:
	// above it a stalled worker is invisible for a day. Chosen, not measured.
	MaxEscalationWindow = 24 * time.Hour
	// MinRecheckWindow is PRD section 10's floor for recheck_window. Chosen,
	// not measured.
	MinRecheckWindow = time.Minute
	// MaxRecheckWindow is PRD section 10's ceiling for recheck_window: above
	// it a forgotten wait is invisible for a week. Chosen, not measured.
	MaxRecheckWindow = 168 * time.Hour
	// MinStallInspectionCount is the floor for stall_inspection_count: at one
	// every surface of a stall demands an inspection, and zero would make the
	// count mean nothing. Chosen, not measured.
	MinStallInspectionCount = 1
	// MaxStallInspectionCount is the ceiling for stall_inspection_count:
	// above it a stall surfaces more often than anyone would read before an
	// inspection is demanded. Chosen, not measured.
	MaxStallInspectionCount = 100
	// MaxAutonomyMerge is the number of repositories autonomy.merge may
	// list. Chosen, not measured.
	MaxAutonomyMerge = 64
	// DefaultUsageCeilingWindow is the window usage_ceiling sums when a
	// document sets the key and names no window, and the window the schema
	// default carries. It is the usage view's own default window; the test
	// that holds the two strings equal is what keeps them one fact.
	DefaultUsageCeilingWindow = "7d"
)

// defaultUsageCeiling is the schema default: the usage view's window and no
// ceiling, so a start is not refused for usage until an operator sets a count.
var defaultUsageCeiling = UsageCeiling{Window: DefaultUsageCeilingWindow}

// ReservedAgentFlags are agent flags configuration may not set, because each
// one would take over something the run manages itself: which session is
// resumed, what the output format is, whether the repository's own
// instructions reach the agent, and the permission mode and tool lists it
// runs under, which are agents.Grant's to decide from the invocation's role.
// Another flag that widens what an agent can do under a grant is not on the
// list; agents.Grant names that gap. PRD
// section 10 requires that reserved flags be rejected at load rather than
// silently ignored, and leaves the list to the implementation; this is that
// list, and it is matched on the flag token alone, so both "--resume x" and
// "--resume=x" are refused.
//
// The list is about managed behavior rather than about safety in general. It
// is not a sandbox: an agent command can still do whatever the agent itself
// allows, and containing that is the launching caller's problem.
var ReservedAgentFlags = []string{
	"--resume",
	"--continue",
	"--session-id",
	"--output-format",
	"--input-format",
	"--system-prompt",
	"--append-system-prompt",
	"--settings",
	"--permission-mode",
	"--dangerously-skip-permissions",
	"--allowedTools",
	"--allowed-tools",
	"--disallowedTools",
	"--disallowed-tools",
	"--tools",
	// Cursor / Grok / Codex grant and prompt delivery the adapters write
	// themselves.
	"--force",
	"--yolo",
	"--mode",
	"--always-approve",
	"--deny",
	"--allow",
	"--single",
	"--prompt-file",
	"--prompt-json",
	"--api-key",
	"--json",
	"--sandbox",
	"--approve-for-me",
	"--dangerously-bypass-approvals-and-sandbox",
	"--dangerously-bypass-hook-trust",
}

// Defaults returns the configuration a repository with no configuration at all
// resolves to. It is built from the same key table Resolve uses, so a default
// stated here and a default applied there cannot drift apart.
func Defaults() Config {
	var c Config
	for _, s := range specs {
		s.set(&c, s.def)
	}
	return c
}

// IsIgnored reports whether a repository-relative path is excluded from review
// and documentation checks, and returns the pattern that excluded it.
func (c Config) IsIgnored(filePath string) (Pattern, bool) {
	return c.IgnorePatterns.Match(filePath)
}

// RenderFixMessage substitutes summary into the fix commit subject template
// and returns the result. Both the summary and the rendered subject are
// validated the way the template was at load: length, and characters that
// could disguise what a commit says.
//
// A Config from Resolve carries a template that was already checked for those
// same things and for the placeholder, so on one of those a refusal here is
// about the summary, or about the length the two reach together. A Config
// assembled by hand gets no such check: the rendered subject is validated,
// which catches a bad character or an overlong template through the result,
// but a hand-set template missing the placeholder renders one constant subject
// and is not caught here.
//
// This checks text only. It does not stage, commit, or otherwise touch git.
func (c Config) RenderFixMessage(summary string) (string, error) {
	if err := checkSubjectText("summary", summary); err != nil {
		return "", err
	}
	if strings.TrimSpace(summary) == "" {
		return "", &KeyError{Key: KeyCommitFixMessage, Value: quote(summary), Detail: "the summary is empty"}
	}
	rendered := strings.ReplaceAll(c.CommitFixMessage, FixMessagePlaceholder, summary)
	if err := checkSubjectText("rendered subject", rendered); err != nil {
		return "", err
	}
	if n := len([]rune(rendered)); n > MaxCommitSubjectRunes {
		return "", &KeyError{
			Key:    KeyCommitFixMessage,
			Value:  quote(rendered),
			Detail: "the rendered subject is " + itoa(n) + " characters and the limit is " + itoa(MaxCommitSubjectRunes),
		}
	}
	return rendered, nil
}

// checkSubjectText refuses characters that could disguise what a commit
// subject says: line breaks, which would smuggle a body or a trailer into a
// subject, other control characters, and the Unicode format characters that
// reorder or hide text when rendered. It is a character check and nothing
// more: text that passes it can still be misleading in plain readable
// characters.
func checkSubjectText(what, s string) error {
	for i, r := range s {
		switch {
		case r == '\n' || r == '\r':
			return &KeyError{
				Key:    KeyCommitFixMessage,
				Value:  quote(s),
				Detail: "the " + what + " contains a line break at byte " + itoa(i) + " and a subject is one line",
			}
		case unicode.IsControl(r):
			return &KeyError{
				Key:    KeyCommitFixMessage,
				Value:  quote(s),
				Detail: "the " + what + " contains a control character at byte " + itoa(i),
			}
		case unicode.Is(unicode.Cf, r):
			return &KeyError{
				Key:    KeyCommitFixMessage,
				Value:  quote(s),
				Detail: "the " + what + " contains a formatting character at byte " + itoa(i) + " that can disguise the text",
			}
		}
	}
	return nil
}

// defaultJudgePatterns is the default of KeyJudgePaths, as PRD section 10's
// schema row writes it. That row owns the list; this is its text, compiled by
// the one matcher. It selects what these twenty-six patterns match and nothing
// more: Go and Python test files by the names listed, Java and Kotlin files
// named *Test, *.test.* and *.spec.* files, anything under a test, tests,
// __tests__, or testdata directory or a src/test directory, GitHub Actions
// workflows and a .gitlab-ci.yml, golangci-lint and ESLint configuration,
// ruff.toml, a file named Makefile, and the pytest, tox, jest, vitest and mocha
// configuration files named. Other conventions are not in it - CI files for
// other hosts, a Java or Kotlin test class not named *Test, pytest settings
// kept in pyproject.toml or setup.cfg, or a build file such as pom.xml or
// build.gradle - and a repository that keeps checks in such files names them
// in its trusted copy.
var defaultJudgePatterns = []string{
	"*_test.go",
	"*_test.py",
	"test_*.py",
	"*.test.*",
	"*.spec.*",
	"**/test/**",
	"**/tests/**",
	"**/__tests__/**",
	"**/testdata/**",
	".github/workflows/**",
	".golangci.yml",
	".golangci.yaml",
	".eslintrc*",
	"eslint.config.*",
	"ruff.toml",
	"Makefile",
	".gitlab-ci.yml",
	"*Test.java",
	"*Test.kt",
	"**/src/test/**",
	"conftest.py",
	"pytest.ini",
	"tox.ini",
	"jest.config.*",
	"vitest.config.*",
	".mocharc*",
}

// defaultJudgePaths is defaultJudgePatterns compiled. A pattern there that
// the matcher refused would be a defect in this file rather than input, so it
// panics at package initialization, where every test of this package meets
// it, instead of quietly shortening the default.
var defaultJudgePaths = func() PatternSet {
	out := make(PatternSet, 0, len(defaultJudgePatterns))
	for _, s := range defaultJudgePatterns {
		p, err := ParsePattern(s)
		if err != nil {
			panic("config: the default judge path " + quote(s) + " does not compile: " + err.Error())
		}
		out = append(out, p)
	}
	return out
}()
