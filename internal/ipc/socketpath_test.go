package ipc_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
)

// socketPathOfLength is a socket path exactly n bytes long under dir, whose
// directory exists, so a bind there fails for its length or not at all.
func socketPathOfLength(t *testing.T, dir string, n int) string {
	t.Helper()
	pad := n - len(dir) - len("/") - len("/s")
	if pad < 1 {
		t.Fatalf("%s is too long to build a %d-byte socket path under", dir, n)
	}
	sub := filepath.Join(dir, strings.Repeat("p", pad))
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sub, "s")
	if len(path) != n {
		t.Fatalf("built a %d-byte path, want %d: %s", len(path), n, path)
	}
	return path
}

// TestMaxSocketPathIsTheLongestPathThatBinds holds MaxSocketPath to the
// mechanism rather than to a number this package states: on the platforms
// the product runs on, a path of exactly that length binds and one byte
// longer does not, and CheckSocketPath and Dial refuse only the second,
// naming the path, its length and the limit (#946). The limits are written
// here rather than read from the package: an address field of 104 bytes on
// macOS and 108 on Linux, each less the byte the path's terminator takes.
func TestMaxSocketPathIsTheLongestPathThatBinds(t *testing.T) {
	want, ok := map[string]int{"darwin": 103, "linux": 107}[runtime.GOOS]
	if !ok {
		t.Skipf("%s is not a platform the product serves a local socket on", runtime.GOOS)
	}
	if ipc.MaxSocketPath != want {
		t.Fatalf("MaxSocketPath is %d on %s, want %d", ipc.MaxSocketPath, runtime.GOOS, want)
	}
	dir, err := filepath.EvalSymlinks(ipcDir(t))
	if err != nil {
		t.Fatal(err)
	}

	at := socketPathOfLength(t, dir, ipc.MaxSocketPath)
	if err := ipc.CheckSocketPath(at); err != nil {
		t.Fatalf("CheckSocketPath refused a %d-byte path: %v", len(at), err)
	}
	l, err := net.Listen("unix", at)
	if err != nil {
		t.Fatalf("a %d-byte path did not bind: %v", len(at), err)
	}
	_ = l.Close()

	over := socketPathOfLength(t, dir, ipc.MaxSocketPath+1)
	if l, err := net.Listen("unix", over); err == nil {
		_ = l.Close()
		t.Fatalf("a %d-byte path bound, so MaxSocketPath %d is not the limit", len(over), ipc.MaxSocketPath)
	}
	named := fmt.Sprintf("%s is %d bytes, and this platform allows at most %d", over, len(over), ipc.MaxSocketPath)
	err = ipc.CheckSocketPath(over)
	if !errors.Is(err, ipc.ErrSocketPathTooLong) || !strings.Contains(err.Error(), named) {
		t.Fatalf("CheckSocketPath(%d bytes) = %v, want ErrSocketPathTooLong saying %q", len(over), err, named)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c, err := ipc.Dial(ctx, over, ipc.ClientConfig{}); !errors.Is(err, ipc.ErrSocketPathTooLong) {
		if c != nil {
			_ = c.Close()
		}
		t.Fatalf("Dial(%d bytes) = %v, want ErrSocketPathTooLong", len(over), err)
	}
}
