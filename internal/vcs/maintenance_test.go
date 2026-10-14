package vcs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// ownConfig reads one key from a repository's own configuration file, and
// nothing above it, with git directly. gitEnvironment's global file sets both
// maintenance keys for every test, so a read that consulted it would find
// them whatever DisableAutomaticMaintenance did.
func ownConfig(t *testing.T, addressing []string, key string) []string {
	t.Helper()
	args := append(append([]string{}, addressing...), "config", "--local", "--get-all", key)
	out, err := tryRawGit(t.TempDir(), args...)
	if err != nil {
		if strings.TrimSpace(out) != "" {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	return strings.Fields(out)
}

// DisableAutomaticMaintenance's claim is the two keys in the repository's own
// configuration and in no other file. It is checked on both kinds of
// repository the program owns and calls it on, a bare repository and a
// clone, each read back unset before the call so the read after it could
// fail. It is called twice to show a repeat leaves one value of each rather
// than two, and the global configuration file is compared byte for byte to
// show it was not the file written.
func TestDisableAutomaticMaintenanceWritesTheRepositorysOwnConfiguration(t *testing.T) {
	global := gitEnvironment(t)
	globalBefore, err := os.ReadFile(global)
	if err != nil {
		t.Fatal(err)
	}
	src, _, _ := sourceRepo(t)

	bare, err := vcs.InitBare(ctx(t), filepath.Join(t.TempDir(), "owned.git"))
	if err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	cloned, err := vcs.Clone(ctx(t), src, filepath.Join(t.TempDir(), "copy"), "")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	for _, tc := range []struct {
		name       string
		repo       *vcs.Repository
		addressing []string
	}{
		{"bare", bare, []string{"--git-dir=" + bare.Path()}},
		{"clone", cloned, []string{"-C", cloned.Path()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"gc.auto", "maintenance.auto"} {
				if got := ownConfig(t, tc.addressing, key); got != nil {
					t.Fatalf("%s is already %v in the repository's own configuration, so the check below could not fail", key, got)
				}
			}
			for range 2 {
				if err := tc.repo.DisableAutomaticMaintenance(ctx(t)); err != nil {
					t.Fatalf("DisableAutomaticMaintenance: %v", err)
				}
			}
			if got := ownConfig(t, tc.addressing, "gc.auto"); len(got) != 1 || got[0] != "0" {
				t.Errorf("gc.auto in the repository's own configuration is %v, want exactly [0]", got)
			}
			if got := ownConfig(t, tc.addressing, "maintenance.auto"); len(got) != 1 || got[0] != "false" {
				t.Errorf("maintenance.auto in the repository's own configuration is %v, want exactly [false]", got)
			}
		})
	}

	globalAfter, err := os.ReadFile(global)
	if err != nil {
		t.Fatal(err)
	}
	if string(globalAfter) != string(globalBefore) {
		t.Errorf("the global configuration file changed:\n%s\nwas:\n%s", globalAfter, globalBefore)
	}
	// The source the clone was made from is the stand-in for a checkout the
	// program does not own, and nothing here was called on it.
	if got := ownConfig(t, []string{"-C", src}, "gc.auto"); got != nil {
		t.Errorf("the clone's source gained gc.auto = %v", got)
	}
}
