package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

func TestRoleHasOneExplicitBackup(t *testing.T) {
	l := mustParse(t, OriginGlobal, `{"roles":{"worker":{"agent":"claude","fallback":{"agent":"cursor"}}}}`)
	r, err := Resolve(l, Absent(OriginTrusted))
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Roles[RoleWorker].Fallback == nil || r.Config.Roles[RoleWorker].Fallback.Agent != "cursor" {
		t.Fatal("backup was lost")
	}
	other := mustParse(t, OriginGlobal, `{"roles":{"worker":{"agent":"claude","fallback":{"agent":"cursor"}}}}`)
	want := cloneRoles(other.values[KeyRoles])
	want[RoleFix] = defaultRoles()[RoleFix]
	if !r.Config.Roles.Equal(want) {
		t.Fatal("equal backups compare unequal")
	}
	for _, doc := range []string{
		`{"roles":{"worker":{"fallback":{"agent":"cursor"}}}}`,
		`{"roles":{"worker":{"agent":"claude","fallback":{}}}}`,
		`{"roles":{"worker":{"agent":"claude","fallback":{"agent":"auto"}}}}`,
		`{"roles":{"worker":{"agent":"claude","fallback":{"agent":"cursor","fallback":{"agent":"claude"}}}}}}`,
	} {
		if _, err := Parse(OriginGlobal, []byte(doc)); err == nil {
			t.Errorf("accepted %s", doc)
		}
	}
}

func TestARoleNamesOneAgentAndAWholeModelTriple(t *testing.T) {
	doc := `{
		"roles": {
			"worker": {"agent": "claude"},
			"pages": {"agent": "claude", "provider": "anthropic", "type": "opus", "version": "5.5", "effort": "high"},
			"plain": {}
		}
	}`
	l := mustParse(t, OriginGlobal, doc)
	res, err := Resolve(l, Absent(OriginTrusted))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	pages := res.Config.Roles["pages"]
	if pages.Agent != "claude" || pages.Provider != "anthropic" || pages.Type != "opus" || pages.Version != "5.5" || pages.Effort != "high" {
		t.Fatalf("pages resolved to %+v", pages)
	}
	if res.Config.Roles["plain"] != (Role{}) {
		t.Fatalf("a role with no agent resolved to %+v", res.Config.Roles["plain"])
	}
	got, err := res.Config.Roles.ForTask("")
	if err != nil || got.Agent != "claude" {
		t.Fatalf("an unnamed task resolved to %+v (%v), want the worker role", got, err)
	}
	if _, err := res.Config.Roles.ForTask("review"); !errors.Is(err, ErrTaskRole) {
		t.Fatalf("a task naming review returned %v, want ErrTaskRole", err)
	}
	if _, err := res.Config.Roles.ForTask("missing"); !errors.Is(err, ErrTaskRole) {
		t.Fatalf("an unknown role returned %v, want ErrTaskRole", err)
	}
}

func TestAPartialOrUnknownModelTripleFailsAtParse(t *testing.T) {
	for _, doc := range []string{
		`{"roles": {"pages": {"agent": "claude", "provider": "anthropic"}}}`,
		`{"roles": {"pages": {"agent": "claude", "provider": "anthropic", "type": "opus", "version": "9"}}}`,
		`{"roles": {"pages": {"agent": "claude", "provider": "openai", "type": "opus", "version": "5.5"}}}`,
		`{"roles": {"pages": {"provider": "anthropic", "type": "opus", "version": "5.5"}}}`,
		`{"roles": {"pages": {"effort": "high"}}}`,
		`{"roles": {"pages": {"agent": "claude --model x"}}}`,
		`{"roles": {"worker": {"agent": "auto"}}}`,
		`{"roles": {"Pages": {"agent": "claude"}}}`,
		`{"roles": {"review": {"agent": "claude", "effort": "max"}}}`,
	} {
		if _, err := Parse(OriginGlobal, []byte(doc)); err == nil {
			t.Errorf("Parse accepted %s", doc)
		}
	}
}

func TestTheModelCatalogMatchesTheRoleTable(t *testing.T) {
	seen := map[string]bool{}
	for _, cell := range ModelCatalog() {
		key := cell.Provider + "/" + cell.Type + "/" + cell.Version
		if seen[key] {
			t.Errorf("the catalog lists %s twice", key)
		}
		seen[key] = true
		if !KnownModel(cell.Provider, cell.Type, cell.Version) {
			t.Errorf("KnownModel refuses a catalog cell %s", key)
		}
	}
	for _, absent := range []string{"anthropic/opus/9", "openai/opus/5.5", "anthropic/composer/2.5"} {
		parts := strings.Split(absent, "/")
		if KnownModel(parts[0], parts[1], parts[2]) {
			t.Errorf("KnownModel accepts %s", absent)
		}
	}
	if len(seen) == 0 {
		t.Fatal("the catalog is empty")
	}
}

// A worker role on Claude Fable 5.1 is one cell of the role table: the YAML
// document an operator writes parses, resolves, and is the role a task naming
// it runs under. A Fable version the table does not list is refused at parse
// time, naming the role, so the accepted case is not the parser accepting any
// version under the type.
func TestAWorkerRoleOnFableParsesAndAnUnlistedVersionIsRefused(t *testing.T) {
	doc := "roles:\n  worker-hard:\n    agent: claude\n    provider: anthropic\n    type: fable\n    version: \"5.1\"\n    effort: high\n"
	l, err := ParseYAML(OriginGlobal, []byte(doc))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	res, err := Resolve(l, Absent(OriginTrusted))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got, err := res.Config.Roles.ForTask("worker-hard")
	want := Role{Agent: "claude", Provider: "anthropic", Type: "fable", Version: "5.1", Effort: "high"}
	if err != nil || !got.equal(want) {
		t.Fatalf("worker-hard resolved to %+v (%v), want %+v", got, err, want)
	}

	unlisted := "roles:\n  worker-hard:\n    agent: claude\n    provider: anthropic\n    type: fable\n    version: \"9\"\n"
	_, err = ParseYAML(OriginGlobal, []byte(unlisted))
	if err == nil {
		t.Fatal("ParseYAML accepted anthropic fable 9")
	}
	if !strings.Contains(err.Error(), `"worker-hard"`) || !strings.Contains(err.Error(), "fable 9") {
		t.Fatalf("the refusal %q does not name the role and the cell", err)
	}
}

func TestTheFixerDefaultsToCursor(t *testing.T) {
	absent := resolve(t, Absent(OriginGlobal), Absent(OriginTrusted)).Config.Roles
	if got := absent[RoleFix]; got != (Role{Agent: defaultFixAgent}) {
		t.Fatalf("an omitted roles table resolved the fixer to %+v", got)
	}
	if _, ok := absent[RoleReview]; ok {
		t.Fatal("the default names a review role")
	}
	if _, ok := absent[RoleWorker]; ok {
		t.Fatal("the default names a worker role")
	}

	partial := resolve(t, mustParse(t, OriginGlobal, `{"roles":{"review":{"agent":"codex"}}}`), Absent(OriginTrusted)).Config.Roles
	if partial[RoleFix].Agent != defaultFixAgent {
		t.Fatalf("a table that omits fix resolved the fixer to %+v", partial[RoleFix])
	}
	if partial[RoleReview].Agent != "codex" {
		t.Fatalf("review resolved to %+v", partial[RoleReview])
	}

	named := resolve(t, mustParse(t, OriginGlobal, `{"roles":{"fix":{"agent":"claude"}}}`), Absent(OriginTrusted)).Config.Roles
	if named[RoleFix].Agent != "claude" {
		t.Fatalf("a named fixer resolved to %+v", named[RoleFix])
	}

	unpinned := resolve(t, mustParse(t, OriginGlobal, `{"roles":{"fix":{}}}`), Absent(OriginTrusted)).Config.Roles
	if unpinned[RoleFix] != (Role{}) {
		t.Fatalf("a fix role that names no agent resolved to %+v", unpinned[RoleFix])
	}
}

// A role names a context window beside its effort, on a complete triple, and
// a backup names its own: it inherits nothing from the primary.
func TestARoleNamesAContextWindowBesideItsTriple(t *testing.T) {
	doc := `{"roles": {
		"long": {"agent": "claude", "provider": "anthropic", "type": "opus", "version": "5.5", "effort": "high", "context": "1m",
			"fallback": {"agent": "codex", "provider": "openai", "type": "gpt", "version": "6-astra"}},
		"short": {"agent": "claude", "provider": "anthropic", "type": "opus", "version": "5.5", "context": "standard"},
		"unnamed": {"agent": "claude", "provider": "anthropic", "type": "opus", "version": "5.5"}
	}}`
	res, err := Resolve(mustParse(t, OriginGlobal, doc), Absent(OriginTrusted))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	roles := res.Config.Roles
	if got := roles["long"]; got.ContextWindow != Context1M || got.Effort != "high" {
		t.Fatalf("long resolved to %+v, want context 1m beside effort high", got)
	}
	if got := roles["long"].Fallback; got == nil || got.ContextWindow != "" {
		t.Fatalf("the backup resolved to %+v, want its own unnamed context", got)
	}
	if got := roles["short"].ContextWindow; got != ContextStandard {
		t.Fatalf("short resolved to context %q, want standard", got)
	}
	if got := roles["unnamed"].ContextWindow; got != "" {
		t.Fatalf("a role naming no context resolved to %q, want not named", got)
	}
	other := res.Config.Roles["short"]
	other.ContextWindow = Context1M
	if (Roles{"short": other}).Equal(Roles{"short": roles["short"]}) {
		t.Fatal("two roles differing only in their context compare equal")
	}
}

// A context outside the closed set, one on a role that names no agent, and
// one on a role that names no triple each fail at parse time and name the
// role. None is read as a window it did not name, and the backup is held to
// the same terms under its own name.
func TestAContextWindowOutsideTheSetOrWithoutATripleFailsAtParse(t *testing.T) {
	triple := `"provider": "anthropic", "type": "opus", "version": "5.5"`
	for _, tc := range []struct{ doc, role string }{
		{`{"roles": {"pages": {"agent": "claude", ` + triple + `, "context": "2m"}}}`, "pages"},
		{`{"roles": {"pages": {"agent": "claude", ` + triple + `, "context": "1M"}}}`, "pages"},
		{`{"roles": {"pages": {"agent": "claude", ` + triple + `, "context": "opus[1m]"}}}`, "pages"},
		{`{"roles": {"pages": {"agent": "claude", ` + triple + `, "context": ""}}}`, "pages"},
		{`{"roles": {"pages": {"agent": "claude", ` + triple + `, "context": 1}}}`, "pages"},
		{`{"roles": {"pages": {"context": "1m"}}}`, "pages"},
		{`{"roles": {"pages": {"agent": "claude", "context": "1m"}}}`, "pages"},
		{`{"roles": {"pages": {"agent": "claude", "effort": "high", "context": "standard"}}}`, "pages"},
		{`{"roles": {"pages": {"agent": "claude", "fallback": {"agent": "codex", "context": "1m"}}}}`, "pages fallback"},
	} {
		_, err := Parse(OriginGlobal, []byte(tc.doc))
		if err == nil {
			t.Errorf("Parse accepted %s", tc.doc)
			continue
		}
		if !strings.Contains(err.Error(), quote(tc.role)) {
			t.Errorf("Parse(%s) = %v, which does not name role %q", tc.doc, err, tc.role)
		}
	}
}

// ContextWindows is the one spelling of the set, and KnownContextWindow
// answers from it: every member is known, and nothing near a member is.
func TestTheContextWindowSetIsClosed(t *testing.T) {
	if got := strings.Join(ContextWindows(), ","); got != "standard,1m" {
		t.Fatalf("ContextWindows = %s, want the PRD's standard,1m", got)
	}
	for _, window := range ContextWindows() {
		if !KnownContextWindow(window) {
			t.Errorf("KnownContextWindow refuses its own member %q", window)
		}
	}
	for _, window := range []string{"", "1M", "200k", "long", " 1m"} {
		if KnownContextWindow(window) {
			t.Errorf("KnownContextWindow accepts %q", window)
		}
	}
}

// A pushed branch that names a role's context window changes nothing that
// runs: roles is in the agent's trust class, so the trusted role governs.
func TestAPushedContextWindowHasNoEffect(t *testing.T) {
	principles.Cite(t, principles.P7)
	global := mustParse(t, OriginGlobal, `{"roles": {"review": {"agent": "claude", "provider": "anthropic", "type": "opus", "version": "5.5", "context": "standard"}}}`)
	pushed := mustParse(t, OriginPushed, `{"roles": {"review": {"agent": "claude", "provider": "anthropic", "type": "opus", "version": "5.5", "context": "1m"}}}`)
	res := resolve(t, global, pushed)
	if got := res.Config.Roles[RoleReview].ContextWindow; got != ContextStandard {
		t.Fatalf("review context = %q, want the trusted standard", got)
	}
}
