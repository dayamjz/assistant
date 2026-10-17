package config

import (
	"errors"
	"strings"
	"testing"
)

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
