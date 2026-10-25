package standin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
)

// registeredTempTag is the namespace RegisterTempTag recorded for this
// process. TestMain writes it before tests run. TempDir and SweepTempDirs
// only read it, so a concurrent test does not choose a namespace.
var registeredTempTag string

// RegisterTempTag records the namespace this process sweeps: the directory
// of the caller, relative to the module that contains it. '_' in a directory
// name encodes as "_0" and a slash between directories encodes as "_1", so
// two directories never share a namespace, and a call cannot pass a different
// one. An absolute caller path is made relative by walking up to go.mod. A
// non-absolute path is accepted only when it begins with the main module
// path recorded in the binary followed by the directory, which is the form
// -trimpath leaves, and that path is not opened. TestMain calls it before
// TempDir or SweepTempDirs. Registering does not sweep. A second call from
// the same directory records the same namespace. A second call from another directory
// is refused and leaves the recorded namespace as it was. A directory whose
// name contains a hyphen is refused, because the stamped name uses a hyphen
// to separate the namespace from the process identity.
func RegisterTempTag() error {
	_, file, _, ok := runtime.Caller(1)
	if !ok || file == "" {
		return fmt.Errorf("standin: RegisterTempTag found no caller")
	}
	tag, err := namespaceFromFile(file)
	if err != nil {
		return err
	}
	if registeredTempTag != "" && registeredTempTag != tag {
		return fmt.Errorf("standin: test temp namespace already recorded as %s, not %s", registeredTempTag, tag)
	}
	registeredTempTag = tag
	return nil
}

// RegisteredTempTag returns the namespace RegisterTempTag recorded. It
// refuses when TestMain recorded none, so a helper cannot stamp a directory
// under a namespace no sweep will see.
func RegisteredTempTag() (string, error) {
	if registeredTempTag == "" {
		return "", fmt.Errorf("standin: no test temp namespace registered; TestMain must call RegisterTempTag before making a directory")
	}
	return registeredTempTag, nil
}

// TempDir makes a test directory under tempRoot, stamped with the namespace
// RegisterTempTag recorded for this process. It refuses when none is
// recorded, so there is no argument a caller can use to select another
// namespace. Pattern is retained in the name (including spaces) for tests
// that exercise paths, and the owning process's PID and start time precede
// it. Callers still remove the directory on normal exit.
func TempDir(pattern string) (string, error) {
	tag, err := RegisteredTempTag()
	if err != nil {
		return "", err
	}
	owner, found, err := agents.Identify(os.Getpid())
	if err != nil {
		return "", fmt.Errorf("standin: identifying test temp directory owner: %w", err)
	}
	if !found || owner.Start == 0 {
		return "", fmt.Errorf("standin: no readable identity for test temp directory owner %d", os.Getpid())
	}
	return os.MkdirTemp(tempRoot(), tempName(tag, pattern, owner))
}

// SweepTempDirs reclaims directories TempDir made for the namespace
// RegisterTempTag recorded, when their recorded owner is absent or replaced.
// It leaves live owners, malformed names and symlinks alone. A failed
// identity read or removal is returned to TestMain; it never turns an
// uncertain owner into permission to remove a directory. It refuses when
// nothing is registered.
func SweepTempDirs() error {
	tag, err := RegisteredTempTag()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(tempRoot())
	if err != nil {
		return fmt.Errorf("standin: listing test temp directories: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		namespace, owner, ok := parseTempName(entry.Name())
		if !ok || namespace != tag {
			continue
		}
		presence, err := owner.Presence()
		if err != nil {
			return fmt.Errorf("standin: checking owner of %s: %w", entry.Name(), err)
		}
		if presence == agents.PresencePresent {
			continue
		}
		path := filepath.Join(tempRoot(), entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("standin: reclaiming %s: %w", path, err)
		}
	}
	return nil
}

// ReclaimProcess removes temp directories whose recorded owner is pid when
// that process is not still present, under any namespace TempDir can make.
// A live owner is left, and so is a name that does not parse, including one
// whose namespace TempDir cannot produce. A failed identity read is returned
// and that directory is not removed.
func ReclaimProcess(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("standin: reclaiming temp directories of pid %d", pid)
	}
	entries, err := os.ReadDir(tempRoot())
	if err != nil {
		return fmt.Errorf("standin: listing test temp directories: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		_, owner, ok := parseTempName(entry.Name())
		if !ok || owner.PID != pid {
			continue
		}
		presence, err := owner.Presence()
		if err != nil {
			return fmt.Errorf("standin: checking owner of %s: %w", entry.Name(), err)
		}
		if presence == agents.PresencePresent {
			continue
		}
		path := filepath.Join(tempRoot(), entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("standin: reclaiming %s: %w", path, err)
		}
	}
	return nil
}

// namespaceFromFile is the caller's directory within its module. '_' encodes
// as "_0" and '/' as "_1", so a directory named a_b and directories a/b or
// a/_b do not match. The encoding emits no hyphen, and the trailing hyphen
// tempPrefix adds keeps a shorter directory from matching a longer one's names.
func namespaceFromFile(file string) (string, error) {
	rel, err := moduleRelativeDir(file)
	if err != nil {
		return "", err
	}
	return encodeNamespace(rel)
}

func moduleRelativeDir(file string) (string, error) {
	dir := filepath.Dir(file)
	if filepath.IsAbs(dir) {
		root, err := moduleRootFrom(dir)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return "", fmt.Errorf("standin: test temp namespace: %w", err)
		}
		return filepath.ToSlash(rel), nil
	}
	slash := filepath.ToSlash(dir)
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil || info.Main.Path == "" {
		return "", fmt.Errorf("standin: no module path recorded for caller %s", file)
	}
	prefix := info.Main.Path
	if slash == prefix {
		return ".", nil
	}
	if !strings.HasPrefix(slash, prefix+"/") {
		return "", fmt.Errorf("standin: caller %s is not in module %s", file, prefix)
	}
	return strings.TrimPrefix(slash, prefix+"/"), nil
}

func encodeNamespace(rel string) (string, error) {
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return "", fmt.Errorf("standin: test temp namespace %s is outside the module", rel)
	}
	if strings.Contains(rel, "-") {
		return "", fmt.Errorf("standin: test temp namespace %s contains a hyphen, which separates the namespace from the process identity", rel)
	}
	var b strings.Builder
	for _, r := range rel {
		switch r {
		case '/':
			b.WriteString("_1")
		case '_':
			b.WriteString("_0")
		default:
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return "", fmt.Errorf("standin: test temp namespace %q is not a module directory", rel)
			}
			b.WriteRune(r)
		}
	}
	tag := b.String()
	if err := validTempTag(tag); err != nil {
		return "", err
	}
	return tag, nil
}

func moduleRootFrom(dir string) (string, error) {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("standin: no module root above %s", dir)
		}
		dir = parent
	}
}

func validTempTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("standin: test temp namespace is empty")
	}
	for _, r := range tag {
		if r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return fmt.Errorf("standin: test temp namespace %q is not a module directory", tag)
		}
	}
	return nil
}

func tempPrefix(tag string) string { return "at" + tag + "-" }

func tempName(tag, pattern string, owner agents.Identity) string {
	return tempPrefix(tag) + strconv.FormatInt(int64(owner.PID), 36) + "-" +
		strconv.FormatUint(owner.Start, 36) + "-" + pattern
}

// unparseableTempDir is a directory whose name does not record an owner.
// SweepTempDirs leaves it. TempDir cannot make one, because every directory
// it makes records the calling process.
func unparseableTempDir(tag string) (string, error) {
	return os.MkdirTemp(tempRoot(), tempPrefix(tag)+"unparseable-")
}

// replacedTempDir is a directory recorded as id, which a sweep test sets to
// this process's identifier with a different start time. TempDir would
// record the real identity, and SweepTempDirs would keep that directory.
func replacedTempDir(tag string, id agents.Identity) (string, error) {
	return os.MkdirTemp(tempRoot(), tempName(tag, "replaced-", id))
}

// tempRoot keeps local socket paths short on the two platforms that host
// terminal tests. The system temp directory on macOS is much deeper than the
// socket limit allows once a process identity is included in the name.
func tempRoot() string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		return "/tmp"
	}
	return os.TempDir()
}

// parseTempName reads a name tempName made: the namespace and the recorded
// owner. ok is false for any name tempName could not have made from a
// namespace encodeNamespace produces, so a sweep never removes a directory
// on the strength of a name this package did not write.
func parseTempName(name string) (namespace string, owner agents.Identity, ok bool) {
	rest, found := strings.CutPrefix(name, "at")
	if !found {
		return "", agents.Identity{}, false
	}
	// The encoding emits no hyphen, so the first one ends the namespace.
	namespace, ownerPart, found := strings.Cut(rest, "-")
	if !found || !producibleTempTag(namespace) {
		return "", agents.Identity{}, false
	}
	owner, ok = parseTempOwner(ownerPart)
	if !ok {
		return "", agents.Identity{}, false
	}
	return namespace, owner, true
}

// producibleTempTag reports whether encodeNamespace returns tag for some
// module directory: tag decodes, "_0" to '_' and "_1" to '/', with every
// other byte a lowercase letter or a digit, to a path with no empty
// segment, and that path encodes back to tag.
func producibleTempTag(tag string) bool {
	var rel strings.Builder
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		switch {
		case c == '_' && i+1 < len(tag) && tag[i+1] == '0':
			rel.WriteByte('_')
			i++
		case c == '_' && i+1 < len(tag) && tag[i+1] == '1':
			rel.WriteByte('/')
			i++
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			rel.WriteByte(c)
		default:
			return false
		}
	}
	decoded := rel.String()
	for segment := range strings.SplitSeq(decoded, "/") {
		if segment == "" {
			return false
		}
	}
	encoded, err := encodeNamespace(decoded)
	return err == nil && encoded == tag
}

func parseTempOwner(name string) (agents.Identity, bool) {
	pidText, tail, ok := strings.Cut(name, "-")
	if !ok {
		return agents.Identity{}, false
	}
	startText, suffix, ok := strings.Cut(tail, "-")
	if !ok || suffix == "" {
		return agents.Identity{}, false
	}
	pid, perr := strconv.ParseInt(pidText, 36, 0)
	start, serr := strconv.ParseUint(startText, 36, 64)
	if perr != nil || serr != nil || pid <= 0 || start == 0 ||
		strconv.FormatInt(pid, 36) != pidText || strconv.FormatUint(start, 36) != startText {
		return agents.Identity{}, false
	}
	return agents.Identity{PID: int(pid), Start: start}, true
}
