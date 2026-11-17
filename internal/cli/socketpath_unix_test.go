//go:build linux || darwin

package cli_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
)

// TestServiceStartRefusesAHomeWhoseSocketPathIsTooLong is #946 as a person
// meets it: a home one byte past what a local socket path may be on this
// platform (103 bytes on macOS and 107 on Linux, each the address field less
// the byte the path's terminator takes). The start says so before it
// launches anything, naming the path, its length, the limit and how to
// choose another home; a service started in the foreground there refuses and
// writes the same to its log; status reports the same rather than the dial's
// "invalid argument", the plain one as a home no service can serve rather
// than one nobody started; and a stop's refusal says to choose another home
// rather than to start a service that would only be refused.
func TestServiceStartRefusesAHomeWhoseSocketPathIsTooLong(t *testing.T) {
	limit := map[string]int{"darwin": 103, "linux": 107}[runtime.GOOS]
	root, err := standin.TempDir("sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	pad := limit + 1 - len(root) - len("/") - len("/socket")
	if pad < 1 {
		t.Fatalf("%s leaves no room to build a home whose socket path is %d bytes", root, limit+1)
	}
	dir := filepath.Join(root, strings.Repeat("l", pad))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	h, err := home.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { endServicesOf(t, h) })
	if len(h.Socket()) != limit+1 {
		t.Fatalf("built a %d-byte socket path, want %d: %s", len(h.Socket()), limit+1, h.Socket())
	}
	named := fmt.Sprintf("%s is %d bytes, and this platform allows at most %d", h.Socket(), limit+1, limit)

	got := run(t, h, "", "service", "start")
	if got.code == machine.ExitOK || !strings.Contains(got.stderr, named) || !strings.Contains(got.stderr, "--home") {
		t.Fatalf("start exited %s, want a refusal naming %q and --home:\n%s%s", got.code, named, got.stdout, got.stderr)
	}
	if _, err := os.Stat(h.LockFile()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a service took the home's lock after the start refused (%v)", err)
	}

	got = run(t, h, "", "service", "start", "--foreground")
	if got.code == machine.ExitOK || !strings.Contains(got.stderr, named) {
		t.Fatalf("a foreground start exited %s, want a refusal naming %q:\n%s%s", got.code, named, got.stdout, got.stderr)
	}
	if log, err := os.ReadFile(h.ServiceLog()); err != nil || !strings.Contains(string(log), named) {
		t.Fatalf("the service log does not name %q (%v):\n%s", named, err, log)
	}

	got = run(t, h, "", "--json", "service", "status")
	var status machine.Service
	if err := json.Unmarshal([]byte(got.stdout), &status); err != nil {
		t.Fatalf("status answered %s, not a service document: %v\n%s", got.code, err, got.stderr)
	}
	if status.Running || !strings.Contains(status.Detail, named) {
		t.Fatalf("status = %+v, want not running with a detail naming %q", status, named)
	}

	for _, verb := range [][]string{{"service", "status"}, {"status"}} {
		got = run(t, h, newSubject(t), verb...)
		if !strings.Contains(got.stdout, "too long") || strings.Contains(got.stdout, machine.PhraseServiceAbsent.String()) {
			t.Fatalf("plain %v exited %s and said %q, want why no service can be up rather than that none was started:\n%s", verb, got.code, got.stdout, got.stderr)
		}
	}
	got = run(t, h, "", "service", "stop")
	if got.code == machine.ExitOK || strings.Contains(got.stderr, "assistant service start") || !strings.Contains(got.stderr, "--home") {
		t.Fatalf("stop exited %s, want a refusal pointing at another home and not at a start:\n%s", got.code, got.stderr)
	}
}

// TestTheSocketPathAStartChecksIsTheOneEveryLaterCommandDials holds a start
// to checking every spelling of the socket path that is bound or dialed when
// the home is named through a symbolic link (#946). The limit is on the
// string a local socket is bound and dialed at, and internal/home's Open
// resolves a root's links only once it exists, so a first start on a root not
// yet created binds one spelling and every later command dials another.
// A root not yet created under a short link to a long directory, and an
// existing root that is a short link to one, are refused before anything is
// launched, and status reports why, each naming the long path later commands
// would dial; before, the
// first started a service at the short name that no later command could
// reach. A long link to an existing short directory is not refused, and its
// service starts and stops, because what is bound and dialed is the short
// path Open resolves it to.
func TestTheSocketPathAStartChecksIsTheOneEveryLaterCommandDials(t *testing.T) {
	requiresIdentifiedPeer(t)
	cli.AwaitReadinessUntilTheTestsDeadline(t)
	limit := map[string]int{"darwin": 103, "linux": 107}[runtime.GOOS]
	base, err := standin.TempDir("sl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if base, err = filepath.EvalSymlinks(base); err != nil {
		t.Fatal(err)
	}
	// padded is a directory under base whose name brings dir+leaf to n bytes.
	padded := func(name string, leaf string, n int) string {
		pad := n - len(base) - len("/") - len(leaf)
		if pad < 1 {
			t.Fatalf("%s leaves no room for a %d-byte path", base, n)
		}
		return filepath.Join(base, name+strings.Repeat("l", pad-len(name)))
	}
	link := func(target, name string) string {
		at := filepath.Join(base, name)
		if err := os.Symlink(target, at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	refused := func(name, given, dialed string) {
		t.Helper()
		h, err := home.Open(given)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { endServicesOf(t, h) })
		if len(given)+len("/socket") > limit {
			t.Fatalf("%s: the name given, %s, is itself too long to show anything", name, given)
		}
		named := fmt.Sprintf("%s is %d bytes, and this platform allows at most %d", dialed, len(dialed), limit)
		got := runArgs(t, "", "--home", given, "service", "start")
		if got.code == machine.ExitOK || !strings.Contains(got.stderr, named) {
			t.Fatalf("%s: start exited %s, want a refusal naming %q:\n%s%s", name, got.code, named, got.stdout, got.stderr)
		}
		got = runArgs(t, "", "--home", given, "--json", "service", "status")
		var status machine.Service
		if err := json.Unmarshal([]byte(got.stdout), &status); err != nil || status.Running || !strings.Contains(status.Detail, named) {
			t.Fatalf("%s: status = %+v (%v), want not running with a detail naming %q", name, status, err, named)
		}
	}

	long := padded("a", "/h/socket", limit+1)
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	refused("a root not yet created under a short link", filepath.Join(link(long, "s"), "h"), filepath.Join(long, "h", "socket"))

	existing := padded("b", "/socket", limit+1)
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	refused("an existing root that is a short link", link(existing, "e"), filepath.Join(existing, "socket"))

	short := filepath.Join(base, "t")
	if err := os.MkdirAll(short, 0o700); err != nil {
		t.Fatal(err)
	}
	longLink := link(short, filepath.Base(padded("k", "/socket", limit+1)))
	h, err := home.Open(longLink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { endServicesOf(t, h) })
	if len(longLink)+len("/socket") <= limit || h.Socket() != filepath.Join(short, "socket") {
		t.Fatalf("the long link %s opens as %s, want a name over the limit resolving to %s", longLink, h.Root(), short)
	}
	if got := runArgs(t, "", "--home", longLink, "service", "start"); got.code != machine.ExitOK {
		t.Fatalf("a long link to a short home did not start: exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if got := runArgs(t, "", "--home", longLink, "service", "stop"); got.code != machine.ExitOK {
		t.Fatalf("stopping it exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
}
