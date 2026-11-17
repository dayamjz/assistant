package home_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
)

// TestARootOpenedBeforeItExistsNamesTheHomeItWillBe holds Open to one name
// for a home whether or not its root exists yet: a root not yet created,
// under a parent reached through a symbolic link, opens with that link
// resolved, and the same as it opens once the root is created. A first start
// on such a root otherwise checks and binds the local socket at one path and
// every later command dials another (#946). A link two levels up, and a link
// as the root itself, resolve the same way; the positive control is that the
// link's own name and its target do differ, so the test is not comparing a
// path with itself.
func TestARootOpenedBeforeItExistsNamesTheHomeItWillBe(t *testing.T) {
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
	for _, c := range []struct{ name, root, want string }{
		{"the root's parent is a link", filepath.Join(link, "home"), filepath.Join(target, "home")},
		{"a link two levels up", filepath.Join(link, "a", "home"), filepath.Join(target, "a", "home")},
	} {
		before, err := home.Open(c.root)
		if err != nil {
			t.Fatal(err)
		}
		if before.Root() != c.want {
			t.Errorf("%s: Open(%s) before it exists = %s, want %s", c.name, c.root, before.Root(), c.want)
		}
		if err := before.Create(); err != nil {
			t.Fatal(err)
		}
		after, err := home.Open(c.root)
		if err != nil {
			t.Fatal(err)
		}
		if after.Root() != before.Root() || after.Socket() != before.Socket() {
			t.Errorf("%s: Open names %s before the root exists and %s after", c.name, before.Root(), after.Root())
		}
	}
	if h, err := home.Open(link); err != nil || h.Root() != target {
		t.Errorf("Open(%s), an existing link as the root, = %v, %v; want %s", link, h, err, target)
	}
}
