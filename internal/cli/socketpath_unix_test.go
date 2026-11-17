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
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
)

// TestServiceStartRefusesAHomeWhoseSocketPathIsTooLong is #946 as a person
// meets it: a home one byte past what a local socket path may be on this
// platform (103 bytes on macOS and 107 on Linux, each the address field less
// the byte the path's terminator takes). The start says so before it
// launches anything, naming the path, its length, the limit and how to
// choose another home; a service started in the foreground there refuses and
// writes the same to its log; and status reports the same rather than the
// dial's "invalid argument".
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
}
