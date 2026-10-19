package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/buildinfo"
)

// Build identifies the software that produced a run. PRD section 8 requires a
// surprising result to be traceable to the exact build and configuration that
// reached it, which is why CreateRun refuses a run that carries neither.
//
// Modified matters as much as Revision: a binary built from a dirty working
// copy is not the commit it names, and a verdict traced to that commit would be
// traced to code that was never what ran.
type Build struct {
	// Version is the release version a release build was stamped with, or
	// otherwise the module version the toolchain recorded, such as "(devel)".
	Version string `json:"version"`
	// Revision is the source control revision the binary was built from.
	Revision string `json:"revision"`
	// Modified reports that the working copy had uncommitted changes at build
	// time, so Revision names the commit the build started from rather than
	// what it contains.
	Modified bool `json:"modified"`
	// Go is the Go toolchain version.
	Go string `json:"go"`
}

// ErrBuildUnidentified is returned by CurrentBuild when the running binary
// carries no build information at all.
var ErrBuildUnidentified = errors.New("store: the running binary carries no build information")

// Validate reports whether the identity is usable for tracing. It requires the
// toolchain version and at least one of the version or the revision, because an
// identity that names neither cannot be traced back to anything and recording
// it would look like traceability without providing it.
func (b Build) Validate() error {
	if strings.TrimSpace(b.Go) == "" {
		return fmt.Errorf("%w: no Go toolchain version", ErrBuildIdentityMissing)
	}
	if strings.TrimSpace(b.Version) == "" && strings.TrimSpace(b.Revision) == "" {
		return fmt.Errorf("%w: neither a version nor a revision", ErrBuildIdentityMissing)
	}
	return nil
}

// String renders the identity for a report.
func (b Build) String() string {
	parts := make([]string, 0, 3)
	if b.Version != "" {
		parts = append(parts, b.Version)
	}
	if b.Revision != "" {
		rev := b.Revision
		if b.Modified {
			rev += "+modified"
		}
		parts = append(parts, rev)
	}
	if b.Go != "" {
		parts = append(parts, b.Go)
	}
	if len(parts) == 0 {
		return "unidentified build"
	}
	return strings.Join(parts, " ")
}

// CurrentBuild reads the running binary's identity from internal/buildinfo,
// the one owner of which build this is, so a release build records the
// release version it was stamped with and any other build the module version
// the toolchain recorded. It is a convenience for the common caller; a caller
// whose build system knows better constructs a Build itself.
//
// It returns an error rather than a partial identity when the binary carries no
// build information, so a run cannot be recorded against a build nobody can
// find. A binary built without source control stamping still validates on its
// version.
func CurrentBuild() (Build, error) {
	id, err := buildinfo.Current()
	if errors.Is(err, buildinfo.ErrUnidentified) {
		return Build{}, ErrBuildUnidentified
	}
	if err != nil {
		return Build{}, err
	}
	b := Build{Version: id.Version, Revision: id.Revision, Modified: id.Modified, Go: id.Go}
	if err := b.Validate(); err != nil {
		return Build{}, err
	}
	return b, nil
}
