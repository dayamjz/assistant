package store

import "fmt"

// Resolver says who resolved a hold. PRD section 8 requires every hold
// resolution to record it, from a closed set, and requires that the value
// meaning a person decided cannot be produced by any path an agent reaches.
//
// The closed half is the type rather than a check at the write. Every other
// vocabulary in this package is a defined string type, so a caller holding text
// it was handed can turn it into a RunStatus or a TaskShape. A Resolver has one
// unexported field and no conversion into it: outside this package the only
// values of this type are the three the functions below return and the zero,
// no number or string names one, and encoding/json cannot write the field, so
// no shape decoded from a wire, a configuration file, or an agent's output
// carries one. A resolver is chosen in source by whichever surface is
// recording, and which surface that is decides what it may say.
//
// They are functions rather than variables because a variable is assignable,
// and one package assigning ResolvedByMachineInterface = ResolvedByPerson would
// hand every resolution that arrives over the socket the one value this type
// exists to withhold from it.
//
// The set is enforced in both directions from one table, resolvers: a write
// encodes through it and a read decodes through it, so the column cannot come
// to hold a value a read would then refuse.
//
// Nothing reads a Resolver to decide whether a resolution may proceed, and PRD
// section 8 is explicit that it has to stay that way: this records and does not
// gate. ResolveHold treats every member identically, and the only thing the
// value changes is what the record says afterwards.
//
// The zero Resolver names nobody, which is what a caller that has not thought
// about the question holds. ResolveHold refuses it with ErrNoResolver rather
// than storing a resolution with nobody attached to it.
type Resolver struct{ name string }

// ResolvedByPerson is a resolution a person made themselves.
//
// It is the value PRD section 8 puts out of an agent's reach, and what keeps it
// there is where it may be written rather than anything checked at the write.
// It belongs to a surface that witnessed the person, meaning one where the
// answer arrived from a terminal the recording process itself holds rather than
// in a request some other process sent it.
//
// No such surface exists in this repository yet, and the local protocol is not
// one and cannot become one. A person's client and an agent's client reach the
// same socket as the same user, so nothing the kernel attributes to a
// connection separates them and everything that would separate them is a claim
// the caller writes about itself. So internal/ipc answers
// ResolvedByMachineInterface for every resolution that arrives on it, and no
// path from a frame reaches this value.
func ResolvedByPerson() Resolver { return Resolver{name: "person"} }

// ResolvedByMachineInterface is a resolution that arrived over the machine
// interface, under the authority PRD section 9 gives a caller of it.
//
// It names the surface because the surface is what is established. An agent
// driving the machine interface resolves holds with the same options and the
// same authority a person has, and a person's own client reaches the same
// socket, so a record saying an agent decided would claim something no
// connection establishes. This value says what is known instead: the answer
// came in over the machine interface, and nothing there witnessed a person.
//
// The cost is named rather than hidden. A person answering through a client of
// the machine interface is recorded this way too, so the record understates who
// decided rather than overstating it, and reading it back tells you that a
// person was not witnessed rather than that one was not there.
func ResolvedByMachineInterface() Resolver { return Resolver{name: "machine-interface"} }

// ResolvedByProgram is a resolution the program made on a fact it re-read, with
// nobody deciding anything. PRD section 11 has one: on restart, a hold waiting
// on external checks whose pull request has since merged or closed is completed
// rather than resumed. Recording that as a person's decision, or as a caller's,
// is the misattribution this vocabulary exists to prevent.
func ResolvedByProgram() Resolver { return Resolver{name: "program"} }

// String renders who resolved, or "nobody" for the zero Resolver, so a
// formatted record cannot print an empty who as though it were a value.
func (r Resolver) String() string {
	if r.name == "" {
		return "nobody"
	}
	return r.name
}

// resolvers is the closed set, and the only place it is written down.
func resolvers() []Resolver {
	return []Resolver{ResolvedByPerson(), ResolvedByMachineInterface(), ResolvedByProgram()}
}

// resolverText returns what the column holds for r. The zero Resolver is
// ErrNoResolver, and anything else outside the set is ErrUnknownResolver.
//
// That second refusal is not reachable from outside this package, because a
// value outside the set cannot be built there. It is here so that the write and
// the read run off one table rather than off two lists that could drift, which
// is what stops this package from writing a row its own read would refuse.
func resolverText(r Resolver) (string, error) {
	if r == (Resolver{}) {
		return "", ErrNoResolver
	}
	for _, known := range resolvers() {
		if r == known {
			return r.name, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownResolver, r.name)
}

// resolverValue turns a stored column back into a Resolver.
//
// It is the one conversion from text into a Resolver, it is unexported, and it
// is reachable only from a read of this package's own column. An exported one,
// or an UnmarshalJSON or a Scan method, would put the person value one decode
// away from any text a caller was handed, which is the property PRD section 8
// asks for. A column that is absent is unknown, which is what a hold resolved
// before this column existed reads back as. A column that is present and
// outside the set is an error rather than an unknown, for the reason
// optionalTimeValue gives: reporting a row nobody can account for as "nothing
// was recorded" is the fabrication Optional exists to prevent.
func resolverValue(o Optional[string], column string) (Optional[Resolver], error) {
	s, ok := o.Get()
	if !ok {
		return Unknown[Resolver](), nil
	}
	for _, known := range resolvers() {
		if s == known.name {
			return Known(known), nil
		}
	}
	return Unknown[Resolver](), fmt.Errorf("store: column %s: %w: %q", column, ErrUnknownResolver, s)
}
