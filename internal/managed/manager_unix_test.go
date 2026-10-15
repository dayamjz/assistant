//go:build unix

package managed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// standInManager writes a manager command line named name into a fresh
// directory: a script that appends its arguments, one call per line, to a
// file beside it and exits with status. It returns the directory, to be the
// search path, and the file the calls land in. It is a stand-in for the
// command line and not for the manager: it loads and starts nothing, so what
// these tests establish is the commands this package sends and the files it
// writes, and the cli package's tests drive one that starts what a definition
// says.
func standInManager(t *testing.T, name string, status int) (dir, calls string) {
	t.Helper()
	dir = t.TempDir()
	calls = filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\n"
	if status != 0 {
		script += "echo 'the manager says no' >&2\nexit " + string(rune('0'+status)) + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, calls
}

// recorded is every call the stand-in recorded.
func recorded(t *testing.T, calls string) []string {
	t.Helper()
	raw, err := os.ReadFile(calls)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// env is an environment naming a user home and a search path.
func env(userHome, searchPath string) func(string) string {
	return func(k string) string {
		switch k {
		case "HOME":
			return userHome
		case "PATH":
			return searchPath
		}
		return ""
	}
}

// spec is what an install of the home at root starts.
func spec(root string) Spec {
	return Spec{Executable: "/opt/bin/assistant", Root: root, Output: filepath.Join(root, "logs", "out.log"), SearchPath: "/usr/bin"}
}

// Each manager is driven through install, start and removal with the
// commands it documents, against the definition in the user's folder for it,
// and the definition is gone after removal. A second install over the first
// is refused, as are a start and a removal with nothing installed.
func TestEachManagerInstallsStartsAndRemovesWithItsOwnCommands(t *testing.T) {
	cases := []struct {
		kind              Kind
		command           string
		unit              string
		install, start    []string
		remove            []string
		label, unitSuffix string
	}{
		{
			kind: KindSystemd, command: "systemctl", unit: filepath.Join(".config", "systemd", "user"),
			install: []string{"--user daemon-reload", "--user enable --now NAME"},
			start:   []string{"--user start NAME"},
			remove:  []string{"--user disable --now NAME", "--user daemon-reload"},
		},
		{
			kind: KindLaunchd, command: "launchctl", unit: filepath.Join("Library", "LaunchAgents"),
			install: []string{"bootstrap gui/501 UNIT"},
			start:   []string{"kickstart gui/501/NAME"},
			remove:  []string{"bootout gui/501/NAME"},
		},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			userHome := t.TempDir()
			dir, calls := standInManager(t, c.command, 0)
			m, err := forKind(c.kind, env(userHome, dir), filepath.Join(userHome, ".assistant"), 501)
			if err != nil {
				t.Fatalf("forKind: %v", err)
			}
			if filepath.Dir(m.Unit()) != filepath.Join(userHome, c.unit) {
				t.Fatalf("the definition is at %s, want it in %s", m.Unit(), filepath.Join(userHome, c.unit))
			}
			expand := func(lines []string) []string {
				out := make([]string, len(lines))
				for i, l := range lines {
					out[i] = strings.NewReplacer("NAME", m.Name(), "UNIT", m.Unit()).Replace(l)
				}
				return out
			}
			ctx := context.Background()
			var refusal *Refusal
			if err := m.Start(ctx); !errors.As(err, &refusal) || refusal.Reason != ReasonNotInstalled {
				t.Fatalf("a start with nothing installed = %v, want not-installed", err)
			}
			if err := m.Install(ctx, spec(filepath.Join(userHome, ".assistant"))); err != nil {
				t.Fatalf("Install: %v", err)
			}
			if installed, err := m.Installed(); err != nil || !installed {
				t.Fatalf("after Install, Installed = %t, %v", installed, err)
			}
			if err := m.Install(ctx, spec(filepath.Join(userHome, ".assistant"))); !errors.As(err, &refusal) || refusal.Reason != ReasonInstalled {
				t.Fatalf("a second install = %v, want installed", err)
			}
			if err := m.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if err := m.Remove(ctx); err != nil {
				t.Fatalf("Remove: %v", err)
			}
			if installed, _ := m.Installed(); installed {
				t.Fatal("the definition is still there after Remove")
			}
			want := slices.Concat(expand(c.install), expand(c.start), expand(c.remove))
			if got := recorded(t, calls); !slices.Equal(got, want) {
				t.Fatalf("the manager was asked\n%q\nwant\n%q", got, want)
			}
			if err := m.Remove(ctx); !errors.As(err, &refusal) || refusal.Reason != ReasonNotInstalled {
				t.Fatalf("a second removal = %v, want not-installed", err)
			}
		})
	}
}

// A manager that refuses the install leaves no definition behind and the
// refusal carries what it said; a manager that is not on the search path is
// its own refusal, and so is an environment naming no user home.
func TestARefusedInstallLeavesNothingBehind(t *testing.T) {
	userHome := t.TempDir()
	dir, _ := standInManager(t, "systemctl", 1)
	m, err := forKind(KindSystemd, env(userHome, dir), "/srv/home", 501)
	if err != nil {
		t.Fatal(err)
	}
	var refusal *Refusal
	if err := m.Install(context.Background(), spec("/srv/home")); !errors.As(err, &refusal) || refusal.Reason != ReasonManagerRefused ||
		!strings.Contains(refusal.Detail, "the manager says no") {
		t.Fatalf("a refusing manager = %v, want manager-refused carrying its message", err)
	}
	if installed, _ := m.Installed(); installed {
		t.Fatal("a refused install left its definition behind")
	}

	missing, err := forKind(KindSystemd, env(userHome, t.TempDir()), "/srv/home", 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := missing.Install(context.Background(), spec("/srv/home")); !errors.As(err, &refusal) || refusal.Reason != ReasonManagerMissing {
		t.Fatalf("no manager on the search path = %v, want manager-missing", err)
	}
	if installed, _ := missing.Installed(); installed {
		t.Fatal("an install with no manager left its definition behind")
	}

	if _, err := forKind(KindLaunchd, env("", dir), "/srv/home", 501); !errors.As(err, &refusal) || refusal.Reason != ReasonNoUserHome {
		t.Fatalf("no user home = %v, want no-user-home", err)
	}
}

// A launchd install refuses before writing or asking anything when the home,
// the program or a checkout is in a protected folder, and names the paths it
// found; given the operator's statement that access was granted it installs.
// The control is the same install for systemd, which holds nothing to those
// folders because the protection is macOS's.
func TestALaunchdInstallRefusesAProtectedFolderUnlessAccessWasGranted(t *testing.T) {
	userHome := t.TempDir()
	dir, calls := standInManager(t, "launchctl", 0)
	m, err := forKind(KindLaunchd, env(userHome, dir), filepath.Join(userHome, ".assistant"), 501)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(userHome, "Documents", "code", "project")
	s := spec(filepath.Join(userHome, ".assistant"))
	s.Repositories = []string{filepath.Join(userHome, "src", "elsewhere"), checkout}
	var refusal *Refusal
	if err := m.Install(context.Background(), s); !errors.As(err, &refusal) || refusal.Reason != ReasonProtectedFolder ||
		refusal.Detail != checkout {
		t.Fatalf("a checkout under Documents = %v, want protected-folder naming only it", err)
	}
	if installed, _ := m.Installed(); installed {
		t.Fatal("a refused install wrote its definition")
	}
	if got := recorded(t, calls); len(got) != 0 {
		t.Fatalf("a refused install asked the manager %q", got)
	}

	s.AccessGranted = true
	if err := m.Install(context.Background(), s); err != nil {
		t.Fatalf("an install with access granted: %v", err)
	}

	systemdDir, _ := standInManager(t, "systemctl", 0)
	other, err := forKind(KindSystemd, env(userHome, systemdDir), filepath.Join(userHome, ".assistant"), 501)
	if err != nil {
		t.Fatal(err)
	}
	s.AccessGranted = false
	if err := other.Install(context.Background(), s); err != nil {
		t.Fatalf("a systemd install of the same checkouts: %v", err)
	}
}

// Two homes have two definitions.
func TestTwoHomesHaveTwoDefinitions(t *testing.T) {
	userHome := t.TempDir()
	a, _ := forKind(KindSystemd, env(userHome, ""), "/srv/one", 501)
	b, _ := forKind(KindSystemd, env(userHome, ""), "/srv/two", 501)
	if a.Unit() == b.Unit() || a.Name() == b.Name() {
		t.Fatalf("two homes share the definition %s", a.Unit())
	}
}
