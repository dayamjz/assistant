package home_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
)

// A domain name is one path element from a closed grammar, refused rather
// than corrected: a separator, a dot, upper case, a leading or trailing
// dash, a reserved name, an empty name and one past the length bound are
// each refused with ErrDomain, and an accepted name composes exactly one
// directory under domains/ with the instructions file inside it.
func TestADomainNameIsOneRuleAndOnePathElement(t *testing.T) {
	t.Parallel()
	h, err := home.Open(filepath.Join(t.TempDir(), home.DirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "a", "web-api", "team2", "x-y-z"} {
		if err := home.ValidDomain(name); err != nil {
			t.Errorf("ValidDomain(%q) = %v, want accepted", name, err)
		}
		dir, err := h.Domain(name)
		if err != nil || dir != filepath.Join(h.Root(), "domains", name) {
			t.Errorf("Domain(%q) = %q, %v", name, dir, err)
		}
		file, err := h.DomainInstructions(name)
		if err != nil || file != filepath.Join(dir, "instructions.md") {
			t.Errorf("DomainInstructions(%q) = %q, %v", name, file, err)
		}
	}
	for _, name := range []string{"", "-alpha", "alpha-", "Alpha", "a/b", "a.b", "..", "con", "a b",
		"abcdefghijklmnopqrstuvwxyz0123456789"} {
		if err := home.ValidDomain(name); !errors.Is(err, home.ErrDomain) {
			t.Errorf("ValidDomain(%q) = %v, want ErrDomain", name, err)
		}
		if _, err := h.Domain(name); !errors.Is(err, home.ErrDomain) {
			t.Errorf("Domain(%q) = %v, want ErrDomain", name, err)
		}
		if _, err := h.DomainInstructions(name); !errors.Is(err, home.ErrDomain) {
			t.Errorf("DomainInstructions(%q) = %v, want ErrDomain", name, err)
		}
	}
}
