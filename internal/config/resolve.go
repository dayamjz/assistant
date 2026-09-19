package config

// Resolution is the outcome of merging the configuration layers.
type Resolution struct {
	// Config is the resolved configuration. Every key holds either the value
	// the winning layer set or the schema default.
	Config Config
	// Rejected lists the keys a layer set that its origin was not allowed to
	// set, in schema order. Rejection documents what a dropped key falls back
	// to and why it is not an error.
	Rejected []Rejection
}

// RepositoryDocument is the name of the repository configuration document,
// which PRD section 10 places at the repository root. This constant is the
// name's one owner: the trusted copy is this file as the default branch holds
// it at a freshly fetched commit, the pushed copy is this file as the branch
// under validation holds it, and whoever reads either spells the name from
// here.
const RepositoryDocument = "assistant.json"

// Resolve merges the global layer and one repository layer into one
// configuration. The repository layer overrides the global one key by key, so
// a repository that sets one fix round limit keeps the global values for
// everything else, including the other fix round limits.
//
// The two layers are distinguished by origin and neither may stand in for the
// other: the global layer must have OriginGlobal, the operator's own file in
// their home, and the repository layer must have a repository origin, trusted
// or pushed.
//
// It is ResolveRun with the repository's other copy absent, and the merge
// rules are stated there once: this entry point exists for a caller holding
// one repository document, such as a tool validating a single file, and for
// the composition ResolveRun performs it is the projection rather than a
// second spelling of the rules.
func Resolve(global, repo Layer) (Resolution, error) {
	if repo.origin == OriginUnknown {
		return Resolution{}, ErrUnknownOrigin
	}
	if !repo.origin.isRepository() {
		return Resolution{}, ErrNotRepositoryLayer
	}
	if repo.origin == OriginTrusted {
		return ResolveRun(global, repo, Absent(OriginPushed))
	}
	return ResolveRun(global, Absent(OriginTrusted), repo)
}

// ResolveRun merges the three documents a run resolves its configuration
// from: the operator's global layer, the repository document as the default
// branch holds it at a freshly fetched commit, and the same document as the
// branch under validation holds it. It is the composition PRD section 10's
// trust diagram describes, and the one owner of it.
//
// The layers are distinguished by origin and none may stand in another's
// position: the global layer must have OriginGlobal, the trusted layer
// OriginTrusted, and the pushed layer OriginPushed. A copy that is not there
// is passed as Absent with that origin; a copy that exists and cannot be read
// or parsed is not representable here at all, because for the trusted
// document PRD section 10 requires the caller to stop the run before this
// function is reached.
//
// Keys are admitted by trust class, latest admitted layer winning:
//
//   - A TrustPushed key is taken from the pushed copy, else the trusted copy,
//     else the global layer.
//   - A TrustCommands key is taken from the trusted copy or the global layer;
//     the pushed copy sets one only when KeyAllowPushedCommands resolved to
//     true, and that key is TrustTrusted, so only the global layer or the
//     trusted copy can turn the opt-out on.
//   - A TrustTrusted key is taken from the trusted copy or the global layer,
//     never from the pushed copy.
//   - A TrustGlobal key is taken from the global layer alone. Parse refuses
//     one in a repository file, so a layer built by Parse cannot carry one
//     here; the check below states the rule anyway rather than relying on
//     that.
//
// A key a repository copy set and its origin may not set is dropped and
// reported as a Rejection, per the package documentation: it was validated
// when its document was parsed, so the author has already been told about a
// broken value, and the drop is what tells them a valid one had no effect.
//
// What ResolveRun enforces is the classification. It cannot check that an
// origin was reported honestly, because it does not fetch: reading each copy
// from the commit its origin names, and stopping the run when the trusted
// copy cannot be read, is the caller's part of P7 and happens before this
// function is called.
func ResolveRun(global, trusted, pushed Layer) (Resolution, error) {
	if global.origin == OriginUnknown || trusted.origin == OriginUnknown || pushed.origin == OriginUnknown {
		return Resolution{}, ErrUnknownOrigin
	}
	if global.origin != OriginGlobal {
		return Resolution{}, ErrNotGlobalLayer
	}
	if trusted.origin != OriginTrusted || pushed.origin != OriginPushed {
		return Resolution{}, ErrNotRepositoryLayer
	}
	allowPushed := resolveAllowPushedCommands(global, trusted)

	var res Resolution
	for _, s := range specs {
		value := s.def
		if v, set := global.values[s.key]; set {
			value = v
		}
		for _, layer := range []Layer{trusted, pushed} {
			v, set := layer.values[s.key]
			if !set {
				continue
			}
			if s.trust.admits(layer.origin, allowPushed) {
				value = v
			} else {
				res.Rejected = append(res.Rejected, Rejection{
					Key:    s.key,
					Origin: layer.origin,
					Trust:  s.trust,
				})
			}
		}
		s.set(&res.Config, value)
	}
	return res, nil
}

// resolveAllowPushedCommands resolves the opt-out on its own, before anything
// depends on it. The key is TrustTrusted, so of the repository copies only
// the trusted one may set it; a pushed copy setting it is ignored here and
// reported as a rejection by the main pass.
func resolveAllowPushedCommands(global, trusted Layer) bool {
	allow := false
	if v, set := global.values[KeyAllowPushedCommands]; set {
		allow = v.(bool)
	}
	if v, set := trusted.values[KeyAllowPushedCommands]; set {
		allow = v.(bool)
	}
	return allow
}
