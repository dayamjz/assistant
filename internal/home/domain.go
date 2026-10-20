package home

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// DomainNameLength bounds a domain name, in characters. A domain names a
// directory under domains/ the way a task identifier names one under
// tasks/, so the same reasons keep it short.
const DomainNameLength = 32

// domainAlphabet is the closed set of characters a domain name is drawn
// from: lower-case ASCII letters, digits, and a dash inside the name. It has
// no separator, no dot and no upper case, for taskIDAlphabet's reasons.
const domainAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789-"

// ErrDomain reports a domain name the one rule here refuses. Every refusal
// wraps it and says which part of the rule the name failed.
var ErrDomain = errors.New("home: invalid domain name")

// ValidDomain is the one rule a domain name answers to, and the only
// validator of one: one to DomainNameLength characters from domainAlphabet,
// neither beginning nor ending with a dash, and not a name Windows reserves.
// Unlike a task identifier a domain is a person's word, chosen by the
// coordinator, so the rule is a grammar rather than a fixed length; what it
// shares with ValidTaskID is that anything outside it is refused and never
// corrected, so two names cannot resolve to one directory.
func ValidDomain(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrDomain)
	}
	if reservedNames[strings.ToLower(name)] {
		return fmt.Errorf("%w: %q is a name Windows reserves", ErrDomain, name)
	}
	if len(name) > DomainNameLength {
		return fmt.Errorf("%w: %q is %d characters, more than %d", ErrDomain, name, len(name), DomainNameLength)
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return fmt.Errorf("%w: %q begins or ends with a dash", ErrDomain, name)
	}
	for i := 0; i < len(name); i++ {
		if !strings.ContainsRune(domainAlphabet, rune(name[i])) {
			return fmt.Errorf("%w: %q has %q at position %d, which is outside the alphabet %q",
				ErrDomain, name, name[i], i, domainAlphabet)
		}
	}
	return nil
}

// Domain is where one delegated domain's subordinate coordinator is started
// and keeps its files: domains/<name> under the root. A name ValidDomain
// refuses is refused here, so a path under domains/ is only ever composed
// from a name that is one path element. Nothing under it is state: the
// delegation is internal/store's record.
func (h *Home) Domain(name string) (string, error) {
	if err := ValidDomain(name); err != nil {
		return "", err
	}
	return filepath.Join(h.root, "domains", name), nil
}

// DomainInstructions is the file holding what a domain's subordinate
// coordinator is started with, under the domain's own directory, refused on
// Domain's terms. internal/service writes it when the domain is delegated
// and reads it at every start of the subordinate.
func (h *Home) DomainInstructions(name string) (string, error) {
	dir, err := h.Domain(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "instructions.md"), nil
}
