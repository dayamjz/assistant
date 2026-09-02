package config

// Resolution is the outcome of merging the two configuration layers.
type Resolution struct {
	// Config is the resolved configuration. Every key holds either the value
	// the winning layer set or the schema default.
	Config Config
	// Rejected lists the keys a layer set that its origin was not allowed to
	// set, in schema order. These are not errors: the key fell back to the
	// trusted layer or to its default. They are reported so a caller can tell
	// an author their setting had no effect instead of leaving them to
	// discover it from behavior.
	Rejected []Rejection
}

// Resolve merges the global layer and the repository layer into one
// configuration. The repository layer overrides the global one key by key, so
// a repository that sets one fix round limit keeps the global values for
// everything else, including the other fix round limits.
//
// The two layers are distinguished by origin and neither may stand in for the
// other: the global layer must have OriginGlobal, the operator's own file in
// their home, and the repository layer must have a repository origin, trusted
// or pushed. The repository layer's origin decides which of its keys are
// admitted:
//
//   - A TrustPushed key is taken from either repository origin.
//   - A TrustCommands key is taken from a pushed origin only when
//     KeyAllowPushedCommands resolved to true, and that key is TrustTrusted,
//     so only a trusted layer can turn the opt-out on.
//   - A TrustTrusted key is never taken from a pushed origin.
//   - A TrustGlobal key is never taken from a repository layer at all. Parse
//     refuses one in a repository file, so a layer built by Parse cannot carry
//     one here; the check below states the rule anyway rather than relying on
//     that.
//
// What Resolve enforces is the classification. It cannot check that an origin
// was reported truthfully, because it does not fetch: reading the trusted
// document from the default branch at a freshly fetched commit, and stopping
// the run when that document cannot be read, is the gate's part of P7 and
// happens before this function is called.
func Resolve(global, repo Layer) (Resolution, error) {
	if global.origin == OriginUnknown || repo.origin == OriginUnknown {
		return Resolution{}, ErrUnknownOrigin
	}
	if global.origin != OriginGlobal {
		return Resolution{}, ErrNotGlobalLayer
	}
	if !repo.origin.isRepository() {
		return Resolution{}, ErrNotRepositoryLayer
	}
	allowPushed := resolveAllowPushedCommands(global, repo)

	var res Resolution
	for _, s := range specs {
		value := s.def
		if v, set := global.values[s.key]; set {
			value = v
		}
		if v, set := repo.values[s.key]; set {
			if s.trust.admits(repo.origin, allowPushed) {
				value = v
			} else {
				res.Rejected = append(res.Rejected, Rejection{
					Key:    s.key,
					Origin: repo.origin,
					Trust:  s.trust,
				})
			}
		}
		s.set(&res.Config, value)
	}
	return res, nil
}

// resolveAllowPushedCommands resolves the opt-out on its own, before anything
// depends on it. The key is TrustTrusted, so a repository layer sets it only
// when that layer is itself trusted; a pushed layer setting it is ignored
// here and reported as a rejection by the main pass.
func resolveAllowPushedCommands(global, repo Layer) bool {
	allow := false
	if v, set := global.values[KeyAllowPushedCommands]; set {
		allow = v.(bool)
	}
	if v, set := repo.values[KeyAllowPushedCommands]; set && repo.origin == OriginTrusted {
		allow = v.(bool)
	}
	return allow
}
