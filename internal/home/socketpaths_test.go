package home_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
)

// TestSocketPathsNamesTheSocketEveryLaterCommandDials holds SocketPaths to
// the two spellings a start has to check (#946): for a root not yet created
// under a symbolic link, the Socket a first start binds, and the Socket every
// command that opens the root once it exists dials, which is the link
// resolved. A link two levels up resolves the same way. A root that exists,
// whether or not it was named through a link, has one spelling, because Open
// resolves it. The positive control is that the link and its target do
// differ, so the test is not comparing a path with itself.
func TestSocketPathsNamesTheSocketEveryLaterCommandDials(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if link == target {
		t.Fatal("the link and its target have one name, so nothing is resolved")
	}
	for _, c := range []struct{ name, root, resolved string }{
		{"the root's parent is a link", filepath.Join(link, "home"), filepath.Join(target, "home")},
		{"a link two levels up", filepath.Join(link, "a", "home"), filepath.Join(target, "a", "home")},
	} {
		first, err := home.Open(c.root)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{filepath.Join(c.root, "socket"), filepath.Join(c.resolved, "socket")}
		if got := first.SocketPaths(); !slices.Equal(got, want) {
			t.Errorf("%s: SocketPaths before the root exists = %v, want %v", c.name, got, want)
		}
		if err := first.Create(); err != nil {
			t.Fatal(err)
		}
		later, err := home.Open(c.root)
		if err != nil {
			t.Fatal(err)
		}
		if got := later.SocketPaths(); !slices.Equal(got, []string{later.Socket()}) || later.Socket() != want[1] {
			t.Errorf("%s: once it exists the root opens with SocketPaths %v, want only %s", c.name, got, want[1])
		}
	}
	for name, root := range map[string]string{"an existing root": target, "an existing link as the root": link} {
		h, err := home.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := h.SocketPaths(), []string{filepath.Join(target, "socket")}; !slices.Equal(got, want) {
			t.Errorf("%s: SocketPaths = %v, want %v", name, got, want)
		}
	}
}
