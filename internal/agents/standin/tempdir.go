package standin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
)

// Temp tags are the one list of namespaces a test binary sweeps. Each names
// the package whose TestMain passes it to RegisterTempTag and SweepTempDirs,
// and a directory is stamped with the tag of the binary that made it so that
// binary's next start reclaims it. TempTags lists every one of these; a new
// tag is a constant here and an entry there.
const (
	// TempTagStandin is the namespace internal/agents/standin's tests sweep.
	TempTagStandin = "a"
	// TempTagStoretest is the namespace internal/store/storetest's own tests
	// sweep. A directory storetest makes for another package uses that
	// package's tag, which RegisterTempTag recorded, rather than this one.
	TempTagStoretest = "b"
	// TempTagCLI is the namespace internal/cli's tests sweep.
	TempTagCLI = "c"
	// TempTagStore is the namespace internal/store's tests sweep.
	TempTagStore = "d"
	// TempTagSession is the namespace internal/session's tests sweep.
	TempTagSession = "e"
	// TempTagFixture is the namespace internal/fixture's tests sweep.
	TempTagFixture = "f"
	// TempTagStages is the namespace internal/stages's tests sweep.
	TempTagStages = "g"
	// TempTagIPC is the namespace internal/ipc's tests sweep.
	TempTagIPC = "i"
	// TempTagJourney is the namespace internal/journey's tests sweep.
	TempTagJourney = "j"
	// TempTagCheckpoints is the namespace internal/checkpoints's tests sweep.
	TempTagCheckpoints = "k"
	// TempTagAgents is the namespace internal/agents's tests sweep.
	TempTagAgents = "n"
	// TempTagFeed is the namespace internal/supervisor/feed's tests sweep.
	TempTagFeed = "p"
	// TempTagService is the namespace internal/service's tests sweep.
	TempTagService = "s"
)

// TempTags is every package namespace, in the order the constants are
// declared. The distinctness test walks this and nothing else.
func TempTags() []string {
	return []string{
		TempTagStandin, TempTagStoretest, TempTagCLI, TempTagStore,
		TempTagSession, TempTagFixture, TempTagStages, TempTagIPC,
		TempTagJourney, TempTagCheckpoints, TempTagAgents, TempTagFeed,
		TempTagService,
	}
}

// registeredTempTag is the namespace RegisterTempTag recorded for this
// process. TestMain writes it before tests run; helpers only read it after
// that, so a concurrent test does not write it.
var registeredTempTag string

// RegisterTempTag records tag as the namespace helpers in this process stamp
// on a directory they make for whichever package is running: SocketDir, the
// tmux wrapper RunOwned installs, and storetest's database template.
// TestMain calls it with the same tag it passes to SweepTempDirs, after
// every mode that makes this binary another process and before those helpers
// run. Registering does not sweep, and a tag that is not one lowercase
// letter is refused without changing what was recorded.
func RegisterTempTag(tag string) error {
	if err := validTempTag(tag); err != nil {
		return err
	}
	registeredTempTag = tag
	return nil
}

// RegisteredTempTag returns the tag RegisterTempTag recorded. It refuses
// when TestMain recorded none, so a helper cannot stamp a directory under a
// namespace no sweep will see.
func RegisteredTempTag() (string, error) {
	if registeredTempTag == "" {
		return "", fmt.Errorf("standin: no test temp tag registered; TestMain must call RegisterTempTag before making a directory")
	}
	return registeredTempTag, nil
}

// TempDir makes a test directory under tempRoot. Tag is the package's
// one-letter namespace, one of the TempTag constants, also passed to
// SweepTempDirs in its TestMain. Pattern is retained in the name (including
// spaces) for tests that exercise paths, and the owning process's PID and
// start time precede it. Callers still remove the directory on normal exit.
func TempDir(tag, pattern string) (string, error) {
	if err := validTempTag(tag); err != nil {
		return "", err
	}
	owner, found, err := agents.Identify(os.Getpid())
	if err != nil {
		return "", fmt.Errorf("standin: identifying test temp directory owner: %w", err)
	}
	if !found || owner.Start == 0 {
		return "", fmt.Errorf("standin: no readable identity for test temp directory owner %d", os.Getpid())
	}
	return os.MkdirTemp(tempRoot(), tempPrefix(tag)+strconv.FormatInt(int64(owner.PID), 36)+"-"+
		strconv.FormatUint(owner.Start, 36)+"-"+pattern)
}

// SweepTempDirs reclaims directories TempDir made for tag when their recorded
// owner is absent or replaced. It leaves live owners, malformed names and
// symlinks alone. A failed identity read or removal is returned to TestMain;
// it never turns an uncertain owner into permission to remove a directory.
// It does not record tag; RegisterTempTag does that.
func SweepTempDirs(tag string) error {
	if err := validTempTag(tag); err != nil {
		return err
	}
	entries, err := os.ReadDir(tempRoot())
	if err != nil {
		return fmt.Errorf("standin: listing test temp directories: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), tempPrefix(tag)) {
			continue
		}
		owner, ok := parseTempOwner(strings.TrimPrefix(entry.Name(), tempPrefix(tag)))
		if !ok {
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

func validTempTag(tag string) error {
	if len(tag) != 1 || tag[0] < 'a' || tag[0] > 'z' {
		return fmt.Errorf("standin: test temp tag %q must be one lowercase letter", tag)
	}
	return nil
}

func tempPrefix(tag string) string { return "at" + tag + "-" }

// tempRoot keeps local socket paths short on the two platforms that host
// terminal tests. The system temp directory on macOS is much deeper than the
// socket limit allows once a process identity is included in the name.
func tempRoot() string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		return "/tmp"
	}
	return os.TempDir()
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
