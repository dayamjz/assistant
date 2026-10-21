package managed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// Kind is which service manager a definition is written for. The set is
// closed: launchd on macOS and systemd's per-user instance on Linux.
type Kind string

const (
	// KindLaunchd is a launchd agent in the user's LaunchAgents folder,
	// loaded into the user's GUI domain.
	KindLaunchd Kind = "launchd"
	// KindSystemd is a systemd user unit in the user's unit folder, run by
	// the user's own systemd instance.
	KindSystemd Kind = "systemd"
)

// Reason says why a managed service was not installed, started or removed.
// It is a closed set, Reasons, and internal/machine holds one phrase row per
// member.
type Reason string

const (
	// ReasonUnsupported is an operating system with no service manager this
	// package writes a definition for, Windows among them.
	ReasonUnsupported Reason = "unsupported"
	// ReasonNoUserHome is an environment naming no user home folder, which is
	// where every definition this package writes lives.
	ReasonNoUserHome Reason = "no-user-home"
	// ReasonUnwritable is a value the definition has to carry - the
	// program's path, the home root, the search path - holding a character
	// this package does not write into that definition, so nothing is
	// written rather than something escaped on a guess.
	ReasonUnwritable Reason = "unwritable"
	// ReasonProtectedFolder is a launchd install where the home, the program
	// or a checkout the home serves is inside a folder macOS protects, and
	// the operator did not say access was granted. Doc.go says what was and
	// was not verified about it.
	ReasonProtectedFolder Reason = "protected-folder"
	// ReasonInstalled is an install over a definition that is already there.
	ReasonInstalled Reason = "installed"
	// ReasonNotInstalled is a start or a removal with no definition there.
	ReasonNotInstalled Reason = "not-installed"
	// ReasonManagerMissing is a service manager command line that is not on
	// the search path the caller gave.
	ReasonManagerMissing Reason = "manager-missing"
	// ReasonManagerRefused is a service manager that ran and exited with a
	// failure, whose own output is in Detail, or one still running at
	// managerTimeout, whose process group was then ended, which Detail says.
	ReasonManagerRefused Reason = "manager-refused"
)

// Reasons is every Reason, so a phrase table and a test walk the set rather
// than a list of their own.
func Reasons() []Reason {
	return []Reason{ReasonUnsupported, ReasonNoUserHome, ReasonUnwritable, ReasonProtectedFolder,
		ReasonInstalled, ReasonNotInstalled, ReasonManagerMissing, ReasonManagerRefused}
}

// Refusal is a request this package did not carry out, and why. It is a typed
// result a caller handles, never a warning execution continues past.
type Refusal struct {
	// Reason is which refusal this is.
	Reason Reason
	// Detail is what was found: the paths a protected folder holds, the
	// value that could not be written, or the manager's own output.
	Detail string
}

// Error is the reason and what was found.
func (r *Refusal) Error() string {
	if r.Detail == "" {
		return "managed: " + string(r.Reason)
	}
	return "managed: " + string(r.Reason) + ": " + r.Detail
}

// Spec is what an installed definition starts: this program, serving one
// home, in the foreground under the manager.
type Spec struct {
	// Executable is the absolute path of the program the manager starts.
	Executable string
	// Root is the absolute home root the started service serves.
	Root string
	// Output is the file a manager that takes one writes the service's
	// standard output and error to. launchd takes one; the systemd unit
	// leaves both to the user's journal.
	Output string
	// SearchPath is the PATH the started service is given. A manager starts
	// a process with an environment of its own, and the service finds its
	// agents and git on the search path, so the installing command's is
	// written into the definition. It is the only variable carried: nothing
	// else in the installing environment, a credential among them, is
	// written to the definition.
	SearchPath string
	// Repositories are the checkouts the home serves, which a launchd install
	// holds to the protected folders along with Executable and Root.
	Repositories []string
	// AccessGranted is the operator's statement that the program was given
	// access to the folders macOS protects. Nothing here can check it; doc.go
	// says what that costs.
	AccessGranted bool
}

// Arguments is the command line a definition starts, after the program
// itself: the home, and the service in the foreground under a manager.
func (s Spec) Arguments() []string {
	return []string{"--home", s.Root, "service", "start", "--foreground", "--managed"}
}

// Manager is one home's definition for this user's service manager: where
// it is, what it is called, and the manager command line that loads it. It
// performs nothing until a method is called.
type Manager struct {
	kind Kind
	// name is the unit's file name for systemd and the job's label for
	// launchd, derived from the home root so that two homes have two.
	name string
	// dir is the folder the definition is written in.
	dir string
	// searchPath is where the manager command line is looked up.
	searchPath string
	// uid is the user whose launchd GUI domain a launchd job is loaded into.
	uid int
	// userHome is the user's home folder, which the definition folder is
	// under and the protected folders are in.
	userHome string
	// bound is the deadline one manager command runs under when the
	// caller's context does not end sooner: managerTimeout, which this
	// package's tests shorten to hold a command that never answers to it.
	bound time.Duration
}

// ForThisPlatform returns the Manager for the operating system this build is
// running on and the home at root, or a *Refusal: ReasonUnsupported where
// this package writes no definition, and ReasonNoUserHome where getenv names
// no user home. getenv is the caller's environment, which is where the user
// home, XDG_CONFIG_HOME for systemd, and the search path the manager command
// line is found on are read from.
func ForThisPlatform(getenv func(string) string, root string) (*Manager, error) {
	kind, ok := platformKind()
	if !ok {
		return nil, &Refusal{Reason: ReasonUnsupported}
	}
	return forKind(kind, getenv, root, os.Getuid())
}

// forKind is ForThisPlatform for a named manager, which is how this package's
// tests drive the definition and the commands for one manager on a machine
// running the other. uid is the user a launchd job is loaded for.
func forKind(kind Kind, getenv func(string) string, root string, uid int) (*Manager, error) {
	userHome := getenv("HOME")
	if userHome == "" || !filepath.IsAbs(userHome) {
		return nil, &Refusal{Reason: ReasonNoUserHome}
	}
	m := &Manager{kind: kind, searchPath: getenv("PATH"), uid: uid, userHome: userHome, bound: managerTimeout}
	id := homeID(root)
	switch kind {
	case KindLaunchd:
		m.name = "assistant.service." + id
		m.dir = filepath.Join(userHome, "Library", "LaunchAgents")
	case KindSystemd:
		m.name = "assistant-" + id + ".service"
		config := getenv("XDG_CONFIG_HOME")
		if config == "" || !filepath.IsAbs(config) {
			config = filepath.Join(userHome, ".config")
		}
		m.dir = filepath.Join(config, "systemd", "user")
	default:
		return nil, &Refusal{Reason: ReasonUnsupported, Detail: string(kind)}
	}
	return m, nil
}

// homeID names a home in a definition's name: a digest of its root, short
// enough to read, so one home has one definition and a second home a second.
func homeID(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(sum[:])[:12]
}

// Kind is which manager this is.
func (m *Manager) Kind() Kind { return m.kind }

// Name is the systemd unit's name or the launchd job's label.
func (m *Manager) Name() string { return m.name }

// Unit is the path of the definition file.
func (m *Manager) Unit() string {
	if m.kind == KindLaunchd {
		return filepath.Join(m.dir, m.name+".plist")
	}
	return filepath.Join(m.dir, m.name)
}

// Installed reports whether the definition file is there. It reads the file
// system and asks the manager nothing, so it says that a definition was
// written and not that the manager has it loaded.
func (m *Manager) Installed() (bool, error) {
	_, err := os.Stat(m.Unit())
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("managed: reading %s: %w", m.Unit(), err)
	}
}

// Install writes the definition for spec and asks the manager to load it and
// start it: for systemd a reload, then enable --now; for launchd a bootstrap
// into the user's GUI domain, where the definition's RunAtLoad starts it.
//
// It refuses before writing anything when a definition is already there,
// when a value cannot be written into the definition, and, for launchd, when
// a path spec names is inside a protected folder and spec.AccessGranted is
// false. A manager command that fails is ReasonManagerRefused, and the
// definition written for it is removed again, so a refused install leaves no
// file behind; for systemd the unit is disabled before the removal and a
// reload follows it, and neither's own failure is reported over the first.
//
// It does not wait for the service to answer. That is the caller's, which
// holds the readiness check.
func (m *Manager) Install(ctx context.Context, spec Spec) error {
	if installed, err := m.Installed(); err != nil {
		return err
	} else if installed {
		return &Refusal{Reason: ReasonInstalled, Detail: m.Unit()}
	}
	var definition []byte
	var err error
	switch m.kind {
	case KindLaunchd:
		if !spec.AccessGranted {
			if found := protectedPaths(m.userHome, append([]string{spec.Root, spec.Executable}, spec.Repositories...)); len(found) > 0 {
				return &Refusal{Reason: ReasonProtectedFolder, Detail: strings.Join(found, ", ")}
			}
		}
		definition, err = launchdPlist(m.name, spec)
	default:
		definition, err = systemdUnit(spec)
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return fmt.Errorf("managed: creating %s: %w", m.dir, err)
	}
	if err := os.WriteFile(m.Unit(), definition, 0o644); err != nil {
		return fmt.Errorf("managed: writing %s: %w", m.Unit(), err)
	}
	var steps [][]string
	if m.kind == KindLaunchd {
		steps = [][]string{{"bootstrap", m.domain(), m.Unit()}}
	} else {
		steps = [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", m.name}}
	}
	for _, step := range steps {
		if err := m.run(ctx, step...); err != nil {
			// enable --now enables before it starts, so a start that failed
			// can leave the unit enabled; it is disabled before its file goes,
			// so no link to a removed unit is left behind.
			if m.kind == KindSystemd {
				_ = m.run(ctx, "--user", "disable", m.name)
			}
			_ = os.Remove(m.Unit())
			if m.kind == KindSystemd {
				_ = m.run(ctx, "--user", "daemon-reload")
			}
			return err
		}
	}
	return nil
}

// Start asks the manager to start the installed definition's service now:
// systemctl --user start, or launchctl kickstart. It refuses with
// ReasonNotInstalled when no definition is there, and does not wait for the
// service to answer.
func (m *Manager) Start(ctx context.Context) error {
	if installed, err := m.Installed(); err != nil {
		return err
	} else if !installed {
		return &Refusal{Reason: ReasonNotInstalled, Detail: m.Unit()}
	}
	if m.kind == KindLaunchd {
		return m.run(ctx, "kickstart", m.domain()+"/"+m.name)
	}
	return m.run(ctx, "--user", "start", m.name)
}

// Remove asks the manager to unload the definition and then deletes it:
// systemctl --user disable --now and a reload after the file is gone, or
// launchctl bootout. It refuses with ReasonNotInstalled when no definition is
// there. A manager command that fails is ReasonManagerRefused and leaves the
// file where it was, so the definition and what the manager holds are not
// made to disagree by this call.
//
// Unloading stops a service the manager is running. Whether that may happen
// is the caller's to decide first: the service's own stop refuses while runs
// are active, and a manager's stop does not ask.
func (m *Manager) Remove(ctx context.Context) error {
	if installed, err := m.Installed(); err != nil {
		return err
	} else if !installed {
		return &Refusal{Reason: ReasonNotInstalled, Detail: m.Unit()}
	}
	if m.kind == KindLaunchd {
		if err := m.run(ctx, "bootout", m.domain()+"/"+m.name); err != nil {
			return err
		}
	} else if err := m.run(ctx, "--user", "disable", "--now", m.name); err != nil {
		return err
	}
	if err := os.Remove(m.Unit()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("managed: removing %s: %w", m.Unit(), err)
	}
	if m.kind == KindSystemd {
		return m.run(ctx, "--user", "daemon-reload")
	}
	return nil
}

// domain is the launchd domain a job of this user's is loaded into.
func (m *Manager) domain() string { return "gui/" + strconv.Itoa(m.uid) }

// command is the manager command line's name.
func (m *Manager) command() string {
	if m.kind == KindLaunchd {
		return "launchctl"
	}
	return "systemctl"
}

// managerTimeout bounds one manager command, so a manager that does not
// answer refuses rather than holding the command that asked forever. Every
// command here asks the manager to load, start or unload a definition, and
// none waits on the service itself: Start does not wait for it to answer,
// and internal/cli asks for a removal only after the service's own stop, so
// no command's length is the service's stop_grace. Thirty seconds is that
// request with room for a manager that is busy, and a command still running
// then is taken as one that will not answer.
const managerTimeout = 30 * time.Second

// managerGrace is the grace procgroup.Run is given for a manager command:
// how long one sent SIGKILL gets to exit, and how long its output is waited
// on once it has, before the call answers without it. A manager command
// holds nothing that outlasts a SIGKILL, so the grace is short.
const managerGrace = 2 * time.Second

// outputLimit is how much of a manager's output a refusal carries.
const outputLimit = 4096

// run invokes the manager command line with args, found on the search path
// the caller's environment named rather than this process's, so the manager
// asked is the one the caller would reach by name.
//
// It runs through procgroup.Run, PRD section 8's terms for an external
// program: the command leads a process group of its own, under ctx and
// m.bound, whichever ends first, and the group is ended when the command
// returns or that deadline passes, so a manager command that does not answer
// is ended together with anything it started in its group. A command that
// failed once m.bound had passed, while ctx had not ended, is
// ReasonManagerRefused saying so; procgroup's package comment states what
// ending a group does not reach.
func (m *Manager) run(ctx context.Context, args ...string) error {
	bin, ok := lookPath(m.command(), m.searchPath)
	if !ok {
		return &Refusal{Reason: ReasonManagerMissing, Detail: m.command()}
	}
	bounded, cancel := context.WithTimeout(ctx, m.bound)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := procgroup.Run(bounded, cmd, managerGrace); err != nil {
		said := strings.TrimSpace(out.String())
		if len(said) > outputLimit {
			said = said[:outputLimit]
		}
		if ctx.Err() == nil && bounded.Err() != nil {
			return &Refusal{Reason: ReasonManagerRefused,
				Detail: fmt.Sprintf("%s %s: still running after %s, so its process group was ended: %v: %s",
					m.command(), strings.Join(args, " "), m.bound, err, said)}
		}
		return &Refusal{Reason: ReasonManagerRefused,
			Detail: fmt.Sprintf("%s %s: %v: %s", m.command(), strings.Join(args, " "), err, said)}
	}
	return nil
}

// lookPath finds name on searchPath, a PATH-shaped list, and reports whether
// it found an executable file there.
func lookPath(name, searchPath string) (string, bool) {
	for _, dir := range filepath.SplitList(searchPath) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, true
		}
	}
	return "", false
}
