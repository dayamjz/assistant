package agents

import (
	"context"
	"errors"
	"strings"
)

// AutoEntry is the configuration value that means "resolve whatever is
// runnable among the adapters auto may choose". It expands to the catalog's
// auto-eligible adapters in the catalog's own order, so a repository that
// names no agent gets the same ordered fallback every other repository
// would. An adapter whose factory does not declare AutoEligible is never
// chosen by it (PRD section 12 makes Claude Code the only phase-one adapter).
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
	// AutoEligible reports whether "auto" may choose this adapter. It records
	// PRD section 12's scope decision that Claude Code is the only phase-one
	// adapter, so it is true for that adapter alone; any other adapter is
	// reachable only by being named in an entry. It is a declaration, not
	// evidence: it says nothing about whether an adapter's parsing matches its
	// real CLI, and no test or fixture in this repository drives a real CLI
	// for any adapter, Claude included. The conformance suite drives a test
	// helper binary for all of them.
	AutoEligible() bool
}

// Catalog is the set of agent adapters this build has. "auto" tries the
// auto-eligible ones among them in this order; any other is reachable only when
// an entry names it. A Catalog is built once and read concurrently afterwards; Add
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

// CatalogOption configures every adapter DefaultCatalog builds, at the one
// place the shipped adapter list lives. It exists for a dependency that is
// the same for all of them and settled by the caller that owns the build:
// each adapter family has its own option type for what varies per adapter,
// and restating the shipped list at a call site to hand all four the same
// value would give that list a second owner. internal/service is the
// consumer, for the invocation recorder its store backs.
//
// It reaches only the catalog DefaultCatalog builds. A caller assembling its
// own catalog from factories configures each factory itself, so nothing here
// applies to it.
type CatalogOption func(*catalogSettings)

type catalogSettings struct {
	recorder Recorder
}

// WithCatalogRecorder gives every adapter DefaultCatalog builds r as its
// Recorder, exactly as if each factory had been constructed with its own
// recorder option. What a Recorder receives, and when, is Recorder's
// contract; this option only delivers it to all four adapters.
func WithCatalogRecorder(r Recorder) CatalogOption {
	return func(s *catalogSettings) { s.recorder = r }
}

// DefaultCatalog returns the adapters this build ships, in the order "auto"
// tries them: Claude, then Cursor, OpenAI, and Grok. Only Claude is
// auto-eligible, because PRD section 12 makes it the phase-one adapter, so
// "auto" resolves it alone; the other three are reachable by naming them in
// the agent key. All implement the core Runner interface, and Claude, Cursor,
// and Grok additionally declare resumable sessions. Cursor and Grok
// authenticate through their CLI login paths: each strips its provider API-key
// environment variable so a key in the service environment cannot redirect a
// run onto API billing.
//
// Options apply to every adapter in the returned catalog and to nothing
// else; DefaultCatalog with none behaves as it always has.
func DefaultCatalog(opts ...CatalogOption) *Catalog {
	var s catalogSettings
	for _, opt := range opts {
		opt(&s)
	}
	var (
		cursor []CursorOption
		claude []ClaudeOption
		openai []OpenAIOption
		grok   []GrokOption
	)
	if s.recorder != nil {
		cursor = append(cursor, WithCursorRecorder(s.recorder))
		claude = append(claude, WithRecorder(s.recorder))
		openai = append(openai, WithOpenAIRecorder(s.recorder))
		grok = append(grok, WithGrokRecorder(s.recorder))
	}
	return NewCatalog(
		ClaudeFactory(claude...),
		CursorFactory(cursor...),
		OpenAIFactory(openai...),
		GrokFactory(grok...),
	)
}

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

// autoNames returns the names "auto" expands to: the registered adapters
// that declare AutoEligible, in catalog order.
func (c *Catalog) autoNames() []string {
	var out []string
	for _, f := range c.factories {
		if f.AutoEligible() {
			out = append(out, f.Name())
		}
	}
	return out
}

// Standing is what one catalog adapter looks like on this machine, for a
// surface that reports adapters rather than resolving one.
type Standing struct {
	// Name is the adapter's name.
	Name string
	// AutoEligible is the factory's AutoEligible declaration.
	AutoEligible bool
	// Runnable is whether the factory built a Runner with no extra flags.
	Runnable bool
	// Err says why it is not runnable, and is nil when Runnable.
	Err error
}

// Survey asks every registered adapter, in catalog order, whether it is
// runnable here. It builds a Runner per adapter and keeps none, and it
// resolves nothing: Resolve alone decides what a run uses.
func (c *Catalog) Survey(ctx context.Context) []Standing {
	out := make([]Standing, 0, len(c.factories))
	for _, f := range c.factories {
		_, err := f.New(ctx, nil)
		out = append(out, Standing{Name: f.Name(), AutoEligible: f.AutoEligible(), Runnable: err == nil, Err: err})
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
	// before it builds one without holding the Runner. It is the same value
	// Resolve verified against the Runner's type, read once, so a caller does
	// not have to depend on Runner.Capabilities answering the same way twice.
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
// "claude --model x". The entry "auto" expands, in place, to every auto-eligible
// adapter the catalog holds in catalog order; other adapters are tried only
// when named.
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
		declared, err := verifyDeclaration(name, runner)
		if err != nil {
			return Resolution{}, err
		}
		return Resolution{
			Runner:       runner,
			Name:         name,
			Capabilities: declared,
			Entry:        entry,
			Index:        i,
			Skipped:      skipped,
		}, nil
	}
	return Resolution{}, &ResolutionError{Tried: skipped}
}

// expand replaces every "auto" entry with the catalog's auto-eligible adapters in the
// catalog's own order, leaving every other entry as written. An empty list expands to nothing, so a caller
// that configured no agent at all gets the same refusal as one whose agents
// were all unavailable, with "no agent was configured" as the reason.
func expand(entries []string, catalog *Catalog) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry) == AutoEntry {
			out = append(out, catalog.autoNames()...)
			continue
		}
		out = append(out, entry)
	}
	return out
}
