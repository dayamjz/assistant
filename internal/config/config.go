package config

import (
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
	// DocumentOwnership says which document owns which subject. Subjects are
	// unique across the list.
	DocumentOwnership []Ownership
	// SuppressProjectInstructions asks every agent to ignore the repository's
	// own instruction files. Whether an agent has a verified mechanism for
	// that is checked by the caller that launches it, not here.
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
	// CommitFixMessage is the subject line template for fix commits. Render it
	// with RenderFixMessage.
	CommitFixMessage string
	// AllowPushedCommands is the opt-out that lets a pushed branch set the
	// command and agent keys. It is itself readable only from a trusted
	// origin.
	AllowPushedCommands bool
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
}

// FixRounds is the per-stage limit on automatic fix attempts.
type FixRounds struct {
	// Review is the number of automatic fix rounds the review stage may take,
	// and it applies only to findings whose action is fix. An ask finding is
	// never eligible, so one parks the stage whatever this value is; that
	// eligibility rule belongs to the review stage, not to this package. Zero
	// sends every review finding to the operator.
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
	// DefaultCommitFixMessage is the subject line template for fix commits.
	// PRD section 10 records the default as "a template" without giving its
	// text; this is that text, and it is a conventional-commit subject.
	DefaultCommitFixMessage = "fix: " + FixMessagePlaceholder
)

// FixMessagePlaceholder is the one substitution a fix commit subject template
// may contain, and must contain. It is replaced by the agent's summary of the
// fix.
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
)

// ReservedAgentFlags are agent flags configuration may not set, because each
// one would take over something the run manages itself: which session is
// resumed, what the output format is, whether the repository's own
// instructions reach the agent, and what permissions it runs under. PRD
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
