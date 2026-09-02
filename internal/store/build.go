package store

import (
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
)

// Build identifies the software that produced a run. PRD section 8 requires a
// surprising result to be traceable to the exact build and configuration that
// reached it, which is why CreateRun refuses a run that carries neither.
//
// Modified matters as much as Revision: a binary built from a dirty working
// copy is not the commit it names, and a verdict traced to that commit would be
// traced to code that was never what ran.
type Build struct {
	// Version is the module version, such as a tag, or "(devel)" for a build
	// from a working copy.
	Version string
	// Revision is the source control revision the binary was built from.
	Revision string
	// Modified reports that the working copy had uncommitted changes at build
	// time, so Revision names the commit the build started from rather than
	// what it contains.
	Modified bool
	// Go is the Go toolchain version.
	Go string
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

// CurrentBuild reads the running binary's identity from the information the Go
// toolchain stamps into it. It is a convenience for the common caller; a caller
// whose build system knows better constructs a Build itself.
//
// It returns an error rather than a partial identity when the binary carries no
// build information, so a run cannot be recorded against a build nobody can
// find. A binary built without source control stamping still validates on its
// module version.
func CurrentBuild() (Build, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Build{}, ErrBuildUnidentified
	}
	b := Build{Version: info.Main.Version, Go: info.GoVersion}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			b.Revision = setting.Value
		case "vcs.modified":
			b.Modified = setting.Value == "true"
		}
	}
	if err := b.Validate(); err != nil {
		return Build{}, err
	}
	return b, nil
}
