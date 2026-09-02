package config

// Origin says where a configuration document's bytes were read from. It is
// required to parse one, because trust is a property of the source and not of
// the text: the same bytes are admissible from one origin and inadmissible
// from another.
//
// This package classifies and admits. It does not fetch, and it cannot check
// that an origin was reported honestly; a caller that reads a pushed branch
// and labels it OriginTrusted gets a trusted layer. Resolving the origin from
// git is the gate's job, per PRD section 10.
type Origin uint8

const (
	// OriginUnknown is the zero Origin. Parsing a layer with it is refused
	// with ErrUnknownOrigin rather than being treated as either trusted or
	// untrusted.
	OriginUnknown Origin = iota
	// OriginTrusted is the operator's global file, or a repository file read
	// from the default branch at a commit resolved by a fresh fetch.
	OriginTrusted
	// OriginPushed is a repository file read from the branch under
	// validation. It is untrusted input: a contributor controls it.
	OriginPushed
)

// String returns the origin's name, which is what appears in rejections.
func (o Origin) String() string {
	switch o {
	case OriginTrusted:
		return "trusted"
	case OriginPushed:
		return "pushed"
	case OriginUnknown:
		return "unknown"
	default:
		return "origin(" + itoa(int(o)) + ")"
	}
}

// Trust is the class of one configuration key: which origins may set it. Every
// key in the schema has exactly one class, declared in the key table, which is
// the only place these are assigned.
type Trust uint8

const (
	// TrustPushed is a key a pushed branch may set. These keys cannot execute
	// anything and cannot weaken a check.
	TrustPushed Trust = iota + 1
	// TrustCommands is a key that runs shell or chooses which process starts
	// with the operator's credentials. It is read from a trusted origin unless
	// KeyAllowPushedCommands is set, and that opt-out is itself TrustTrusted,
	// so a branch cannot enable itself.
	TrustCommands
	// TrustTrusted is a key that shapes what review or documentation demands,
	// or that declares a check unnecessary. A pushed branch may never set one.
	TrustTrusted
)

// String returns the class name, which is what appears in rejections.
func (t Trust) String() string {
	switch t {
	case TrustPushed:
		return "pushed"
	case TrustCommands:
		return "trusted-unless-opted-out"
	case TrustTrusted:
		return "trusted-only"
	default:
		return "trust(" + itoa(int(t)) + ")"
	}
}

// admits reports whether a key of this class may be taken from origin.
// allowPushedCommands is the resolved value of KeyAllowPushedCommands, which
// only ever comes from a trusted origin because that key is TrustTrusted.
func (t Trust) admits(o Origin, allowPushedCommands bool) bool {
	if o == OriginTrusted {
		return true
	}
	if o != OriginPushed {
		return false
	}
	switch t {
	case TrustPushed:
		return true
	case TrustCommands:
		return allowPushedCommands
	default:
		return false
	}
}

// Rejection is one key that parsed cleanly but was not admitted, because the
// origin it was set from is not allowed to set it. It is not an error: the key
// falls back to the trusted layer or to its default. It is reported so the
// caller can tell an author that their setting had no effect rather than
// leaving them to wonder.
type Rejection struct {
	// Key is the key that was dropped.
	Key Key
	// Origin is where the dropped value was set.
	Origin Origin
	// Trust is the key's class, which is why it was dropped.
	Trust Trust
}

// String renders the rejection as one line naming the key, its class, and the
// origin it was refused from.
func (r Rejection) String() string {
	return string(r.Key) + " is " + r.Trust.String() + " and was set from the " + r.Origin.String() + " layer"
}
