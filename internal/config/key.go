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
	// KeyCommitFixMessage is "commit.fix_message": the fix commit subject
	// template.
	KeyCommitFixMessage Key = "commit.fix_message"
	// KeyAllowPushedCommands is "allow_pushed_commands": the opt-out that lets
	// a pushed branch set the command and agent keys.
	//
	// PRD section 10 describes this opt-out in prose but does not give it a
	// row in the schema table, so the name was chosen here. It is a settled
	// decision, not an open question: the name says exactly what it permits,
	// and what the PRD still owes is a schema row, not a rename.
	KeyAllowPushedCommands Key = "allow_pushed_commands"
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
	{KeyCommitFixMessage, TrustPushed, DefaultCommitFixMessage, decodeFixMessage,
		func(c *Config, v any) { c.CommitFixMessage = v.(string) }},
	{KeyAllowPushedCommands, TrustTrusted, false, decodeBool,
		func(c *Config, v any) { c.AllowPushedCommands = v.(bool) }},
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
