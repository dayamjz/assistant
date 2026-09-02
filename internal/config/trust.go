package config

// Origin says which document a configuration layer is and where its bytes were
// read from. It is required to parse one, because both halves of that decide
// what the document may say: the same bytes are admissible as one origin and
// inadmissible as another.
//
// The origin distinguishes the operator's own file from a repository file,
// which is what the global-only class turns on. A repository file is in turn
// trusted or pushed depending on where it was read from, which is what the
// command and trusted-only classes turn on; the operator's file has no such
// split, because it never travels with a branch.
//
// This package classifies and admits. It does not fetch, and it cannot check
// that an origin was reported honestly; a caller that reads a pushed branch
// and labels it OriginTrusted gets a trusted layer. Resolving the origin from
// git is the gate's job, per PRD section 10.
type Origin uint8

const (
	// OriginUnknown is the zero Origin. Parsing a layer with it is refused
	// with ErrUnknownOrigin rather than being treated as any of the others.
	OriginUnknown Origin = iota
	// OriginGlobal is the operator's own configuration file in their home. It
	// is the only origin that may set a TrustGlobal key.
	OriginGlobal
	// OriginTrusted is a repository file read from the default branch at a
	// commit resolved by a fresh fetch.
	OriginTrusted
	// OriginPushed is a repository file read from the branch under
	// validation. It is untrusted input: a contributor controls it.
	OriginPushed
)

// String returns the origin's name, which is what appears in rejections.
func (o Origin) String() string {
	switch o {
	case OriginGlobal:
		return "global"
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

// isRepository reports whether the origin is a repository file rather than the
// operator's own file.
func (o Origin) isRepository() bool {
	return o == OriginTrusted || o == OriginPushed
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
	// with the operator's credentials. A pushed branch may set one only when
	// KeyAllowPushedCommands is set, and that opt-out is itself TrustTrusted,
	// so a branch cannot enable itself.
	TrustCommands
	// TrustTrusted is a key that shapes what review or documentation demands,
	// or that declares a check unnecessary. A pushed branch may never set one.
	TrustTrusted
	// TrustGlobal is a key only the operator's own file may set. These are
	// preferences about how this machine behaves rather than facts about a
	// repository, so a repository file may not carry one at all, harmless or
	// not. It is the one class refused when a document is parsed rather than
	// dropped when layers merge, because the key has no business appearing in
	// a repository file in the first place.
	TrustGlobal
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
	case TrustGlobal:
		return "global-only"
	default:
		return "trust(" + itoa(int(t)) + ")"
	}
}

// refusedInRepositoryFile reports whether a repository file may not carry a key
// of this class at all. Only TrustGlobal is in that position, and Parse uses
// this to refuse such a key rather than letting it reach the merge.
//
// This is a different question from admits, which decides whose value wins once
// documents that may legitimately contain a key are merged. A pushed file may
// contain a TrustTrusted key: a contributor commonly inherits one from the
// trusted file, and dropping it at the merge is right. A repository file
// containing a TrustGlobal key is instead a file saying something it has no
// standing to say, and it is refused where the author will see it.
func (t Trust) refusedInRepositoryFile() bool { return t == TrustGlobal }

// admits reports whether a key of this class may be taken from origin when
// layers merge. allowPushedCommands is the resolved value of
// KeyAllowPushedCommands, which only ever comes from a trusted origin because
// that key is TrustTrusted.
//
// TrustGlobal returns false for either repository origin here as well, but
// nothing reaches that branch through Parse, which refuses such a key when the
// document is read. Resolve consults this for every key regardless, so the
// answer is stated rather than assumed.
func (t Trust) admits(o Origin, allowPushedCommands bool) bool {
	if t == TrustGlobal {
		return o == OriginGlobal
	}
	if o == OriginGlobal || o == OriginTrusted {
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
// falls back to the global layer or to its default. It is reported so the
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
