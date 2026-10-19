package config

import "slices"

// ReducedPath is the reduced-validation path a repository may name. The set
// is closed: decoding refuses any other text, including "direct", which PRD
// section 12 defers and KeyDelivery already refuses on the delivery key.
//
// Off is the default and names no reduced path. The nine-stage gate stays
// what a run does until a run asks and the resolved value is record or
// publish. The key is trusted-only, so a pushed branch cannot name one for
// itself.
type ReducedPath string

const (
	// ReducedPathOff names no reduced path. A run validates through the
	// nine-stage gate.
	ReducedPathOff ReducedPath = "off"
	// ReducedPathRecord skips validation and publishes nothing. The run
	// record states that validation was not performed.
	ReducedPathRecord ReducedPath = "record"
	// ReducedPathPublish skips validation and may open a pull request. The
	// run record states that validation was not performed. Merge authority
	// is unchanged: autonomy.merge, and only a run that ended checks-passed.
	ReducedPathPublish ReducedPath = "publish"
)

// ValidationRequired is what a delivery mode requires before the change may
// be treated as validated.
type ValidationRequired string

const (
	// ValidationNineStage is the default gate.
	ValidationNineStage ValidationRequired = "the nine-stage gate"
	// ValidationNone is the reduced path. The run record states that
	// validation was not performed.
	ValidationNone ValidationRequired = "none; the run record states that validation was not performed"
)

// Publication is what a mode may publish.
type Publication string

const (
	// PublicationPullRequest is a pull request.
	PublicationPullRequest Publication = "a pull request"
	// PublicationNone publishes nothing.
	PublicationNone Publication = "nothing"
)

// MergeAuthority is who may authorize a merge. Every mode names the same
// rule, so a reduced path cannot grant a merge today's rules would refuse.
type MergeAuthority string

const (
	// MergeByAutonomy is autonomy.merge, and only after a run ended
	// checks-passed.
	MergeByAutonomy MergeAuthority = "autonomy.merge after a run ended checks-passed, and not otherwise"
)

// DeliveryMode is one row of what a reduced-path value means: the validation
// it requires, what it may publish, and who may authorize a merge. The table
// is the closed set decode accepts, so a value the table does not name cannot
// be configured.
type DeliveryMode struct {
	Path        ReducedPath
	Validation  ValidationRequired
	Publication Publication
	Merge       MergeAuthority
}

// deliveryModes is the one list. Off is first because it is the default.
var deliveryModes = []DeliveryMode{
	{ReducedPathOff, ValidationNineStage, PublicationPullRequest, MergeByAutonomy},
	{ReducedPathRecord, ValidationNone, PublicationNone, MergeByAutonomy},
	{ReducedPathPublish, ValidationNone, PublicationPullRequest, MergeByAutonomy},
}

// DeliveryModes returns the closed set, default first. The result is a copy.
func DeliveryModes() []DeliveryMode { return slices.Clone(deliveryModes) }

// Mode returns the row for p. The second result is false when p is not in
// the table, which is every value decode refuses.
func (p ReducedPath) Mode() (DeliveryMode, bool) {
	for _, mode := range deliveryModes {
		if mode.Path == p {
			return mode, true
		}
	}
	return DeliveryMode{}, false
}

// Named reports whether p is a reduced path. It is true exactly when the
// mode's validation is none, so the default nine-stage row is not one and a
// row added to the table with that validation is, without a second list.
func (p ReducedPath) Named() bool {
	mode, ok := p.Mode()
	return ok && mode.Validation == ValidationNone
}

// MayPublish reports whether this reduced path may open a pull request. The
// default row publishes through the nine-stage gate and is not this path, so
// it reports false.
func (m DeliveryMode) MayPublish() bool {
	return m.Path.Named() && m.Publication == PublicationPullRequest
}

// decodeReducedPath reads a reduced path. The accepted values are the mode
// table, so a second list of them cannot drift from the statements.
func decodeReducedPath(k Key, v any) (any, error) {
	s, err := asString(k, v)
	if err != nil {
		return nil, err
	}
	for _, mode := range deliveryModes {
		if string(mode.Path) == s {
			return mode.Path, nil
		}
	}
	return nil, keyErr(k, v, reducedPathExpected())
}

func reducedPathExpected() string {
	parts := make([]string, len(deliveryModes))
	for i, mode := range deliveryModes {
		parts[i] = quote(string(mode.Path))
	}
	if len(parts) < 2 {
		return "expected " + parts[0]
	}
	return "expected " + joinWithOr(parts)
}

func joinWithOr(parts []string) string {
	if len(parts) == 2 {
		return parts[0] + " or " + parts[1]
	}
	last := parts[len(parts)-1]
	return joinComma(parts[:len(parts)-1]) + ", or " + last
}

func joinComma(parts []string) string {
	out := parts[0]
	for _, part := range parts[1:] {
		out += ", " + part
	}
	return out
}
