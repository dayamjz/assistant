// Package buildinfo is the one owner of which build this binary is: the
// release version a release build is stamped with at link time, and what the
// Go toolchain recorded about the build either way.
//
// Two readers ask it. internal/cli reports Line for --version, and
// internal/store's CurrentBuild composes Current into the build identity a
// run is recorded against, so the version a person reads and the version a
// run is traced to are one read.
//
// A release build sets Version with the linker, through
//
//	go build -ldflags "-X github.com/dayamjz/assistant/internal/buildinfo.Version=v1.2.3" ./cmd/assistant
//
// which .github/workflows/release.yml does with the tag it was pushed. The
// linker ignores -X for a symbol that does not exist, so renaming Version or
// moving this package would leave a release build reporting no release
// version with nothing failing. Two checks stand against that: this
// package's test builds the binary with VersionSymbol stamped and reads
// --version back through the process, and the release workflow reads the
// version back out of the binary it built before it publishes anything. The
// first proves VersionSymbol names the real symbol; the second proves the
// workflow's own spelling of it, which nothing here can see.
//
// Any other build carries no stamp, and the version is the main module
// version the Go toolchain recorded: "(devel)" where it derived none, the
// module version for go install module@version, and whatever the toolchain
// derived from the checkout otherwise. The revision and the modified flag are
// the toolchain's VCS stamping, absent when it recorded none, as for a build
// outside a checkout or under -buildvcs=false.
//
// The stamp is taken as given once surrounding space is trimmed. Whether it
// is a well-formed release tag is WellFormedReleaseTag, which the release
// workflow invokes on the tag before it builds; nothing else here validates it.
package buildinfo

import (
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Version is the release version a release build is stamped with through the
// linker flag VersionSymbol names. It is empty in every other build, and an
// empty or all-space stamp means no stamp. Nothing in this module assigns it;
// read Current rather than this variable.
var Version string

// VersionSymbol is the fully qualified name the linker's -X flag takes to
// stamp Version. The package's test stamps through it, which is what fails if
// Version is renamed or this package moves.
const VersionSymbol = "github.com/dayamjz/assistant/internal/buildinfo.Version"

// ErrUnidentified is returned by Current when the binary carries neither a
// release stamp nor any build information the toolchain recorded, so there is
// nothing to say which build it is.
var ErrUnidentified = errors.New("buildinfo: the running binary carries no build information")

// Identity is which build a binary is.
type Identity struct {
	// Version is the release version when Stamped, and otherwise the main
	// module version the toolchain recorded, such as "(devel)".
	Version string
	// Stamped reports that Version is the release stamp rather than the
	// toolchain's module version.
	Stamped bool
	// Revision is the source control revision the toolchain recorded, or
	// empty when it recorded none.
	Revision string
	// Modified reports that the toolchain recorded uncommitted changes in the
	// working copy, so Revision names the commit the build started from
	// rather than what it contains.
	Modified bool
	// Go is the Go toolchain version the binary was built with.
	Go string
}

// Current reads the running binary's identity: the release stamp when there
// is one, and the toolchain's build information for the rest. It returns
// ErrUnidentified only when there is neither; a stamped binary whose build
// information cannot be read still answers with its version and the running
// toolchain's version, and no revision.
func Current() (Identity, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		info = nil
	}
	return compose(Version, info)
}

// compose is Current over its two inputs, so a test can hand it build
// information parsed from the toolchain's own text format.
func compose(stamp string, info *debug.BuildInfo) (Identity, error) {
	stamp = strings.TrimSpace(stamp)
	if info == nil {
		if stamp == "" {
			return Identity{}, ErrUnidentified
		}
		return Identity{Version: stamp, Stamped: true, Go: runtime.Version()}, nil
	}
	id := Identity{Version: info.Main.Version, Go: info.GoVersion}
	if stamp != "" {
		id.Version, id.Stamped = stamp, true
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			id.Revision = setting.Value
		case "vcs.modified":
			id.Modified = setting.Value == "true"
		}
	}
	return id, nil
}

// String renders the identity as the words after "assistant" on the version
// line: the version, then the revision with "+modified" when the working copy
// was dirty, then the toolchain version, each left out when empty.
func (id Identity) String() string {
	parts := make([]string, 0, 3)
	if id.Version != "" {
		parts = append(parts, id.Version)
	}
	if id.Revision != "" {
		rev := id.Revision
		if id.Modified {
			rev += "+modified"
		}
		parts = append(parts, rev)
	}
	if id.Go != "" {
		parts = append(parts, id.Go)
	}
	return strings.Join(parts, " ")
}

// Line is what --version reports: "assistant" followed by the identity, so
// its second word is the release version in a release build. A binary
// carrying no identity says so rather than printing an empty line.
func Line() string {
	id, err := Current()
	if err != nil {
		return "assistant (no build information)"
	}
	return fmt.Sprintf("assistant %s", id)
}
