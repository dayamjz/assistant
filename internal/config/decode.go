package config

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

// The decoders below turn one JSON value into one typed configuration value.
// Each refuses rather than coercing, and every refusal names the key and the
// value it was given, which is what PRD section 10 requires of a parse-time
// failure. None of them consults another key, so a document is validated the
// same way whatever else it contains.

func asString(k Key, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", keyErr(k, v, "expected a string")
	}
	return s, nil
}

func asBool(k Key, v any) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, keyErr(k, v, "expected true or false")
	}
	return b, nil
}

func asInt(k Key, v any) (int, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, keyErr(k, v, "expected a whole number")
	}
	i, err := n.Int64()
	if err != nil {
		return 0, keyErr(k, v, "expected a whole number")
	}
	if int64(int(i)) != i {
		return 0, keyErr(k, v, "the number does not fit in an int on this platform")
	}
	return int(i), nil
}

func asList(k Key, v any) ([]any, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, keyErr(k, v, "expected a list")
	}
	return l, nil
}

// checkPrintable refuses control and Unicode format characters in text that
// ends up in a shell command or in a prompt, because a newline in a command
// line, or a character that reorders how text renders, changes what a reader
// believes they approved. A tab is allowed: it is ordinary whitespace in a
// command line and cannot hide anything on its own.
func checkPrintable(k Key, v any, what, s string) error {
	for i, r := range s {
		if r == '\t' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return keyErr(k, v, "the "+what+" contains a control or formatting character at byte "+itoa(i))
		}
	}
	return nil
}

func checkRunes(k Key, v any, what, s string, limit int) error {
	if n := len([]rune(s)); n > limit {
		return keyErr(k, v, "the "+what+" is "+itoa(n)+" characters and the limit is "+itoa(limit))
	}
	return nil
}

// unknownField returns the first field of an object that is not one of the
// allowed ones, in sorted order. The order is the point: Go randomizes map
// iteration, so an entry with two unrecognized fields would otherwise name a
// different one on each run, and this package reports the same fault every
// time for the same document.
func unknownField(m map[string]any, allowed ...string) (string, bool) {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !slices.Contains(allowed, name) {
			return name, true
		}
	}
	return "", false
}

// listEntry locates one element of a bounded list while that element is being
// decoded. Every refusal raised about an element is built through it or names
// its label, so the refusal says which element it came from and carries that
// element rather than the list it sits in. A refusal about the list itself,
// such as one that exceeds its length limit, is not built through a listEntry
// and names the list.
type listEntry struct {
	key   Key
	noun  string
	index int
	value any
}

// label names the element the way a refusal refers to it, counting from zero.
func (e listEntry) label() string { return e.noun + " " + itoa(e.index) }

// err refuses the element as a whole, naming the element as the value.
func (e listEntry) err(detail string) error {
	return keyErr(e.key, e.value, e.label()+" "+detail)
}

// fieldErr refuses one field of the element, naming that field's own value
// rather than the element around it.
func (e listEntry) fieldErr(value any, detail string) error {
	return keyErr(e.key, value, e.label()+" "+detail)
}

// of names one field of an element for the checks that build their own
// message, so "guidance" becomes "guidance of rule 3".
func (e listEntry) of(what string) string { return what + " of " + e.label() }

func checkLen(k Key, v any, what string, n, limit int) error {
	if n > limit {
		return keyErr(k, v, "there are "+itoa(n)+" "+what+" and the limit is "+itoa(limit))
	}
	return nil
}

// decodeCommand accepts a shell command line. The empty string is a legitimate
// value and means the repository has no command of its own for that stage; it
// is distinct from the key being absent, which inherits the layer below.
func decodeCommand(k Key, v any) (any, error) {
	s, err := asString(k, v)
	if err != nil {
		return nil, err
	}
	if err := checkPrintable(k, v, "command", s); err != nil {
		return nil, err
	}
	if err := checkRunes(k, v, "command", s, MaxCommandRunes); err != nil {
		return nil, err
	}
	return s, nil
}

// decodeAgent accepts one agent name or an ordered fallback list of them. Each
// entry is a command word optionally followed by flags, split on whitespace
// with no quoting or escaping interpreted. Entries naming a reserved flag are
// refused here rather than ignored later, per PRD section 10.
func decodeAgent(k Key, v any) (any, error) {
	var raw []string
	switch t := v.(type) {
	case string:
		raw = []string{t}
	case []any:
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, keyErr(k, v, "every entry must be a string")
			}
			raw = append(raw, s)
		}
	default:
		return nil, keyErr(k, v, "expected an agent name or a list of agent names")
	}
	if len(raw) == 0 {
		return nil, keyErr(k, v, "an agent list may not be empty; omit the key to use the default")
	}
	if err := checkLen(k, v, "agents", len(raw), MaxAgents); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		if strings.TrimSpace(entry) == "" {
			return nil, keyErr(k, v, "an agent entry may not be blank")
		}
		if err := checkPrintable(k, v, "agent entry", entry); err != nil {
			return nil, err
		}
		if err := checkRunes(k, v, "agent entry", entry, MaxCommandRunes); err != nil {
			return nil, err
		}
		for _, tok := range strings.Fields(entry) {
			flag, _, _ := strings.Cut(tok, "=")
			for _, reserved := range ReservedAgentFlags {
				if flag == reserved {
					return nil, keyErr(k, v, "the agent flag "+quote(reserved)+" is reserved because the run manages it")
				}
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// decodeFixRounds accepts a per-stage automatic fix attempt limit. Zero is a
// legitimate value and means every finding at that stage goes to the operator.
func decodeFixRounds(k Key, v any) (any, error) {
	n, err := asInt(k, v)
	if err != nil {
		return nil, err
	}
	if n < 0 {
		return nil, keyErr(k, v, "a fix round limit may not be negative; zero sends every finding to you")
	}
	if n > MaxFixRounds {
		return nil, keyErr(k, v, "a fix round limit may be at most "+itoa(MaxFixRounds))
	}
	return n, nil
}

// decodeRunBudget accepts the total node executions allowed per run. Zero is
// refused: a run that may execute nothing cannot report anything about the
// change, and a budget of zero is far more likely a mistake than a request for
// that.
func decodeRunBudget(k Key, v any) (any, error) {
	n, err := asInt(k, v)
	if err != nil {
		return nil, err
	}
	if n < 1 {
		return nil, keyErr(k, v, "a run budget must allow at least one node execution")
	}
	if n > MaxRunBudget {
		return nil, keyErr(k, v, "a run budget may be at most "+itoa(MaxRunBudget))
	}
	return n, nil
}

func decodeBool(k Key, v any) (any, error) {
	b, err := asBool(k, v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// decodeChecksTimeout accepts a Go duration string such as "168h". A number is
// refused rather than guessed at, because the unit a bare number would mean is
// exactly the thing the reader would have to assume.
func decodeChecksTimeout(k Key, v any) (any, error) {
	s, err := asString(k, v)
	if err != nil {
		return nil, keyErr(k, v, `expected a duration string such as "168h"`)
	}
	d, parseErr := time.ParseDuration(s)
	if parseErr != nil {
		return nil, keyErr(k, v, "not a duration: "+parseErr.Error())
	}
	if d <= 0 {
		return nil, keyErr(k, v, "an idle timeout must be positive")
	}
	if d > MaxChecksTimeout {
		return nil, keyErr(k, v, "an idle timeout may be at most "+MaxChecksTimeout.String())
	}
	return d, nil
}

// decodeFixMessage accepts the fix commit subject template. It must contain
// the summary placeholder, because a template without it renders the same
// subject for every fix, which makes a history of fix commits unreadable.
func decodeFixMessage(k Key, v any) (any, error) {
	s, err := asString(k, v)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(s, FixMessagePlaceholder) {
		return nil, keyErr(k, v, "the template must contain "+quote(FixMessagePlaceholder))
	}
	if err := checkSubjectText("template", s); err != nil {
		return nil, err
	}
	if err := checkRunes(k, v, "template", s, MaxCommitSubjectRunes); err != nil {
		return nil, err
	}
	return s, nil
}

// decodePatterns compiles a list of path patterns, reporting the first that is
// malformed with the pattern text in the message.
func decodePatterns(k Key, v any, where, what string, limit int) (PatternSet, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, keyErr(k, v, "expected a list of "+what)
	}
	if err := checkLen(k, v, what, len(l), limit); err != nil {
		return nil, err
	}
	out := make(PatternSet, 0, len(l))
	for i, e := range l {
		entry := listEntry{key: k, noun: where + "pattern", index: i, value: e}
		s, isString := e.(string)
		if !isString {
			return nil, entry.err("must be a string")
		}
		p, perr := ParsePattern(s)
		if perr != nil {
			return nil, entry.err("is malformed: " + perr.Error())
		}
		out = append(out, p)
	}
	return out, nil
}

// decodeIgnorePatterns accepts the ignore list. An empty list is a legitimate
// value and means this layer ignores nothing, which is not the same as the key
// being absent and inheriting the layer below.
func decodeIgnorePatterns(k Key, v any) (any, error) {
	return decodePatterns(k, v, "", "ignore patterns", MaxIgnorePatterns)
}

// decodePathRules accepts the path-scoped review rules, in declared order.
// Each rule states its own scope and its own guidance, and both are required:
// guidance with no scope would be repository-wide review guidance wearing a
// scoped rule's clothes.
func decodePathRules(k Key, v any) (any, error) {
	l, err := asList(k, v)
	if err != nil {
		return nil, err
	}
	if err := checkLen(k, v, "path rules", len(l), MaxPathRules); err != nil {
		return nil, err
	}
	out := make([]PathRule, 0, len(l))
	for i, e := range l {
		entry := listEntry{key: k, noun: "rule", index: i, value: e}
		m, ok := e.(map[string]any)
		if !ok {
			return nil, entry.err("must be an object with a paths list and guidance")
		}
		if field, found := unknownField(m, "paths", "guidance"); found {
			return nil, entry.err("has an unrecognized field " + quote(field))
		}
		rawPaths, ok := m["paths"]
		if !ok {
			return nil, entry.err("has no paths")
		}
		paths, perr := decodePatterns(k, rawPaths, entry.label()+" ", "paths in "+entry.label(), MaxPathRulePaths)
		if perr != nil {
			return nil, perr
		}
		if len(paths) == 0 {
			return nil, entry.err("must name at least one path; a rule with no scope is not a scoped rule")
		}
		guidance, gok := m["guidance"]
		if !gok {
			return nil, entry.err("has no guidance")
		}
		text, gerr := asString(k, guidance)
		if gerr != nil {
			return nil, entry.fieldErr(guidance, "guidance must be a string")
		}
		if strings.TrimSpace(text) == "" {
			return nil, entry.err("has empty guidance")
		}
		if err := checkPrintable(k, guidance, entry.of("guidance"), text); err != nil {
			return nil, err
		}
		if err := checkRunes(k, guidance, entry.of("guidance"), text, MaxGuidanceRunes); err != nil {
			return nil, err
		}
		out = append(out, PathRule{Paths: paths, Guidance: text})
	}
	return out, nil
}

// decodeOwnership accepts the document ownership list. A subject may be
// claimed once: P14 gives every fact exactly one owner, and two documents
// claiming one subject is that principle broken in configuration rather than
// in prose.
func decodeOwnership(k Key, v any) (any, error) {
	l, err := asList(k, v)
	if err != nil {
		return nil, err
	}
	if err := checkLen(k, v, "ownership entries", len(l), MaxOwnership); err != nil {
		return nil, err
	}
	out := make([]Ownership, 0, len(l))
	seen := make(map[string]int, len(l))
	for i, e := range l {
		entry := listEntry{key: k, noun: "entry", index: i, value: e}
		m, ok := e.(map[string]any)
		if !ok {
			return nil, entry.err("must be an object with a subject and a document")
		}
		if field, found := unknownField(m, "subject", "document"); found {
			return nil, entry.err("has an unrecognized field " + quote(field))
		}
		subject, serr := ownershipField(entry, m, "subject")
		if serr != nil {
			return nil, serr
		}
		document, derr := ownershipField(entry, m, "document")
		if derr != nil {
			return nil, derr
		}
		if prev, dup := seen[subject]; dup {
			return nil, entry.err("claims subject " + quote(subject) +
				" which entry " + itoa(prev) + " already owns; a subject has exactly one owner")
		}
		seen[subject] = i
		out = append(out, Ownership{Subject: subject, Document: document})
	}
	return out, nil
}

func ownershipField(entry listEntry, m map[string]any, field string) (string, error) {
	raw, ok := m[field]
	if !ok {
		return "", entry.err("has no " + field)
	}
	s, err := asString(entry.key, raw)
	if err != nil {
		return "", entry.fieldErr(raw, field+" must be a string")
	}
	if strings.TrimSpace(s) == "" {
		return "", entry.fieldErr(raw, "has an empty "+field)
	}
	if err := checkPrintable(entry.key, raw, entry.of(field), s); err != nil {
		return "", err
	}
	if err := checkRunes(entry.key, raw, entry.of(field), s, MaxSubjectRunes); err != nil {
		return "", err
	}
	return s, nil
}
