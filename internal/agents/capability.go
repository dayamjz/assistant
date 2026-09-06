package agents

import (
	"slices"
	"strings"
)

// Capability names one thing an agent adapter may support. Adapters differ,
// and PRD section 8 makes that difference a declaration the program reads
// before it commits to a path rather than something a stage discovers halfway
// through one.
//
// The set is closed and lives in the table below. A capability outside it is
// unrecognized, and an unrecognized capability is never satisfied by any
// declaration, so naming one can only refuse a path and never open one.
type Capability string

const (
	// CapabilityResumableSessions is an adapter that can reopen a
	// conversation it held earlier, which is what a fixer session is. It is
	// the capability P4 rests on: a fixer's memory crosses rounds and a
	// review's never does, so an adapter without it gets no substitute.
	// Nothing shared, replayed, or reconstructed may carry a fixer's memory
	// into the review that checks it, and running both roles in one session is
	// not a degraded mode an undeclared adapter may fall back to.
	CapabilityResumableSessions Capability = "resumable_sessions"
	// CapabilitySuppressProjectInstructions is an adapter with a mechanism for
	// ignoring the repository's own instruction files, which PRD section 10
	// makes config.KeySuppressProjectInstructions. No adapter this build ships
	// declares it, because no adapter implements it, so a run configured to
	// suppress instructions is refused rather than run with the instructions
	// still in force.
	CapabilitySuppressProjectInstructions Capability = "suppress_project_instructions"
)

// capabilitySpec is one row of the capability table: the capability, and how
// this package can tell whether a Runner really carries the mechanism it
// names. Every fact about a capability lives in one row, per P14.
type capabilitySpec struct {
	capability Capability
	// carried reports whether r carries the mechanism, for a capability whose
	// mechanism is visible in a runner's type. It is nil for a capability
	// whose mechanism is not, and a nil row is what makes a declaration of
	// that capability something this package takes at its word.
	carried func(r Runner) bool
}

// capabilityTable is the closed set, in the order List and AllCapabilities
// report.
var capabilityTable = []capabilitySpec{
	{CapabilityResumableSessions, func(r Runner) bool {
		_, ok := r.(SessionRunner)
		return ok
	}},
	{CapabilitySuppressProjectInstructions, nil},
}

// spec returns the capability's row, and false when c names none.
func (c Capability) spec() (capabilitySpec, bool) {
	for _, row := range capabilityTable {
		if row.capability == c {
			return row, true
		}
	}
	return capabilitySpec{}, false
}

// Recognized reports whether c is one of the capabilities this package
// defines.
func (c Capability) Recognized() bool {
	_, ok := c.spec()
	return ok
}

// String renders the capability as it is declared and reported.
func (c Capability) String() string { return string(c) }

// AllCapabilities returns the closed set in table order. The result is a copy,
// so a caller cannot add to the set by writing to it.
func AllCapabilities() []Capability {
	out := make([]Capability, len(capabilityTable))
	for i, row := range capabilityTable {
		out[i] = row.capability
	}
	return out
}

// Capabilities is what one adapter declares it supports. The zero value
// declares nothing, which is the answer an adapter that says nothing gets:
// undeclared means unavailable, so a path needing anything at all is refused
// against it.
//
// A Capabilities is immutable once built and safe for concurrent use.
type Capabilities struct {
	declared map[Capability]struct{}
}

// Declare returns the declaration for an adapter supporting exactly caps. A
// capability given twice is declared once, and an unrecognized one is kept as
// given so a refusal can name it; it satisfies nothing.
func Declare(caps ...Capability) Capabilities {
	if len(caps) == 0 {
		return Capabilities{}
	}
	declared := make(map[Capability]struct{}, len(caps))
	for _, c := range caps {
		declared[c] = struct{}{}
	}
	return Capabilities{declared: declared}
}

// Has reports whether the adapter declared capability. An unrecognized
// capability is never had, whatever was declared, so a typo in a declaration
// cannot open a path.
func (c Capabilities) Has(capability Capability) bool {
	if !capability.Recognized() {
		return false
	}
	_, ok := c.declared[capability]
	return ok
}

// List returns what was declared: the recognized capabilities in table order,
// then anything unrecognized in sorted order, so the rendering of one
// declaration is the same on every run.
func (c Capabilities) List() []Capability {
	out := make([]Capability, 0, len(c.declared))
	for _, row := range capabilityTable {
		if _, ok := c.declared[row.capability]; ok {
			out = append(out, row.capability)
		}
	}
	var unrecognized []Capability
	for declared := range c.declared {
		if !declared.Recognized() {
			unrecognized = append(unrecognized, declared)
		}
	}
	slices.Sort(unrecognized)
	return append(out, unrecognized...)
}

// String renders the declaration for a diagnostic. An adapter that declared
// nothing renders as "none" rather than as an empty string, so a message
// quoting it does not read as a missing value.
func (c Capabilities) String() string {
	declared := c.List()
	if len(declared) == 0 {
		return "none"
	}
	names := make([]string, len(declared))
	for i, capability := range declared {
		names[i] = string(capability)
	}
	return strings.Join(names, ", ")
}

// verifyDeclaration reports whether an adapter's declaration and its type say
// the same thing, for every capability whose mechanism this package can see,
// and returns the declaration it checked.
//
// It reads Runner.Capabilities once and hands that value back rather than
// leaving a caller to read it again, so what a caller carries away is the
// value that was verified and not a second answer from the same method.
//
// It is checked where a run obtains a Runner rather than left to the path that
// needs the capability, because both directions of disagreement are adapter
// defects and neither should be discovered on the round that needed it. An
// adapter declaring a mechanism it does not carry would refuse the path late,
// with a message about the path rather than about the adapter. An adapter
// carrying one it did not declare would have the path refused while the
// mechanism sat there working, which is the case a reader would spend the
// longest on.
//
// A capability whose row carries no probe is taken at its word here. That is a
// residual gap and doc.go names it.
func verifyDeclaration(name string, r Runner) (Capabilities, error) {
	declared := r.Capabilities()
	for _, capability := range declared.List() {
		if !capability.Recognized() {
			return Capabilities{}, &AdapterError{
				Agent:  name,
				Reason: "declares " + string(capability) + ", which is not a capability this build defines",
			}
		}
	}
	for _, row := range capabilityTable {
		if row.carried == nil {
			continue
		}
		says, carries := declared.Has(row.capability), row.carried(r)
		switch {
		case says && !carries:
			return Capabilities{}, declaredWithoutMechanism(name, row.capability)
		case carries && !says:
			return Capabilities{}, &AdapterError{
				Agent:  name,
				Reason: "carries the mechanism for " + string(row.capability) + " and does not declare it",
			}
		}
	}
	return declared, nil
}

// declaredWithoutMechanism is the defect an adapter has when it declares a
// capability it does not carry. Resolve and OpenFixer both find it, at
// different moments, and one function is what keeps them reporting it in the
// same words.
func declaredWithoutMechanism(name string, capability Capability) *AdapterError {
	return &AdapterError{
		Agent:  name,
		Reason: "declares " + string(capability) + " and carries no mechanism for it",
	}
}
