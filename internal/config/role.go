package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// The three role names the program assigns. review is the review stage,
// fix is a fix round, and worker is a task that names no other role. Any
// other name that validRoleName accepts is a worker role a task may name.
const (
	RoleReview = "review"
	RoleFix    = "fix"
	RoleWorker = "worker"
)

// ErrTaskRole reports that a task named a role it cannot run: review or
// fix, a name that is not a role name, or a name no configured role has.
// Intake wraps it so the task is not created.
var ErrTaskRole = errors.New("config: the task's role cannot be run")

// Role is one configured role: the agent it runs, and the model triple,
// effort, and context window when it names them. An empty Agent means the
// role uses the agent list. An empty Provider means the role names no model,
// and the agent uses its own. An empty Effort means the role names no
// effort. An empty ContextWindow means the role names no window, and the
// agent starts the named model at its own; a named one is always beside a
// complete triple, because an adapter declares its windows per model cell.
type Role struct {
	Agent    string
	Provider string
	Type     string
	Version  string
	Effort   string
	// ContextWindow is the role's "context", one of ContextWindows.
	ContextWindow string
	// Fallback names one backup with its own agent, model, effort, and
	// context window. It inherits none of them from the primary.
	Fallback *Role
}

// The context windows a role may name. standard is the model's standard
// window and 1m its one-million-token window. Which of them an adapter can
// start a model cell at is that adapter's declaration, not this package's.
const (
	ContextStandard = "standard"
	Context1M       = "1m"
)

// ContextWindows is the closed set a role's context window is drawn from, in
// the order the PRD lists it. A window outside it fails at parse time.
// Adding one is adding it here.
func ContextWindows() []string { return []string{ContextStandard, Context1M} }

// KnownContextWindow reports whether window is one of ContextWindows. The
// empty string is not a window: it is a role that named none.
func KnownContextWindow(window string) bool {
	for _, have := range ContextWindows() {
		if window == have {
			return true
		}
	}
	return false
}

// defaultFixAgent is the fixer used when no document names the fix role.
// It is the name configuration spells the Cursor adapter with.
const defaultFixAgent = "cursor"

// defaultRoles is the roles table a configuration resolves to when no
// document sets the key. The fixer is Cursor. Review and worker are unnamed,
// so they use the agent list.
func defaultRoles() Roles {
	return Roles{RoleFix: {Agent: defaultFixAgent}}
}

// Roles is the configured roles, keyed by role name. Nil and empty are the
// same before resolution: every role uses the agent list and that agent's
// own model. A resolved configuration names the fixer as Cursor unless the
// winning document named the fix role.
type Roles map[string]Role

// Equal reports whether a and b name the same roles. Nil and empty are equal.
func (a Roles) Equal(b Roles) bool {
	if len(a) != len(b) {
		return false
	}
	for name, role := range a {
		other, ok := b[name]
		if !ok || !role.equal(other) {
			return false
		}
	}
	return true
}

func (r Role) equal(other Role) bool {
	a, b := r.Fallback, other.Fallback
	r.Fallback, other.Fallback = nil, nil
	return r == other && ((a == nil && b == nil) || (a != nil && b != nil && a.equal(*b)))
}

// ForTask resolves the role a task may run under. An empty name is the
// worker role. review and fix are refused. A worker role that nobody
// configured is the agent list, with no model and no effort. Any other
// missing name is refused.
func (r Roles) ForTask(name string) (Role, error) {
	name = TaskRoleName(name)
	if name == RoleReview || name == RoleFix {
		return Role{}, fmt.Errorf("%w: a task cannot name %s", ErrTaskRole, name)
	}
	if err := validRoleName(name); err != nil {
		return Role{}, fmt.Errorf("%w: %w", ErrTaskRole, err)
	}
	role, ok := r[name]
	if !ok {
		if name == RoleWorker {
			return Role{}, nil
		}
		return Role{}, fmt.Errorf("%w: %q is not a configured role", ErrTaskRole, name)
	}
	return role, nil
}

// TaskRoleName is the role name a task's recorded role runs under: the name
// itself, or worker when the task named none. It decides nothing about
// whether that role can run; ForTask does.
func TaskRoleName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return RoleWorker
	}
	return name
}

// ModelCell is one provider, type, and version the role table allows.
type ModelCell struct {
	Provider string
	Type     string
	Version  string
}

// modelTable is the closed set a role's triple is drawn from. A provider,
// type, or version outside it fails at parse time. Adding one is adding a
// row here.
var modelTable = map[string]map[string][]string{
	"anthropic": {
		"opus":   {"5.5", "5", "4.8", "4.7", "4.6", "4.5"},
		"sonnet": {"5.5", "5", "4.6"},
		"haiku":  {"5.5", "4.5"},
		"fable":  {"5.1", "5"},
	},
	"openai": {
		"gpt": {"6-astra", "6.1-sol", "6-luna"},
	},
	"google": {
		"gemini": {"4-argon"},
		"flash":  {"3.8", "3.6"},
	},
	"xai": {
		"grok": {"4.7", "4.6", "4.5"},
	},
	"cursor": {
		"composer": {"2.5", "2"},
	},
}

// KnownModel reports whether provider, type, and version are one cell of
// the role table. A type is known only on its provider, and a version only
// in that type's cell.
func KnownModel(provider, typ, version string) bool {
	types, ok := modelTable[provider]
	if !ok {
		return false
	}
	for _, have := range types[typ] {
		if have == version {
			return true
		}
	}
	return false
}

// ModelCatalog returns every cell of the role table, in provider, type,
// and version order. It is the list an adapter's translation is held to.
func ModelCatalog() []ModelCell {
	providers := make([]string, 0, len(modelTable))
	for provider := range modelTable {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	var out []ModelCell
	for _, provider := range providers {
		types := make([]string, 0, len(modelTable[provider]))
		for typ := range modelTable[provider] {
			types = append(types, typ)
		}
		sort.Strings(types)
		for _, typ := range types {
			versions := append([]string(nil), modelTable[provider][typ]...)
			sort.Strings(versions)
			for _, version := range versions {
				out = append(out, ModelCell{Provider: provider, Type: typ, Version: version})
			}
		}
	}
	return out
}

func validRoleName(name string) error {
	if name == "" {
		return errors.New("a role name is required")
	}
	if utf8.RuneCountInString(name) > MaxRoleNameRunes {
		return fmt.Errorf("a role name may be at most %d characters", MaxRoleNameRunes)
	}
	for i, r := range name {
		switch {
		case i == 0 && (r < 'a' || r > 'z'):
			return fmt.Errorf("role %q must start with a lowercase letter", name)
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Errorf("role %q is not lowercase letters, digits, and hyphens", name)
		}
	}
	return nil
}

func decodeRoles(k Key, v any) (any, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, keyErr(k, v, "expected an object whose members are roles")
	}
	if len(obj) == 0 {
		return nil, keyErr(k, v, "a roles object may not be empty; omit the key to keep the default, which names the fixer as cursor")
	}
	if err := checkLen(k, v, "roles", len(obj), MaxRoles); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make(Roles, len(names))
	for _, name := range names {
		if err := validRoleName(name); err != nil {
			return nil, keyErr(k, v, err.Error())
		}
		role, err := decodeOneRole(k, v, name, obj[name])
		if err != nil {
			return nil, err
		}
		out[name] = role.clone()
	}
	return out, nil
}

func decodeOneRole(k Key, root any, name string, v any) (Role, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return Role{}, keyErr(k, root, "role "+quote(name)+" must be an object")
	}
	if extra, found := unknownField(obj, "agent", "provider", "type", "version", "effort", "context", "fallback"); found {
		return Role{}, keyErr(k, root, "role "+quote(name)+" has an unrecognized field "+quote(extra))
	}
	agent, err := roleString(k, root, name, obj, "agent")
	if err != nil {
		return Role{}, err
	}
	provider, err := roleString(k, root, name, obj, "provider")
	if err != nil {
		return Role{}, err
	}
	typ, err := roleString(k, root, name, obj, "type")
	if err != nil {
		return Role{}, err
	}
	version, err := roleString(k, root, name, obj, "version")
	if err != nil {
		return Role{}, err
	}
	effort, err := roleString(k, root, name, obj, "effort")
	if err != nil {
		return Role{}, err
	}
	window, err := roleString(k, root, name, obj, "context")
	if err != nil {
		return Role{}, err
	}
	if agent != "" {
		if strings.Fields(agent)[0] != agent {
			return Role{}, keyErr(k, root, "role "+quote(name)+" names an agent with arguments; a role names one agent and no arguments")
		}
		if agent == "auto" {
			return Role{}, keyErr(k, root, "role "+quote(name)+" names auto; name one adapter explicitly for each primary or backup")
		}
	}
	named := 0
	for _, part := range []string{provider, typ, version} {
		if part != "" {
			named++
		}
	}
	if named != 0 && named != 3 {
		return Role{}, keyErr(k, root, "role "+quote(name)+" must name a provider, a type, and a version together, or none of them")
	}
	if agent == "" && (named != 0 || effort != "") {
		return Role{}, keyErr(k, root, "role "+quote(name)+" names a provider, type, version, or effort and names no agent")
	}
	// A context needs a triple, and a triple needs an agent, so this also
	// refuses a context on a role that names no agent.
	if window != "" && named == 0 {
		return Role{}, keyErr(k, root, "role "+quote(name)+" names context "+quote(window)+" and no provider, type, and version; a context window is a window of the model the role names")
	}
	if named == 3 && !KnownModel(provider, typ, version) {
		return Role{}, keyErr(k, root, "role "+quote(name)+" names "+provider+" "+typ+" "+version+", which is not in the model table")
	}
	switch effort {
	case "", "low", "medium", "high":
	default:
		return Role{}, keyErr(k, root, "role "+quote(name)+" has effort "+quote(effort)+"; effort is low, medium, or high")
	}
	if window != "" && !KnownContextWindow(window) {
		return Role{}, keyErr(k, root, "role "+quote(name)+" has context "+quote(window)+"; context is "+strings.Join(ContextWindows(), " or "))
	}
	role := Role{Agent: agent, Provider: provider, Type: typ, Version: version, Effort: effort, ContextWindow: window}
	if value, ok := obj["fallback"]; ok {
		backup, valid := value.(map[string]any)
		if !valid {
			return Role{}, keyErr(k, root, "role "+quote(name)+" fallback must be an object")
		}
		if _, nested := backup["fallback"]; nested {
			return Role{}, keyErr(k, root, "role "+quote(name)+" allows only one backup")
		}
		parsed, err := decodeOneRole(k, root, name+" fallback", value)
		if err != nil {
			return Role{}, err
		}
		if agent == "" || parsed.Agent == "" {
			return Role{}, keyErr(k, root, "a fallback requires an explicit primary and backup agent")
		}
		if parsed.Agent == agent {
			return Role{}, keyErr(k, root, "the backup must name a different agent")
		}
		role.Fallback = &parsed
	}
	return role, nil
}

func roleString(k Key, root any, name string, obj map[string]any, field string) (string, error) {
	v, ok := obj[field]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", keyErr(k, root, "role "+quote(name)+" field "+quote(field)+" must be a string")
	}
	if strings.TrimSpace(s) == "" {
		return "", keyErr(k, root, "role "+quote(name)+" field "+quote(field)+" is empty; omit it instead")
	}
	return s, nil
}

// resolvedRoles is the roles value that lands in a Config. A document that
// names the fix role keeps that role, including one that names no agent and
// so uses the agent list. A document that does not name it keeps the default
// fixer.
func resolvedRoles(v any) Roles {
	roles := cloneRoles(v)
	if _, ok := roles[RoleFix]; ok {
		return roles
	}
	if roles == nil {
		return defaultRoles()
	}
	roles[RoleFix] = defaultRoles()[RoleFix]
	return roles
}

func cloneRoles(v any) Roles {
	in := v.(Roles)
	if len(in) == 0 {
		return nil
	}
	out := make(Roles, len(in))
	for name, role := range in {
		out[name] = role.clone()
	}
	return out
}

func (r Role) clone() Role {
	if r.Fallback != nil {
		b := r.Fallback.clone()
		r.Fallback = &b
	}
	return r
}
