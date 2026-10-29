package agents

import (
	"bytes"
	"context"
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

// Herdr names the files and binary one herdr-hosted worker runs under. The
// worker itself is started by startHerdrWorker (a later change); this file
// owns the shape those starts share: Validate, the version pin, the
// configuration and run-once launch file written under Root, and the
// procgroup-backed herdr CLI helper. Nothing here reads the person's herdr
// configuration. Built against herdr 0.9.5; another version is refused when
// requireHerdrVersion runs.
//
// Root holds config.toml, the launch file, the once marker, and an xdg/
// tree used as that worker's XDG_CONFIG_HOME so its socket never shares the
// person's default session.

// herdrRequiredVersion is the herdr release this package pins.
const herdrRequiredVersion = "0.9.5"

// Herdr is where a herdr-hosted worker's files and binary are: the absolute
// herdr binary, a session name of its own, and a root directory for that
// worker alone.
type Herdr struct {
	// Binary is the absolute path of the herdr binary. The caller resolves
	// it, so which binary runs is decided once rather than per worker.
	Binary string
	// Session is the herdr session name. It must be one path element herdr
	// accepts: ASCII letters, digits, '.', '_' and '-', and not "." or "..".
	Session string
	// Root is the absolute directory holding this worker's configuration,
	// launch file and XDG tree.
	Root string
}

// Validate refuses a Herdr that cannot be used as written, wrapping
// ErrInvalidInvocation and naming the field.
func (h Herdr) Validate() error {
	if !filepath.IsAbs(h.Binary) {
		return &invocationFieldError{field: "Binary", reason: fmt.Sprintf("%q is not an absolute path", h.Binary)}
	}
	if !filepath.IsAbs(h.Root) {
		return &invocationFieldError{field: "Root", reason: fmt.Sprintf("%q is not an absolute path", h.Root)}
	}
	if err := validHerdrSession(h.Session); err != nil {
		return &invocationFieldError{field: "Session", reason: err.Error()}
	}
	return nil
}

func validHerdrSession(name string) error {
	if name == "" {
		return fmt.Errorf("empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%q is not a session name", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("%q has %q, which a session name may not", name, r)
		}
	}
	return nil
}

func (h Herdr) configPath() string { return filepath.Join(h.Root, "config.toml") }
func (h Herdr) launchPath() string { return filepath.Join(h.Root, "launch") }
func (h Herdr) oncePath() string   { return filepath.Join(h.Root, "once") }
func (h Herdr) xdgHome() string    { return filepath.Join(h.Root, "xdg") }

// ErrHerdrVersion is the refusal of a herdr binary that does not report
// herdrRequiredVersion.
var ErrHerdrVersion = errors.New("agents: the herdr binary is not the version this provider was built against")

// ErrHerdrOccupied is the refusal of a session whose socket already exists
// under the worker's XDG tree.
var ErrHerdrOccupied = errors.New("agents: the herdr session socket already exists")

const (
	herdrStartBound  = 10 * time.Second
	herdrTypeBound   = 10 * time.Second
	herdrReadBound   = 10 * time.Second
	herdrEndBound    = 10 * time.Second
	herdrClientGrace = time.Second
)

// ErrHerdrClientTimedOut is a herdr CLI client still running at its bound.
var ErrHerdrClientTimedOut = errors.New("agents: a herdr client ran past its bound and its process group was ended")

// runHerdr runs one herdr CLI invocation, cmd, built with procgroup.Command
// and not yet started, through procgroup.Run under ctx and bound.
func runHerdr(ctx context.Context, bound time.Duration, what string, cmd *exec.Cmd) error {
	bounded, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	err := procgroup.Run(bounded, cmd, herdrClientGrace)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("%s ended with its caller's context: %w: %w", what, ctx.Err(), err)
	case bounded.Err() != nil:
		return fmt.Errorf("%w: %s ran past %s: %w", ErrHerdrClientTimedOut, what, bound, err)
	}
	return err
}

func herdrOutput(ctx context.Context, bound time.Duration, what string, cmd *exec.Cmd) ([]byte, error) {
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := runHerdr(ctx, bound, what, cmd); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// requireHerdrVersion refuses a binary that does not report herdrRequiredVersion.
func requireHerdrVersion(ctx context.Context, binary string) error {
	out, err := herdrOutput(ctx, herdrStartBound, "reading the herdr version", procgroup.Command(binary, "--version"))
	if err != nil {
		return fmt.Errorf("reading the herdr version: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "herdr" || fields[1] != herdrRequiredVersion {
		return fmt.Errorf("%w: reported %q, want herdr %s", ErrHerdrVersion, strings.TrimSpace(string(out)), herdrRequiredVersion)
	}
	return nil
}

// writeHerdrFiles writes the per-worker configuration and run-once launch
// file under h.Root. The configuration names the launch file as the pane's
// program; the launch file replaces itself with bin and args on first run
// and exits without starting anything on a later run. Words on that launch
// line are quoted with procgroup.ShellQuote so a POSIX shell that runs the
// file passes each of bin and args to exec byte for byte: $VAR, $(cmd),
// backticks, quotes, newlines and an empty string are not expanded or split.
func writeHerdrFiles(h Herdr, bin string, args []string) error {
	if err := h.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(bin) {
		return &invocationFieldError{field: "command", reason: fmt.Sprintf("%q is not an absolute path", bin)}
	}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		return fmt.Errorf("creating the herdr worker's root: %w", err)
	}
	cfg := fmt.Sprintf("[terminal]\ndefault_shell = %s\nshell_mode = \"non_login\"\n", strconv.Quote(h.launchPath()))
	if err := os.WriteFile(h.configPath(), []byte(cfg), 0o600); err != nil {
		return fmt.Errorf("writing the herdr configuration: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#!/bin/sh\nif [ -f %s ]; then\n  exit 0\nfi\n: > %s\nexec %s",
		procgroup.ShellQuote(h.oncePath()), procgroup.ShellQuote(h.oncePath()), procgroup.ShellQuote(bin))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(procgroup.ShellQuote(a))
	}
	b.WriteByte('\n')
	if err := procgroup.WriteProgram(h.launchPath(), []byte(b.String()), 0o755); err != nil {
		return fmt.Errorf("writing the herdr launch file: %w", err)
	}
	return nil
}

// herdrServerEnv is base with ambient HERDR_* and XDG_CONFIG_HOME removed and
// this worker's HERDR_CONFIG_PATH and XDG_CONFIG_HOME set, so a service that
// itself runs inside herdr starts a server of its own rather than joining
// the person's.
func herdrServerEnv(base []string, h Herdr) []string {
	env := make([]string, 0, len(base)+2)
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "HERDR_") || name == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HERDR_CONFIG_PATH="+h.configPath(), "XDG_CONFIG_HOME="+h.xdgHome())
}
