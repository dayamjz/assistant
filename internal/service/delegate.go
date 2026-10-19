package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// ErrDelegationMerge is the refusal a subordinate home gives a program merge.
// The grant has no field that permits a merge, so every subordinate refuses,
// before the autonomy list is consulted and before any code host is asked.
var ErrDelegationMerge = errors.New("service: a subordinate may not merge")

// ErrDeliveryNotOwned is the refusal a subordinate gives a delivery of a
// parent's task. The parent store is the only writer of that record.
var ErrDeliveryNotOwned = errors.New("service: delivery of a delegated task is the parent's")

// ErrDelegationOwns is the refusal to relaunch a delegated task on this
// home. A restart does not take the task back. The one exception is
// MayRelaunchLocally: the recorded recovery is relaunch-if-dead and the
// latest attempt has an end record.
var ErrDelegationOwns = errors.New("service: a delegated task is not relaunched locally")

// ErrGrantPeer is the refusal a subordinate gives a connection whose kernel
// credentials are not the parent process recorded on the grant.
var ErrGrantPeer = errors.New("service: the peer is not the recorded parent of this subordinate")

// SubordinateSpec is a child home the parent starts. Home is a different
// assistant home: its own store and its own lock. Tasks are the parent's
// task identifiers the child may run. ParentBranches are working copies the
// child may not bind a gate to. Recovery is recorded on the parent and is
// not a permission the child can change. Forge, when set, is the child's
// code host; the zero value is the child's own default host.
type SubordinateSpec struct {
	Home           *home.Home
	Tasks          []string
	ParentBranches []string
	Recovery       store.DelegationRecovery
	Forge          forge.Host
}

// Subordinate is a child home this parent started, and the local connection
// to it. Close stops the child. Drop closes the connection and leaves the
// child's copy where it is.
type Subordinate struct {
	mu      sync.Mutex
	parent  *Service
	child   *Service
	client  *ipc.Client
	served  chan error
	tasks   []string
	copy    string
	dropped bool
}

// Child is the subordinate's service until Close. Its home is not the parent's.
func (d *Subordinate) Child() *Service {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.child
}

// Tasks are the task identifiers the parent named, in that order.
func (d *Subordinate) Tasks() []string { return append([]string(nil), d.tasks...) }

// Copy is the workspace the child was given for the first task. Drop and a
// parent restart leave it in place.
func (d *Subordinate) Copy() string { return d.copy }

// StartSubordinate records the delegation on the parent, writes the grant
// into the child home, and starts the child's service. The grant's parent
// identity is this process as the kernel will report it on a connection:
// pid, uid and gid. A platform that reports no user identity is refused
// and no child is started. The child dials nothing; this process dials the
// child's local socket. No network listener is opened.
func (s *Service) StartSubordinate(ctx context.Context, spec SubordinateSpec) (*Subordinate, error) {
	if s.hasGrant {
		return nil, fmt.Errorf("%w: a subordinate cannot start a further home", ErrDelegationOwns)
	}
	if spec.Home == nil || spec.Home.Root() == s.home.Root() {
		return nil, fmt.Errorf("service: a subordinate needs its own home")
	}
	if !spec.Recovery.Recognized() {
		return nil, fmt.Errorf("service: recovery %q is not a delegation policy", spec.Recovery)
	}
	uid, gid := os.Getuid(), os.Getgid()
	if os.Getpid() <= 0 || uid < 0 || gid < 0 {
		return nil, fmt.Errorf("%w: this platform has no user identity to record", ErrGrantPeer)
	}
	if err := spec.Home.Create(); err != nil {
		return nil, err
	}
	copyPath, err := s.delegateTasks(ctx, spec)
	if err != nil {
		return nil, err
	}
	if err := writeChildGrant(ctx, spec, s.home.Root(), uid, gid); err != nil {
		return nil, err
	}
	child, err := Open(ctx, Options{
		Home:      spec.Home,
		NewStages: s.newStages,
		NewFixer:  s.newFixer,
		Build:     s.build,
		Catalog:   s.catalog,
		LockWait:  -1,
		Forge:     spec.Forge,
	})
	if err != nil {
		return nil, err
	}
	served := make(chan error, 1)
	go func() { served <- child.Serve(context.Background()) }()
	client, err := ipc.Dial(ctx, child.Socket(), ipc.ClientConfig{})
	if err != nil {
		_ = child.Close()
		<-served
		return nil, fmt.Errorf("service: dialing the subordinate: %w", err)
	}
	var health machine.Health
	if err := client.Call(ctx, ipc.MethodHealth, struct{}{}, &health); err != nil {
		_ = client.Close()
		_ = child.Close()
		<-served
		return nil, fmt.Errorf("service: the subordinate refused its parent: %w", err)
	}
	return &Subordinate{
		parent: s, child: child, client: client, served: served,
		tasks: append([]string(nil), spec.Tasks...), copy: copyPath,
	}, nil
}

// Drop closes the local connection and does not remove the copy, the child
// home, or any task row.
func (d *Subordinate) Drop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropLocked()
}

func (d *Subordinate) dropLocked() {
	if d.dropped {
		return
	}
	d.dropped = true
	if d.client != nil {
		_ = d.client.Close()
		d.client = nil
	}
}

// Close drops the connection and stops the child service. The copy stays.
func (d *Subordinate) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	d.dropLocked()
	child := d.child
	served := d.served
	d.child = nil
	d.served = nil
	d.mu.Unlock()
	if child == nil {
		return nil
	}
	err := child.Close()
	if served != nil {
		err = errors.Join(err, <-served)
	}
	return err
}

// DelegationObservation is what the parent can say about the child from the
// local connection. Liveness is taskstate.Unobserved in both directions: a
// socket that answers is not a session this process holds, and a socket that
// has gone is not a dead worker. Connected is whether the last call reached
// the child. A drop is Connected false. This value writes nothing.
type DelegationObservation struct {
	Liveness  taskstate.Liveness
	Connected bool
}

// Observe asks the child over the local connection. A closed connection, and
// a call that fails because the connection is gone, are Unobserved and not a
// failed task. The copy is not removed. The log line is redacted; the copy's
// files are not read into it.
func (d *Subordinate) Observe(ctx context.Context) DelegationObservation {
	if d == nil {
		return DelegationObservation{Liveness: taskstate.Unobserved}
	}
	d.mu.Lock()
	dropped := d.dropped
	client := d.client
	d.mu.Unlock()
	if dropped || client == nil {
		d.logUnobserved(errors.New("the local connection is closed"))
		return DelegationObservation{Liveness: taskstate.Unobserved}
	}
	var health machine.Health
	if err := client.Call(ctx, ipc.MethodHealth, struct{}{}, &health); err != nil {
		d.logUnobserved(err)
		return DelegationObservation{Liveness: taskstate.Unobserved}
	}
	return DelegationObservation{Liveness: taskstate.Unobserved, Connected: true}
}

func (d *Subordinate) logUnobserved(cause error) {
	if d == nil || d.parent == nil || d.parent.log == nil {
		return
	}
	task := ""
	if len(d.tasks) > 0 {
		task = d.tasks[0]
	}
	d.parent.log.Printf("delegation: task %s is unobserved: %s", task, delegationNote(cause))
}

// delegationNote is the text a delegation log line may carry. Credentials
// this package recognizes are removed. Anything else in the cause is the
// cause's own words, and a copy's files are not part of it.
func delegationNote(cause error) string {
	if cause == nil {
		return ""
	}
	return redact.New().Redact(cause.Error())
}

// DeliverDelegated records delivery of a delegated task on the parent.
// A subordinate refuses without writing, on its own store or the parent's.
// A second record on the parent is store.ErrDelegationDelivered.
func (s *Service) DeliverDelegated(ctx context.Context, taskID string) error {
	if s.hasGrant {
		return ErrDeliveryNotOwned
	}
	_, err := s.store.RecordDelegationDelivery(ctx, taskID)
	return err
}

// ClaimGate binds workingPath to a gate of this home, which for a
// subordinate is the child's own gate. A path the grant names as the
// parent's, and a path inside the parent home, is refused and no gate is
// created. A home that is not a subordinate has no claim on this path; the
// product's gate initialization is unchanged.
func (s *Service) ClaimGate(ctx context.Context, workingPath, command string) error {
	if !s.hasGrant {
		return fmt.Errorf("service: only a subordinate claims a gate on this path")
	}
	if err := s.grant.RefuseGate(workingPath); err != nil {
		return err
	}
	_, err := gate.Initialize(ctx, gate.Spec{
		Home:        s.home.Root(),
		WorkingPath: workingPath,
		Command:     command,
	}, gate.WithIndex(s.store))
	return err
}

// MayRelaunchLocally reports whether a parent may begin a local attempt for
// a task it delegated. Recovery none never does. Recovery relaunch-if-dead
// does only when attemptDead is the end record, not when the child is merely
// unobserved.
func MayRelaunchLocally(recovery store.DelegationRecovery, attemptDead bool) bool {
	return recovery == store.RecoveryRelaunchIfDead && attemptDead
}

func (s *Service) loadGrant(ctx context.Context) error {
	g, err := s.store.DelegationGrant(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	s.grant = g
	s.hasGrant = true
	return nil
}

func (s *Service) refuseDelegatedMerge() error {
	if !s.hasGrant {
		return nil
	}
	return ErrDelegationMerge
}

func (s *Service) refuseDelegatedPeer(req ipc.Request) error {
	if !s.hasGrant {
		return nil
	}
	creds, err := req.Peer.Credentials()
	if err != nil {
		return fmt.Errorf("%w: %w", ipc.ErrUnidentifiedPeer, err)
	}
	g := s.grant
	if creds.PID != g.ParentPID || creds.UID != g.ParentUID || creds.GID != g.ParentGID {
		return fmt.Errorf("%w: %w: peer pid %d uid %d gid %d", ipc.ErrRefused, ErrGrantPeer, creds.PID, creds.UID, creds.GID)
	}
	return nil
}

func (s *Service) refuseLocalRelaunch(ctx context.Context, id string) error {
	d, err := s.store.Delegation(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	attempts, err := s.store.TaskAttempts(ctx, id)
	if err != nil {
		return err
	}
	dead := false
	if len(attempts) > 0 {
		latest := attempts[len(attempts)-1]
		dead, err = s.store.DelegationAttemptDead(ctx, id, latest.Sequence)
		if err != nil {
			return err
		}
	}
	if MayRelaunchLocally(d.Recovery, dead) {
		return nil
	}
	return fmt.Errorf("%w: task %s stays with %s", ErrDelegationOwns, id, d.ChildHome)
}

func (s *Service) delegateTasks(ctx context.Context, spec SubordinateSpec) (string, error) {
	if len(spec.Tasks) == 0 {
		return "", fmt.Errorf("service: a subordinate names no task")
	}
	var copyPath string
	for i, id := range spec.Tasks {
		if err := home.ValidTaskID(id); err != nil {
			return "", err
		}
		path, err := spec.Home.TaskCopy(id)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return "", err
		}
		marker := filepath.Join(path, "kept.txt")
		if err := os.WriteFile(marker, []byte("kept\n"), 0o600); err != nil {
			return "", err
		}
		if i == 0 {
			copyPath = path
		}
		if _, err := s.store.Task(ctx, id); errors.Is(err, store.ErrNotFound) {
			if _, err := s.store.CreateTask(ctx, store.Task{
				ID: id, Shape: store.TaskDelivery, Project: "delegated", Mode: "delegated",
				WorktreePath: path,
			}, store.Disposition{}); err != nil {
				return "", err
			}
		} else if err != nil {
			return "", err
		}
		if _, err := s.store.Delegation(ctx, id); errors.Is(err, store.ErrNotFound) {
			if _, err := s.store.RecordDelegation(ctx, id, spec.Home.Root(), spec.Recovery); err != nil {
				return "", err
			}
		} else if err != nil {
			return "", err
		}
		attempts, err := s.store.TaskAttempts(ctx, id)
		if err != nil {
			return "", err
		}
		if len(attempts) == 0 {
			if _, err := s.store.BeginTaskAttempt(ctx, id, 0); err != nil {
				return "", err
			}
		}
	}
	return copyPath, nil
}

func writeChildGrant(ctx context.Context, spec SubordinateSpec, parentHome string, uid, gid int) error {
	records, err := store.Open(ctx, spec.Home.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		return err
	}
	defer func() { _ = records.Close() }()
	_, err = records.RecordDelegationGrant(ctx, store.DelegationGrant{
		ParentHome:     parentHome,
		Tasks:          spec.Tasks,
		ParentBranches: spec.ParentBranches,
		ParentPID:      os.Getpid(),
		ParentUID:      uid,
		ParentGID:      gid,
	})
	return err
}
