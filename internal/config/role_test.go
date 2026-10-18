package config

import (
	"errors"
	"strings"
	"testing"
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
