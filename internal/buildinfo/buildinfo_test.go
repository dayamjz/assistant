package buildinfo

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

// parsed is build information in the toolchain's own text format, the one
// "go version -m" prints and debug.BuildInfo.String writes, read back by the
// standard library's parser rather than stated as a struct. That parser never
// sets GoVersion, and debug.ReadBuildInfo sets it from runtime.Version, so
// this sets it the same way, to a fixed value a test can compare against.
func parsed(t *testing.T, text string) *debug.BuildInfo {
	t.Helper()
	info, err := debug.ParseBuildInfo(text)
	if err != nil {
		t.Fatalf("parsing build information: %v\n%s", err, text)
	}
	info.GoVersion = "go1.26.4"
	return info
}

const pathLine = "path\tgithub.com/dayamjz/assistant/cmd/assistant\n"

// The release stamp is the version, and everything else still comes from the
// toolchain's record, so a release binary names both the tag and the commit.
func TestAStampIsTheVersionAndTheToolchainRecordIsTheRest(t *testing.T) {
	t.Parallel()
	info := parsed(t, pathLine+
		"mod\tgithub.com/dayamjz/assistant\t(devel)\t\n"+
		"build\tvcs=git\n"+
		"build\tvcs.revision=0123456789abcdef0123456789abcdef01234567\n"+
		"build\tvcs.modified=true\n")

	id, err := compose("  v1.2.3\n", info)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	want := Identity{Version: "v1.2.3", Stamped: true, Revision: "0123456789abcdef0123456789abcdef01234567", Modified: true, Go: "go1.26.4"}
	if id != want {
		t.Fatalf("compose = %+v, want %+v", id, want)
	}
	if got := id.String(); got != "v1.2.3 0123456789abcdef0123456789abcdef01234567+modified go1.26.4" {
		t.Fatalf("String = %q", got)
	}
}

// With no stamp the version is the module version the toolchain recorded,
// "(devel)" included, and an all-space stamp is no stamp rather than an empty
// version.
func TestNoStampIsTheModuleVersion(t *testing.T) {
	t.Parallel()
	for _, stamp := range []string{"", " \t\n"} {
		info := parsed(t, pathLine+"mod\tgithub.com/dayamjz/assistant\t(devel)\t\n")
		id, err := compose(stamp, info)
		if err != nil {
			t.Fatalf("compose(%q): %v", stamp, err)
		}
		want := Identity{Version: "(devel)", Go: "go1.26.4"}
		if id != want {
			t.Fatalf("compose(%q) = %+v, want %+v", stamp, id, want)
		}
		if got := id.String(); got != "(devel) go1.26.4" {
			t.Fatalf("String = %q", got)
		}
	}
}

// A binary with no build information is unidentified unless it was stamped,
// and a stamped one still says which release it is.
func TestNoBuildInformationIsUnidentifiedUnlessStamped(t *testing.T) {
	t.Parallel()
	if _, err := compose("", nil); !errors.Is(err, ErrUnidentified) {
		t.Fatalf("compose with nothing = %v, want ErrUnidentified", err)
	}
	id, err := compose("v1.2.3", nil)
	if err != nil {
		t.Fatalf("compose stamped: %v", err)
	}
	if !id.Stamped || id.Version != "v1.2.3" || id.Revision != "" || id.Go != runtime.Version() {
		t.Fatalf("compose stamped with no build information = %+v", id)
	}
}

// The linker ignores -X for a symbol that does not exist, so a release build
// stamping a name that is not Version's would report no release version and
// nothing would fail. This builds the real binary the way the release workflow
// does, stamped through VersionSymbol, and reads --version back through the
// process in both renderings, which is what fails if Version is renamed, this
// package moves, or cmd/assistant stops reporting Line. The unstamped build is
// the control: without it a read that always returned the stamp would pass.
func TestTheLinkerStampReachesVersionThroughTheBinary(t *testing.T) {
	t.Parallel()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("this test builds the binary and needs go on PATH: %v", err)
	}
	const stamp = "v0.0.0-stamp.test"
	dir := t.TempDir()

	build := func(name string, ldflags ...string) string {
		t.Helper()
		out := filepath.Join(dir, name)
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		args := append([]string{"build", "-o", out}, ldflags...)
		cmd := exec.Command(goTool, append(args, "github.com/dayamjz/assistant/cmd/assistant")...)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if combined, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(cmd.Args[1:], " "), err, combined)
		}
		return out
	}
	versionOf := func(binary string, args ...string) string {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "ASSISTANT_HOME="+filepath.Join(dir, "home"))
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	stamped := build("stamped", "-ldflags", "-X "+VersionSymbol+"="+stamp)
	line := versionOf(stamped, "--version")
	if fields := strings.Fields(line); len(fields) < 2 || fields[0] != "assistant" || fields[1] != stamp {
		t.Fatalf("a binary stamped %s reports %q, want \"assistant %s ...\"", stamp, line, stamp)
	}
	var document struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(versionOf(stamped, "--json", "--version")), &document); err != nil {
		t.Fatalf("--json --version wrote no document: %v", err)
	}
	if document.Version != line {
		t.Fatalf("--json --version names %q, the plain line is %q", document.Version, line)
	}

	plain := versionOf(build("plain"), "--version")
	if fields := strings.Fields(plain); len(fields) < 2 || fields[0] != "assistant" || fields[1] == stamp {
		t.Fatalf("an unstamped binary reports %q, which should name the module version and not %s", plain, stamp)
	}
}
