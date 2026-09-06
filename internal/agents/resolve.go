package agents

import (
	"context"
	"errors"
	"strings"
)

// AutoEntry is the configuration value that means "resolve whatever is
// runnable". It expands to the catalog's own order, so a repository that names
// no agent gets the same ordered fallback every other repository would.
const AutoEntry = "auto"

// Factory builds the Runner for one coding agent and decides whether that
// agent is runnable here. Availability and construction are one call because
// they are one question: an agent is available exactly when a Runner for it
// can be built.
type Factory interface {
	// Name is the agent's name as configuration spells it, such as "claude".
	// It is matched against the first word of a configuration entry.
	Name() string
	// New returns a Runner, or an error explaining why this agent cannot run
	// here. args are the words the configuration entry carried after the name,
	// which are passed to the agent ahead of the flags the run manages itself.
	New(ctx context.Context, args []string) (Runner, error)
}

// Catalog is the set of agent adapters this build has, in the order "auto"
// tries them. A Catalog is built once and read concurrently afterwards; Add
// must not be called while a Resolve is in flight.
type Catalog struct {
	factories []Factory
}

// NewCatalog returns a catalog holding the given factories in the given order.
// A nil factory is ignored, and a second factory with a name already present
// replaces the first, so a caller substituting an adapter does not end up with
// two answers to one name.
func NewCatalog(factories ...Factory) *Catalog {
	c := &Catalog{}
	for _, f := range factories {
		c.Add(f)
	}
	return c
}

// DefaultCatalog returns the adapters this build ships, in the order "auto"
// tries them. Claude Code is the only one, per the MVP cut in PRD section 12,
// and it sits behind the full interface so a second adapter is additive.
func DefaultCatalog() *Catalog { return NewCatalog(ClaudeFactory()) }

// Add appends a factory, replacing any factory already registered under the
// same name.
func (c *Catalog) Add(f Factory) {
	if f == nil {
		return
	}
	for i, existing := range c.factories {
		if existing.Name() == f.Name() {
			c.factories[i] = f
			return
		}
	}
	c.factories = append(c.factories, f)
}

// Names returns the registered agent names in catalog order.
func (c *Catalog) Names() []string {
	out := make([]string, 0, len(c.factories))
	for _, f := range c.factories {
		out = append(out, f.Name())
	}
	return out
}

// lookup returns the factory registered under name, if any.
func (c *Catalog) lookup(name string) (Factory, bool) {
	for _, f := range c.factories {
		if f.Name() == name {
			return f, true
		}
	}
	return nil, false
}

// Resolution names the agent a run resolved to and what was passed over on the
// way. It exists so a run can report which agent it actually used rather than
// leaving that to be inferred from the configuration it was given.
type Resolution struct {
	// Runner is the resolved agent.
	Runner Runner
	// Name is its name, such as "claude".
	Name string
	// Capabilities is what the resolved adapter declared it supports. It is
	// the Runner's own declaration, carried here so a caller can refuse a path
	// before it builds one without holding the Runner.
	Capabilities Capabilities
	// Entry is the configuration entry that resolved, as written. For an entry
	// that "auto" expanded, it is the expansion rather than the word "auto".
	Entry string
	// Index is the entry's position in the list after "auto" was expanded,
	// counting from zero.
	Index int
	// Skipped is every entry considered before this one and why each did not
	// resolve. It is empty when the first entry resolved.
	Skipped []Unavailability
}

// Resolve picks the first runnable agent from an ordered list of configuration
// entries and reports which one it was.
//
// An entry is a name optionally followed by flags, in the form
// internal/config's agent key already validated: "claude", or
// "claude --model x". The single entry "auto" expands, in place, to every
// adapter the catalog holds in catalog order.
//
// Entries are tried in order and the first that yields a Runner wins. An entry
// naming an adapter this build does not have, and an entry whose adapter
// reports it cannot run here, are both passed over and recorded in
// Resolution.Skipped, because that is what an ordered fallback list is for.
//
// Resolving nothing is a refusal, not a degraded run: the returned error is a
// *ResolutionError wrapping ErrNoAgent that names every entry and why each one
// failed. PRD section 10 requires a run to fail before its first stage rather
// than report command-only validation as a pass, and this is where that
// happens.
//
// An adapter that resolves is held to its own declaration before it is
// returned, and an adapter whose declaration and type disagree ends the whole
// resolution with an *AdapterError rather than being passed over. That is the
// one refusal here that is not about availability: passing over a defective
// adapter would leave whichever one came next answering for it, and the defect
// would show up as a difference between two machines rather than as itself.
// What a caller therefore holds after Resolve is a Runner whose declaration
// and mechanisms agree for every capability this package can see.
func Resolve(ctx context.Context, entries []string, catalog *Catalog) (Resolution, error) {
	if catalog == nil {
		catalog = DefaultCatalog()
	}
	expanded := expand(entries, catalog)

	var skipped []Unavailability
	for i, entry := range expanded {
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			skipped = append(skipped, Unavailability{
				Entry: entry,
				Err:   errors.New("entry names no agent"),
			})
			continue
		}
		name, args := fields[0], fields[1:]
		factory, ok := catalog.lookup(name)
		if !ok {
			skipped = append(skipped, Unavailability{
				Entry: entry,
				Name:  name,
				Err:   errors.New("this build has no adapter named " + name),
			})
			continue
		}
		runner, err := factory.New(ctx, args)
		if err != nil {
			skipped = append(skipped, Unavailability{Entry: entry, Name: name, Err: err})
			continue
		}
		if err := verifyDeclaration(name, runner); err != nil {
			return Resolution{}, err
		}
		return Resolution{
			Runner:       runner,
			Name:         name,
			Capabilities: runner.Capabilities(),
			Entry:        entry,
			Index:        i,
			Skipped:      skipped,
		}, nil
	}
	return Resolution{}, &ResolutionError{Tried: skipped}
}

// expand replaces every "auto" entry with the catalog's own order, leaving
// every other entry as written. An empty list expands to nothing, so a caller
// that configured no agent at all gets the same refusal as one whose agents
// were all unavailable, with "no agent was configured" as the reason.
func expand(entries []string, catalog *Catalog) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry) == AutoEntry {
			out = append(out, catalog.Names()...)
			continue
		}
		out = append(out, entry)
	}
	return out
}
