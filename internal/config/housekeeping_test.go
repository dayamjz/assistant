package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// With no document setting either key, housekeeping.interval is zero, which
// makes no pass due anywhere, and housekeeping.paths is PRD section 10's
// default list, in its order.
func TestHousekeepingDefaultsToOffWithTheAgentContextList(t *testing.T) {
	c := Defaults()
	if c.HousekeepingInterval != 0 {
		t.Errorf("HousekeepingInterval = %v, want zero, so no pass is due until the operator sets it", c.HousekeepingInterval)
	}
	want := "AGENTS.md,**/AGENTS.md,CLAUDE.md,**/CLAUDE.md,.claude/rules/**,.cursor/rules/**,README.md,assistant-knowledge.md"
	if got := strings.Join(c.HousekeepingPaths.Strings(), ","); got != want {
		t.Errorf("HousekeepingPaths = %q, want %q", got, want)
	}
	for _, path := range []string{"AGENTS.md", "internal/x/AGENTS.md", "CLAUDE.md", ".claude/rules/store.md", ".cursor/rules/a.mdc",
		"README.md", "assistant-knowledge.md"} {
		if !c.HousekeepingPaths.Matches(path) {
			t.Errorf("the default list does not match %s", path)
		}
	}
	// A positive control on the other side: a source file is not agent context.
	if c.HousekeepingPaths.Matches("internal/config/key.go") {
		t.Error("the default list matches a source file")
	}
}

// housekeeping.paths is trusted-only: the trusted copy's list replaces the
// default, its empty list turns housekeeping off for the repository, and a
// pushed copy's list has no effect and is reported as rejected (P7).
func TestHousekeepingPathsAreReadOnlyFromTheTrustedCopy(t *testing.T) {
	principles.Cite(t, principles.P7)
	global := mustParse(t, OriginGlobal, `{}`)
	trusted := mustParse(t, OriginTrusted, `{"housekeeping": {"paths": ["docs/agents/**"]}}`)
	res, err := ResolveRun(global, trusted, Absent(OriginPushed))
	if err != nil {
		t.Fatalf("ResolveRun: %v", err)
	}
	if got := strings.Join(res.Config.HousekeepingPaths.Strings(), ","); got != "docs/agents/**" {
		t.Errorf("HousekeepingPaths = %q, want the trusted list replacing the default", got)
	}

	off := mustParse(t, OriginTrusted, `{"housekeeping": {"paths": []}}`)
	res, err = ResolveRun(global, off, Absent(OriginPushed))
	if err != nil {
		t.Fatalf("ResolveRun: %v", err)
	}
	if len(res.Config.HousekeepingPaths) != 0 {
		t.Errorf("HousekeepingPaths = %v, want empty: the trusted copy turned housekeeping off", res.Config.HousekeepingPaths.Strings())
	}

	pushed := mustParse(t, OriginPushed, `{"housekeeping": {"paths": ["**/*"]}}`)
	res, err = ResolveRun(global, off, pushed)
	if err != nil {
		t.Fatalf("ResolveRun: %v", err)
	}
	if len(res.Config.HousekeepingPaths) != 0 {
		t.Errorf("HousekeepingPaths = %v, want the trusted empty list: a pushed copy may not widen it", res.Config.HousekeepingPaths.Strings())
	}
	rejected := false
	for _, r := range res.Rejected {
		if r.Key == KeyHousekeepingPaths {
			rejected = true
		}
	}
	if !rejected {
		t.Errorf("Rejected = %v, want the pushed housekeeping.paths reported", res.Rejected)
	}
}

// A housekeeping path list past its bound, and a malformed pattern in it,
// are refused at parse time and name the key.
func TestHousekeepingPathsAreBoundedAndParsed(t *testing.T) {
	entries := make([]string, MaxHousekeepingPaths+1)
	for i := range entries {
		entries[i] = `"a.md"`
	}
	for _, doc := range []string{
		`{"housekeeping": {"paths": [` + strings.Join(entries, ",") + `]}}`,
		`{"housekeeping": {"paths": ["a**b"]}}`,
		`{"housekeeping": {"paths": "AGENTS.md"}}`,
	} {
		_, err := Parse(OriginTrusted, []byte(doc))
		var ke *KeyError
		if !errors.As(err, &ke) || ke.Key != KeyHousekeepingPaths {
			t.Errorf("Parse(%.60s...) = %v, want a *KeyError naming housekeeping.paths", doc, err)
		}
	}
	at := `{"housekeeping": {"paths": [` + strings.Join(entries[:MaxHousekeepingPaths], ",") + `]}}`
	if _, err := Parse(OriginTrusted, []byte(at)); err != nil {
		t.Errorf("Parse of exactly %d patterns: %v", MaxHousekeepingPaths, err)
	}
}

// housekeeping.interval takes zero or a duration between its floor and its
// ceiling, from the operator's own file, and refuses anything else rather
// than clamping it. A repository file naming it at all is refused at parse
// time (P7), which TestParseRefusesGlobalOnlyKeysInARepositoryFile holds for
// both repository origins, zero included.
func TestHousekeepingIntervalIsZeroOrWithinItsLimits(t *testing.T) {
	principles.Cite(t, principles.P7)
	for doc, want := range map[string]time.Duration{
		`{"housekeeping": {"interval": "0s"}}`:   0,
		`{"housekeeping": {"interval": "0"}}`:    0,
		`{"housekeeping": {"interval": "1h"}}`:   MinHousekeepingInterval,
		`{"housekeeping": {"interval": "12h"}}`:  12 * time.Hour,
		`{"housekeeping": {"interval": "168h"}}`: MaxHousekeepingInterval,
	} {
		l := mustParse(t, OriginGlobal, doc)
		res := resolve(t, l, Absent(OriginPushed))
		if res.Config.HousekeepingInterval != want {
			t.Errorf("%s resolved to %v, want %v", doc, res.Config.HousekeepingInterval, want)
		}
	}
	for _, doc := range []string{
		`{"housekeeping": {"interval": "59m"}}`,
		`{"housekeeping": {"interval": "169h"}}`,
		`{"housekeeping": {"interval": "-1h"}}`,
		`{"housekeeping": {"interval": 12}}`,
		`{"housekeeping": {"interval": "often"}}`,
	} {
		_, err := Parse(OriginGlobal, []byte(doc))
		var ke *KeyError
		if !errors.As(err, &ke) || ke.Key != KeyHousekeepingInterval {
			t.Errorf("Parse(%s) = %v, want a *KeyError naming housekeeping.interval", doc, err)
		}
	}
}

// The housekeeping role is the program's: no task may name it, and a pass
// resolves to the fix row when the roles table does not name housekeeping,
// and to the housekeeping row when it does.
func TestTheHousekeepingRoleIsAssignedAndResolvesToTheFixer(t *testing.T) {
	c := Defaults()
	if _, err := c.Roles.ForTask(RoleHousekeeping); !errors.Is(err, ErrTaskRole) {
		t.Errorf("ForTask(housekeeping) = %v, want ErrTaskRole: no task may name the role", err)
	}
	if got := c.Roles.ForHousekeeping(); got.Agent != defaultFixAgent {
		t.Errorf("ForHousekeeping() = %+v, want the default fix row, %s", got, defaultFixAgent)
	}
	if got, err := c.Roles.ForRecorded(RoleHousekeeping); err != nil || got.Agent != defaultFixAgent {
		t.Errorf("ForRecorded(housekeeping) = %+v, %v, want the fix row", got, err)
	}

	l := mustParse(t, OriginGlobal, `{"roles": {"fix": {"agent": "codex"}, "housekeeping": {"agent": "claude"}}}`)
	named := resolve(t, l, Absent(OriginPushed)).Config
	if got := named.Roles.ForHousekeeping(); got.Agent != "claude" {
		t.Errorf("ForHousekeeping() = %+v, want the table's housekeeping row", got)
	}
	l = mustParse(t, OriginGlobal, `{"roles": {"fix": {"agent": "codex"}}}`)
	fixOnly := resolve(t, l, Absent(OriginPushed)).Config
	if got := fixOnly.Roles.ForHousekeeping(); got.Agent != "codex" {
		t.Errorf("ForHousekeeping() = %+v, want the table's fix row", got)
	}
	// ForRecorded still refuses for every other task what ForTask refuses.
	if _, err := fixOnly.Roles.ForRecorded(RoleFix); !errors.Is(err, ErrTaskRole) {
		t.Errorf("ForRecorded(fix) = %v, want ErrTaskRole", err)
	}
}
