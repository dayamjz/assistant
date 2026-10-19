package home

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Var is the environment variable that relocates the home root. PRD section 8
// makes the root relocatable by one variable, and this package owns its name.
const Var = "ASSISTANT_HOME"

// DirName is the directory the root defaults to inside the user's home
// directory when Var is unset.
const DirName = ".assistant"

// ErrNoRoot reports that a home root could not be resolved: the environment
// names none and the operating system reports no user home directory to put
// the default under.
var ErrNoRoot = errors.New("home: no home root, and none could be defaulted")

// ErrRelativeRoot reports a root that is not an absolute path. A relative root
// would name a different directory for every process depending on where it was
// started, so one home would not be one home.
var ErrRelativeRoot = errors.New("home: the home root must be an absolute path")

// Home is one home root and every path derived from it.
//
// It is immutable and safe for concurrent use. Every accessor is a path
// computed from the root, so asking where something is neither touches the
// filesystem nor creates anything.
type Home struct {
	root string
}

// Resolve returns the home root the environment asks for, or the default under
// the user's home directory when it asks for none. lookup reads the
// environment, which is os.Getenv for a caller that wants the process's own.
//
// A root the environment names is used as it stands, including one that does
// not exist yet, and it must be absolute. The default is the only path this
// package composes.
func Resolve(lookup func(string) string) (string, error) {
	if lookup != nil {
		if named := lookup(Var); named != "" {
			if !filepath.IsAbs(named) {
				return "", fmt.Errorf("%w: %s is %q", ErrRelativeRoot, Var, named)
			}
			return filepath.Clean(named), nil
		}
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %w; set %s", ErrNoRoot, err, Var)
	}
	return filepath.Join(dir, DirName), nil
}

// Open returns the home at root. The root must be absolute; it does not have
// to exist, because a command that reports on a home that was never created
// has to be able to say so rather than fail before it can.
//
// Symbolic links in a root that exists are resolved, so two names for one
// directory answer as one home rather than as two. A root that does not exist
// is cleaned and used as it stands, because there is nothing to resolve.
func Open(root string) (*Home, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: %q", ErrRelativeRoot, root)
	}
	cleaned := filepath.Clean(root)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		cleaned = resolved
	}
	return &Home{root: cleaned}, nil
}

// Root is the absolute path of the home root.
func (h *Home) Root() string { return h.root }

// ConfigFile is the global configuration document, a YAML document that
// config.ParseYAML reads and config.Resolve takes as its global layer.
func (h *Home) ConfigFile() string { return filepath.Join(h.root, "config.yaml") }

// Database is the embedded database internal/store opens.
func (h *Home) Database() string { return filepath.Join(h.root, "state.db") }

// Socket is the local endpoint the service binds and the command line dials.
func (h *Home) Socket() string { return filepath.Join(h.root, "socket") }

// Notifications is the private-channel log the notification gateway appends
// to when the operator turns it on. Nothing listens on it. The file is
// created by the first delivery, not by Create.
func (h *Home) Notifications() string { return filepath.Join(h.root, "notifications.log") }

// LockFile is the file the home's exclusive lock is taken on.
func (h *Home) LockFile() string { return filepath.Join(h.root, "lock") }

// Worktree is the disposable isolated copy one run of one repository works in.
// It is removed when the run ends.
func (h *Home) Worktree(repositoryID, runID string) string {
	return filepath.Join(h.root, "worktrees", repositoryID, runID)
}

// Evidence is where a run's test evidence is written. PRD section 8 puts it
// outside the isolated copy on purpose, so test artifacts never become part of
// the change being validated.
func (h *Home) Evidence(runID string) string {
	return filepath.Join(h.root, "evidence", runID)
}

// EvidenceLog is the file one stage writes its evidence to, under Evidence's
// directory for the run and named for the stage. It is a full path including
// the leaf, so a caller records evidence without composing a name of its own
// and this package stays the one owner of the layout.
//
// The directory is not created here. Evidence's root is, and whoever writes a
// file under it creates the run's directory, on the same terms as every other
// path this package returns below its own directories list.
func (h *Home) EvidenceLog(runID, stage string) string {
	return filepath.Join(h.Evidence(runID), stage+".log")
}

// InstanceFile is the service's instance identity, which WriteInstance writes
// once at start. PRD section 8 lists it beside the lock and says it is not
// state: it names the process that wrote it and proves nothing about
// readiness.
func (h *Home) InstanceFile() string { return filepath.Join(h.root, "instance") }

// Beacon is the supervisor's liveness beacon. PRD section 8 has the supervisor
// loop write it and says it is not state. This package composes the path and
// nothing here writes or reads it: what the beacon holds is the supervisor's
// to define.
func (h *Home) Beacon() string { return filepath.Join(h.root, "beacon") }

// StageLog is one stage's log, which PRD section 8 names the authoritative
// full output of that stage; this build writes it best effort, so it is that
// output only as far as its writes landed, and a lost write leaves no mark.
// What travels in findings, streams, and prompts is a bounded projection of
// what was written. This package composes the path and
// nothing here writes or reads it: internal/service's stagelog.go is its one
// writer and its one reader, and the run's directory under logs is created
// there, on the terms Create states for everything below its directories.
func (h *Home) StageLog(runID, stage string) string {
	return filepath.Join(h.root, "logs", runID, stage+".log")
}

// TerminalSocket is the socket one tmux-hosted worker's own server listens
// on, named by name, which internal/session chooses and which must be a
// single path element: the server is started with this path and nothing
// else addresses it, so the session package spells no path under the root.
// It sits under the root rather than tmux's own default directory because
// the default moves with the environment of whichever process starts the
// server, and a path the service composes does not.
//
// The path is used as a local socket, so it is bounded by the platform's
// socket path length; the caller refuses a root too long to hold one, since
// this accessor touches nothing. The directory is created by Create and,
// for a home created before it was listed, by whoever starts the server.
func (h *Home) TerminalSocket(name string) string { return filepath.Join(h.TerminalSockets(), name) }

// TerminalSockets is the directory TerminalSocket's paths are in. It is
// created owner-only, like every directory here, which is what keeps
// another user from reaching a worker's server through it.
func (h *Home) TerminalSockets() string { return filepath.Join(h.root, "terminals") }

// ServiceLog is the bounded lifecycle log, which OpenLog writes.
func (h *Home) ServiceLog() string { return filepath.Join(h.root, "logs", "service.log") }

// ManagedServiceOutput is where a service started by the operating system's
// service manager has its standard output and error written, for a manager
// that takes a file for them. internal/managed writes it into the service
// definition and the manager opens it; nothing here bounds it, unlike
// ServiceLog, because the process writing it is not this package's.
func (h *Home) ManagedServiceOutput() string {
	return filepath.Join(h.root, "logs", "managed-service.log")
}

// directories are the directories this package creates, relative to the root.
// Everything deeper is created by whoever writes it.
//
// repos is deliberately absent. internal/gate composes <home>/repos/<id>.git
// from the root it is given and creates it, so this package saying where a
// gate repository goes would make that path have two owners, which is the
// thing P14 forbids. gate.Spec takes the root for that reason.
//
// queue is absent too. PRD section 8 makes the durable wake queue a table in
// the database and not a directory, and its layout has no queue row. A home
// created by a build that listed one keeps an empty queue/ directory that
// nothing reads, and nothing here removes it: Create makes directories and
// removes none.
var directories = []string{"worktrees", "evidence", "tasks", "logs", "terminals"}

// Create makes the home root and the directories under it, and is safe to run
// against a home that already exists.
//
// It creates directories and no files. The database, the socket, the lock, and
// the logs are each created by whoever opens them, so a home that exists but
// has never been served holds no database and no socket, which is a state a
// caller can tell apart from a home that was never created.
func (h *Home) Create() error {
	if err := os.MkdirAll(h.root, 0o700); err != nil {
		return fmt.Errorf("home: creating %s: %w", h.root, err)
	}
	for _, dir := range directories {
		path := filepath.Join(h.root, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("home: creating %s: %w", path, err)
		}
	}
	return nil
}
