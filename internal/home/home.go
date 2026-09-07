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

// ConfigFile is the global configuration document, which config.Resolve reads
// as its global layer.
func (h *Home) ConfigFile() string { return filepath.Join(h.root, "config.yaml") }

// Database is the embedded database internal/store opens.
func (h *Home) Database() string { return filepath.Join(h.root, "state.db") }

// Socket is the local endpoint the service binds and the command line dials.
func (h *Home) Socket() string { return filepath.Join(h.root, "socket") }

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

// Task is where one task's records live: its instructions, its report, its
// state, and its events.
func (h *Home) Task(id string) string { return filepath.Join(h.root, "tasks", id) }

// Queue is the durable wake queue.
func (h *Home) Queue() string { return filepath.Join(h.root, "queue") }

// StageLog is one stage's log, which PRD section 8 makes the authoritative
// full output of that stage. What travels in findings, streams, and prompts is
// a bounded projection of this file.
func (h *Home) StageLog(runID, stage string) string {
	return filepath.Join(h.root, "logs", runID, stage+".log")
}

// ServiceLog is the bounded lifecycle log, which OpenLog writes.
func (h *Home) ServiceLog() string { return filepath.Join(h.root, "logs", "service.log") }

// directories are the directories this package creates, relative to the root.
// Everything deeper is created by whoever writes it.
//
// repos is deliberately absent. internal/gate composes <home>/repos/<id>.git
// from the root it is given and creates it, so this package saying where a
// gate repository goes would make that path have two owners, which is the
// thing P14 forbids. gate.Spec takes the root for that reason.
var directories = []string{"worktrees", "evidence", "tasks", "queue", "logs"}

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
